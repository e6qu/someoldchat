package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
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
