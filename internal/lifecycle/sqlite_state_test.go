package lifecycle

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// hibernatedRecord is the durable record a fresh control store is seeded with:
// a hibernated stack released its volume, so the published snapshot is the only
// copy and restoring it is required.
func hibernatedRecord(generation uint64) StateRecord {
	return StateRecord{State: StateHibernated, Generation: generation, SnapshotAuthoritative: true}
}

func TestSQLiteStateStoreSurvivesRestartAndFencesCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	first, err := OpenSQLiteStateStore(path, hibernatedRecord(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CompareAndSwap(hibernatedRecord(4), StateRecord{State: StateWaking, Generation: 5}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenSQLiteStateStore(path, StateRecord{State: StateActive, Generation: 99})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	record, err := second.Load()
	if err != nil || record.State != StateWaking || record.Generation != 5 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if err := second.CompareAndSwap(hibernatedRecord(4), StateRecord{State: StateActive, Generation: 6}); err != ErrStateConflict {
		t.Fatalf("stale CAS error=%v", err)
	}
}

func TestSQLiteStateStorePersistsWakeDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	deadline := time.Date(2026, time.July, 17, 5, 0, 0, 123, time.UTC)
	store, err := OpenSQLiteStateStore(path, hibernatedRecord(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(
		hibernatedRecord(4),
		StateRecord{State: StateHibernated, Generation: 4, WakeDeadline: deadline},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenSQLiteStateStore(path, StateRecord{State: StateActive, Generation: 99})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	record, err := restarted.Load()
	if err != nil || !record.WakeDeadline.Equal(deadline) {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

// Snapshot authority decides whether a wake may write a snapshot over the active
// volume, so it must round-trip durably and must be part of the compare-and-swap
// predicate. A replica whose cached answer is stale has to lose the swap; if it
// could win, it would overwrite the winner's answer and the next wake would make
// the decision from a value nobody wrote.
func TestSQLiteStateStorePersistsAndFencesSnapshotAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	store, err := OpenSQLiteStateStore(path, hibernatedRecord(4))
	if err != nil {
		t.Fatal(err)
	}
	serving := StateRecord{State: StateActive, Generation: 5}
	if err := store.CompareAndSwap(hibernatedRecord(4), serving); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenSQLiteStateStore(path, hibernatedRecord(99))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	record, err := restarted.Load()
	if err != nil || record != serving {
		t.Fatalf("record=%+v err=%v, want %+v", record, err, serving)
	}
	stale := StateRecord{State: StateActive, Generation: 5, SnapshotAuthoritative: true}
	if err := restarted.CompareAndSwap(stale, StateRecord{State: StateQuiescing, Generation: 6}); err != ErrStateConflict {
		t.Fatalf("stale volume-authority CAS error=%v, want %v", err, ErrStateConflict)
	}
}

// A control store written before the column existed carries no answer. The
// column default supplies the conservative one — assume the volume holds writes
// no snapshot has — and a one-time migration grants authority only to the two
// states whose fence provably published a manifest. Guessing in the other
// direction destroys the data.
func TestSQLiteStateStoreMigratesLegacyRowsConservatively(t *testing.T) {
	for _, testCase := range []struct {
		state State
		want  bool
	}{
		{state: StateActive, want: false},
		{state: StateQuiescing, want: false},
		{state: StateSnapshot, want: false},
		{state: StateWaking, want: false},
		{state: StateFailed, want: false},
		{state: StateStopping, want: true},
		{state: StateHibernated, want: true},
	} {
		t.Run(string(testCase.state), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			legacy, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				`CREATE TABLE lifecycle_state (id INTEGER PRIMARY KEY CHECK (id = 1), state TEXT NOT NULL, generation INTEGER NOT NULL, wake_deadline TEXT NOT NULL DEFAULT '')`,
				`INSERT INTO lifecycle_state(id, state, generation) VALUES (1, '` + string(testCase.state) + `', 7)`,
			} {
				if _, err := legacy.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenSQLiteStateStore(path, StateRecord{State: StateHibernated})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			record, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if record.State != testCase.state || record.Generation != 7 || record.SnapshotAuthoritative != testCase.want {
				t.Fatalf("record=%+v, want %s at generation 7 with snapshot authority %t", record, testCase.state, testCase.want)
			}
		})
	}
}

func TestSQLiteStateStoreFencesConcurrentControllers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	first, err := OpenSQLiteStateStore(path, hibernatedRecord(1))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenSQLiteStateStore(path, hibernatedRecord(1))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	expected := hibernatedRecord(1)
	next := StateRecord{State: StateWaking, Generation: 2, SnapshotAuthoritative: true}
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() { firstErr <- first.CompareAndSwap(expected, next) }()
	go func() { secondErr <- second.CompareAndSwap(expected, next) }()
	var successes int
	for _, result := range []error{<-firstErr, <-secondErr} {
		if result == nil {
			successes++
		} else if result != ErrStateConflict {
			t.Fatalf("unexpected CAS error=%v", result)
		}
	}
	if successes != 1 {
		t.Fatalf("successful CAS operations=%d, want 1", successes)
	}
}
