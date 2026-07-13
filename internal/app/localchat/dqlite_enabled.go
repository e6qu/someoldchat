//go:build dqlite

package localchat

import (
	"context"
	"io"

	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/dqlite"
)

func openDqlite(ctx context.Context, config Config) (store.Store, io.Closer, error) {
	selected, err := dqlite.Open(ctx, dqlite.Config{
		Directory: config.DqliteDirectory,
		Address:   config.DqliteAddress,
		Cluster:   append([]string(nil), config.DqliteCluster...),
		Database:  config.DqliteDatabase,
	})
	if err != nil {
		return nil, nil, err
	}
	return selected, selected, nil
}
