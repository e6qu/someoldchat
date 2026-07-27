package lifecycle

import (
	"errors"
	"sync"
	"time"
)

type State string

const (
	StateHibernated State = "hibernated"
	StateWaking     State = "waking"
	StateActive     State = "active"
	StateQuiescing  State = "quiescing"
	StateSnapshot   State = "snapshotting"
	StateStopping   State = "stopping"
	StateFailed     State = "failed"
)

var (
	ErrInvalidTransition = errors.New("invalid lifecycle transition")
	ErrStaleFence        = errors.New("stale lifecycle fence")
	ErrWakeInProgress    = errors.New("wake already in progress")
	ErrStateConflict     = errors.New("lifecycle state compare-and-swap conflict")
	// ErrAlreadyActive reports that the stack is serving, so no wake election
	// was held and the caller owns no wake generation. It is a distinct
	// sentinel because a caller that mistakes it for a won election drives the
	// running stack into FAILED.
	ErrAlreadyActive = errors.New("lifecycle stack is already active")
)

type StateRecord struct {
	State        State
	Generation   uint64
	WakeDeadline time.Time
}

type StateStore interface {
	Load() (StateRecord, error)
	CompareAndSwap(StateRecord, StateRecord) error
}

type Controller struct {
	mu           sync.Mutex
	state        State
	generation   uint64
	wakeDeadline time.Time
	store        StateStore
}

func New(initial State) *Controller { return &Controller{state: initial} }

func NewPersistent(store StateStore) (*Controller, error) {
	if store == nil {
		return nil, errors.New("persistent lifecycle controller requires a state store")
	}
	record, err := store.Load()
	if err != nil {
		return nil, err
	}
	if !validState(record.State) {
		return nil, errors.New("persistent lifecycle state is invalid")
	}
	return &Controller{state: record.State, generation: record.Generation, wakeDeadline: record.WakeDeadline, store: store}, nil
}

func (c *Controller) Snapshot() (State, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.generation
}

// Metadata returns the durable lifecycle state and scheduled wake hint.
// Callers receive a value copy that cannot mutate controller state.
func (c *Controller) Metadata() StateRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return StateRecord{State: c.state, Generation: c.generation, WakeDeadline: c.wakeDeadline}
}

// SetWakeDeadline publishes the earliest required wake time for the current
// fencing generation. A zero deadline clears the hint after the job has been
// consumed or cancelled.
func (c *Controller) SetWakeDeadline(fence uint64, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	deadline = deadline.UTC()
	if err := c.persistLocked(c.state, c.generation, deadline); err != nil {
		return err
	}
	c.wakeDeadline = deadline
	return nil
}

// BeginWake elects exactly one owner of a new wake generation. Only a nil
// error grants ownership: an already-active stack yields ErrAlreadyActive with
// the serving generation, so a caller that lost the race cannot mistake it for
// a won election and drive the running stack into FAILED.
//
// The scheduled wake deadline is preserved across the transition. It is a hint
// owned by the scheduler, and clearing it here would silently discard the
// reason this wake was required before the job had a chance to run.
func (c *Controller) BeginWake() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The election is the one decision that must see other replicas' progress:
	// a cached view can only ever report the state this replica last wrote, so
	// without a read-through a replica that lost one race never learns that the
	// stack came up and answers every later request from a stale expectation.
	if err := c.reloadLocked(); err != nil {
		return 0, err
	}
	if c.state == StateActive {
		return c.generation, ErrAlreadyActive
	}
	if c.state == StateFailed {
		return 0, ErrRecoveryRequired
	}
	if c.state != StateHibernated {
		return 0, ErrWakeInProgress
	}
	nextGeneration := c.generation + 1
	if err := c.persistLocked(StateWaking, nextGeneration, c.wakeDeadline); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateWaking
	return c.generation, nil
}

// AcknowledgeFailure is an explicit operator action that clears a failed
// lifecycle attempt. It advances the fencing generation before returning the
// stack to hibernated, so processes from the failed attempt cannot re-enter.
// A failed state is never an implicit wake retry.
func (c *Controller) AcknowledgeFailure(fence uint64) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return 0, ErrStaleFence
	}
	if c.state != StateFailed {
		return 0, ErrInvalidTransition
	}
	nextGeneration := c.generation + 1
	if err := c.persistLocked(StateHibernated, nextGeneration, time.Time{}); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateHibernated
	c.wakeDeadline = time.Time{}
	return nextGeneration, nil
}

// BeginRecovery re-enters the ordinary fenced wake path after a crash during
// hibernation. The existing generation is already newer than the processes
// that were quiesced, so recovery keeps it and does not invent a new fence.
func (c *Controller) BeginRecovery(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	switch c.state {
	case StateQuiescing, StateSnapshot, StateStopping:
	default:
		return ErrInvalidTransition
	}
	if err := c.persistLocked(StateWaking, fence, c.wakeDeadline); err != nil {
		return err
	}
	c.state = StateWaking
	return nil
}

func (c *Controller) Activate(fence uint64) error {
	return c.transition(fence, StateWaking, StateActive)
}

func (c *Controller) BeginHibernate(fence uint64) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return 0, ErrStaleFence
	}
	if c.state != StateActive {
		return 0, ErrInvalidTransition
	}
	nextGeneration := c.generation + 1
	if err := c.persistLocked(StateQuiescing, nextGeneration, c.wakeDeadline); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateQuiescing
	return nextGeneration, nil
}

func (c *Controller) BeginSnapshot(fence uint64) error {
	return c.transition(fence, StateQuiescing, StateSnapshot)
}

func (c *Controller) BeginStop(fence uint64) error {
	return c.transition(fence, StateSnapshot, StateStopping)
}

func (c *Controller) CompleteHibernate(fence uint64) error {
	return c.transition(fence, StateStopping, StateHibernated)
}

// Fail records an unrecoverable lifecycle attempt. Like every other
// transition it is conditional on the expected state as well as the fencing
// generation: only an in-progress attempt can fail. An ACTIVE or HIBERNATED
// stack is a settled state that no fence holder may knock over.
func (c *Controller) Fail(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	switch c.state {
	case StateWaking, StateQuiescing, StateSnapshot, StateStopping:
	default:
		return ErrInvalidTransition
	}
	if err := c.persistLocked(StateFailed, c.generation, c.wakeDeadline); err != nil {
		return err
	}
	c.state = StateFailed
	return nil
}

func (c *Controller) transition(fence uint64, from, to State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	if c.state != from {
		return ErrInvalidTransition
	}
	if err := c.persistLocked(to, c.generation, c.wakeDeadline); err != nil {
		return err
	}
	c.state = to
	return nil
}

// persistLocked applies one fenced compare-and-swap against the durable
// record. The expected value is this controller's cached view, so a losing
// replica must refresh that view before it can make progress again; otherwise
// its expectation stays permanently stale and every later transition — wake,
// activate, fail, hibernate — fails forever against a record that has moved on.
func (c *Controller) persistLocked(next State, generation uint64, wakeDeadline time.Time) error {
	if c.store == nil {
		return nil
	}
	err := c.store.CompareAndSwap(
		StateRecord{State: c.state, Generation: c.generation, WakeDeadline: c.wakeDeadline},
		StateRecord{State: next, Generation: generation, WakeDeadline: wakeDeadline},
	)
	if errors.Is(err, ErrStateConflict) {
		return errors.Join(err, c.reloadLocked())
	}
	return err
}

// reloadLocked adopts the durable record after a lost compare-and-swap so the
// caller's next attempt is evaluated against reality. A reload failure is
// surfaced, never swallowed: an unreadable control store is not an empty one.
func (c *Controller) reloadLocked() error {
	if c.store == nil {
		return nil
	}
	record, err := c.store.Load()
	if err != nil {
		return err
	}
	if !validState(record.State) {
		return errors.New("durable lifecycle state is invalid")
	}
	c.state, c.generation, c.wakeDeadline = record.State, record.Generation, record.WakeDeadline
	return nil
}

func validState(state State) bool {
	switch state {
	case StateHibernated, StateWaking, StateActive, StateQuiescing, StateSnapshot, StateStopping, StateFailed:
		return true
	default:
		return false
	}
}
