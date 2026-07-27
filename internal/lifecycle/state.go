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
	// SnapshotAuthoritative records whether the published snapshot, rather than
	// the database on the active volume, is the authoritative copy of the data.
	// It is the durable answer to the one question generation arithmetic cannot
	// answer: a fence advances identically whether the stack failed *during* a
	// hibernation (the volume holds writes no snapshot has, so restoring one
	// destroys data) or *during* a wake from a released volume (the snapshot is
	// the only copy, so restoring it is mandatory).
	//
	// It is a statement about bytes, never about permission. It becomes true only
	// where the volume has provably stopped being the newest copy: at
	// SNAPSHOTTING → STOPPING, where a verified manifest for this exact fence has
	// been published over a stopped database, and immediately before a restore
	// writes its first byte over the volume. It becomes false again as soon as
	// the restored volume is live under a running persistence process. Every
	// other transition — including an operator's failure acknowledgement and a
	// crash inside recovery — carries it forward unchanged.
	//
	// False is the conservative answer, and it is deliberately the zero value: a
	// control store that does not persist this field therefore reports "never
	// restore", which costs availability and can never destroy data. The opposite
	// polarity would make a forgotten column mean "a serving stack's snapshot is
	// authoritative", which is precisely the state that overwrites live data.
	SnapshotAuthoritative bool
	// RestoreGeneration is the snapshot generation an operator explicitly
	// selected for the attempt that owns the current fence, and zero when no
	// operator selection is outstanding.
	//
	// It is consent, and consent is not a fact about bytes, so it is a separate
	// field rather than a second meaning for SnapshotAuthoritative. Granting
	// authority at the transition — before a single byte had been written — was
	// how a *refused* restore latched "the snapshot is newer than the volume"
	// onto a stack whose volume was strictly newer, so the next unrelated request
	// destroyed it with no operator involved. Consent is therefore scoped to one
	// attempt: it names the exact generation, it is cleared by Fail and by
	// Activate, and it never survives the attempt it was given for.
	//
	// It is carried across a crash on purpose. An attempt that was interrupted is
	// still the attempt the operator asked for, so a replacement process resumes
	// exactly the selected generation rather than guessing.
	RestoreGeneration uint64
}

type StateStore interface {
	Load() (StateRecord, error)
	CompareAndSwap(StateRecord, StateRecord) error
}

type Controller struct {
	mu                    sync.Mutex
	state                 State
	generation            uint64
	wakeDeadline          time.Time
	snapshotAuthoritative bool
	restoreGeneration     uint64
	store                 StateStore
}

// New builds an in-memory controller for a stack a caller asserts is already in
// the named state. Snapshot authority is derived from that state because such a
// caller has said everything it knows: HIBERNATED and STOPPING are exactly the
// states a stack can only have reached by publishing a verified manifest for its
// fence, and every other state holds live writes no snapshot has.
//
// This is not the rule for seeding a durable control store. A seed is the record
// of a deployment that has never run, so it has published nothing and asserts
// nothing; see OpenSQLiteStateStore.
func New(initial State) *Controller {
	return &Controller{state: initial, snapshotAuthoritative: snapshotAuthoritativeFor(initial)}
}

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
	return &Controller{
		state: record.State, generation: record.Generation, wakeDeadline: record.WakeDeadline,
		snapshotAuthoritative: record.SnapshotAuthoritative, restoreGeneration: record.RestoreGeneration, store: store,
	}, nil
}

// snapshotAuthoritativeFor is the answer implied by a state a stack has actually
// reached: HIBERNATED and STOPPING are exactly the states whose fence has a
// verified published manifest, and every other state holds live writes no
// snapshot contains. It answers for an in-memory controller and for a row
// written before the column existed. It must never answer for a seed, because a
// seed describes a deployment that has published nothing at all.
func snapshotAuthoritativeFor(state State) bool {
	switch state {
	case StateHibernated, StateStopping:
		return true
	default:
		return false
	}
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
	return c.recordLocked()
}

// recordLocked is this controller's cached view of the durable record. Every
// transition builds its successor from it, so a field is carried forward unless
// the transition names it — which is the only structure that makes "carried
// through unchanged" a property of the code rather than of nine separate
// argument lists.
func (c *Controller) recordLocked() StateRecord {
	return StateRecord{
		State: c.state, Generation: c.generation, WakeDeadline: c.wakeDeadline,
		SnapshotAuthoritative: c.snapshotAuthoritative, RestoreGeneration: c.restoreGeneration,
	}
}

// carryLocked is the successor record for a transition that changes the state
// and the fence and nothing else.
func (c *Controller) carryLocked(next State, generation uint64) StateRecord {
	record := c.recordLocked()
	record.State, record.Generation = next, generation
	return record
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
	next := c.recordLocked()
	next.WakeDeadline = deadline
	if err := c.persistLocked(next); err != nil {
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
	// Snapshot authority is carried, not reset. A wake elected out of HIBERNATED
	// after an ordinary hibernation carries "true" and restores the published
	// snapshot; a wake elected after an operator acknowledged a failure that
	// happened mid-hibernation, or out of a control store that has never run,
	// carries "false" and must start from the volume.
	//
	// Operator consent is not carried: an ordinary wake is nobody's decision to
	// overwrite a volume, and a HIBERNATED record that still named a selected
	// generation would turn the next inbound request into a restore.
	next := c.carryLocked(StateWaking, nextGeneration)
	next.RestoreGeneration = 0
	if err := c.persistLocked(next); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateWaking
	c.restoreGeneration = 0
	return c.generation, nil
}

// AcknowledgeFailure is an explicit operator action that clears a failed
// lifecycle attempt. It advances the fencing generation before returning the
// stack to hibernated, so processes from the failed attempt cannot re-enter.
// A failed state is never an implicit wake retry.
//
// Snapshot authority is carried through unchanged. Acknowledging a failure says
// "I have seen this and the stack may try again"; it does not say "the snapshot
// is now the newest copy". Recording nothing here is what let an operator
// following the runbook destroy every write made since the previous hibernation:
// the next wake restored a snapshot over an intact, strictly newer volume.
//
// The scheduled wake deadline is carried too, for the reason BeginWake states:
// it is the scheduler's hint about work that is still due, and the failure being
// acknowledged is not the job's fault. Discarding it here left the activator's
// scheduled wake loop with nothing to fire on, so a due message waited for
// unrelated traffic — the inert timer this deadline exists to prevent.
//
// Any operator restore selection is cleared, because Fail already ended the
// attempt it belonged to.
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
	next := c.carryLocked(StateHibernated, nextGeneration)
	next.RestoreGeneration = 0
	if err := c.persistLocked(next); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateHibernated
	c.restoreGeneration = 0
	return nextGeneration, nil
}

// BeginOperatorRestore is the explicit, authenticated selection of a known-good
// snapshot generation that specs/scale-to-zero.md requires in place of an
// implicit fallback. It records the operator's consent to overwrite the active
// volume with exactly that generation, under exactly the fence it returns.
//
// It advances the fence like any other wake election, so processes from the
// failed attempt are fenced out.
//
// It deliberately does NOT touch snapshot authority. Authority is a claim about
// which copy of the data is newer, and at this point nothing has been written:
// the manifest may not exist, the object store may be unreachable, the operator
// may have mistyped the generation. Granting it here made a *refused* restore
// permanent — Fail and AcknowledgeFailure carried the flag forward, and the next
// ordinary request restored a snapshot over a strictly newer volume. Authority
// is granted by Coordinator.wakeAtManifest at the point of no return, one call
// before the first byte is written.
func (c *Controller) BeginOperatorRestore(fence, generation uint64) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation == 0 {
		return 0, ErrGenerationNotRestorable
	}
	if fence != c.generation {
		return 0, ErrStaleFence
	}
	switch c.state {
	case StateFailed, StateHibernated:
	default:
		return 0, ErrInvalidTransition
	}
	nextGeneration := c.generation + 1
	next := c.carryLocked(StateWaking, nextGeneration)
	next.RestoreGeneration = generation
	if err := c.persistLocked(next); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateWaking
	c.restoreGeneration = generation
	return nextGeneration, nil
}

// DeclareSnapshotAuthoritative records that the selected snapshot is about to
// become — or already is — the only complete copy of the data. It is the point
// of no return of a restore: the caller must invoke it immediately before the
// first byte is written over the active volume and must not proceed if it fails.
//
// A crash anywhere after this call leaves a durable record that says "the volume
// is a half-written ruin, restore again", which is the only true answer. That is
// why consent (BeginOperatorRestore) and authority (here) are separate: consent
// can be refused without consequence, authority cannot be taken back.
func (c *Controller) DeclareSnapshotAuthoritative(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	if c.state != StateWaking {
		return ErrInvalidTransition
	}
	if c.snapshotAuthoritative {
		return nil
	}
	next := c.recordLocked()
	next.SnapshotAuthoritative = true
	if err := c.persistLocked(next); err != nil {
		return err
	}
	c.snapshotAuthoritative = true
	return nil
}

// DeclareVolumeAuthoritative records that the active volume holds a complete
// copy again and is live under a running persistence process. From here the
// volume accepts writes no snapshot has, so restoring a snapshot over it would
// destroy them.
//
// The wake path calls it as soon as persistence is up, not at Activate. Between
// those two points the migration runs and the workers start, and those write:
// leaving authority set across that window meant a crash there was recovered by
// restoring the snapshot over the writes, contradicting the invariant this field
// is documented with.
func (c *Controller) DeclareVolumeAuthoritative(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fence != c.generation {
		return ErrStaleFence
	}
	if c.state != StateWaking {
		return ErrInvalidTransition
	}
	if !c.snapshotAuthoritative {
		return nil
	}
	next := c.recordLocked()
	next.SnapshotAuthoritative = false
	if err := c.persistLocked(next); err != nil {
		return err
	}
	c.snapshotAuthoritative = false
	return nil
}

// BeginRecovery re-enters the ordinary fenced wake path after a crash during
// hibernation. The existing generation is already newer than the processes
// that were quiesced, so recovery keeps it and does not invent a new fence.
//
// Snapshot authority is carried through unchanged, and that is what makes the
// persisted WAKING record safe: recovery writes WAKING *before* the live-volume
// start runs, so a second crash leaves a durable WAKING that a fresh process
// resumes through the ordinary wake path. Without the carried flag that path
// restored a snapshot over the volume with no operator involved at all.
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
	if err := c.persistLocked(c.carryLocked(StateWaking, fence)); err != nil {
		return err
	}
	c.state = StateWaking
	return nil
}

// Activate settles the wake. Snapshot authority is false from the moment the
// stack serves — the volume accepts writes that no published snapshot contains —
// and any operator restore selection is spent, because the restore it named has
// completed.
//
// A wake deadline that has already elapsed is consumed here, and only here. It
// demanded a wake, this is that wake, and its time has come and gone; leaving it
// behind kept the activator's scheduled wake loop firing at a stack it had just
// woken. A deadline still in the future is preserved: the job has not run yet,
// and hibernation must still be refused inside its safety window.
func (c *Controller) Activate(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.carryLocked(StateActive, c.generation)
	next.SnapshotAuthoritative = false
	next.RestoreGeneration = 0
	if !next.WakeDeadline.IsZero() && !next.WakeDeadline.After(time.Now().UTC()) {
		next.WakeDeadline = time.Time{}
	}
	return c.transitionLocked(fence, StateWaking, next)
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
	if err := c.persistLocked(c.carryLocked(StateQuiescing, nextGeneration)); err != nil {
		return 0, err
	}
	c.generation = nextGeneration
	c.state = StateQuiescing
	return nextGeneration, nil
}

func (c *Controller) BeginSnapshot(fence uint64) error {
	return c.transition(fence, StateQuiescing, StateSnapshot)
}

// BeginStop is where the snapshot becomes the authoritative copy, and it is the
// only transition other than an explicit operator restore that grants snapshot
// authority. Persistence was stopped inside SNAPSHOTTING and the manifest for
// this fence has been created, verified, and selected as current, so the volume
// and the snapshot now hold exactly the same bytes. That equality is what makes
// granting it safe here rather than after the storage is released: a crash between
// this transition and CompleteHibernate may have released the volume, and a
// stack that could only ever recover from a volume that no longer exists is
// permanently unrecoverable.
func (c *Controller) BeginStop(fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.carryLocked(StateStopping, c.generation)
	next.SnapshotAuthoritative = true
	return c.transitionLocked(fence, StateSnapshot, next)
}

func (c *Controller) CompleteHibernate(fence uint64) error {
	return c.transition(fence, StateStopping, StateHibernated)
}

// Fail records an unrecoverable lifecycle attempt. Like every other
// transition it is conditional on the expected state as well as the fencing
// generation: only an in-progress attempt can fail. An ACTIVE or HIBERNATED
// stack is a settled state that no fence holder may knock over.
//
// Snapshot authority is carried forward: it describes which copy of the data is
// newer, and a failure does not change that. The operator's restore selection is
// cleared, because it described *this* attempt and this attempt is over. A
// refused restore that wrote nothing therefore leaves the record exactly as it
// found it, and the next ordinary wake starts from the volume.
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
	next := c.carryLocked(StateFailed, c.generation)
	next.RestoreGeneration = 0
	if err := c.persistLocked(next); err != nil {
		return err
	}
	c.state = StateFailed
	c.restoreGeneration = 0
	return nil
}

func (c *Controller) transition(fence uint64, from, to State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transitionLocked(fence, from, c.carryLocked(to, c.generation))
}

// transitionLocked applies one fenced, state-conditional move to the successor
// record the caller built from carryLocked. Building the successor from the
// current record is what makes every field carry forward by default; a
// transition that means to change one says so on one line.
func (c *Controller) transitionLocked(fence uint64, from State, next StateRecord) error {
	if fence != c.generation {
		return ErrStaleFence
	}
	if c.state != from {
		return ErrInvalidTransition
	}
	if err := c.persistLocked(next); err != nil {
		return err
	}
	c.state, c.generation = next.State, next.Generation
	c.wakeDeadline, c.snapshotAuthoritative, c.restoreGeneration = next.WakeDeadline, next.SnapshotAuthoritative, next.RestoreGeneration
	return nil
}

// persistLocked applies one fenced compare-and-swap against the durable
// record. The expected value is this controller's cached view, so a losing
// replica must refresh that view before it can make progress again; otherwise
// its expectation stays permanently stale and every later transition — wake,
// activate, fail, hibernate — fails forever against a record that has moved on.
func (c *Controller) persistLocked(next StateRecord) error {
	if c.store == nil {
		return nil
	}
	err := c.store.CompareAndSwap(c.recordLocked(), next)
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
	c.snapshotAuthoritative, c.restoreGeneration = record.SnapshotAuthoritative, record.RestoreGeneration
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
