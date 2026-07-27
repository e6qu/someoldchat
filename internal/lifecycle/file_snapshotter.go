package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// FileSnapshotter binds a SnapshotManager to one durable state file. The
// binding is explicit so lifecycle orchestration cannot silently choose a
// different state source or restore destination.
type FileSnapshotter struct {
	Manager      SnapshotManager
	SourcePath   string
	OutputPath   string
	BaseMetadata Manifest
}

func NewFileSnapshotter(manager SnapshotManager, sourcePath, outputPath string, metadata Manifest) (FileSnapshotter, error) {
	if err := manager.Validate(); err != nil {
		return FileSnapshotter{}, fmt.Errorf("snapshot manager is not configured: %w", err)
	}
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(outputPath) == "" || !filepath.IsAbs(sourcePath) || !filepath.IsAbs(outputPath) {
		return FileSnapshotter{}, errors.New("snapshot source and output paths must be absolute")
	}
	if metadata.Backend == "" || metadata.SchemaVersion < 1 {
		return FileSnapshotter{}, errors.New("snapshot backend and schema version are required")
	}
	return FileSnapshotter{Manager: manager, SourcePath: sourcePath, OutputPath: outputPath, BaseMetadata: metadata}, nil
}

func (s FileSnapshotter) Create(_ context.Context, generation uint64) (Manifest, error) {
	metadata := s.BaseMetadata
	metadata.Generation = generation
	return s.Manager.Create(s.SourcePath, metadata)
}

func (s FileSnapshotter) Current(_ context.Context, generation uint64) (Manifest, error) {
	// A hibernation snapshot carries the hibernation fence. Wake advances the
	// fence before restore, so the selected current snapshot may be older than
	// the wake fence but must never be newer than it.
	return s.Manager.Current(generation)
}

func (s FileSnapshotter) LastVerified(_ context.Context, maxGeneration uint64) (Manifest, error) {
	return s.Manager.LastVerified(maxGeneration)
}

// LiveState describes the database already present at OutputPath. It reads no
// snapshot, so recovery of an interrupted hibernation can start the stack from
// the newest copy of the data without overwriting it.
func (s FileSnapshotter) LiveState(_ context.Context, generation uint64) (Manifest, error) {
	return liveStateManifest(s.BaseMetadata, generation)
}

func (s FileSnapshotter) Restore(ctx context.Context, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureRestorable(manifest, s.BaseMetadata); err != nil {
		return err
	}
	return s.Manager.Restore(manifest, s.OutputPath)
}

// liveStateManifest describes the on-volume database using the running binary's
// own backend and schema version. It is never signed or published: it exists so
// startup stages that need a descriptor can run without a snapshot.
func liveStateManifest(base Manifest, generation uint64) (Manifest, error) {
	if base.Backend == "" || base.SchemaVersion < 1 {
		return Manifest{}, errors.New("live state description requires a backend and schema version")
	}
	base.Generation = generation
	return base, nil
}

// ensureRestorable rejects a snapshot the running binary must not write over the
// active volume. specs/scale-to-zero.md requires restore to defend against
// unsupported schema versions; without this check a rollback to an older
// activator accepts a newer snapshot, restores it, and only then hands the
// newer schema version to the migration stage — after the live volume is gone.
func ensureRestorable(manifest, base Manifest) error {
	if base.SchemaVersion < 1 {
		return errors.New("restore requires the running schema version")
	}
	if manifest.SchemaVersion > base.SchemaVersion {
		return fmt.Errorf("snapshot schema version %d is newer than the running schema version %d", manifest.SchemaVersion, base.SchemaVersion)
	}
	if base.Backend != "" && manifest.Backend != "" && manifest.Backend != base.Backend {
		return fmt.Errorf("snapshot backend %q does not match the running backend %q", manifest.Backend, base.Backend)
	}
	return nil
}
