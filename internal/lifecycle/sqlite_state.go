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
		return nil, errors.Join(err, db.Close())
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
		`CREATE TABLE IF NOT EXISTS lifecycle_state (id INTEGER PRIMARY KEY CHECK (id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL, wake_deadline TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize lifecycle SQLite state store: %w", err)
		}
	}
	var hasWakeDeadline bool
	rows, err := s.db.Query(`PRAGMA table_info(lifecycle_state)`)
	if err != nil {
		return fmt.Errorf("inspect lifecycle SQLite state store schema: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return errors.Join(fmt.Errorf("scan lifecycle SQLite state store schema: %w", err), rows.Close())
		}
		if name == "wake_deadline" {
			hasWakeDeadline = true
		}
	}
	if err := rows.Err(); err != nil {
		return errors.Join(fmt.Errorf("read lifecycle SQLite state store schema: %w", err), rows.Close())
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close lifecycle SQLite state store schema: %w", err)
	}
	if !hasWakeDeadline {
		if _, err := s.db.Exec(`ALTER TABLE lifecycle_state ADD COLUMN wake_deadline TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add lifecycle wake deadline: %w", err)
		}
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO lifecycle_state(id, state, generation, wake_deadline) VALUES (1, ?, ?, ?)`, initial.State, initial.Generation, formatWakeDeadline(initial.WakeDeadline))
	return err
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
	if err := s.db.QueryRow(`SELECT state, generation, wake_deadline FROM lifecycle_state WHERE id = 1`).Scan(&state, &generation, &wakeDeadline); err != nil {
		return StateRecord{}, err
	}
	if !validState(State(state)) {
		return StateRecord{}, errors.New("lifecycle SQLite state store contains an invalid state")
	}
	deadline, err := parseWakeDeadline(wakeDeadline)
	if err != nil {
		return StateRecord{}, err
	}
	return StateRecord{State: State(state), Generation: generation, WakeDeadline: deadline}, nil
}

func (s *SQLiteStateStore) CompareAndSwap(expected, next StateRecord) error {
	if s == nil || s.db == nil {
		return errors.New("lifecycle SQLite state store is not initialized")
	}
	if !validState(expected.State) || !validState(next.State) {
		return errors.New("lifecycle state compare-and-swap contains an invalid state")
	}
	result, err := s.db.Exec(`UPDATE lifecycle_state SET state = ?, generation = ?, wake_deadline = ? WHERE id = 1 AND state = ? AND generation = ? AND wake_deadline = ?`, next.State, next.Generation, formatWakeDeadline(next.WakeDeadline), expected.State, expected.Generation, formatWakeDeadline(expected.WakeDeadline))
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
