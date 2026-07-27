package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Filesystem struct {
	root    string
	maxSize int64
}

var _ WalkStore = Filesystem{}

// temporaryBlobPrefix names in-progress uploads. They live inside the enumerated
// namespace, so Walk must exclude them: an enumerated temporary file matches no
// reference, and deleting one makes the user's upload fail at its final rename.
const temporaryBlobPrefix = ".blob-"

func NewFilesystem(root string, maxSize int64) (Filesystem, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) || maxSize <= 0 {
		return Filesystem{}, errors.New("filesystem blob store requires an absolute root and positive size limit")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Filesystem{}, err
	}
	return Filesystem{root: root, maxSize: maxSize}, nil
}

func (s Filesystem) Put(ctx context.Context, key string, size int64, source io.Reader) (Object, error) {
	if source == nil || size < 0 || size > s.maxSize {
		return Object{}, errors.New("blob source and bounded size are required")
	}
	path, err := s.safePath(key)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Object{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), temporaryBlobPrefix+"*")
	if err != nil {
		return Object{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, err := io.CopyN(temporary, io.LimitReader(readerContext{ctx, source}, size+1), size+1)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = temporary.Close()
		return Object{}, err
	}
	if written != size {
		_ = temporary.Close()
		return Object{}, fmt.Errorf("blob size mismatch: wrote %d, expected %d", written, size)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Object{}, err
	}
	if err := temporary.Close(); err != nil {
		return Object{}, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return Object{}, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return Object{}, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return Object{}, err
	}
	return Object{Key: key, Size: size}, nil
}

func (s Filesystem) Open(_ context.Context, key string) (Object, io.ReadCloser, error) {
	path, err := s.safePath(key)
	if err != nil {
		return Object{}, nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, nil, ErrNotFound
	}
	if err != nil {
		return Object{}, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Object{}, nil, err
	}
	if info.Size() < 0 || info.Size() > s.maxSize {
		_ = file.Close()
		return Object{}, nil, errors.New("stored blob exceeds size limit")
	}
	return Object{Key: key, Size: info.Size(), ModTime: info.ModTime()}, file, nil
}

func (s Filesystem) Delete(_ context.Context, key string) error {
	path, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s Filesystem) Walk(ctx context.Context, prefix string, visit func(Object) error) error {
	if visit == nil {
		return errors.New("object visitor is required")
	}
	if prefix != "" {
		if _, err := s.safePath(prefix); err != nil {
			return err
		}
	}
	root := s.root
	if prefix != "" {
		root = filepath.Join(root, filepath.FromSlash(prefix))
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), temporaryBlobPrefix) {
			return nil
		}
		relative, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		if info.Size() < 0 || info.Size() > s.maxSize {
			return errors.New("stored blob exceeds size limit")
		}
		return visit(Object{Key: filepath.ToSlash(relative), Size: info.Size(), ModTime: info.ModTime()})
	})
}

// SweepTemporaryObjects removes staging files older than olderThan.
//
// Walk hides every .blob-* entry so an upload in flight cannot be classified as
// an orphan and deleted out from under its own rename. The consequence is that a
// process killed between os.CreateTemp and that rename leaves a full-size file no
// audit reports and no worker reclaims, growing the blob volume without bound.
// The age bound is what keeps this safe: a staging file younger than the grace
// period may still belong to a live upload.
func (s Filesystem) SweepTemporaryObjects(ctx context.Context, prefix string, olderThan time.Time) (int, error) {
	if prefix != "" {
		if _, err := s.safePath(prefix); err != nil {
			return 0, err
		}
	}
	root := s.root
	if prefix != "" {
		root = filepath.Join(root, filepath.FromSlash(prefix))
	}
	removed := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// The tree is being written to while it is swept; an entry that
				// vanished is one fewer thing to reclaim, not a failure.
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.IsDir() || !strings.HasPrefix(filepath.Base(path), temporaryBlobPrefix) {
			return nil
		}
		if !info.ModTime().Before(olderThan) {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		} else if removeErr == nil {
			removed++
		}
		return nil
	})
	return removed, err
}

func (s Filesystem) safePath(key string) (string, error) {
	if strings.TrimSpace(key) == "" || filepath.IsAbs(key) || strings.Contains(key, "..") {
		return "", errors.New("unsafe blob key")
	}
	path := filepath.Join(s.root, filepath.FromSlash(key))
	root, _ := filepath.Abs(s.root)
	clean, _ := filepath.Abs(path)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return "", errors.New("blob key escapes root")
	}
	return path, nil
}

type readerContext struct {
	ctx context.Context
	io.Reader
}

func (r readerContext) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.Reader.Read(buffer)
	}
}
