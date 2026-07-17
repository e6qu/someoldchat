package lifecycle

import (
	"context"
	"errors"
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
	if !manager.configured() {
		return FileSnapshotter{}, errors.New("snapshot manager is not configured")
	}
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(outputPath) == "" || !filepath.IsAbs(sourcePath) || !filepath.IsAbs(outputPath) {
		return FileSnapshotter{}, errors.New("snapshot source and output paths must be absolute")
	}
	if metadata.Backend == "" || metadata.SchemaVersion < 1 {
		return FileSnapshotter{}, errors.New("snapshot backend and schema version are required")
	}
	return FileSnapshotter{Manager: manager, SourcePath: sourcePath, OutputPath: outputPath, BaseMetadata: metadata}, nil
}

func (s FileSnapshotter) Create(ctx context.Context, generation uint64) (Manifest, error) {
	metadata := s.BaseMetadata
	metadata.Generation = generation
	return s.Manager.Create(ctx, s.SourcePath, metadata)
}

func (s FileSnapshotter) Current(ctx context.Context, generation uint64) (Manifest, error) {
	// A hibernation snapshot carries the hibernation fence. Wake advances the
	// fence before restore, so the selected current snapshot may be older than
	// the wake fence but must never be newer than it.
	return s.Manager.Current(ctx, generation)
}

func (s FileSnapshotter) LastVerified(ctx context.Context, maxGeneration uint64) (Manifest, error) {
	return s.Manager.LastVerified(ctx, maxGeneration)
}

func (s FileSnapshotter) Restore(ctx context.Context, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Manager.Restore(ctx, manifest, s.OutputPath)
}
