package lifecycle

import (
	"errors"
	"testing"
)

type testStateStore struct{ record StateRecord }

func (s *testStateStore) Load() (StateRecord, error) { return s.record, nil }
func (s *testStateStore) CompareAndSwap(expected, next StateRecord) error {
	if s.record != expected {
		return ErrStateConflict
	}
	s.record = next
	return nil
}

func TestHibernateAndWakeTransitions(t *testing.T) {
	c := New(StateHibernated)
	fence, err := c.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Activate(fence); err != nil {
		t.Fatal(err)
	}
	hibernateFence, err := c.BeginHibernate(fence)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(uint64) error{c.BeginSnapshot, c.BeginStop, c.CompleteHibernate} {
		if err := step(hibernateFence); err != nil {
			t.Fatal(err)
		}
	}
	state, generation := c.Snapshot()
	if state != StateHibernated || generation != hibernateFence {
		t.Fatalf("state=%s generation=%d", state, generation)
	}
}

func TestSecondWakeIsRejectedUntilFirstCompletes(t *testing.T) {
	c := New(StateHibernated)
	fence, err := c.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginWake(); !errors.Is(err, ErrWakeInProgress) {
		t.Fatalf("err=%v", err)
	}
	if err := c.Activate(fence + 1); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("err=%v", err)
	}
}

func TestFailedWakeRequiresExplicitAcknowledgement(t *testing.T) {
	c := New(StateHibernated)
	fence, _ := c.BeginWake()
	if err := c.Fail(fence); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginWake(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("automatic wake after failure err=%v", err)
	}
	next, err := c.AcknowledgeFailure(fence)
	if err != nil || next == fence {
		t.Fatalf("acknowledged generation=%d err=%v", next, err)
	}
	wakeFence, err := c.BeginWake()
	if err != nil || wakeFence == next {
		t.Fatalf("explicitly restarted generation=%d err=%v", wakeFence, err)
	}
}

func TestPersistentControllerSurvivesControllerRestart(t *testing.T) {
	backend := &testStateStore{record: StateRecord{State: StateHibernated}}
	first, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := first.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	state, generation := second.Snapshot()
	if state != StateWaking || generation != fence {
		t.Fatalf("state=%s generation=%d", state, generation)
	}
	if err := second.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if err := first.Activate(fence); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale controller error=%v", err)
	}
}
