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

type ReferenceSource interface {
	WalkBlobReferences(context.Context, domain.WorkspaceID, func(string) error) error
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
	if minOrphanAge <= 0 {
		return Reconciler{}, errors.New("blob reconciliation requires a positive minimum orphan age so an in-flight upload cannot be classified as an orphan")
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
	if r.Now == nil || r.MinOrphanAge <= 0 {
		return Reconciliation{}, errors.New("blob reconciliation is not configured")
	}
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
	cutoff := r.Now().UTC()
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

func (r Reconciler) EnqueueOrphans(ctx context.Context, workspace domain.WorkspaceID, result Reconciliation) (int, error) {
	if workspace == "" {
		return 0, errors.New("blob cleanup requires a workspace")
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
