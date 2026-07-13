package lifecycle

import (
	"path/filepath"
	"testing"
)

func TestSQLiteStateStoreSurvivesRestartAndFencesCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	first, err := OpenSQLiteStateStore(path, StateRecord{State: StateHibernated, Generation: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CompareAndSwap(StateRecord{State: StateHibernated, Generation: 4}, StateRecord{State: StateWaking, Generation: 5}); err != nil {
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
	if err := second.CompareAndSwap(StateRecord{State: StateHibernated, Generation: 4}, StateRecord{State: StateActive, Generation: 6}); err != ErrStateConflict {
		t.Fatalf("stale CAS error=%v", err)
	}
}

func TestSQLiteStateStoreFencesConcurrentControllers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	first, err := OpenSQLiteStateStore(path, StateRecord{State: StateHibernated, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenSQLiteStateStore(path, StateRecord{State: StateHibernated, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	expected := StateRecord{State: StateHibernated, Generation: 1}
	next := StateRecord{State: StateWaking, Generation: 2}
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
