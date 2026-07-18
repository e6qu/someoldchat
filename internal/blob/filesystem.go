package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Filesystem struct {
	root    string
	maxSize int64
}

var _ Store = Filesystem{}

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
	temporary, err := os.CreateTemp(filepath.Dir(path), ".blob-*")
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
	return Object{Key: key, Size: info.Size()}, file, nil
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
