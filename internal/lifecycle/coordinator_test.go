package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/observability"
)

const testWakeSafetyMargin = time.Minute

func newTestCoordinator(controller *Controller, driver RuntimeDriver, snapshots Snapshotter) (Coordinator, error) {
	return NewCoordinator(controller, driver, snapshots, observability.NewRegistry(), testWakeSafetyMargin)
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
	// live is the descriptor of the database already on the volume. A
	// coordinator that recovers from live state must never touch manifest.
	live         Manifest
	calls        []string
	restored     []uint64
	restoreFails map[uint64]bool
	verified     map[uint64]Manifest
	lastVerified error
}

func (s *coordinatorSnapshots) Create(context.Context, uint64) (Manifest, error) {
	s.calls = append(s.calls, "create")
	return s.manifest, nil
}
func (s *coordinatorSnapshots) Current(context.Context, uint64) (Manifest, error) {
	s.calls = append(s.calls, "current")
	return s.manifest, nil
}
func (s *coordinatorSnapshots) LastVerified(_ context.Context, maxGeneration uint64) (Manifest, error) {
	s.calls = append(s.calls, "last-verified")
	if s.lastVerified != nil {
		return Manifest{}, s.lastVerified
	}
	if s.verified != nil {
		var newest Manifest
		for generation, manifest := range s.verified {
			if generation <= maxGeneration && generation > newest.Generation {
				newest = manifest
			}
		}
		if newest.Generation == 0 {
			return Manifest{}, ErrNoVerifiedSnapshot
		}
		return newest, nil
	}
	if s.manifest.Generation > maxGeneration {
		return Manifest{}, ErrNoVerifiedSnapshot
	}
	return s.manifest, nil
}

func (s *coordinatorSnapshots) LiveState(_ context.Context, generation uint64) (Manifest, error) {
	s.calls = append(s.calls, "live-state")
	live := s.live
	if live.Backend == "" {
		live = Manifest{Backend: "sqlite", SchemaVersion: 1}
	}
	live.Generation = generation
	return live, nil
}

func (s *coordinatorSnapshots) Restore(_ context.Context, manifest Manifest) error {
	s.calls = append(s.calls, "restore")
	if s.restoreFails[manifest.Generation] {
		return errors.New("restore failed")
	}
	s.restored = append(s.restored, manifest.Generation)
	return nil
}

func TestCoordinatorHibernateAndWake(t *testing.T) {
	controller := New(StateActive)
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	metrics := observability.NewRegistry()
	coordinator, err := NewCoordinator(controller, driver, snapshots, metrics, testWakeSafetyMargin)
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
	wakeFence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), wakeFence); err != nil {
		t.Fatal(err)
	}
	if err := controller.Activate(wakeFence); err != nil {
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

// TestCoordinatorRecoversInterruptedQuiescenceWithoutRestoringOlderSnapshot
// replaces an earlier test that asserted the interrupted-hibernation path
// restores LastVerified. That assertion described data loss: quiescence
// publishes no manifest, so LastVerified is the *previous* cycle's snapshot and
// restoring it renames an older database over the intact live volume, destroying
// every change since the last hibernation.
func TestCoordinatorRecoversInterruptedQuiescenceWithoutRestoringOlderSnapshot(t *testing.T) {
	for _, interrupted := range []struct {
		name  string
		state State
	}{
		{name: "quiescing", state: StateQuiescing},
		{name: "snapshotting", state: StateSnapshot},
	} {
		t.Run(interrupted.name, func(t *testing.T) {
			// A realistic history: several cycles have already published
			// snapshots, so LastVerified has an older generation to offer.
			controller, err := NewPersistent(&testStateStore{record: StateRecord{State: StateActive, Generation: 5}})
			if err != nil {
				t.Fatal(err)
			}
			fence, err := controller.BeginHibernate(5)
			if err != nil {
				t.Fatal(err)
			}
			if interrupted.state == StateSnapshot {
				if err := controller.BeginSnapshot(fence); err != nil {
					t.Fatal(err)
				}
			}
			driver := &coordinatorDriver{}
			// The newest published manifest belongs to the PREVIOUS cycle.
			snapshots := &coordinatorSnapshots{
				manifest: Manifest{Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1},
				verified: map[uint64]Manifest{fence - 1: {Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1}},
			}
			coordinator, err := newTestCoordinator(controller, driver, snapshots)
			if err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, generation := controller.Snapshot()
			if state != StateActive || generation != fence {
				t.Fatalf("state=%s generation=%d, want active at the recovery fence", state, generation)
			}
			if len(snapshots.restored) != 0 {
				t.Fatalf("restored generations=%v, want the live volume left untouched", snapshots.restored)
			}
			for _, call := range snapshots.calls {
				if call == "restore" {
					t.Fatalf("snapshot calls=%v, want no restore over live data", snapshots.calls)
				}
			}
			if len(driver.calls) != 5 || driver.calls[1] != "start-persistence" {
				t.Fatalf("driver=%v, want the stack restarted from the live volume", driver.calls)
			}
		})
	}
}

// A crash during the very first hibernation leaves no snapshot at all. The
// intact volume is the only copy of the data, so recovery must start from it
// rather than declare the deployment unrecoverable.
func TestCoordinatorRecoversFirstInterruptedHibernateWithoutAnySnapshot(t *testing.T) {
	controller := New(StateActive)
	if _, err := controller.BeginHibernate(0); err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{lastVerified: ErrNoVerifiedSnapshot, verified: map[uint64]Manifest{}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := controller.Snapshot(); state != StateActive {
		t.Fatalf("state=%s, want active without any snapshot", state)
	}
}

// STOPPING is the one interrupted state whose own snapshot is already published
// and verified, and the deployment profile may already have released the active
// volume, so that exact generation is the correct restore source.
func TestCoordinatorRecoversInterruptedStopFromThisCycleSnapshot(t *testing.T) {
	controller, err := NewPersistent(&testStateStore{record: StateRecord{State: StateActive, Generation: 5}})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := controller.BeginHibernate(5)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(uint64) error{controller.BeginSnapshot, controller.BeginStop} {
		if err := step(fence); err != nil {
			t.Fatal(err)
		}
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{verified: map[uint64]Manifest{
		fence - 1: {Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1},
		fence:     {Generation: fence, Backend: "sqlite", SchemaVersion: 1},
	}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snapshots.restored) != 1 || snapshots.restored[0] != fence {
		t.Fatalf("restored=%v, want only this cycle's generation %d", snapshots.restored, fence)
	}
}

// A crash in STOPPING whose manifest cannot be found must not silently fall back
// to an older generation over the volume.
func TestCoordinatorInterruptedStopWithoutItsOwnSnapshotUsesLiveVolume(t *testing.T) {
	controller, err := NewPersistent(&testStateStore{record: StateRecord{State: StateActive, Generation: 5}})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := controller.BeginHibernate(5)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(uint64) error{controller.BeginSnapshot, controller.BeginStop} {
		if err := step(fence); err != nil {
			t.Fatal(err)
		}
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{verified: map[uint64]Manifest{fence - 1: {Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want no older generation written over the volume", snapshots.restored)
	}
}

// specs/scale-to-zero.md:139 requires restore failure to select older compatible
// known-good generations in order. Only the interrupted-hibernation branch
// consulted LastVerified before, so a corrupt newest artifact failed the wake.
func TestWakeFallsBackToOlderKnownGoodGenerationOnRestoreFailure(t *testing.T) {
	controller := New(StateHibernated)
	for range 3 {
		fence, err := controller.BeginWake()
		if err != nil {
			t.Fatal(err)
		}
		if err := controller.Activate(fence); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.BeginHibernate(fence); err != nil {
			t.Fatal(err)
		}
		for _, step := range []func(uint64) error{controller.BeginSnapshot, controller.BeginStop, controller.CompleteHibernate} {
			if err := step(fence + 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{
		manifest:     Manifest{Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1},
		verified:     map[uint64]Manifest{2: {Generation: 2, Backend: "sqlite", SchemaVersion: 1}, fence - 1: {Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1}},
		restoreFails: map[uint64]bool{fence - 1: true},
	}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), fence); err != nil {
		t.Fatalf("wake=%v, want fallback to an older known-good generation", err)
	}
	if len(snapshots.restored) != 1 || snapshots.restored[0] != 2 {
		t.Fatalf("restored=%v, want the older known-good generation 2", snapshots.restored)
	}
}

// specs/scale-to-zero.md lists "no scheduled deadline falls within the wake
// safety window" as an idle-eligibility precondition. Hibernate consulted the
// deadline nowhere, so it would stop the stack seconds before a scheduled
// message was due.
func TestHibernateRefusesInsideTheWakeSafetyWindow(t *testing.T) {
	controller := New(StateActive)
	if err := controller.SetWakeDeadline(0, time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Hibernate(context.Background(), 0); !errors.Is(err, ErrWakeDeadlineWithinSafetyWindow) {
		t.Fatalf("hibernate error=%v, want the scheduled deadline to block hibernation", err)
	}
	if state, generation := controller.Snapshot(); state != StateActive || generation != 0 {
		t.Fatalf("state=%s generation=%d, want the serving stack untouched", state, generation)
	}
	if len(driver.calls) != 0 || len(snapshots.calls) != 0 {
		t.Fatalf("driver=%v snapshots=%v, want no hibernation work", driver.calls, snapshots.calls)
	}
}

func TestHibernateProceedsWhenTheScheduledDeadlineIsOutsideTheSafetyWindow(t *testing.T) {
	controller := New(StateActive)
	if err := controller.SetWakeDeadline(0, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Hibernate(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if state, _ := controller.Snapshot(); state != StateHibernated {
		t.Fatalf("state=%s, want hibernated", state)
	}
}
