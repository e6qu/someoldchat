package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A migration that loses a deadlock must be retried, not surfaced. The
// backfills a previous replica started are deliberately released from the
// migration fence so it can serve early, which means the next replica's DDL
// can deadlock against them; PostgreSQL breaks the cycle by aborting one side,
// and aborting the migration used to stop the replica from starting at all.
func TestMigrationRetriesWhenTheDatabaseAbortsIt(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	aborted := errors.New("deadlock detected")
	attempts := 0
	store.retryableMigrationError = func(err error) bool { return errors.Is(err, aborted) }
	store.migrateAttempt = func(context.Context) error {
		attempts++
		if attempts < 3 {
			return aborted
		}
		return nil
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate = %v, want the retry to succeed once the conflict cleared", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want the aborted ones retried", attempts)
	}
}

// An error the driver does not call retryable is returned immediately: a schema
// that cannot be applied must not be attempted five times and then reported as
// a retry exhaustion, which hides the real cause.
func TestMigrationDoesNotRetryAnOrdinaryFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "no-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	fatal := errors.New("column does not exist")
	attempts := 0
	store.retryableMigrationError = func(error) bool { return false }
	store.migrateAttempt = func(context.Context) error {
		attempts++
		return fatal
	}
	if err := store.Migrate(ctx); !errors.Is(err, fatal) {
		t.Fatalf("migrate = %v, want the original failure", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want exactly one for an unretryable failure", attempts)
	}
}

// Retries are bounded: a conflict that never clears must report the last error
// rather than loop.
func TestMigrationStopsRetryingAndReportsTheLastFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "bounded.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	aborted := errors.New("deadlock detected")
	attempts := 0
	store.retryableMigrationError = func(err error) bool { return errors.Is(err, aborted) }
	store.migrateAttempt = func(context.Context) error {
		attempts++
		return aborted
	}
	if err := store.Migrate(ctx); !errors.Is(err, aborted) {
		t.Fatalf("migrate = %v, want the last failure wrapped", err)
	}
	if attempts != migrationAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, migrationAttempts)
	}
}
