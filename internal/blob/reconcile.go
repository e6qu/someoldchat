package blob

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// ExternalUploadWindow is the longest interval this product allows between an
// object being written to blob storage and the durable row that references it
// being committed. It is the ticket lifetime of an external upload: the client
// receives an upload URL, PUTs the bytes under workspace/external/<id>, and only
// then calls files.completeUploadExternal, which is what creates the row the
// reference walk can see.
//
// The reconciler's minimum orphan age must never be shorter than this, and the
// relationship is enforced rather than documented: with a five-minute grace
// period an audit deleted the bytes of an upload the user completed six minutes
// later, and terraform/ecs-runtime suspends bucket versioning, so that deletion
// is unrecoverable.
//
// internal/api/slack/handler.go mints the ticket with this same duration written
// as a literal; it should read this constant so the two cannot drift.
const ExternalUploadWindow = 15 * time.Minute

type ReferenceSource interface {
	WalkBlobReferences(context.Context, domain.WorkspaceID, func(string) error) error
}

// TemporaryObjectStore is implemented by providers that stage in-progress uploads
// inside the namespace Walk enumerates. Walk hides those entries so an upload in
// flight can never be classified as an orphan, which means nothing else ever
// reclaims one abandoned by a killed process; this is how they are reclaimed.
type TemporaryObjectStore interface {
	SweepTemporaryObjects(ctx context.Context, prefix string, olderThan time.Time) (int, error)
}

type EventSink interface {
	AppendEvent(context.Context, events.Event) error
}

type Reconciliation struct {
	Objects       int
	References    int
	OrphanKeys    []string
	MissingKeys   []string
	DuplicateKeys int
	// RecentObjects counts unreferenced objects held back because they are newer
	// than the minimum orphan age. They are not orphans yet: an upload writes its
	// object before it commits the metadata that references it.
	RecentObjects int
}

type Reconciler struct {
	References ReferenceSource
	Objects    WalkStore
	Events     EventSink
	MaxResults int
	// MinOrphanAge is the grace period an unreferenced object must survive before
	// it may be classified as an orphan.
	MinOrphanAge time.Duration
	Now          func() time.Time
}

func NewReconciler(references ReferenceSource, objects WalkStore, events EventSink, maxResults int, minOrphanAge time.Duration) (Reconciler, error) {
	if references == nil || objects == nil || events == nil || maxResults <= 0 {
		return Reconciler{}, errors.New("blob reconciliation requires reference source, walk store, event sink, and positive result limit")
	}
	if minOrphanAge < ExternalUploadWindow {
		return Reconciler{}, fmt.Errorf("blob reconciliation requires a minimum orphan age of at least %s, the external upload window, so an upload whose bytes are written before its reference is committed cannot be classified as an orphan", ExternalUploadWindow)
	}
	return Reconciler{References: references, Objects: objects, Events: events, MaxResults: maxResults, MinOrphanAge: minOrphanAge, Now: time.Now}, nil
}

// Audit compares durable references against provider objects.
//
// Provider objects are enumerated first and references second. An upload writes
// its object before it commits the metadata that makes it a reference, so walking
// references first meant every blob uploaded during the audit was unreferenced at
// read time and present at object-walk time — classified as an orphan, and with
// -enqueue-orphans its bytes were deleted while its metadata stayed live.
//
// Ordering closes the wide window; the minimum orphan age closes the remaining
// narrow one where an upload straddles the whole reference walk. A key that looks
// missing is re-checked against the provider for the mirror-image reason.
func (r Reconciler) Audit(ctx context.Context, workspace domain.WorkspaceID) (Reconciliation, error) {
	if workspace == "" {
		return Reconciliation{}, errors.New("blob reconciliation requires a workspace")
	}
	if r.Now == nil || r.MinOrphanAge < ExternalUploadWindow {
		return Reconciliation{}, errors.New("blob reconciliation is not configured")
	}
	// The hold-back instant is taken before anything is enumerated, not after
	// both walks finish. Measured at the end, the grace period shrinks by the
	// duration of the audit itself: on a workspace whose walks take longer than
	// MinOrphanAge — exactly the large deployments that need the audit — an object
	// written moments after the audit started was already "old enough" by the time
	// it was judged, and its reference had not yet been committed. Anchoring the
	// comparison at the start makes the guarantee independent of how long the
	// audit runs, and makes an object written during the walk trivially too new.
	cutoff := r.Now().UTC()
	result := Reconciliation{}
	objects := make(map[string]time.Time)
	prefix := string(workspace) + "/"
	if err := r.Objects.Walk(ctx, prefix, func(object Object) error {
		if object.Key == "" {
			return errors.New("blob object store returned an empty key")
		}
		result.Objects++
		objects[object.Key] = object.ModTime
		return nil
	}); err != nil {
		return Reconciliation{}, err
	}
	references := make(map[string]struct{}, len(objects))
	missing := make([]string, 0)
	if err := r.References.WalkBlobReferences(ctx, workspace, func(key string) error {
		if key == "" {
			return errors.New("blob reference source returned an empty key")
		}
		if _, exists := references[key]; exists {
			result.DuplicateKeys++
			return nil
		}
		references[key] = struct{}{}
		result.References++
		if _, exists := objects[key]; exists {
			delete(objects, key)
			return nil
		}
		if len(missing) >= r.MaxResults {
			return fmt.Errorf("blob reconciliation found more than %d missing objects", r.MaxResults)
		}
		missing = append(missing, key)
		return nil
	}); err != nil {
		return Reconciliation{}, err
	}
	for key, modified := range objects {
		if modified.IsZero() || cutoff.Sub(modified.UTC()) < r.MinOrphanAge {
			// An unknown age is treated as too recent: deleting live bytes is
			// unrecoverable, while deferring an orphan costs one audit cycle.
			result.RecentObjects++
			continue
		}
		if len(result.OrphanKeys) >= r.MaxResults {
			return Reconciliation{}, fmt.Errorf("blob reconciliation found more than %d orphan objects", r.MaxResults)
		}
		result.OrphanKeys = append(result.OrphanKeys, key)
	}
	confirmed, err := r.confirmMissing(ctx, missing)
	if err != nil {
		return Reconciliation{}, err
	}
	result.MissingKeys = confirmed
	sort.Strings(result.OrphanKeys)
	sort.Strings(result.MissingKeys)
	return result, nil
}

// confirmMissing re-reads each candidate directly from the provider. A reference
// committed after the object walk finished is present but was not enumerated;
// reporting it as missing is a false alarm that trains operators to ignore the
// signal, and cmd/blobgc turns any missing key into a non-zero exit.
func (r Reconciler) confirmMissing(ctx context.Context, candidates []string) ([]string, error) {
	confirmed := make([]string, 0, len(candidates))
	for _, key := range candidates {
		_, reader, err := r.Objects.Open(ctx, key)
		if err == nil {
			if closeErr := reader.Close(); closeErr != nil {
				return nil, closeErr
			}
			continue
		}
		if errors.Is(err, ErrNotFound) {
			confirmed = append(confirmed, key)
			continue
		}
		// An unavailable provider is not an empty one.
		return nil, fmt.Errorf("confirm blob reference %q: %w", key, err)
	}
	return confirmed, nil
}

// SweepAbandonedUploads removes staging files a killed process left behind.
//
// Walk deliberately hides them, which is what stops an in-flight upload being
// deleted, but it also means nothing reports or reclaims one whose writer died
// between os.CreateTemp and the final rename. Without this they accumulate at
// full object size on the blob volume forever. Providers that stage server-side
// have nothing to sweep and simply do not implement the interface.
//
// It is destructive, so it lives beside EnqueueOrphans behind the same operator
// opt-in rather than inside the read-only audit, and it uses the same grace
// period: a temporary file younger than MinOrphanAge may be an upload in flight.
func (r Reconciler) SweepAbandonedUploads(ctx context.Context, workspace domain.WorkspaceID) (int, error) {
	if workspace == "" {
		return 0, errors.New("blob cleanup requires a workspace")
	}
	if r.Now == nil || r.MinOrphanAge < ExternalUploadWindow {
		return 0, errors.New("blob reconciliation is not configured")
	}
	sweeper, ok := r.Objects.(TemporaryObjectStore)
	if !ok {
		return 0, nil
	}
	return sweeper.SweepTemporaryObjects(ctx, string(workspace)+"/", r.Now().UTC().Add(-r.MinOrphanAge))
}

func (r Reconciler) EnqueueOrphans(ctx context.Context, workspace domain.WorkspaceID, result Reconciliation) (int, error) {
	if workspace == "" {
		return 0, errors.New("blob cleanup requires a workspace")
	}
	// A referenced object the provider does not have is evidence that this
	// audit's own view of the provider is wrong — the wrong prefix, a partially
	// restored snapshot, a bucket that has not caught up. Deleting the objects it
	// classified as orphans from that same view is exactly the wrong response,
	// and cmd/blobgc's non-zero exit came after the deletions were already
	// enqueued.
	if len(result.MissingKeys) != 0 {
		return 0, fmt.Errorf("blob cleanup refuses to enqueue %d orphan deletions while %d referenced objects are missing", len(result.OrphanKeys), len(result.MissingKeys))
	}
	for _, key := range result.OrphanKeys {
		if key == "" {
			return 0, errors.New("blob cleanup cannot enqueue an empty key")
		}
	}
	for index, key := range result.OrphanKeys {
		id, err := domain.NewEventID()
		if err != nil {
			return index, err
		}
		if err := r.Events.AppendEvent(ctx, events.Event{ID: id, WorkspaceID: workspace, Topic: events.FileBlobDeleteTopic, Payload: key, CreatedAt: time.Now().UTC()}); err != nil {
			return index, err
		}
	}
	return len(result.OrphanKeys), nil
}
