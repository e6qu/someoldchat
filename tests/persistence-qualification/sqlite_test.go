//go:build !dqlite

package qualification

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

func openStore(t *testing.T, ctx context.Context) (qualificationStore, func()) {
	t.Helper()
	repository, err := sqlstore.Open(ctx, filepath.Join(t.TempDir(), "qualification.db"))
	if err != nil {
		t.Fatal(err)
	}
	return repository, func() { _ = repository.Close() }
}
