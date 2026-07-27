package lifecycle

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStateStore is the small durable control store used while the chat
// database is absent. It contains lifecycle metadata only; tenant data never
// belongs in this store.
type SQLiteStateStore struct {
	db *sql.DB
}

// OpenSQLiteStateStore opens or creates the control store and seeds it with
// initial if and only if it has never been written.
//
// The seed is stored verbatim. It describes a deployment that has never run, so
// it has published no manifest and its snapshot authority is whatever the caller
// asked for — which for a fresh deployment is the zero value, false. Deriving
// authority from the seeded state instead made every brand-new deployment
// HIBERNATED-with-authority: the first wake took the restore branch, found no
// current.json, went FAILED, and recovery carried the flag forward so it failed
// identically forever. A deployment that cannot start is not a conservative
// answer, and there was no runbook out of it.
func OpenSQLiteStateStore(dsn string, initial StateRecord) (*SQLiteStateStore, error) {
	if dsn == "" {
		return nil, errors.New("lifecycle SQLite state store requires a DSN")
	}
	if !validState(initial.State) {
		return nil, errors.New("lifecycle SQLite state store requires a valid initial state")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open lifecycle SQLite state store: %w", err)
	}
	store := &SQLiteStateStore{db: db}
	if err := store.initialize(initial); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStateStore) initialize(initial StateRecord) error {
	if s == nil || s.db == nil {
		return errors.New("lifecycle SQLite state store is not initialized")
	}
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS lifecycle_state (id INTEGER PRIMARY KEY CHECK (id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL, wake_deadline TEXT NOT NULL DEFAULT '', snapshot_authoritative INTEGER NOT NULL DEFAULT 0, restore_generation INTEGER NOT NULL DEFAULT 0)`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize lifecycle SQLite state store: %w", err)
		}
	}
	columns, err := s.columnNames()
	if err != nil {
		return err
	}
	if !columns["wake_deadline"] {
		if _, err := s.db.Exec(`ALTER TABLE lifecycle_state ADD COLUMN wake_deadline TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add lifecycle wake deadline: %w", err)
		}
	}
	if !columns["snapshot_authoritative"] {
		// The column default is the conservative answer, so a row written before
		// this column existed decodes as "the volume holds writes no snapshot
		// has" and cannot be restored over. The one-time UPDATE then grants
		// authority to the two states whose fence provably published a manifest.
		if _, err := s.db.Exec(`ALTER TABLE lifecycle_state ADD COLUMN snapshot_authoritative INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add lifecycle snapshot authority: %w", err)
		}
		if _, err := s.db.Exec(`UPDATE lifecycle_state SET snapshot_authoritative = 1 WHERE state IN (?, ?)`, string(StateHibernated), string(StateStopping)); err != nil {
			return fmt.Errorf("migrate lifecycle snapshot authority: %w", err)
		}
	}
	if !columns["restore_generation"] {
		// Zero is "no operator restore is outstanding", which is the truth for
		// every row written before the column existed: the transition that
		// records a selection is the one this column was added for.
		if _, err := s.db.Exec(`ALTER TABLE lifecycle_state ADD COLUMN restore_generation INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add lifecycle operator restore selection: %w", err)
		}
	}
	// The seed is written verbatim; see OpenSQLiteStateStore for why deriving
	// snapshot authority from the seeded state bricks a fresh deployment.
	_, err = s.db.Exec(
		`INSERT OR IGNORE INTO lifecycle_state(id, state, generation, wake_deadline, snapshot_authoritative, restore_generation) VALUES (1, ?, ?, ?, ?, ?)`,
		initial.State, initial.Generation, formatWakeDeadline(initial.WakeDeadline), boolToInt(initial.SnapshotAuthoritative), initial.RestoreGeneration,
	)
	return err
}

func (s *SQLiteStateStore) columnNames() (map[string]bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(lifecycle_state)`)
	if err != nil {
		return nil, fmt.Errorf("inspect lifecycle SQLite state store schema: %w", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan lifecycle SQLite state store schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read lifecycle SQLite state store schema: %w", err)
	}
	return columns, nil
}

func (s *SQLiteStateStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStateStore) Load() (StateRecord, error) {
	if s == nil || s.db == nil {
		return StateRecord{}, errors.New("lifecycle SQLite state store is not initialized")
	}
	var state string
	var generation uint64
	var wakeDeadline string
	var snapshotAuthoritative int
	var restoreGeneration uint64
	if err := s.db.QueryRow(`SELECT state, generation, wake_deadline, snapshot_authoritative, restore_generation FROM lifecycle_state WHERE id = 1`).Scan(&state, &generation, &wakeDeadline, &snapshotAuthoritative, &restoreGeneration); err != nil {
		return StateRecord{}, err
	}
	if !validState(State(state)) {
		return StateRecord{}, errors.New("lifecycle SQLite state store contains an invalid state")
	}
	deadline, err := parseWakeDeadline(wakeDeadline)
	if err != nil {
		return StateRecord{}, err
	}
	return StateRecord{
		State: State(state), Generation: generation, WakeDeadline: deadline,
		SnapshotAuthoritative: snapshotAuthoritative != 0, RestoreGeneration: restoreGeneration,
	}, nil
}

func (s *SQLiteStateStore) CompareAndSwap(expected, next StateRecord) error {
	if s == nil || s.db == nil {
		return errors.New("lifecycle SQLite state store is not initialized")
	}
	if !validState(expected.State) || !validState(next.State) {
		return errors.New("lifecycle state compare-and-swap contains an invalid state")
	}
	// Snapshot authority and the operator's restore selection are part of both
	// halves of the swap: between them they decide whether a wake may write a
	// snapshot over the active volume, so a replica whose cached view of either is
	// stale must lose the swap rather than silently overwrite the winner's answer.
	result, err := s.db.Exec(
		`UPDATE lifecycle_state SET state = ?, generation = ?, wake_deadline = ?, snapshot_authoritative = ?, restore_generation = ? WHERE id = 1 AND state = ? AND generation = ? AND wake_deadline = ? AND snapshot_authoritative = ? AND restore_generation = ?`,
		next.State, next.Generation, formatWakeDeadline(next.WakeDeadline), boolToInt(next.SnapshotAuthoritative), next.RestoreGeneration,
		expected.State, expected.Generation, formatWakeDeadline(expected.WakeDeadline), boolToInt(expected.SnapshotAuthoritative), expected.RestoreGeneration,
	)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrStateConflict
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatWakeDeadline(deadline time.Time) string {
	if deadline.IsZero() {
		return ""
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}

func parseWakeDeadline(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode lifecycle wake deadline: %w", err)
	}
	return deadline, nil
}
