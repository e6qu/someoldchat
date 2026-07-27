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

func TestCleanupWorkerReclaimsAfterDurableDeleteEvent(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Put(context.Background(), "T1/file", 4, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	fileEvent := events.Event{ID: "evt_blob", WorkspaceID: "T1", Topic: events.FileBlobDeleteTopic, Payload: "T1/file", CreatedAt: time.Now().UTC()}
	if err := store.CreateFile(context.Background(), domain.File{ID: "file_1", WorkspaceID: "T1", Uploader: "U1", BlobKey: "T1/file", Name: "x", Title: "x", MIMEType: "text/plain", Size: 4, CreatedAt: time.Now().UTC()}, events.Event{ID: "evt_file", WorkspaceID: "T1", Topic: "file.created", Payload: "file_1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFile(context.Background(), "file_1", fileEvent); err != nil {
		t.Fatal(err)
	}
	worker, err := NewCleanupWorker(store, objects, "blob-cleaner-1", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(context.Background(), "T1"); err != nil || count != 1 {
		t.Fatalf("run count=%d err=%v", count, err)
	}
	if _, _, err := objects.Open(context.Background(), "T1/file"); err != ErrNotFound {
		t.Fatalf("blob lookup error=%v, want not found", err)
	}
}

type slowDeleteStore struct {
	started chan struct{}
	release chan struct{}
}

func (s slowDeleteStore) Put(context.Context, string, int64, io.Reader) (Object, error) {
	return Object{}, errors.New("slow delete test store does not put objects")
}

func (s slowDeleteStore) Open(context.Context, string) (Object, io.ReadCloser, error) {
	return Object{}, nil, errors.New("slow delete test store does not open objects")
}

func (s slowDeleteStore) Delete(ctx context.Context, _ string) error {
	close(s.started)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type renewalTrackingSource struct {
	*memory.Store
	renewed chan struct{}
	once    sync.Once
}

func (s *renewalTrackingSource) RenewEvents(ctx context.Context, owner string, sequences []uint64, lease time.Duration) error {
	s.once.Do(func() { close(s.renewed) })
	return s.Store.RenewEvents(ctx, owner, sequences, lease)
}

func TestCleanupWorkerRenewsLeaseDuringSlowDelete(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	event := events.Event{ID: "evt_blob_slow", WorkspaceID: "T1", Topic: events.FileBlobDeleteTopic, Payload: "T1/slow", CreatedAt: time.Now().UTC()}
	if err := store.CreateFile(context.Background(), domain.File{ID: "file_slow", WorkspaceID: "T1", Uploader: "U1", BlobKey: event.Payload, Name: "slow", Title: "slow", MIMEType: "text/plain", Size: 4, CreatedAt: time.Now().UTC()}, events.Event{ID: "evt_file_slow", WorkspaceID: "T1", Topic: "file.created", Payload: "file_slow", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFile(context.Background(), "file_slow", event); err != nil {
		t.Fatal(err)
	}
	source := &renewalTrackingSource{Store: store, renewed: make(chan struct{})}
	slowStore := slowDeleteStore{started: make(chan struct{}), release: make(chan struct{})}
	worker, err := NewCleanupWorker(source, slowStore, "slow-cleaner", 1, 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan struct {
		count int
		err   error
	}, 1)
	go func() {
		count, err := worker.RunOnce(context.Background(), "T1")
		result <- struct {
			count int
			err   error
		}{count: count, err: err}
	}()
	<-slowStore.started
	select {
	case <-source.renewed:
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not renew the slow delete lease")
	}
	close(slowStore.release)
	select {
	case outcome := <-result:
		if outcome.err != nil || outcome.count != 1 {
			t.Fatalf("cleanup outcome=%+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup worker did not finish")
	}
}

// A lost lease means another worker is deleting the same object. The suppression
// dropped that signal whenever the delete also failed, so the only observable
// symptom was a generic delete error and "two owners ran the same work" — the
// condition behind duplicate deletion — was invisible in logs and metrics.
func TestCleanupWorkerSurfacesLostLeaseAlongsideDeleteFailure(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	event := events.Event{ID: "evt_blob_lost", WorkspaceID: "T1", Topic: events.FileBlobDeleteTopic, Payload: "T1/lost", CreatedAt: time.Now().UTC()}
	if err := store.CreateFile(context.Background(), domain.File{ID: "file_lost", WorkspaceID: "T1", Uploader: "U1", BlobKey: event.Payload, Name: "lost", Title: "lost", MIMEType: "text/plain", Size: 4, CreatedAt: time.Now().UTC()}, events.Event{ID: "evt_file_lost", WorkspaceID: "T1", Topic: "file.created", Payload: "file_lost", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFile(context.Background(), "file_lost", event); err != nil {
		t.Fatal(err)
	}
	source := &lostLeaseSource{Store: store, lost: errors.New("another owner holds the cleanup lease")}
	failing := failingDeleteStore{started: make(chan struct{}), err: errors.New("provider rejected the delete")}
	worker, err := NewCleanupWorker(source, failing, "losing-cleaner", 1, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.RunOnce(context.Background(), "T1")
	if err == nil {
		t.Fatal("cleanup reported success while its lease was lost")
	}
	if !errors.Is(err, source.lost) {
		t.Fatalf("error=%v, want the lost lease surfaced alongside the delete failure", err)
	}
}

type lostLeaseSource struct {
	*memory.Store
	lost error
}

func (s *lostLeaseSource) RenewEvents(context.Context, string, []uint64, time.Duration) error {
	return s.lost
}

type failingDeleteStore struct {
	started chan struct{}
	err     error
}

func (failingDeleteStore) Put(context.Context, string, int64, io.Reader) (Object, error) {
	return Object{}, errors.New("failing delete test store does not put objects")
}

func (failingDeleteStore) Open(context.Context, string) (Object, io.ReadCloser, error) {
	return Object{}, nil, errors.New("failing delete test store does not open objects")
}

func (s failingDeleteStore) Delete(ctx context.Context, _ string) error {
	// Wait for the renewal loop to lose the lease, then fail with an error of
	// this store's own so both signals are present at once.
	<-ctx.Done()
	return s.err
}
