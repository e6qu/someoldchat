package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/blob"
)

type memorySnapshotStore struct {
	objects map[string][]byte
}

func (s *memorySnapshotStore) Put(_ context.Context, key string, size int64, source io.Reader) (blob.Object, error) {
	body, err := io.ReadAll(source)
	if err != nil {
		return blob.Object{}, err
	}
	if int64(len(body)) != size {
		return blob.Object{}, errors.New("snapshot object size mismatch")
	}
	s.objects[key] = append([]byte(nil), body...)
	return blob.Object{Key: key, Size: size}, nil
}

func (s *memorySnapshotStore) Open(_ context.Context, key string) (blob.Object, io.ReadCloser, error) {
	body, ok := s.objects[key]
	if !ok {
		return blob.Object{}, nil, blob.ErrNotFound
	}
	return blob.Object{Key: key, Size: int64(len(body))}, io.NopCloser(bytes.NewReader(body)), nil
}

func (s *memorySnapshotStore) Delete(_ context.Context, key string) error {
	if _, ok := s.objects[key]; !ok {
		return blob.ErrNotFound
	}
	delete(s.objects, key)
	return nil
}

func (s *memorySnapshotStore) List(_ context.Context, prefix string) ([]blob.Object, error) {
	objects := make([]blob.Object, 0)
	for key, body := range s.objects {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, blob.Object{Key: key, Size: int64(len(body))})
		}
	}
	return objects, nil
}

var _ blob.ListStore = (*memorySnapshotStore)(nil)

func TestEncryptedSnapshotRoundTrip(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	want := []byte("durable database bytes\n")
	if err := os.WriteFile(sourcePath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Create(sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1, ApplicationVersion: "test", MinRestorerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "restored.sqlite")
	if err := manager.Restore(manifest, outputPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored = %q, want %q", got, want)
	}
	if manifest.Signature == "" || manifest.PlaintextSHA256 == manifest.CiphertextSHA256 {
		t.Fatalf("weak manifest: %+v", manifest)
	}
	current, err := manager.Current(1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Artifact != manifest.Artifact || current.Signature != manifest.Signature {
		t.Fatalf("current manifest = %+v, created = %+v", current, manifest)
	}
	last, err := manager.LastVerified(1)
	if err != nil || last.Artifact != manifest.Artifact {
		t.Fatalf("last verified manifest = %+v err=%v", last, err)
	}
	if _, err := manager.LastVerified(0); err == nil {
		t.Fatal("snapshot newer than recovery fence was accepted")
	}
	if current, err := manager.Current(2); err != nil || current.Generation != 1 {
		t.Fatalf("snapshot at or before recovery fence was rejected: %+v err=%v", current, err)
	}
	if _, err := manager.Current(0); err == nil {
		t.Fatal("snapshot newer than recovery fence was accepted")
	}
}

func TestObjectSnapshotRoundTrip(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	want := []byte("durable object snapshot\n")
	if err := os.WriteFile(sourcePath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memorySnapshotStore{objects: make(map[string][]byte)}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{19}, 32), bytes.Repeat([]byte{20}, 32), "object-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Create(sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Current(1); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "restored.sqlite")
	if err := manager.Restore(manifest, outputPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored=%q, want %q", got, want)
	}
	if last, err := manager.LastVerified(1); err != nil || last.Generation != 1 {
		t.Fatalf("last verified=%+v err=%v", last, err)
	}
}

func TestFileSnapshotterCancelledRestoreDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	outputPath := filepath.Join(root, "restored.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{13}, 32), bytes.Repeat([]byte{14}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewFileSnapshotter(manager, sourcePath, outputPath, Manifest{Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := snapshotter.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := snapshotter.Restore(ctx, manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error = %v, want context cancellation", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("output was replaced after cancellation: %q", got)
	}
}

func TestSnapshotRejectsUnsafeArtifact(t *testing.T) {
	root := t.TempDir()
	manager, err := NewSnapshotManager(root, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.Restore(Manifest{Generation: 1, KeyID: "test-key", Signature: "bad", Artifact: "../outside"}, filepath.Join(root, "out"))
	if err == nil {
		t.Fatal("unsafe artifact was accepted")
	}
}

func TestLastVerifiedSkipsCorruptNewestGeneration(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{5}, 32), bytes.Repeat([]byte{6}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Create(sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(sourcePath, Manifest{Generation: 2, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.PreviousGeneration != first.Generation {
		t.Fatalf("previous generation=%d, want %d", second.PreviousGeneration, first.Generation)
	}
	artifact, err := safePath(manager.Root, second.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(artifact, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var corruptedByte [1]byte
	if _, err := file.ReadAt(corruptedByte[:], 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	corruptedByte[0] ^= 0xff
	if _, err := file.WriteAt(corruptedByte[:], 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	last, err := manager.LastVerified(2)
	if err != nil || last.Generation != first.Generation {
		t.Fatalf("last=%+v err=%v", last, err)
	}
}

func TestSnapshotPublicationRejectsStaleGeneration(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(sourcePath, Manifest{Generation: 2, Backend: "sqlite", SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}); err == nil {
		t.Fatal("stale snapshot generation was published")
	}
	current, err := manager.Current(2)
	if err != nil || current.Generation != 2 {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestSnapshotFailurePreservesPriorManifest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(source, []byte("durable database bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{10}, 32), "key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Create(source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	failed := manager
	failed.MaxBytes = 1
	if _, err := failed.Create(source, Manifest{Generation: 2, Backend: "sqlite", SchemaVersion: 1}); err == nil {
		t.Fatal("interrupted snapshot unexpectedly succeeded")
	}
	current, err := manager.Current(2)
	if err != nil || current.Generation != first.Generation || current.Signature != first.Signature {
		t.Fatalf("current=%+v err=%v, want prior manifest=%+v", current, err, first)
	}
	if _, err := manager.LastVerified(2); err != nil {
		t.Fatalf("prior verified snapshot was lost: %v", err)
	}
}

func TestSnapshotRejectsCorruptedArtifact(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(source, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32), "key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Create(source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := safePath(manager.Root, manifest.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(artifact, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var corruptedByte [1]byte
	if _, err := file.ReadAt(corruptedByte[:], 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	corruptedByte[0] ^= 0xff
	if _, err := file.WriteAt(corruptedByte[:], 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Current(1); err == nil {
		t.Fatal("corrupted snapshot was accepted")
	}
}

func TestSnapshotRejectsInconsistentManifestMetadata(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(source, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{17}, 32), bytes.Repeat([]byte{18}, 32), "key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Create(source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	manifest.CiphertextBytes++
	manifest.Signature = manager.sign(manifest)
	if err := manager.verifyManifest(manifest); err == nil {
		t.Fatal("inconsistent snapshot size metadata was accepted")
	}
}
