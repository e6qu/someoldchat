package lifecycle

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// testStateStore stands in for the durable control store. It is mutex-guarded
// because the interleavings that matter — two replicas, or a replacement process
// resuming a record an abandoned one is still holding — are concurrent by
// definition, and a data race in the fake would hide the behaviour under test.
type testStateStore struct {
	mu     sync.Mutex
	record StateRecord
}

func (s *testStateStore) Load() (StateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}

func (s *testStateStore) load() StateRecord {
	record, _ := s.Load()
	return record
}

func (s *testStateStore) CompareAndSwap(expected, next StateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record != expected {
		return ErrStateConflict
	}
	s.record = next
	return nil
}

func TestScheduledWakeDeadlineIsFencedAndSurvivesControllerRestart(t *testing.T) {
	deadline := time.Date(2026, time.July, 17, 5, 0, 0, 123, time.UTC)
	backend := &testStateStore{record: StateRecord{State: StateActive, Generation: 4}}
	controller, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetWakeDeadline(4, deadline); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetWakeDeadline(3, deadline); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale deadline update error=%v", err)
	}
	restarted, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	metadata := restarted.Metadata()
	if !metadata.WakeDeadline.Equal(deadline) {
		t.Fatalf("wake deadline=%s, want %s", metadata.WakeDeadline, deadline)
	}
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

// BeginWake must never report a won election for a stack that is already
// serving. A caller that treats the already-active answer as ownership holds a
// fence that matches the running generation and can therefore drive a healthy
// stack into FAILED with a single Fail call.
func TestBeginWakeOnActiveStackGrantsNoElection(t *testing.T) {
	c := New(StateHibernated)
	fence, err := c.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Activate(fence); err != nil {
		t.Fatal(err)
	}
	generation, err := c.BeginWake()
	if !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("BeginWake on an active stack err=%v, want ErrAlreadyActive", err)
	}
	if generation != fence {
		t.Fatalf("generation=%d, want the serving generation %d reported alongside the sentinel", generation, fence)
	}
}

// Every transition must be conditional on the expected state as well as the
// generation. Fail was conditional on the generation only, so a fence holder
// could knock over a settled ACTIVE or HIBERNATED stack.
func TestFailRejectsSettledStates(t *testing.T) {
	active := New(StateHibernated)
	fence, err := active.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := active.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if err := active.Fail(fence); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("failing an active stack err=%v, want ErrInvalidTransition", err)
	}
	if state, _ := active.Snapshot(); state != StateActive {
		t.Fatalf("state=%s, want the serving stack intact", state)
	}
	hibernated := New(StateHibernated)
	if err := hibernated.Fail(0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("failing a hibernated stack err=%v, want ErrInvalidTransition", err)
	}
}

func TestFailAcceptsEveryInProgressState(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func() (*Controller, uint64)
	}{
		{name: "waking", setup: func() (*Controller, uint64) {
			c := New(StateHibernated)
			fence, _ := c.BeginWake()
			return c, fence
		}},
		{name: "quiescing", setup: func() (*Controller, uint64) {
			c := New(StateActive)
			fence, _ := c.BeginHibernate(0)
			return c, fence
		}},
		{name: "snapshotting", setup: func() (*Controller, uint64) {
			c := New(StateActive)
			fence, _ := c.BeginHibernate(0)
			_ = c.BeginSnapshot(fence)
			return c, fence
		}},
		{name: "stopping", setup: func() (*Controller, uint64) {
			c := New(StateActive)
			fence, _ := c.BeginHibernate(0)
			_ = c.BeginSnapshot(fence)
			_ = c.BeginStop(fence)
			return c, fence
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c, fence := testCase.setup()
			if err := c.Fail(fence); err != nil {
				t.Fatalf("failing an in-progress attempt err=%v", err)
			}
			if state, _ := c.Snapshot(); state != StateFailed {
				t.Fatalf("state=%s, want failed", state)
			}
		})
	}
}

// Two activator replicas share one durable record. The replica that loses a
// compare-and-swap must adopt the record it lost to; otherwise its cached
// expectation is permanently stale and every later transition it attempts fails
// forever, which behind a load balancer is a permanent partial outage.
func TestLosingReplicaConvergesAfterStateConflict(t *testing.T) {
	backend := &testStateStore{record: StateRecord{State: StateHibernated}}
	winner, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	loser, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := winner.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	// The loser must see the winner's in-progress wake, which is a retryable
	// condition, rather than a compare-and-swap conflict it can never clear.
	if _, err := loser.BeginWake(); !errors.Is(err, ErrWakeInProgress) {
		t.Fatalf("losing replica err=%v, want the durable in-progress wake", err)
	}
	if state, generation := loser.Snapshot(); state != StateWaking || generation != fence {
		t.Fatalf("losing replica state=%s generation=%d, want the durable record", state, generation)
	}
	if err := winner.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if _, err := loser.BeginWake(); !errors.Is(err, ErrAlreadyActive) {
		t.Fatalf("after activation err=%v, want the loser to observe the serving stack", err)
	}
}

// A lost compare-and-swap must also refresh the cached expectation, so a
// transition this replica initiated and lost does not leave it writing against a
// record that has moved on.
func TestLostCompareAndSwapRefreshesTheCachedExpectation(t *testing.T) {
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
	if err := second.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if err := first.Activate(fence); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale controller err=%v, want ErrStateConflict", err)
	}
	if state, generation := first.Snapshot(); state != StateActive || generation != fence {
		t.Fatalf("state=%s generation=%d, want the durable record adopted after the conflict", state, generation)
	}
	// Without the refresh this hibernation would fail forever against the stale
	// waking/fence expectation.
	if _, err := first.BeginHibernate(fence); err != nil {
		t.Fatalf("hibernate after conflict err=%v, want the refreshed replica to make progress", err)
	}
}

// The scheduled wake deadline is a hint owned by the scheduler. BeginWake
// cleared it, so the reason the wake was required disappeared before the job
// could run and no later hibernation could see it either.
func TestBeginWakePreservesTheScheduledWakeDeadline(t *testing.T) {
	deadline := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	backend := &testStateStore{record: StateRecord{State: StateActive, Generation: 7}}
	controller, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.SetWakeDeadline(7, deadline); err != nil {
		t.Fatal(err)
	}
	fence, err := controller.BeginHibernate(7)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(uint64) error{controller.BeginSnapshot, controller.BeginStop, controller.CompleteHibernate} {
		if err := step(fence); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := controller.BeginWake(); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.Equal(deadline) {
		t.Fatalf("wake deadline=%s, want %s preserved across the wake", got, deadline)
	}
	if got := backend.record.WakeDeadline; !got.Equal(deadline) {
		t.Fatalf("durable wake deadline=%s, want %s", got, deadline)
	}
}

// Snapshot authority must be produced and consumed at exactly the two points where
// the answer changes, and carried unchanged everywhere else. A per-case
// conditional in the wake path cannot express that; only the durable record can,
// which is why the whole transition table is pinned here rather than in the one
// or two transitions a bug was observed in.
func TestVolumeAuthorityIsCarriedThroughEveryTransition(t *testing.T) {
	c := New(StateHibernated)
	if !c.Metadata().SnapshotAuthoritative {
		t.Fatal("a hibernated stack released its volume; the snapshot is the only copy")
	}
	fence, err := c.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Metadata().SnapshotAuthoritative {
		t.Fatal("a wake elected out of hibernation must still restore its snapshot")
	}
	if err := c.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if c.Metadata().SnapshotAuthoritative {
		t.Fatal("a serving stack accepts writes no snapshot has")
	}
	hibernateFence, err := c.BeginHibernate(fence)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata().SnapshotAuthoritative {
		t.Fatal("quiescing publishes no manifest, so the volume is still the only copy")
	}
	if err := c.BeginSnapshot(hibernateFence); err != nil {
		t.Fatal(err)
	}
	if c.Metadata().SnapshotAuthoritative {
		t.Fatal("snapshotting has not published a manifest yet")
	}
	if err := c.BeginStop(hibernateFence); err != nil {
		t.Fatal(err)
	}
	if !c.Metadata().SnapshotAuthoritative {
		t.Fatal("stopping has a verified published manifest for this exact fence")
	}
	if err := c.CompleteHibernate(hibernateFence); err != nil {
		t.Fatal(err)
	}
	if !c.Metadata().SnapshotAuthoritative {
		t.Fatal("a completed hibernation released the volume")
	}
}

// The recorded data-loss path, at the level of the state machine: a failure that
// happened while the volume was the only copy must still say so after an operator
// has acknowledged it and after the next wake election.
func TestAcknowledgedFailureAndTheNextWakeKeepVolumeAuthority(t *testing.T) {
	c := New(StateActive)
	fence, err := c.BeginHibernate(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BeginSnapshot(fence); err != nil {
		t.Fatal(err)
	}
	if err := c.Fail(fence); err != nil {
		t.Fatal(err)
	}
	if c.Metadata().SnapshotAuthoritative {
		t.Fatal("failing mid-hibernation forgot that the volume is the only copy")
	}
	acknowledged, err := c.AcknowledgeFailure(fence)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata().SnapshotAuthoritative {
		t.Fatal("acknowledging the failure forgot that the volume is the only copy")
	}
	wakeFence, err := c.BeginWake()
	if err != nil || wakeFence != acknowledged+1 {
		t.Fatalf("wake fence=%d err=%v", wakeFence, err)
	}
	if c.Metadata().SnapshotAuthoritative {
		t.Fatal("the wake after an acknowledged mid-hibernation failure would restore over the volume")
	}
}

// A recovery that persists WAKING must persist snapshot authority with it,
// because that record — not the call stack that wrote it — is what a replacement
// process reads.
func TestBeginRecoveryPersistsVolumeAuthorityWithTheWakingRecord(t *testing.T) {
	backend := &testStateStore{record: StateRecord{State: StateActive, Generation: 3}}
	c, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := c.BeginHibernate(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BeginRecovery(fence); err != nil {
		t.Fatal(err)
	}
	record := backend.load()
	if record.State != StateWaking || record.Generation != fence || record.SnapshotAuthoritative {
		t.Fatalf("durable record=%+v, want a WAKING record that still names the volume authoritative", record)
	}
}

// The explicit operator restore is the only transition that may declare the
// snapshot authoritative without a hibernation having published one, and it must
// advance the fence so the failed attempt's processes cannot re-enter.
func TestOperatorRestoreRecordsConsentAndAdvancesTheFence(t *testing.T) {
	c := New(StateActive)
	hibernateFence, err := c.BeginHibernate(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Fail(hibernateFence); err != nil {
		t.Fatal(err)
	}
	restoreFence, err := c.BeginOperatorRestore(hibernateFence)
	if err != nil {
		t.Fatal(err)
	}
	if restoreFence != hibernateFence+1 {
		t.Fatalf("restore fence=%d, want %d", restoreFence, hibernateFence+1)
	}
	metadata := c.Metadata()
	if metadata.State != StateWaking || !metadata.SnapshotAuthoritative {
		t.Fatalf("metadata=%+v, want a waking record with the operator's consent recorded", metadata)
	}
	if _, err := c.BeginOperatorRestore(hibernateFence); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale operator restore err=%v, want %v", err, ErrStaleFence)
	}
}

func TestOperatorRestoreRejectsAnInProgressAttempt(t *testing.T) {
	c := New(StateHibernated)
	fence, err := c.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginOperatorRestore(fence); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("operator restore during a wake err=%v, want %v", err, ErrInvalidTransition)
	}
}
