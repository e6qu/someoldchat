//go:build !dqlite && !postgres

package qualification

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

func TestSQLiteQualification(t *testing.T) { runQualification(t, openStore) }

func TestSQLiteRestartQualification(t *testing.T) { runRestartQualification(t, openRestartableStore) }

// openRestartableStore keeps the database path fixed across reopen, so closing
// the handle and opening a new one is the same database and not a fresh one.
func openRestartableStore(t *testing.T, ctx context.Context) (qualificationStore, restarter, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restart.db")
	open := func(t *testing.T, ctx context.Context) *sqlstore.Store {
		t.Helper()
		repository, err := sqlstore.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		return repository
	}
	current := open(t, ctx)
	return current, func(t *testing.T, ctx context.Context) qualificationStore {
			t.Helper()
			if err := current.Close(); err != nil {
				t.Fatalf("close before restart: %v", err)
			}
			current = open(t, ctx)
			return current
		}, func() {
			_ = current.Close()
		}
}

func openStore(t *testing.T, ctx context.Context) (qualificationStore, func()) {
	t.Helper()
	repository, err := sqlstore.Open(ctx, filepath.Join(t.TempDir(), "qualification.db"))
	if err != nil {
		t.Fatal(err)
	}
	return repository, func() { _ = repository.Close() }
}
