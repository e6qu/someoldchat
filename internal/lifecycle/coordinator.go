package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/observability"
)

var ErrRecoveryRequired = errors.New("lifecycle recovery requires an explicit operator or provider action")

// ErrWakeDeadlineWithinSafetyWindow reports that a scheduled wake is due sooner
// than a hibernate-plus-restore cycle can complete, which
// specs/scale-to-zero.md lists as an idle-eligibility precondition.
var ErrWakeDeadlineWithinSafetyWindow = errors.New("scheduled wake deadline falls within the wake safety window")

// ErrGenerationNotRestorable reports that the generation an operator selected is
// not a verified known-good snapshot. It is a distinct sentinel because
// specs/scale-to-zero.md requires the operator's selection to carry its own
// compatibility checks: silently restoring the nearest older generation instead
// would be the implicit fallback the same paragraph forbids.
var ErrGenerationNotRestorable = errors.New("selected snapshot generation is not verified and known-good")

type RuntimeDriver interface {
	Inspect(context.Context, uint64) error
	StartPersistence(context.Context, uint64, Manifest) error
	RunMigration(context.Context, uint64, int) error
	StartWorkers(context.Context, uint64) error
	StartServers(context.Context, uint64) error
	DrainServers(context.Context, uint64) error
	StopWorkers(context.Context, uint64) error
	StopPersistence(context.Context, uint64) error
	ReleaseActiveStorage(context.Context, uint64) error
}

type Snapshotter interface {
	Create(context.Context, uint64) (Manifest, error)
	Current(context.Context, uint64) (Manifest, error)
	LastVerified(context.Context, uint64) (Manifest, error)
	// LiveState describes the database already present on the active volume
	// without reading, downloading, or restoring any snapshot. Recovery of an
	// interrupted hibernation starts from it so an older snapshot can never
	// overwrite newer live data.
	LiveState(context.Context, uint64) (Manifest, error)
	Restore(context.Context, Manifest) error
}

type Coordinator struct {
	Controller *Controller
	Driver     RuntimeDriver
	Snapshots  Snapshotter
	Metrics    *observability.Registry
	// WakeSafetyMargin is the measured restore budget plus margin that a
	// scheduled wake needs. Hibernation is refused inside it.
	WakeSafetyMargin time.Duration
}

func NewCoordinator(controller *Controller, driver RuntimeDriver, snapshots Snapshotter, metrics *observability.Registry, wakeSafetyMargin time.Duration) (Coordinator, error) {
	if controller == nil || driver == nil || snapshots == nil || metrics == nil {
		return Coordinator{}, errors.New("lifecycle coordinator requires controller, runtime driver, snapshotter, and metrics")
	}
	if wakeSafetyMargin <= 0 {
		return Coordinator{}, errors.New("lifecycle coordinator requires a positive wake safety margin")
	}
	return Coordinator{Controller: controller, Driver: driver, Snapshots: snapshots, Metrics: metrics, WakeSafetyMargin: wakeSafetyMargin}, nil
}

func (c Coordinator) Hibernate(ctx context.Context, fence uint64) (Manifest, error) {
	started := time.Now()
	defer func() { c.Metrics.ObserveDuration("sameoldchat_hibernate_duration", time.Since(started)) }()
	// Idle eligibility is checked before the fence advances so a refused
	// hibernation leaves the serving stack exactly as it was.
	// Only a deadline still in the future demands a wake this hibernation could
	// miss. An elapsed one has already been met by the stack that is serving right
	// now, and the scheduled worker owns the job from here; treating it as
	// permanently inside the window refused every later hibernation forever, so a
	// deployment that woke once for a scheduled job stopped scaling to zero and
	// reported it only as repeated 409s.
	if remaining := time.Until(c.Controller.Metadata().WakeDeadline); remaining > 0 && remaining <= c.WakeSafetyMargin {
		c.Metrics.AddCounter("sameoldchat_hibernate_refused_wake_deadline_total", 1)
		return Manifest{}, ErrWakeDeadlineWithinSafetyWindow
	}
	activeFence, err := c.Controller.BeginHibernate(fence)
	if err != nil {
		return Manifest{}, err
	}
	if err := c.Driver.Inspect(ctx, activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	if err := c.Driver.DrainServers(ctx, activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	if err := c.Driver.StopWorkers(ctx, activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	if err := c.Controller.BeginSnapshot(activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	if err := c.Driver.StopPersistence(ctx, activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	snapshotStarted := time.Now()
	manifest, err := c.Snapshots.Create(ctx, activeFence)
	c.Metrics.ObserveDuration("sameoldchat_snapshot_duration", time.Since(snapshotStarted))
	if err != nil {
		c.Metrics.AddCounter("sameoldchat_snapshot_failures_total", 1)
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	c.Metrics.SetGauge("sameoldchat_snapshot_plaintext_bytes", manifest.PlaintextBytes)
	c.Metrics.SetGauge("sameoldchat_snapshot_ciphertext_bytes", manifest.CiphertextBytes)
	c.Metrics.SetGauge("sameoldchat_last_successful_snapshot_unix_seconds", time.Now().UTC().Unix())
	if err := c.Controller.BeginStop(activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	if err := c.Driver.ReleaseActiveStorage(ctx, activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	// The terminal transition fails like every other step. Leaving STOPPING
	// behind with the caller told only "error" hides the one state whose
	// recovery semantics differ from the rest.
	if err := c.Controller.CompleteHibernate(activeFence); err != nil {
		return Manifest{}, errors.Join(err, c.Controller.Fail(activeFence))
	}
	return manifest, nil
}

// WakeAt performs the fenced restore/start work after an outer activator has
// acquired the wake generation. It deliberately does not activate the
// controller; the outer owner completes that transition after this returns.
//
// The restore decision is read from durable snapshot authority, never from the
// state name. WAKING means two incompatible things — "the volume was released,
// the published snapshot is the only copy" and "an interrupted hibernation is
// being resumed, the volume is strictly newer than every snapshot" — and nothing
// in the fencing generation distinguishes them. The flag does.
//
// A snapshot that cannot be selected or verified is terminal: specs/scale-to-zero.md
// requires restore failure to enter FAILED and an older generation to be chosen
// by an explicit operator action, so no fallback is attempted here.
func (c Coordinator) WakeAt(ctx context.Context, fence uint64) error {
	started := time.Now()
	defer func() { c.Metrics.ObserveDuration("sameoldchat_wake_duration", time.Since(started)) }()
	metadata := c.Controller.Metadata()
	if metadata.State != StateWaking || metadata.Generation != fence {
		return ErrInvalidTransition
	}
	if metadata.RestoreGeneration != 0 {
		// This fence carries an operator's selection of a specific generation.
		// Completing it as an ordinary wake would restore whatever happens to be
		// current instead of what was chosen, so the caller must use RestoreAt.
		return ErrInvalidTransition
	}
	if !metadata.SnapshotAuthoritative {
		live, err := c.Snapshots.LiveState(ctx, fence)
		if err != nil {
			return err
		}
		c.Metrics.AddCounter("sameoldchat_recovery_from_live_volume_total", 1)
		return c.wakeAtManifest(ctx, fence, live, false)
	}
	manifest, err := c.Snapshots.Current(ctx, fence)
	if err != nil {
		c.Metrics.AddCounter("sameoldchat_snapshot_selection_failures_total", 1)
		return err
	}
	return c.wakeAtManifest(ctx, fence, manifest, true)
}

// RestoreAt performs the explicit operator-selected restore that
// specs/scale-to-zero.md mandates in place of an implicit fallback. The caller
// must already own the fence through Controller.BeginOperatorRestore, which is
// the transition that records the operator's consent to overwrite the volume.
//
// The consent is matched against the generation being restored, not merely
// against snapshot authority. An ordinary wake out of a hibernated stack also
// carries authority, so checking authority admitted a fence that recorded no
// consent at all — the same conflation that let a refused restore latch the flag
// on and destroy the volume on the next unrelated request.
//
// The selection is exact: a generation that is not itself verified and
// known-good is refused rather than rounded down to the nearest one that is.
func (c Coordinator) RestoreAt(ctx context.Context, fence, generation uint64) error {
	started := time.Now()
	defer func() { c.Metrics.ObserveDuration("sameoldchat_wake_duration", time.Since(started)) }()
	if generation == 0 {
		return ErrGenerationNotRestorable
	}
	metadata := c.Controller.Metadata()
	if metadata.State != StateWaking || metadata.Generation != fence {
		return ErrInvalidTransition
	}
	if metadata.RestoreGeneration != generation {
		return ErrInvalidTransition
	}
	manifest, err := c.Snapshots.LastVerified(ctx, generation)
	if err != nil {
		c.Metrics.AddCounter("sameoldchat_snapshot_selection_failures_total", 1)
		return err
	}
	if manifest.Generation != generation {
		return fmt.Errorf("%w: generation %d", ErrGenerationNotRestorable, generation)
	}
	// specs/scale-to-zero.md requires operator-selected restore generation counts
	// to be published. The old counter meant "an implicit fallback happened",
	// which is no longer a thing that can occur.
	c.Metrics.AddCounter("sameoldchat_operator_selected_restore_total", 1)
	return c.wakeAtManifest(ctx, fence, manifest, true)
}

// wakeAtManifest runs the fenced startup path. When restore is false the
// database already on the active volume is authoritative and no snapshot bytes
// are written over it.
//
// The two durable declarations around the restore stage are what make snapshot
// authority mean the same thing to a replacement process as it does here. The
// first is the point of no return: from the instant Restore may have written a
// byte, the volume is not a complete copy and only the snapshot is, so the
// record must say so before the write, not after. The second is its mirror: once
// persistence is running on the restored volume, the volume is complete and is
// about to accept migration and worker writes no snapshot has, so authority is
// dropped there rather than at Activate several stages later.
func (c Coordinator) wakeAtManifest(ctx context.Context, fence uint64, manifest Manifest, restore bool) error {
	if err := c.runStage(ctx, "inspect", func() error { return c.Driver.Inspect(ctx, fence) }); err != nil {
		return err
	}
	if restore {
		if err := c.Controller.DeclareSnapshotAuthoritative(fence); err != nil {
			return err
		}
		if err := c.runStage(ctx, "restore", func() error { return c.Snapshots.Restore(ctx, manifest) }); err != nil {
			c.Metrics.AddCounter("sameoldchat_restore_failures_total", 1)
			return err
		}
	}
	if err := c.runStage(ctx, "start_persistence", func() error { return c.Driver.StartPersistence(ctx, fence, manifest) }); err != nil {
		return err
	}
	if err := c.Controller.DeclareVolumeAuthoritative(fence); err != nil {
		return err
	}
	c.Metrics.SetGauge("sameoldchat_last_successful_restore_unix_seconds", time.Now().UTC().Unix())
	if err := c.runStage(ctx, "migration", func() error { return c.Driver.RunMigration(ctx, fence, manifest.SchemaVersion) }); err != nil {
		return err
	}
	c.Metrics.SetGauge("sameoldchat_migration_schema_version", int64(manifest.SchemaVersion))
	if err := c.runStage(ctx, "start_workers", func() error { return c.Driver.StartWorkers(ctx, fence) }); err != nil {
		return err
	}
	if err := c.runStage(ctx, "start_servers", func() error { return c.Driver.StartServers(ctx, fence) }); err != nil {
		return err
	}
	return nil
}

func (c Coordinator) runStage(ctx context.Context, name string, operation func() error) error {
	started := time.Now()
	err := operation()
	c.Metrics.ObserveDuration("sameoldchat_wake_stage_"+name, time.Since(started))
	return err
}

// Recover resumes a persisted wake or an interrupted hibernation.
func (c Coordinator) Recover(ctx context.Context) error {
	metadata := c.Controller.Metadata()
	fence := metadata.Generation
	switch metadata.State {
	case StateActive, StateHibernated:
		return nil
	case StateWaking:
		// A WAKING record that names a generation is an operator restore that was
		// interrupted. The operator's selection is the one thing a replacement
		// process must not guess at, so it resumes that exact generation rather
		// than the current manifest.
		resume := c.WakeAt
		if metadata.RestoreGeneration != 0 {
			resume = func(ctx context.Context, fence uint64) error {
				return c.RestoreAt(ctx, fence, metadata.RestoreGeneration)
			}
		}
		if err := resume(ctx, fence); err != nil {
			return errors.Join(err, c.Controller.Fail(fence))
		}
		return c.Controller.Activate(fence)
	case StateQuiescing, StateSnapshot, StateStopping:
		return c.recoverInterruptedHibernate(ctx, fence)
	case StateFailed:
		return ErrRecoveryRequired
	default:
		return ErrInvalidTransition
	}
}

// recoverInterruptedHibernate restarts the stack after a crash between
// QUIESCING and HIBERNATED.
//
// It carries no restore policy of its own. BeginRecovery persists WAKING while
// preserving durable snapshot authority, and WakeAt then makes the same decision
// it makes for any other WAKING record. That is the point: recovery used to
// decide by inspecting the state name it was called with, so the decision was
// lost the moment WAKING was persisted, and a second crash inside recovery
// restored an old snapshot over the live volume with no operator involved.
//
// The two cases the flag encodes here are the ones the states already implied.
// QUIESCING and SNAPSHOTTING published no manifest for this fence, so the volume
// is strictly newer than every snapshot and nothing is restored — "no snapshot
// exists yet" is therefore not fatal, and a crash during the very first
// hibernation recovers normally. STOPPING has a verified manifest for this exact
// fence and may already have released the volume, so it restores it.
func (c Coordinator) recoverInterruptedHibernate(ctx context.Context, fence uint64) error {
	if err := c.Controller.BeginRecovery(fence); err != nil {
		return err
	}
	if err := c.WakeAt(ctx, fence); err != nil {
		return errors.Join(err, c.Controller.Fail(fence))
	}
	return c.Controller.Activate(fence)
}
