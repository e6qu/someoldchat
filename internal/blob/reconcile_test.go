package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

const testMinOrphanAge = time.Hour

// agedClock makes the audit clock deterministic: every object written by a test is
// treated as older than the grace period unless the test says otherwise.
func agedClock() func() time.Time {
	return func() time.Time { return time.Now().Add(24 * time.Hour) }
}

func TestReconcilerReportsOrphansAndMissingReferences(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"T1/kept", "T1/orphan"} {
		if _, err := objects.Put(context.Background(), key, 1, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	if err := state.CreateFile(context.Background(), domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", BlobKey: "T1/kept", Name: "x", Title: "x", Size: 1}, events.Event{ID: "file-created", WorkspaceID: "T1", Topic: "file.created"}); err != nil {
		t.Fatal(err)
	}
	if err := state.CreateFile(context.Background(), domain.File{ID: "F2", WorkspaceID: "T1", Uploader: "U1", BlobKey: "T1/missing", Name: "y", Title: "y", Size: 1}, events.Event{ID: "file-created-missing", WorkspaceID: "T1", Topic: "file.created"}); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(state, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Now = agedClock()
	result, err := reconciler.Audit(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Objects != 2 || result.References != 2 || len(result.OrphanKeys) != 1 || result.OrphanKeys[0] != "T1/orphan" || len(result.MissingKeys) != 1 || result.MissingKeys[0] != "T1/missing" {
		t.Fatalf("unexpected reconciliation result: %+v", result)
	}
}

// An upload writes its object before it commits the reference. With references
// walked first, a blob uploaded during the audit was present at the object walk
// and absent from the reference set, so -enqueue-orphans deleted a live file's
// bytes. Walking objects first makes that classification impossible.
func TestReconcilerNeverClassifiesABlobUploadedDuringTheAuditAsOrphan(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	// The upload lands after the audit has begun and commits before the reference
	// walk observes it.
	racing := &racingReferenceSource{
		inner: state,
		onWalk: func() {
			if _, err := objects.Put(context.Background(), "T1/uploaded-mid-audit", 1, bytes.NewReader([]byte("x"))); err != nil {
				t.Errorf("mid-audit upload: %v", err)
			}
		},
	}
	reconciler, err := NewReconciler(racing, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Now = agedClock()
	result, err := reconciler.Audit(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range result.OrphanKeys {
		if key == "T1/uploaded-mid-audit" {
			t.Fatalf("a blob uploaded during the audit was classified as an orphan: %+v", result)
		}
	}
}

// The remaining window is an upload whose object predates the object walk and
// whose commit lands after the reference walk. A minimum orphan age closes it.
func TestReconcilerHoldsBackObjectsYoungerThanTheMinimumOrphanAge(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Put(context.Background(), "T1/just-uploaded", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	reconciler, err := NewReconciler(state, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Audit(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.OrphanKeys) != 0 || result.RecentObjects != 1 {
		t.Fatalf("result=%+v, want the fresh object held back from orphan cleanup", result)
	}
}

func TestReconcilerRequiresAPositiveMinimumOrphanAge(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	if _, err := NewReconciler(state, objects, state, 10, 0); err == nil {
		t.Fatal("a zero minimum orphan age was accepted")
	}
}

// A reference committed after the object walk is present in the provider. Trusting
// the stale enumeration reported it missing, and cmd/blobgc turns any missing key
// into a non-zero exit, which trains operators to ignore the signal.
func TestReconcilerConfirmsMissingKeysAgainstTheProvider(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	if err := state.CreateFile(context.Background(), domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", BlobKey: "T1/late", Name: "x", Title: "x", Size: 1}, events.Event{ID: "file-created-late", WorkspaceID: "T1", Topic: "file.created"}); err != nil {
		t.Fatal(err)
	}
	racing := &racingReferenceSource{
		inner: state,
		onWalk: func() {
			if _, err := objects.Put(context.Background(), "T1/late", 1, bytes.NewReader([]byte("x"))); err != nil {
				t.Errorf("late upload: %v", err)
			}
		},
	}
	reconciler, err := NewReconciler(racing, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Now = agedClock()
	result, err := reconciler.Audit(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MissingKeys) != 0 {
		t.Fatalf("missing=%v, want the late upload confirmed present", result.MissingKeys)
	}
}

// A provider that cannot answer is not an empty provider.
func TestReconcilerSurfacesProviderFailuresWhileConfirmingMissingKeys(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	state.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	if err := state.CreateFile(context.Background(), domain.File{ID: "F1", WorkspaceID: "T1", Uploader: "U1", BlobKey: "T1/unreadable", Name: "x", Title: "x", Size: 1}, events.Event{ID: "file-created-unreadable", WorkspaceID: "T1", Topic: "file.created"}); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(state, unavailableOpenStore{Filesystem: objects}, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Now = agedClock()
	if _, err := reconciler.Audit(context.Background(), "T1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("audit error=%v, want the provider failure surfaced", err)
	}
}

// In-progress uploads stage inside the enumerated namespace. Enumerating them
// produced an orphan key whose deletion breaks the upload's final rename.
func TestFilesystemWalkExcludesInProgressUploads(t *testing.T) {
	root := t.TempDir()
	objects, err := NewFilesystem(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	staged := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			_, err := objects.Put(context.Background(), "T1/slow", 4, &blockingReader{staged: staged, release: release, body: []byte("slow")})
			return err
		}()
	}()
	<-staged
	seen := make([]string, 0)
	if err := objects.Walk(context.Background(), "T1/", func(object Object) error {
		seen = append(seen, object.Key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, key := range seen {
		if key != "T1/slow" {
			t.Fatalf("walk emitted the in-progress upload %q", key)
		}
	}
}

type blockingReader struct {
	staged  chan struct{}
	release chan struct{}
	body    []byte
	once    sync.Once
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.staged)
		<-r.release
	})
	if len(r.body) == 0 {
		return 0, io.EOF
	}
	count := copy(p, r.body)
	r.body = r.body[count:]
	return count, nil
}

type unavailableOpenStore struct {
	Filesystem
}

func (unavailableOpenStore) Open(context.Context, string) (Object, io.ReadCloser, error) {
	return Object{}, nil, ErrUnavailable
}

type racingReferenceSource struct {
	inner  ReferenceSource
	onWalk func()
	fired  bool
}

func (s *racingReferenceSource) WalkBlobReferences(ctx context.Context, workspace domain.WorkspaceID, visit func(string) error) error {
	if !s.fired && s.onWalk != nil {
		s.fired = true
		s.onWalk()
	}
	return s.inner.WalkBlobReferences(ctx, workspace, visit)
}

func TestReconcilerEnqueuesOrphansForLeasedCleanup(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	reconciler, err := NewReconciler(state, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	count, err := reconciler.EnqueueOrphans(context.Background(), "T1", Reconciliation{OrphanKeys: []string{"T1/orphan-a", "T1/orphan-b"}})
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	worker, err := NewCleanupWorker(state, objects, "reconciler-test", 10, 0x100000000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background(), "T1"); err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleanup error=%v", err)
	}
}

func TestReconcilerRejectsUnboundedResults(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	if _, err := NewReconciler(state, objects, state, 0, testMinOrphanAge); err == nil {
		t.Fatal("accepted zero result limit")
	}
}

// The hold-back instant used to be taken after both walks, so the grace period
// shrank by the duration of the audit itself. On a workspace whose walks take
// longer than MinOrphanAge — exactly the deployments large enough to need an
// audit — an object written just after the audit started was judged "old enough"
// before its reference had been committed, and -enqueue-orphans deleted the bytes
// of a live file.
func TestReconcilerMeasuresTheGracePeriodFromTheStartOfTheAudit(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Put(context.Background(), "T1/uploaded-just-before-the-audit", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	start := time.Now()
	// A long audit: the reference walk alone outlasts the whole grace period.
	clock := &steppingClock{now: start}
	slow := &racingReferenceSource{inner: state, onWalk: func() { clock.advance(24 * time.Hour) }}
	reconciler, err := NewReconciler(slow, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.Now = clock.read
	result, err := reconciler.Audit(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.OrphanKeys) != 0 || result.RecentObjects != 1 {
		t.Fatalf("result=%+v, want an object written just before a long audit held back", result)
	}
}

type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppingClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppingClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

// The grace period is not a tunable number, it is a bound on a real window: the
// external upload writes its object and commits the referencing row up to
// ExternalUploadWindow later. -min-orphan-age 5m was accepted and deleted the
// bytes of an upload completed six minutes after it started, unrecoverably,
// because the deployment's bucket versioning is suspended.
func TestReconcilerRejectsAGracePeriodShorterThanTheExternalUploadWindow(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	for _, tooShort := range []time.Duration{0, time.Minute, ExternalUploadWindow - time.Second} {
		if _, err := NewReconciler(state, objects, state, 10, tooShort); err == nil {
			t.Fatalf("minimum orphan age %s was accepted", tooShort)
		}
	}
	if _, err := NewReconciler(state, objects, state, 10, ExternalUploadWindow); err != nil {
		t.Fatalf("the external upload window itself was rejected: %v", err)
	}
}

// A referenced object the provider does not have means this audit's own view is
// wrong. Enqueueing the deletions derived from that same view is the worst
// possible response, and cmd/blobgc's non-zero exit came only after they were
// already queued.
func TestEnqueueOrphansRefusesAnAuditWithMissingObjects(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	reconciler, err := NewReconciler(state, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	count, err := reconciler.EnqueueOrphans(context.Background(), "T1", Reconciliation{
		OrphanKeys:  []string{"T1/orphan-a"},
		MissingKeys: []string{"T1/gone"},
	})
	if err == nil {
		t.Fatal("orphan deletions were enqueued from an audit that reported missing objects")
	}
	if count != 0 {
		t.Fatalf("count=%d, want nothing enqueued", count)
	}
	worker, err := NewCleanupWorker(state, objects, "reconciler-test", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := worker.RunOnce(context.Background(), "T1"); err != nil || completed != 0 {
		t.Fatalf("cleanup completed=%d err=%v, want no queued deletion", completed, err)
	}
}

// Walk hides staging files so an upload in flight is never deleted. Nothing then
// reclaimed one whose writer was killed between CreateTemp and the rename, so
// full-size files accumulated on the blob volume without bound and no audit
// reported them.
func TestSweepReclaimsAbandonedStagingFilesButNotLiveOnes(t *testing.T) {
	root := t.TempDir()
	objects, err := NewFilesystem(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "T1")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	abandoned := filepath.Join(directory, temporaryBlobPrefix+"abandoned")
	if err := os.WriteFile(abandoned, []byte("orphaned bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatal(err)
	}
	inFlight := filepath.Join(directory, temporaryBlobPrefix+"in-flight")
	if err := os.WriteFile(inFlight, []byte("live upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := "T1/kept"
	if _, err := objects.Put(context.Background(), kept, 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	reconciler, err := NewReconciler(state, objects, state, 10, testMinOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	swept, err := reconciler.SweepAbandonedUploads(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Fatalf("swept=%d, want only the abandoned staging file reclaimed", swept)
	}
	if _, err := os.Stat(abandoned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat abandoned staging file=%v, want it removed", err)
	}
	if _, err := os.Stat(inFlight); err != nil {
		t.Fatalf("an upload in flight was reclaimed: %v", err)
	}
	if _, _, err := objects.Open(context.Background(), kept); err != nil {
		t.Fatalf("a stored blob was reclaimed: %v", err)
	}
}
