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
	objects         map[string][]byte
	openErr         error
	artifactOpenErr error
	listErr         error
}

type cancelOnSecondErrorContext struct {
	context.Context
	calls int
}

func (c *cancelOnSecondErrorContext) Err() error {
	c.calls++
	if c.calls >= 2 {
		return context.Canceled
	}
	return nil
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
	if s.openErr != nil {
		return blob.Object{}, nil, s.openErr
	}
	if strings.HasPrefix(key, "artifacts/") && s.artifactOpenErr != nil {
		return blob.Object{}, nil, s.artifactOpenErr
	}
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
	if s.listErr != nil {
		return nil, s.listErr
	}
	objects := make([]blob.Object, 0)
	for key, body := range s.objects {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, blob.Object{Key: key, Size: int64(len(body))})
		}
	}
	return objects, nil
}

func (s *memorySnapshotStore) Walk(_ context.Context, prefix string, visit func(blob.Object) error) error {
	if s.listErr != nil {
		return s.listErr
	}
	if visit == nil {
		return errors.New("snapshot visitor is required")
	}
	for key, body := range s.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if err := visit(blob.Object{Key: key, Size: int64(len(body))}); err != nil {
			return err
		}
	}
	return nil
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
	manifest, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1, ApplicationVersion: "test", MinRestorerVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "restored.sqlite")
	if err := manager.Restore(context.Background(), manifest, outputPath); err != nil {
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
	current, err := manager.Current(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Artifact != manifest.Artifact || current.Signature != manifest.Signature {
		t.Fatalf("current manifest = %+v, created = %+v", current, manifest)
	}
	last, err := manager.LastVerified(context.Background(), 1)
	if err != nil || last.Artifact != manifest.Artifact {
		t.Fatalf("last verified manifest = %+v err=%v", last, err)
	}
	if _, err := manager.LastVerified(context.Background(), 0); err == nil {
		t.Fatal("snapshot newer than recovery fence was accepted")
	}
	if current, err := manager.Current(context.Background(), 2); err != nil || current.Generation != 1 {
		t.Fatalf("snapshot at or before recovery fence was rejected: %+v err=%v", current, err)
	}
	if _, err := manager.Current(context.Background(), 0); err == nil {
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
	manifest, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Current(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "restored.sqlite")
	if err := manager.Restore(context.Background(), manifest, outputPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored=%q, want %q", got, want)
	}
	if last, err := manager.LastVerified(context.Background(), 1); err != nil || last.Generation != 1 {
		t.Fatalf("last verified=%+v err=%v", last, err)
	}
}

func TestObjectSnapshotManagerHonorsCancelledContext(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memorySnapshotStore{objects: make(map[string][]byte)}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{23}, 32), bytes.Repeat([]byte{24}, 32), "cancelled-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Create(ctx, sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want context cancellation", err)
	}
	if _, err := manager.Current(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Current error = %v, want context cancellation", err)
	}
	if _, err := manager.LastVerified(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("LastVerified error = %v, want context cancellation", err)
	}
	if err := manager.Restore(ctx, Manifest{}, filepath.Join(root, "restored")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore error = %v, want context cancellation", err)
	}
}

func TestObjectLastVerifiedPropagatesProviderFailure(t *testing.T) {
	providerErr := errors.New("object provider unavailable")
	store := &memorySnapshotStore{objects: make(map[string][]byte), listErr: providerErr}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{25}, 32), bytes.Repeat([]byte{26}, 32), "provider-error-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LastVerified(context.Background(), 1); !errors.Is(err, providerErr) {
		t.Fatalf("LastVerified error = %v, want provider error", err)
	}
}

func TestObjectLastVerifiedPropagatesArtifactProviderFailure(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memorySnapshotStore{objects: make(map[string][]byte)}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{25}, 32), bytes.Repeat([]byte{26}, 32), "artifact-provider-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	providerErr := errors.New("artifact provider unavailable")
	store.artifactOpenErr = providerErr
	if _, err := manager.LastVerified(context.Background(), 1); !errors.Is(err, providerErr) {
		t.Fatalf("LastVerified error = %v, want artifact provider error", err)
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
	err = manager.Restore(context.Background(), Manifest{Generation: 1, KeyID: "test-key", Signature: "bad", Artifact: "../outside"}, filepath.Join(root, "out"))
	if err == nil {
		t.Fatal("unsafe artifact was accepted")
	}
}

func TestSnapshotRejectsManifestSymlink(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{31}, 32), bytes.Repeat([]byte{32}, 32), "symlink-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath, err := safePath(manager.Root, "manifests/00000000000000000001.json")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, manifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LastVerified(context.Background(), manifest.Generation); err == nil {
		t.Fatal("manifest symlink was followed")
	}
}

func TestSnapshotRejectsCurrentManifestSymlink(t *testing.T) {
	root := t.TempDir()
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{33}, 32), bytes.Repeat([]byte{34}, 32), "current-symlink-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	currentPath, err := safePath(manager.Root, "current.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(currentPath), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-current.json")
	if err := os.WriteFile(outside, []byte(`{"generation":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, currentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Current(context.Background(), 1); err == nil {
		t.Fatal("current manifest symlink was followed")
	}
}

func TestSnapshotRejectsArtifactSymlink(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{35}, 32), bytes.Repeat([]byte{36}, 32), "artifact-symlink-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	artifactPath, err := safePath(manager.Root, manifest.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-artifact.bin")
	artifactBody, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, artifactBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifactPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, artifactPath); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(context.Background(), manifest, filepath.Join(root, "restored.sqlite")); err == nil {
		t.Fatal("artifact symlink was followed")
	}
}

func TestSnapshotRejectsSymlinkedManifestDirectory(t *testing.T) {
	root := t.TempDir()
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{37}, 32), bytes.Repeat([]byte{38}, 32), "manifest-directory-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-manifests")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(manager.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(manager.Root, "manifests")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LastVerified(context.Background(), 1); err == nil {
		t.Fatal("symlinked manifest directory was accepted")
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
	first, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 2, Backend: "sqlite", SchemaVersion: 1})
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
	last, err := manager.LastVerified(context.Background(), 2)
	if err != nil || last.Generation != first.Generation {
		t.Fatalf("last=%+v err=%v", last, err)
	}
}

func TestLastVerifiedStopsWhenContextIsCancelledDuringFilesystemScan(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelOnSecondErrorContext{Context: context.Background()}
	if _, err := manager.LastVerified(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("LastVerified error = %v, want context cancellation", err)
	}
}

func TestLastVerifiedPropagatesFilesystemManifestReadFailure(t *testing.T) {
	root := t.TempDir()
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{27}, 32), bytes.Repeat([]byte{28}, 32), "filesystem-read-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manifestDirectory := filepath.Join(manager.Root, "manifests")
	if err := os.MkdirAll(manifestDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(manifestDirectory, "00000000000000000001.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LastVerified(context.Background(), 1); err == nil {
		t.Fatal("filesystem manifest read failure was hidden")
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
	if _, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 2, Backend: "sqlite", SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), sourcePath, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}); err == nil {
		t.Fatal("stale snapshot generation was published")
	}
	current, err := manager.Current(context.Background(), 2)
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
	first, err := manager.Create(context.Background(), source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	failed := manager
	failed.MaxBytes = 1
	if _, err := failed.Create(context.Background(), source, Manifest{Generation: 2, Backend: "sqlite", SchemaVersion: 1}); err == nil {
		t.Fatal("interrupted snapshot unexpectedly succeeded")
	}
	current, err := manager.Current(context.Background(), 2)
	if err != nil || current.Generation != first.Generation || current.Signature != first.Signature {
		t.Fatalf("current=%+v err=%v, want prior manifest=%+v", current, err, first)
	}
	if _, err := manager.LastVerified(context.Background(), 2); err != nil {
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
	manifest, err := manager.Create(context.Background(), source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1, ApplicationVersion: "test"})
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
	if _, err := manager.Current(context.Background(), 1); err == nil {
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
	manifest, err := manager.Create(context.Background(), source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	manifest.CiphertextBytes++
	manifest.Signature = manager.sign(manifest)
	if err := manager.verifyManifest(context.Background(), manifest); err == nil {
		t.Fatal("inconsistent snapshot size metadata was accepted")
	}
}
