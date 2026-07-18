//go:build postgres

package qualification

import (
	"context"
	"os"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/store/postgres"
)

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
