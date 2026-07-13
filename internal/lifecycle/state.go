package lifecycle

import (
	"errors"
	"sync"
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
)

type StateRecord struct {
	State      State
	Generation uint64
}

type StateStore interface {
	Load() (StateRecord, error)
	CompareAndSwap(StateRecord, StateRecord) error
}

type Controller struct {
	mu         sync.Mutex
	state      State
	generation uint64
	store      StateStore
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
	return &Controller{state: record.State, generation: record.Generation, store: store}, nil
}

func (c *Controller) Snapshot() (State, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, c.generation
}

func (c *Controller) BeginWake() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateActive {
		return c.generation, nil
	}
	if c.state == StateFailed {
		return 0, ErrRecoveryRequired
	}
	if c.state != StateHibernated {
		return 0, ErrWakeInProgress
	}
	nextGeneration := c.generation + 1
	if err := c.persistLocked(StateWaking, nextGeneration); err != nil {
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
	if err := c.persistLocked(StateHibernated, nextGeneration); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateHibernated
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
	if err := c.persistLocked(StateWaking, fence); err != nil {
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
	if err := c.persistLocked(StateQuiescing, nextGeneration); err != nil {
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

func (c *Controller) Fail(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	if err := c.persistLocked(StateFailed, c.generation); err != nil {
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
	if err := c.persistLocked(to, c.generation); err != nil {
		return err
	}
	c.state = to
	return nil
}

func (c *Controller) persistLocked(next State, generation uint64) error {
	if c.store == nil {
		return nil
	}
	return c.store.CompareAndSwap(StateRecord{State: c.state, Generation: c.generation}, StateRecord{State: next, Generation: generation})
}

func validState(state State) bool {
	switch state {
	case StateHibernated, StateWaking, StateActive, StateQuiescing, StateSnapshot, StateStopping, StateFailed:
		return true
	default:
		return false
	}
}
