package blob

import (
	"context"
	"errors"
	"io"
)

var ErrNotFound = errors.New("blob not found")
var ErrUnavailable = errors.New("blob storage is unavailable")

type Object struct {
	Key  string
	Size int64
}

type Store interface {
	Put(context.Context, string, int64, io.Reader) (Object, error)
	Open(context.Context, string) (Object, io.ReadCloser, error)
	Delete(context.Context, string) error
}

// Disabled is the explicit blob-store choice for deployments without file
// storage. It fails every operation so a missing capability cannot be
// mistaken for an empty store or silently degrade file behavior.
type Disabled struct{}

var _ Store = Disabled{}

func (Disabled) Put(context.Context, string, int64, io.Reader) (Object, error) {
	return Object{}, ErrUnavailable
}

func (Disabled) Open(context.Context, string) (Object, io.ReadCloser, error) {
	return Object{}, nil, ErrUnavailable
}

func (Disabled) Delete(context.Context, string) error {
	return ErrUnavailable
}
