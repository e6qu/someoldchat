package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/store"
)

// TestSQLiteOpenHonoursAnOperatorBusyTimeout pins the DSN contract. Open used to
// append its own pragmas and assume the driver resolves duplicates last-wins; it
// does for foreign_keys and not for busy_timeout, so an ordinary operator DSN
// carrying _pragma=busy_timeout(N) made the product refuse to start with an
// error naming an internal invariant rather than the parameter responsible.
func TestSQLiteOpenHonoursAnOperatorBusyTimeout(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	for _, testCase := range []struct {
		name string
		dsn  string
		want int64
	}{
		{"below the floor is raised to it", filepath.Join(directory, "low.db") + "?_pragma=busy_timeout(1)", requiredBusyTimeout},
		{"above the floor is kept", filepath.Join(directory, "high.db") + "?_pragma=busy_timeout(9000)", 9000},
		{"foreign keys cannot be turned off", filepath.Join(directory, "fk.db") + "?_pragma=foreign_keys(0)", requiredBusyTimeout},
		{"unrelated parameters survive", "file:" + filepath.Join(directory, "other.db") + "?mode=rwc&_txlock=immediate", requiredBusyTimeout},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, err := Open(ctx, testCase.dsn)
			if err != nil {
				t.Fatalf("an operator DSN must not stop the product from starting: %v", err)
			}
			defer s.Close()
			var busyTimeout int64
			if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
				t.Fatal(err)
			}
			if busyTimeout != testCase.want {
				t.Fatalf("busy_timeout=%d, want %d", busyTimeout, testCase.want)
			}
			var foreignKeys int64
			if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if foreignKeys != 1 {
				t.Fatal("an operator DSN turned the schema's referential integrity off")
			}
			assertForeignKeysEnforced(t, s)
		})
	}
	// The unrelated parameters really are still there.
	if dsn := sqliteDSN("file:x.db?mode=rwc&cache=shared&_pragma=busy_timeout(1)"); !strings.Contains(dsn, "mode=rwc") || !strings.Contains(dsn, "cache=shared") || strings.Contains(dsn, "busy_timeout(1)") {
		t.Fatalf("sqliteDSN produced %q", dsn)
	}
}

// TestReferentialIntegrityIsVerifiedOnEveryConnection pins the startup guard
// that dqlite never had: the profile is constructed with no pragmas, so it
// skipped every verification, and whether its REFERENCES clauses were enforced
// at all was an assumption written in a comment.
//
// It also covers what that guard immediately found here: a plain ":memory:" DSN
// gives EVERY pooled connection its own private empty database, so the schema
// existed on exactly one of them.
func TestReferentialIntegrityIsVerifiedOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	for _, dsn := range []string{
		filepath.Join(t.TempDir(), "referential.db"),
		":memory:",
		"file::memory:?cache=shared",
	} {
		t.Run(dsn, func(t *testing.T) {
			s, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.VerifyReferentialIntegrity(ctx); err != nil {
				t.Fatalf("referential integrity: %v", err)
			}
			// The same statement the probe uses, through the pool, must classify.
			_, execErr := s.db.ExecContext(ctx, referentialProbeStatement)
			if !errors.Is(classify(execErr), store.ErrNotFound) {
				t.Fatalf("an orphaned row was accepted or misclassified: %v", execErr)
			}
		})
	}
}

// TestReferentialVerificationRefusesAnUnguardedProfile proves the guard fails
// closed rather than reporting success on a handle whose foreign keys are inert
// — the state the dqlite constructor could have been in all along.
func TestReferentialVerificationRefusesAnUnguardedProfile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "inert.db")
	seeded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seeded.Close(); err != nil {
		t.Fatal(err)
	}
	// The same database, opened WITHOUT the foreign-key pragma: exactly what a
	// profile that "owns connection configuration" and never had it checked looks
	// like from here.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	unguarded := &Store{db: db, sqliteDialect: true, now: systemClock}
	err = unguarded.VerifyReferentialIntegrity(ctx)
	if err == nil {
		t.Fatal("a handle with foreign keys off was reported as referentially sound")
	}
	if !strings.Contains(err.Error(), "REFERENCES") {
		t.Fatalf("error=%v, want it to say what is unenforced", err)
	}
}

// TestClassifyDoesNotOverrideATypedError pins the scope of the message fallback.
// It used to run for every error that had not matched, including errors carrying
// a perfectly good machine-readable code that deliberately is not one of the four
// classes, so a broken query whose text quotes a UNIQUE clause was reported to
// the client as "the resource already exists".
func TestClassifyDoesNotOverrideATypedError(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "classify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A real syntax error from the engine whose text contains "unique".
	_, syntaxErr := s.db.ExecContext(ctx, `SELECT unique FROM users`)
	if syntaxErr == nil {
		t.Fatal("the engine accepted a syntactically invalid statement")
	}
	if classified := classify(syntaxErr); errors.Is(classified, store.ErrAlreadyExists) || errors.Is(classified, store.ErrNotFound) || errors.Is(classified, store.ErrInvalidArgument) {
		t.Fatalf("a syntax error mentioning %q was classified as %v", "unique", classified)
	}

	// A postgres-shaped error carrying a SQLSTATE that is deliberately not one of
	// the four classes must survive its own English too.
	exclusion := sqlStateError{state: "23P01", message: "conflicting key value violates exclusion constraint (unique)"}
	if classified := classify(exclusion); errors.Is(classified, store.ErrAlreadyExists) {
		t.Fatalf("SQLSTATE 23P01 was reclassified as %v by its message", classified)
	}
	// The classes that ARE mapped still map.
	if classified := classify(sqlStateError{state: "23505", message: "duplicate key"}); !errors.Is(classified, store.ErrAlreadyExists) {
		t.Fatalf("SQLSTATE 23505 classified as %v", classified)
	}
	// And an error with no machine-readable classification at all still falls
	// back to its message, which is how dqlite reports constraint failures.
	if classified := classify(fmt.Errorf("constraint failed: UNIQUE constraint failed: users.id")); !errors.Is(classified, store.ErrAlreadyExists) {
		t.Fatalf("an untyped constraint failure classified as %v", classified)
	}
}

type sqlStateError struct {
	state   string
	message string
}

func (e sqlStateError) Error() string    { return e.message }
func (e sqlStateError) SQLState() string { return e.state }
