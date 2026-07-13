package blob

import (
	"bytes"
	"context"
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
