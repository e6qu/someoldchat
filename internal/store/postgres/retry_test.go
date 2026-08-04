//go:build postgres

package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The classifier has to recognise the codes as the driver actually reports
// them: wrapped, and as a *pgconn.PgError rather than a string. Matching on
// message text would pass here and fail against a localised server.
func TestRetryableMigrationRecognisesSerialisationAborts(t *testing.T) {
	for _, code := range []string{"40P01", "40001"} {
		wrapped := fmt.Errorf("migrate schema indexes: %w", &pgconn.PgError{Code: code, Message: "deadlock detected"})
		if !retryableMigration(wrapped) {
			t.Errorf("SQLSTATE %s was not treated as retryable", code)
		}
	}
	if retryableMigration(fmt.Errorf("migrate: %w", &pgconn.PgError{Code: "42703", Message: "column does not exist"})) {
		t.Error("an undefined column was treated as retryable, which would hide a broken migration behind five attempts")
	}
	if retryableMigration(errors.New("connection refused")) {
		t.Error("a non-PostgreSQL error was treated as retryable")
	}
}
