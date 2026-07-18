package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/observability"
)

func newTestCoordinator(controller *Controller, driver RuntimeDriver, snapshots Snapshotter) (Coordinator, error) {
	return NewCoordinator(controller, driver, snapshots, observability.NewRegistry())
}

type coordinatorDriver struct {
	calls []string
	fail  string
}

func (d *coordinatorDriver) Inspect(context.Context, uint64) error {
	d.calls = append(d.calls, "inspect")
	if d.fail == "inspect" {
		return errors.New("inspect failed")
	}
	return nil
}
func (d *coordinatorDriver) StartPersistence(context.Context, uint64, Manifest) error {
	d.calls = append(d.calls, "start-persistence")
	return nil
}
func (d *coordinatorDriver) RunMigration(context.Context, uint64, int) error {
	d.calls = append(d.calls, "migration")
	return nil
}
func (d *coordinatorDriver) StartWorkers(context.Context, uint64) error {
	d.calls = append(d.calls, "start-workers")
	return nil
}
func (d *coordinatorDriver) StartServers(context.Context, uint64) error {
	d.calls = append(d.calls, "start-servers")
	return nil
}
func (d *coordinatorDriver) DrainServers(context.Context, uint64) error {
	d.calls = append(d.calls, "drain-servers")
	if d.fail == "drain-servers" {
		return errors.New("drain failed")
	}
	return nil
}
func (d *coordinatorDriver) StopWorkers(context.Context, uint64) error {
	d.calls = append(d.calls, "stop-workers")
	return nil
}
func (d *coordinatorDriver) StopPersistence(context.Context, uint64) error {
	d.calls = append(d.calls, "stop-persistence")
	if d.fail == "stop-persistence" {
		return errors.New("stop failed")
	}
	return nil
}
func (d *coordinatorDriver) ReleaseActiveStorage(context.Context, uint64) error {
	d.calls = append(d.calls, "release-storage")
	return nil
}

type coordinatorSnapshots struct {
	manifest Manifest
	calls    []string
}

func (s *coordinatorSnapshots) Create(context.Context, uint64) (Manifest, error) {
	s.calls = append(s.calls, "create")
	return s.manifest, nil
}
func (s *coordinatorSnapshots) Current(context.Context, uint64) (Manifest, error) {
	s.calls = append(s.calls, "current")
	return s.manifest, nil
}
func (s *coordinatorSnapshots) LastVerified(context.Context, uint64) (Manifest, error) {
	s.calls = append(s.calls, "last-verified")
	return s.manifest, nil
}
func (s *coordinatorSnapshots) Restore(context.Context, Manifest) error {
	s.calls = append(s.calls, "restore")
	return nil
}

func TestCoordinatorHibernateAndWake(t *testing.T) {
	controller := New(StateActive)
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	metrics := observability.NewRegistry()
	coordinator, err := NewCoordinator(controller, driver, snapshots, metrics)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := coordinator.Hibernate(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Generation != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	state, generation := controller.Snapshot()
	if state != StateHibernated || generation != 1 {
		t.Fatalf("state=%s generation=%d", state, generation)
	}
	if err := coordinator.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, generation = controller.Snapshot()
	if state != StateActive || generation != 2 {
		t.Fatalf("state=%s generation=%d", state, generation)
	}
	if len(driver.calls) != 10 || len(snapshots.calls) != 3 {
		t.Fatalf("driver=%v snapshots=%v", driver.calls, snapshots.calls)
	}
	values := metrics.Snapshot()
	if values.Counters["sameoldchat_snapshot_failures_total"] != 0 || values.Durations["sameoldchat_snapshot_duration"].Count != 1 || values.Durations["sameoldchat_wake_stage_restore"].Count != 1 || values.Gauges["sameoldchat_last_successful_snapshot_unix_seconds"] <= 0 || values.Gauges["sameoldchat_last_successful_restore_unix_seconds"] <= 0 || values.Gauges["sameoldchat_migration_schema_version"] != 1 {
		t.Fatalf("lifecycle metrics=%+v", values)
	}
}

func TestCoordinatorFailureFencesAndFails(t *testing.T) {
	controller := New(StateActive)
	driver := &coordinatorDriver{fail: "stop-persistence"}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Hibernate(context.Background(), 0); err == nil {
		t.Fatal("hibernate succeeded after stop failure")
	}
	state, _ := controller.Snapshot()
	if state != StateFailed {
		t.Fatalf("state=%s", state)
	}
}

func TestCoordinatorDoesNotSnapshotAfterDrainFailure(t *testing.T) {
	controller := New(StateActive)
	driver := &coordinatorDriver{fail: "drain-servers"}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Hibernate(context.Background(), 0); err == nil {
		t.Fatal("hibernate succeeded after drain failure")
	}
	state, _ := controller.Snapshot()
	if state != StateFailed {
		t.Fatalf("state=%s, want failed", state)
	}
	if len(snapshots.calls) != 0 {
		t.Fatalf("snapshot calls=%v, want no snapshot after drain failure", snapshots.calls)
	}
}

func TestCoordinatorRecoversPersistedWake(t *testing.T) {
	controller := New(StateHibernated)
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: fence, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, generation := controller.Snapshot()
	if state != StateActive || generation != fence || len(driver.calls) != 5 || len(snapshots.calls) != 2 {
		t.Fatalf("state=%s generation=%d driver=%v snapshots=%v", state, generation, driver.calls, snapshots.calls)
	}
}

func TestCoordinatorMigrationRunsOncePerActivation(t *testing.T) {
	controller := New(StateHibernated)
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: fence, Backend: "sqlite", SchemaVersion: 2}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	migrations := 0
	for _, call := range driver.calls {
		if call == "migration" {
			migrations++
		}
	}
	if migrations != 1 {
		t.Fatalf("migration calls=%d, want one per activation: %v", migrations, driver.calls)
	}
}

func TestCoordinatorRecoversPersistedWakeAfterProcessRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle.db")
	firstStore, err := OpenSQLiteStateStore(path, StateRecord{State: StateHibernated})
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewPersistent(firstStore)
	if err != nil {
		firstStore.Close()
		t.Fatal(err)
	}
	fence, err := first.BeginWake()
	if err != nil {
		firstStore.Close()
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := OpenSQLiteStateStore(path, StateRecord{State: StateActive, Generation: 99})
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	second, err := NewPersistent(secondStore)
	if err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: fence, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(second, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, generation := second.Snapshot()
	if state != StateActive || generation != fence || len(driver.calls) != 5 || len(snapshots.calls) != 2 {
		t.Fatalf("state=%s generation=%d driver=%v snapshots=%v", state, generation, driver.calls, snapshots.calls)
	}
}

func TestCoordinatorRecoversInterruptedHibernateFromVerifiedSnapshot(t *testing.T) {
	controller := New(StateActive)
	fence, err := controller.BeginHibernate(0)
	if err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: fence, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, generation := controller.Snapshot()
	if state != StateActive || generation != fence {
		t.Fatalf("state=%s generation=%d", state, generation)
	}
	if len(driver.calls) != 5 || len(snapshots.calls) != 2 || snapshots.calls[0] != "last-verified" || snapshots.calls[1] != "restore" {
		t.Fatalf("driver=%v snapshots=%v", driver.calls, snapshots.calls)
	}
}
