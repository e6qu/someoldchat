package blob

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

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
	reconciler, err := NewReconciler(state, objects, state, 10)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Audit(context.Background(), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Objects != 2 || result.References != 2 || len(result.OrphanKeys) != 1 || result.OrphanKeys[0] != "T1/orphan" || len(result.MissingKeys) != 1 || result.MissingKeys[0] != "T1/missing" {
		t.Fatalf("unexpected reconciliation result: %+v", result)
	}
}

func TestReconcilerEnqueuesOrphansForLeasedCleanup(t *testing.T) {
	objects, err := NewFilesystem(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	state := memory.New()
	state.SeedWorkspace(domain.Workspace{ID: "T1"})
	reconciler, err := NewReconciler(state, objects, state, 10)
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
	if _, err := NewReconciler(state, objects, state, 0); err == nil {
		t.Fatal("accepted zero result limit")
	}
}
