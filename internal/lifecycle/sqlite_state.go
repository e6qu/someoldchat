package lifecycle

import (
	"database/sql"
	"errors"
	"fmt"

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
		`CREATE TABLE IF NOT EXISTS lifecycle_state (id INTEGER PRIMARY KEY CHECK (id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL)`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize lifecycle SQLite state store: %w", err)
		}
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO lifecycle_state(id, state, generation) VALUES (1, ?, ?)`, initial.State, initial.Generation)
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
	if err := s.db.QueryRow(`SELECT state, generation FROM lifecycle_state WHERE id = 1`).Scan(&state, &generation); err != nil {
		return StateRecord{}, err
	}
	if !validState(State(state)) {
		return StateRecord{}, errors.New("lifecycle SQLite state store contains an invalid state")
	}
	return StateRecord{State: State(state), Generation: generation}, nil
}

func (s *SQLiteStateStore) CompareAndSwap(expected, next StateRecord) error {
	if s == nil || s.db == nil {
		return errors.New("lifecycle SQLite state store is not initialized")
	}
	if !validState(expected.State) || !validState(next.State) {
		return errors.New("lifecycle state compare-and-swap contains an invalid state")
	}
	result, err := s.db.Exec(`UPDATE lifecycle_state SET state = ?, generation = ? WHERE id = 1 AND state = ? AND generation = ?`, next.State, next.Generation, expected.State, expected.Generation)
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
