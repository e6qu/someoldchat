package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySnapshotterRoundTrip(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cluster.yaml"), []byte("cluster: qualification\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "segment"), bytes.Repeat([]byte("segment\n"), 10), 0o640); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), "directory-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirectorySnapshotter(manager, source, filepath.Join(root, "rejected"), Manifest{Backend: "dqlite", SchemaVersion: 1}, 0); err == nil {
		t.Fatal("directory snapshotter accepted an unspecified source state")
	}
	snapshotter, err := NewDirectorySnapshotter(manager, source, filepath.Join(root, "restored"), Manifest{Backend: "dqlite", SchemaVersion: 1, ApplicationVersion: "qualification", MinRestorerVersion: "qualification", MaxRestorerVersion: "qualification"}, DirectorySnapshotSourceStopped)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotter.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Restore(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"cluster.yaml", filepath.Join("nested", "segment")} {
		want, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, "restored", relative))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("restored %q differs", relative)
		}
	}
}

func TestDirectorySnapshotterObjectStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cluster.yaml"), []byte("cluster: object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memorySnapshotStore{objects: make(map[string][]byte)}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{21}, 32), bytes.Repeat([]byte{22}, 32), "object-directory-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewDirectorySnapshotter(manager, source, filepath.Join(root, "restored"), Manifest{Backend: "dqlite", SchemaVersion: 1}, DirectorySnapshotSourceStopped)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotter.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshotter.Restore(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "restored", "cluster.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cluster: object\n" {
		t.Fatalf("restored object directory content = %q", got)
	}
}

func TestDirectorySnapshotterRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32), "directory-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewDirectorySnapshotter(manager, source, filepath.Join(root, "restored"), Manifest{Backend: "dqlite", SchemaVersion: 1}, DirectorySnapshotSourceStopped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotter.Create(context.Background(), 1); err == nil {
		t.Fatal("symlink snapshot unexpectedly succeeded")
	}
}

func TestDirectorySnapshotterCancellationDoesNotReplaceDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "state"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{11}, 32), bytes.Repeat([]byte{12}, 32), "directory-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewDirectorySnapshotter(manager, source, filepath.Join(root, "restored"), Manifest{Backend: "dqlite", SchemaVersion: 1}, DirectorySnapshotSourceStopped)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotter.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	destination := snapshotter.OutputPath
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "state"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshotter.Restore(ctx, manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error = %v, want context cancellation", err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("destination was replaced after cancellation: %q", got)
	}
}

func TestRecoverDirectorySwapRestoresPreviousDirectory(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "database")
	backup := filepath.Join(root, ".directory-previous-recovery")
	source := filepath.Join(root, ".directory-restore-tree-recovery")
	journalPath := filepath.Join(root, ".database.swap.json")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "state"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(directorySwapJournal{Source: source, Destination: destination, Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(journalPath, body); err != nil {
		t.Fatal(err)
	}
	if err := recoverDirectorySwap(journalPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("recovered state = %q, want old", got)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists, error = %v", err)
	}
	if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal still exists, error = %v", err)
	}
}

func TestRecoverDirectorySwapRemovesBackupAfterInstall(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "database")
	backup := filepath.Join(root, ".directory-previous-recovery")
	source := filepath.Join(root, ".directory-restore-tree-recovery")
	journalPath := filepath.Join(root, ".database.swap.json")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "state"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "state"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(directorySwapJournal{Source: source, Destination: destination, Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(journalPath, body); err != nil {
		t.Fatal(err)
	}
	if err := recoverDirectorySwap(journalPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("installed state = %q, want new", got)
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists, error = %v", err)
	}
	if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal still exists, error = %v", err)
	}
}

func TestRecoverDirectorySwapRejectsJournalOutsideParent(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, ".database.swap.json")
	body, err := json.Marshal(directorySwapJournal{
		Source:      filepath.Join(root, "restore"),
		Destination: filepath.Join(root, "database"),
		Backup:      filepath.Join(filepath.Dir(root), "outside-backup"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(journalPath, body); err != nil {
		t.Fatal(err)
	}
	if err := recoverDirectorySwap(journalPath); err == nil {
		t.Fatal("journal with path outside parent was accepted")
	}
}

func TestSafeArchiveNameRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../outside", "nested/../../outside", "/absolute", "nested\\outside"} {
		if _, err := safeArchiveName(name); err == nil {
			t.Fatalf("unsafe archive name %q was accepted", name)
		}
	}
}
