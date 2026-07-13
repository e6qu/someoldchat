//go:build !dqlite

package localchat

import (
	"context"
	"errors"
	"io"

	"github.com/sameoldchat/sameoldchat/internal/store"
)

func openDqlite(_ context.Context, _ Config) (store.Store, io.Closer, error) {
	return nil, nil, errors.New("dqlite storage requires the dqlite build profile")
}
