package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/observability"
)

var ErrRecoveryRequired = errors.New("lifecycle recovery requires an explicit operator or provider action")

// ErrWakeDeadlineWithinSafetyWindow reports that a scheduled wake is due sooner
// than a hibernate-plus-restore cycle can complete, which
// specs/scale-to-zero.md lists as an idle-eligibility precondition.
var ErrWakeDeadlineWithinSafetyWindow = errors.New("scheduled wake deadline falls within the wake safety window")

// errRestoreStage marks a failure of the restore stage specifically, so the
// documented recovery policy can walk back to an older known-good generation
// without also retrying failures that no other snapshot would fix.
var errRestoreStage = errors.New("snapshot restore stage failed")

// maxRestoreGenerations bounds the "select older compatible known-good
// generations in order" recovery policy. The budget is spent before the stack
// is declared FAILED.
const maxRestoreGenerations = 4

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
	if deadline := c.Controller.Metadata().WakeDeadline; !deadline.IsZero() && time.Until(deadline) <= c.WakeSafetyMargin {
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
func (c Coordinator) WakeAt(ctx context.Context, fence uint64) error {
	started := time.Now()
	defer func() { c.Metrics.ObserveDuration("sameoldchat_wake_duration", time.Since(started)) }()
	state, generation := c.Controller.Snapshot()
	if state != StateWaking || generation != fence {
		return ErrInvalidTransition
	}
	manifest, err := c.Snapshots.Current(ctx, fence)
	if err != nil {
		// A missing or unverifiable current.json is exactly the case the
		// recovery policy exists for: fall back to the newest generation that
		// verifies rather than declaring the stack unrecoverable.
		c.Metrics.AddCounter("sameoldchat_snapshot_selection_failures_total", 1)
		older, selectErr := c.Snapshots.LastVerified(ctx, fence)
		if selectErr != nil {
			return errors.Join(err, selectErr)
		}
		c.Metrics.AddCounter("sameoldchat_restore_selected_older_generation_total", 1)
		manifest = older
	}
	return c.wakeFromSnapshot(ctx, fence, manifest)
}

// wakeFromSnapshot restores the selected generation and, when the restore
// itself fails, walks back through older known-good generations within a
// bounded budget, as specs/scale-to-zero.md requires.
func (c Coordinator) wakeFromSnapshot(ctx context.Context, fence uint64, manifest Manifest) error {
	var failures error
	for attempt := 0; attempt < maxRestoreGenerations; attempt++ {
		err := c.wakeAtManifest(ctx, fence, manifest, true)
		if err == nil {
			return nil
		}
		failures = errors.Join(failures, err)
		if !errors.Is(err, errRestoreStage) || ctx.Err() != nil || manifest.Generation <= 1 {
			return failures
		}
		older, selectErr := c.Snapshots.LastVerified(ctx, manifest.Generation-1)
		if selectErr != nil {
			return errors.Join(failures, selectErr)
		}
		if older.Generation == 0 || older.Generation >= manifest.Generation {
			return failures
		}
		c.Metrics.AddCounter("sameoldchat_restore_selected_older_generation_total", 1)
		manifest = older
	}
	return failures
}

// wakeAtManifest runs the fenced startup path. When restore is false the
// database already on the active volume is authoritative and no snapshot bytes
// are written over it.
func (c Coordinator) wakeAtManifest(ctx context.Context, fence uint64, manifest Manifest, restore bool) error {
	if err := c.runStage(ctx, "inspect", func() error { return c.Driver.Inspect(ctx, fence) }); err != nil {
		return err
	}
	if restore {
		if err := c.runStage(ctx, "restore", func() error { return c.Snapshots.Restore(ctx, manifest) }); err != nil {
			c.Metrics.AddCounter("sameoldchat_restore_failures_total", 1)
			return errors.Join(errRestoreStage, err)
		}
	}
	if err := c.runStage(ctx, "start_persistence", func() error { return c.Driver.StartPersistence(ctx, fence, manifest) }); err != nil {
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
	state, fence := c.Controller.Snapshot()
	switch state {
	case StateActive, StateHibernated:
		return nil
	case StateWaking:
		if err := c.WakeAt(ctx, fence); err != nil {
			return errors.Join(err, c.Controller.Fail(fence))
		}
		return c.Controller.Activate(fence)
	case StateQuiescing, StateSnapshot, StateStopping:
		return c.recoverInterruptedHibernate(ctx, state, fence)
	case StateFailed:
		return ErrRecoveryRequired
	default:
		return ErrInvalidTransition
	}
}

// recoverInterruptedHibernate restarts the stack after a crash between
// QUIESCING and HIBERNATED.
//
// In QUIESCING and SNAPSHOTTING this cycle never published a manifest, so the
// database on the active volume is strictly newer than every snapshot at or
// before the fence. Restoring one would permanently destroy every change made
// since the previous hibernation, so recovery starts persistence from the
// volume and restores nothing. "No snapshot exists yet" is therefore not fatal
// either: a crash during the very first hibernation recovers normally.
//
// STOPPING is the one interrupted state that already created, verified, and
// published a manifest for this exact fence, and specs/scale-to-zero.md allows
// the active volume to be released after publication, so that one generation is
// restorable. An older generation still is not, for the reason above.
func (c Coordinator) recoverInterruptedHibernate(ctx context.Context, state State, fence uint64) error {
	manifest, restore, err := c.recoveryState(ctx, state, fence)
	if err != nil {
		return errors.Join(err, c.Controller.Fail(fence))
	}
	if err := c.Controller.BeginRecovery(fence); err != nil {
		return err
	}
	if err := c.wakeAtManifest(ctx, fence, manifest, restore); err != nil {
		return errors.Join(err, c.Controller.Fail(fence))
	}
	return c.Controller.Activate(fence)
}

func (c Coordinator) recoveryState(ctx context.Context, state State, fence uint64) (Manifest, bool, error) {
	if state == StateStopping {
		manifest, err := c.Snapshots.LastVerified(ctx, fence)
		switch {
		case err == nil && manifest.Generation == fence:
			return manifest, true, nil
		case err != nil && !errors.Is(err, ErrNoVerifiedSnapshot):
			return Manifest{}, false, err
		}
		c.Metrics.AddCounter("sameoldchat_recovery_published_snapshot_missing_total", 1)
	}
	live, err := c.Snapshots.LiveState(ctx, fence)
	if err != nil {
		return Manifest{}, false, err
	}
	c.Metrics.AddCounter("sameoldchat_recovery_from_live_volume_total", 1)
	return live, false, nil
}
