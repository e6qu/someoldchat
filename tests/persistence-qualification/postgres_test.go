//go:build postgres

package qualification

import (
	"context"
	"os"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/store/postgres"
	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

func TestPostgresQualification(t *testing.T) { runQualification(t, openStore) }

// TestPostgresRestartQualification is the reason this harness exists. Every
// reopen test in the repository lived in internal/store/sqlstore and was
// SQLite-only, and `make test-postgres` does not run that package — so the
// profile the production Terraform requires for the worker tier had no
// restart-survival coverage at all.
func TestPostgresRestartQualification(t *testing.T) {
	runRestartQualification(t, openRestartableStore)
}

// openRestartableStore reopens the same DSN. Unlike the SQLite profile there is
// no file to keep: the server outlives every handle, which is exactly the
// property under test.
func openRestartableStore(t *testing.T, ctx context.Context) (qualificationStore, restarter, func()) {
	t.Helper()
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL qualification")
	}
	open := func(t *testing.T, ctx context.Context) *sqlstore.Store {
		t.Helper()
		repository, err := postgres.Open(ctx, dsn)
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
	dsn := os.Getenv("SAMEOLDCHAT_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SAMEOLDCHAT_POSTGRES_DSN is required for PostgreSQL qualification")
	}
	repository, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	return repository, func() { _ = repository.Close() }
}
