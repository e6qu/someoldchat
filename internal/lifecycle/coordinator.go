package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/observability"
)

var ErrRecoveryRequired = errors.New("lifecycle recovery requires an explicit operator or provider action")

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
	Restore(context.Context, Manifest) error
}

type Coordinator struct {
	Controller *Controller
	Driver     RuntimeDriver
	Snapshots  Snapshotter
	Metrics    *observability.Registry
}

func NewCoordinator(controller *Controller, driver RuntimeDriver, snapshots Snapshotter, metrics *observability.Registry) (Coordinator, error) {
	if controller == nil || driver == nil || snapshots == nil || metrics == nil {
		return Coordinator{}, errors.New("lifecycle coordinator requires controller, runtime driver, snapshotter, and metrics")
	}
	return Coordinator{Controller: controller, Driver: driver, Snapshots: snapshots, Metrics: metrics}, nil
}

func (c Coordinator) Hibernate(ctx context.Context, fence uint64) (Manifest, error) {
	started := time.Now()
	defer func() { c.Metrics.ObserveDuration("sameoldchat_hibernate_duration", time.Since(started)) }()
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
	if err := c.Controller.CompleteHibernate(activeFence); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (c Coordinator) Wake(ctx context.Context) error {
	fence, err := c.Controller.BeginWake()
	if err != nil {
		return err
	}
	if err := c.WakeAt(ctx, fence); err != nil {
		return errors.Join(err, c.Controller.Fail(fence))
	}
	return c.Controller.Activate(fence)
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
		return err
	}
	return c.wakeAtManifest(ctx, fence, manifest)
}

func (c Coordinator) wakeAtManifest(ctx context.Context, fence uint64, manifest Manifest) error {
	if err := c.runStage(ctx, "inspect", func() error { return c.Driver.Inspect(ctx, fence) }); err != nil {
		return err
	}
	if err := c.runStage(ctx, "restore", func() error { return c.Snapshots.Restore(ctx, manifest) }); err != nil {
		c.Metrics.AddCounter("sameoldchat_restore_failures_total", 1)
		return err
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

// Recover resumes a persisted wake or an interrupted hibernation. For an
// interrupted hibernation it uses only the latest independently verified
// snapshot at or before the recovery fence, then runs the same fenced startup
// path as an ordinary wake. Missing or corrupt snapshots remain fatal.
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
		manifest, err := c.Snapshots.LastVerified(ctx, fence)
		if err != nil {
			return errors.Join(err, c.Controller.Fail(fence))
		}
		if err := c.Controller.BeginRecovery(fence); err != nil {
			return err
		}
		if err := c.wakeAtManifest(ctx, fence, manifest); err != nil {
			return errors.Join(err, c.Controller.Fail(fence))
		}
		return c.Controller.Activate(fence)
	case StateFailed:
		return ErrRecoveryRequired
	default:
		return ErrInvalidTransition
	}
}
