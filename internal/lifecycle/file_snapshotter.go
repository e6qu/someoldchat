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
	if !filepath.IsAbs(manager.Root) || len(manager.EncryptionKey) != 32 || len(manager.SigningKey) < 32 || strings.TrimSpace(manager.KeyID) == "" {
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

func (s FileSnapshotter) Restore(_ context.Context, manifest Manifest) error {
	return s.Manager.Restore(manifest, s.OutputPath)
}
