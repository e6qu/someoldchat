package blob

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotFound = errors.New("blob not found")
var ErrUnavailable = errors.New("blob storage is unavailable")

type Object struct {
	Key  string
	Size int64
	// ModTime is when the provider last wrote the object. Reconciliation needs
	// it to hold back objects that are too new to be orphans; a provider that
	// cannot report it leaves the zero value, which is treated as unknown age.
	ModTime time.Time
}

type Store interface {
	Put(context.Context, string, int64, io.Reader) (Object, error)
	Open(context.Context, string) (Object, io.ReadCloser, error)
	Delete(context.Context, string) error
}

// WalkStore exposes bounded provider enumeration for reconciliation and snapshot
// manifest scans. Walk must invoke visit in provider order and must stop
// immediately when visit returns an error.
type WalkStore interface {
	Store
	Walk(context.Context, string, func(Object) error) error
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
