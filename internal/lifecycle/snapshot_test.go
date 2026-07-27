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

func (s *memorySnapshotStore) Walk(_ context.Context, prefix string, visit func(blob.Object) error) error {
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

var _ blob.WalkStore = (*memorySnapshotStore)(nil)

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
	if manifest.Signature, err = manager.signManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := manager.verifyManifest(manifest); err == nil {
		t.Fatal("inconsistent snapshot size metadata was accepted")
	}
}

// The object store is the only durable snapshot target for a scale-to-zero
// deployment, because the filesystem root disappears with the volume. Both
// snapshotter constructors required an absolute Manager.Root, which
// NewObjectSnapshotManager never sets, so -snapshot-store=s3 could not start in
// either snapshot mode.
func TestObjectSnapshotManagerConfiguresBothSnapshotters(t *testing.T) {
	store := &memorySnapshotStore{objects: make(map[string][]byte)}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{5}, 32), bytes.Repeat([]byte{6}, 32), "object-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	metadata := Manifest{Backend: "sqlite", SchemaVersion: 1, ApplicationVersion: "test"}
	if _, err := NewFileSnapshotter(manager, "/srv/state/database.sqlite", "/srv/state/database.sqlite", metadata); err != nil {
		t.Fatalf("file snapshotter over an object store: %v", err)
	}
	if _, err := NewDirectorySnapshotter(manager, "/srv/state/dqlite", "/srv/state/dqlite", metadata, DirectorySnapshotSourceStopped); err != nil {
		t.Fatalf("directory snapshotter over an object store: %v", err)
	}
}

// Exactly one durable target may be configured, so the two storage shapes cannot
// silently drift apart again.
func TestSnapshotManagerRequiresExactlyOneDurableTarget(t *testing.T) {
	keys := func(m *SnapshotManager) {
		m.EncryptionKey, m.SigningKey, m.KeyID, m.MaxBytes = bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32), "key", 1<<20
	}
	both := SnapshotManager{Root: "/srv/snapshots", ObjectStore: &memorySnapshotStore{objects: map[string][]byte{}}}
	keys(&both)
	if err := both.Validate(); err == nil {
		t.Fatal("a manager with both a root and an object store was accepted")
	}
	neither := SnapshotManager{}
	keys(&neither)
	if err := neither.Validate(); err == nil {
		t.Fatal("a manager with no durable target was accepted")
	}
	relative := SnapshotManager{Root: "snapshots"}
	keys(&relative)
	if err := relative.Validate(); err == nil {
		t.Fatal("a relative snapshot root was accepted")
	}
}

// A directory snapshot over the object store has no local snapshot root to stage
// its archive in; it must still be able to create and restore one.
func TestDirectorySnapshotRoundTripOverObjectStore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "dqlite")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("dqlite segment bytes\n")
	if err := os.WriteFile(filepath.Join(source, "nested", "0000000001"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memorySnapshotStore{objects: make(map[string][]byte)}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{10}, 32), "object-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "restored")
	snapshotter, err := NewDirectorySnapshotter(manager, source, output, Manifest{Backend: "dqlite", SchemaVersion: 3, ApplicationVersion: "test"}, DirectorySnapshotSourceStopped)
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
	got, err := os.ReadFile(filepath.Join(output, "nested", "0000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored=%q want %q", got, want)
	}
}

// specs/scale-to-zero.md requires restore to defend against unsupported schema
// versions. A rollback to an older activator must refuse a newer snapshot before
// any bytes reach the live volume, not after.
func TestRestoreRefusesSnapshotFromANewerSchema(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("rows"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{11}, 32), bytes.Repeat([]byte{12}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "live.sqlite")
	if err := os.WriteFile(outputPath, []byte("newer live rows"), 0o600); err != nil {
		t.Fatal(err)
	}
	newer, err := NewFileSnapshotter(manager, sourcePath, outputPath, Manifest{Backend: "sqlite", SchemaVersion: 9, ApplicationVersion: "new"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := newer.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := NewFileSnapshotter(manager, sourcePath, outputPath, Manifest{Backend: "sqlite", SchemaVersion: 4, ApplicationVersion: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Restore(context.Background(), manifest); err == nil {
		t.Fatal("a snapshot from a newer schema version was restored over the live volume")
	}
	live, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(live) != "newer live rows" {
		t.Fatalf("live volume=%q, want it untouched by the refused restore", live)
	}
}

// docs/blob-lifecycle.md forbids treating an unavailable provider as empty, and
// the same rule belongs here: a transient read failure on the newest manifest
// must not be indistinguishable from corruption, because that silently selects
// older data.
func TestLastVerifiedSurfacesProviderFailuresInsteadOfSelectingOlderData(t *testing.T) {
	store := &failingSnapshotStore{memorySnapshotStore: memorySnapshotStore{objects: make(map[string][]byte)}}
	manager, err := NewObjectSnapshotManager(store, bytes.Repeat([]byte{13}, 32), bytes.Repeat([]byte{14}, 32), "object-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	for generation := uint64(1); generation <= 2; generation++ {
		if err := os.WriteFile(sourcePath, []byte("generation "+string(rune('0'+generation))), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Create(sourcePath, Manifest{Generation: generation, Backend: "sqlite", SchemaVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	store.failKey = "manifests/00000000000000000002.json"
	if _, err := manager.LastVerified(2); err == nil {
		t.Fatal("an unreadable newest manifest silently selected an older generation")
	}
}

type failingSnapshotStore struct {
	memorySnapshotStore
	failKey string
}

func (s *failingSnapshotStore) Open(ctx context.Context, key string) (blob.Object, io.ReadCloser, error) {
	if s.failKey != "" && key == s.failKey {
		return blob.Object{}, nil, errors.New("snapshot provider is throttling")
	}
	return s.memorySnapshotStore.Open(ctx, key)
}

// The two storage backends must answer LastVerified the same way.
//
// The filesystem path fell back to reading current.json when the manifests/ scan
// found nothing verifiable; the object-store path returned ErrNoVerifiedSnapshot.
// That is the implicit fallback specs/scale-to-zero.md:180-183 forbids, still
// alive in one backend: the coordinator's exact-generation check defanged it for
// today's callers, but the same question answered two ways is how the next caller
// silently restores a generation nobody selected.
func TestLastVerifiedDoesNotFallBackToCurrentInEitherBackend(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "database.sqlite")
	if err := os.WriteFile(sourcePath, []byte("published generation 3"), 0o600); err != nil {
		t.Fatal(err)
	}
	object := &memorySnapshotStore{objects: make(map[string][]byte)}
	objectManager, err := NewObjectSnapshotManager(object, bytes.Repeat([]byte{21}, 32), bytes.Repeat([]byte{22}, 32), "object-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fileManager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{21}, 32), bytes.Repeat([]byte{22}, 32), "object-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, manager := range []SnapshotManager{fileManager, objectManager} {
		if _, err := manager.Create(sourcePath, Manifest{Generation: 3, Backend: "sqlite", SchemaVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// The published manifests are lost — a partially restored bucket, a truncated
	// volume — while current.json survives. Both backends must now say the same
	// thing: there is no verified generation to select.
	if err := os.RemoveAll(filepath.Join(root, "snapshots", "manifests")); err != nil {
		t.Fatal(err)
	}
	delete(object.objects, "manifests/00000000000000000003.json")

	for name, manager := range map[string]SnapshotManager{"filesystem": fileManager, "object store": objectManager} {
		if _, err := manager.Current(3); err != nil {
			t.Fatalf("%s: current.json is expected to survive: %v", name, err)
		}
		if _, err := manager.LastVerified(3); !errors.Is(err, ErrNoVerifiedSnapshot) {
			t.Fatalf("%s: LastVerified error=%v, want %v rather than a generation nobody published to manifests/", name, err, ErrNoVerifiedSnapshot)
		}
	}
}
