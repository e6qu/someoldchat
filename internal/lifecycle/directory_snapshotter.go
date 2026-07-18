package lifecycle

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DirectorySnapshotter archives a stopped state directory into the encrypted
// SnapshotManager artifact and restores it with an atomic directory swap.
// This is the filesystem shape required by dqlite's documented restore
// procedure.
type DirectorySnapshotter struct {
	Manager      SnapshotManager
	SourcePath   string
	OutputPath   string
	BaseMetadata Manifest
}

type DirectorySnapshotSourceState uint8

const DirectorySnapshotSourceStopped DirectorySnapshotSourceState = 1

func NewDirectorySnapshotter(manager SnapshotManager, sourcePath, outputPath string, metadata Manifest, sourceState DirectorySnapshotSourceState) (DirectorySnapshotter, error) {
	if !filepath.IsAbs(manager.Root) || len(manager.EncryptionKey) != 32 || len(manager.SigningKey) < 32 || strings.TrimSpace(manager.KeyID) == "" {
		return DirectorySnapshotter{}, errors.New("snapshot manager is not configured")
	}
	if sourceState != DirectorySnapshotSourceStopped {
		return DirectorySnapshotter{}, errors.New("directory snapshot source state must be explicitly stopped")
	}
	if strings.TrimSpace(sourcePath) == "" || strings.TrimSpace(outputPath) == "" || !filepath.IsAbs(sourcePath) || !filepath.IsAbs(outputPath) {
		return DirectorySnapshotter{}, errors.New("snapshot source and output paths must be absolute")
	}
	if metadata.Backend == "" || metadata.SchemaVersion < 1 {
		return DirectorySnapshotter{}, errors.New("snapshot backend and schema version are required")
	}
	return DirectorySnapshotter{Manager: manager, SourcePath: sourcePath, OutputPath: outputPath, BaseMetadata: metadata}, nil
}

func (s DirectorySnapshotter) Create(ctx context.Context, generation uint64) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	metadata := s.BaseMetadata
	metadata.Generation = generation
	if err := ensureDirectory(s.SourcePath); err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(s.Manager.Root, 0o700); err != nil {
		return Manifest{}, err
	}
	archiveFile, err := os.CreateTemp(s.Manager.Root, ".directory-snapshot-*")
	if err != nil {
		return Manifest{}, err
	}
	archivePath := archiveFile.Name()
	defer os.Remove(archivePath)
	if err := archiveDirectory(ctx, archiveFile, s.SourcePath); err != nil {
		_ = archiveFile.Close()
		return Manifest{}, err
	}
	if err := archiveFile.Sync(); err != nil {
		_ = archiveFile.Close()
		return Manifest{}, err
	}
	if err := archiveFile.Close(); err != nil {
		return Manifest{}, err
	}
	return s.Manager.Create(archivePath, metadata)
}

func (s DirectorySnapshotter) Current(_ context.Context, generation uint64) (Manifest, error) {
	return s.Manager.Current(generation)
}

func (s DirectorySnapshotter) LastVerified(_ context.Context, maxGeneration uint64) (Manifest, error) {
	return s.Manager.LastVerified(maxGeneration)
}

func (s DirectorySnapshotter) Restore(ctx context.Context, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(s.OutputPath)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	archiveFile, err := os.CreateTemp(parent, ".directory-restore-*")
	if err != nil {
		return err
	}
	archivePath := archiveFile.Name()
	if err := archiveFile.Close(); err != nil {
		os.Remove(archivePath)
		return err
	}
	defer os.Remove(archivePath)
	if err := s.Manager.Restore(manifest, archivePath); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	temporaryDirectory, err := os.MkdirTemp(parent, ".directory-restore-tree-*")
	if err != nil {
		return err
	}
	if err := extractDirectory(ctx, archivePath, temporaryDirectory, s.Manager.MaxBytes); err != nil {
		return errors.Join(err, os.RemoveAll(temporaryDirectory))
	}
	return replaceDirectory(temporaryDirectory, s.OutputPath)
}

func ensureDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("snapshot source %q is not a directory", path)
	}
	return nil
}

func archiveDirectory(ctx context.Context, output io.Writer, root string) error {
	writer := tar.NewWriter(output)
	closeWriter := func(err error) error {
		closeErr := writer.Close()
		return errors.Join(err, closeErr)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot source contains unsupported symlink %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("snapshot source contains unsupported file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.ModTime = time.Unix(0, 0).UTC()
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := copyContext(ctx, writer, input)
		closeErr := input.Close()
		return errors.Join(copyErr, closeErr)
	})
	return closeWriter(err)
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func extractDirectory(ctx context.Context, archivePath, destination string, maxBytes int64) error {
	input, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer input.Close()
	reader := tar.NewReader(input)
	seen := make(map[string]struct{})
	var expandedBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		name, err := safeArchiveName(header.Name)
		if err != nil {
			return err
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("snapshot archive contains duplicate path %q", name)
		}
		seen[name] = struct{}{}
		path := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fmt.Errorf("directory archive entry %q has content", name)
			}
			if err := os.MkdirAll(path, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxBytes-expandedBytes {
				return errors.New("snapshot archive expands beyond the configured size limit")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			expandedBytes += written
		default:
			return fmt.Errorf("snapshot archive contains unsupported entry %q", name)
		}
	}
}

func safeArchiveName(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", errors.New("snapshot archive contains an unsafe path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("snapshot archive contains a path traversal")
		}
	}
	clean := pathCleanSlash(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("snapshot archive contains a path traversal")
	}
	return clean, nil
}

func pathCleanSlash(value string) string {
	parts := strings.Split(value, "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(clean) > 0 {
				clean = clean[:len(clean)-1]
				continue
			}
			return ".."
		default:
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "/")
}

func replaceDirectory(source, destination string) error {
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("snapshot output %q is not a directory", destination)
		}
		backup, err := os.CreateTemp(filepath.Dir(destination), ".directory-previous-*")
		if err != nil {
			return err
		}
		backupPath := backup.Name()
		if err := backup.Close(); err != nil {
			os.Remove(backupPath)
			return err
		}
		if err := os.Remove(backupPath); err != nil {
			return err
		}
		if err := os.Rename(destination, backupPath); err != nil {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			restoreErr := os.Rename(backupPath, destination)
			return errors.Join(err, restoreErr)
		}
		return os.RemoveAll(backupPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}
