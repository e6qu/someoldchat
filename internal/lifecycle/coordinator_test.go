package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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
	// startPersistenceEntered is closed when the wake reaches the first stage
	// that touches the volume; blockStartPersistence holds it there until the
	// test releases it. Together they let a test observe an interleaving without
	// sleeping for it.
	startPersistenceEntered chan struct{}
	blockStartPersistence   chan struct{}
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
	// blockStartPersistence is the injected synchronization point that stands in
	// for a process death partway through a wake: the caller never returns from
	// this stage and never persists another transition.
	if d.blockStartPersistence != nil {
		close(d.startPersistenceEntered)
		<-d.blockStartPersistence
	}
	if d.fail == "start-persistence" {
		return errors.New("start persistence failed")
	}
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
	mu       sync.Mutex
	manifest Manifest
	// live is the descriptor of the database already on the volume. A
	// coordinator that recovers from live state must never touch manifest.
	live         Manifest
	calls        []string
	restored     []uint64
	restoreFails map[uint64]bool
	verified     map[uint64]Manifest
	lastVerified error
	current      error
}

// callNames copies the recorded calls under the lock, so a test that inspects
// them while an abandoned wake is still parked in a stage does not race it.
func (s *coordinatorSnapshots) callNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *coordinatorSnapshots) restoredGenerations() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.restored...)
}

func (s *coordinatorSnapshots) record(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
}

func (s *coordinatorSnapshots) Create(context.Context, uint64) (Manifest, error) {
	s.record("create")
	return s.manifest, nil
}
func (s *coordinatorSnapshots) Current(context.Context, uint64) (Manifest, error) {
	s.record("current")
	if s.current != nil {
		return Manifest{}, s.current
	}
	return s.manifest, nil
}
func (s *coordinatorSnapshots) LastVerified(_ context.Context, maxGeneration uint64) (Manifest, error) {
	s.record("last-verified")
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
	s.record("live-state")
	live := s.live
	if live.Backend == "" {
		live = Manifest{Backend: "sqlite", SchemaVersion: 1}
	}
	live.Generation = generation
	return live, nil
}

func (s *coordinatorSnapshots) Restore(_ context.Context, manifest Manifest) error {
	s.record("restore")
	if s.restoreFails[manifest.Generation] {
		return errors.New("restore failed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restored = append(s.restored, manifest.Generation)
	return nil
}

// servingRecord is the durable record of a stack that is serving: no published
// snapshot is authoritative, because the volume has been accepting writes since
// the wake that started it.
func servingRecord(generation uint64) StateRecord {
	return StateRecord{State: StateActive, Generation: generation}
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
			controller, err := NewPersistent(&testStateStore{record: servingRecord(5)})
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
			if controller.Metadata().SnapshotAuthoritative {
				t.Fatal("a recovery that restored nothing claimed the snapshot is authoritative")
			}
		})
	}
}

// Hole 1: an operator following the runbook destroyed the data.
//
// A crash during hibernation leaves the volume as the only copy of everything
// written since the previous hibernation. Recovery starts from that volume, but
// if a start stage then fails the stack is FAILED, and AcknowledgeFailure used to
// return it to HIBERNATED with nothing recorded about why. The next wake was an
// ordinary wake, so it restored the last published snapshot over the intact,
// strictly newer volume. Carrying snapshot authority through the acknowledgement is
// what closes it.
func TestAcknowledgedFailureDuringHibernationDoesNotRestoreOverTheLiveVolume(t *testing.T) {
	backend := &testStateStore{record: servingRecord(5)}
	controller, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := controller.BeginHibernate(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginSnapshot(fence); err != nil {
		t.Fatal(err)
	}
	// The previous cycle published generation 5; nothing was published for this
	// fence, so 5 is strictly older than the volume.
	published := Manifest{Generation: 5, Backend: "sqlite", SchemaVersion: 1}
	snapshots := &coordinatorSnapshots{manifest: published, verified: map[uint64]Manifest{5: published}}
	failing, err := newTestCoordinator(controller, &coordinatorDriver{fail: "start-persistence"}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.Recover(context.Background()); err == nil {
		t.Fatal("recovery succeeded although the persistence stage failed")
	}
	if state, _ := controller.Snapshot(); state != StateFailed {
		t.Fatalf("state=%s, want failed", state)
	}

	// The operator does exactly what docs/operations.md tells them to do.
	next, err := controller.AcknowledgeFailure(fence)
	if err != nil {
		t.Fatal(err)
	}
	if backend.load().SnapshotAuthoritative {
		t.Fatal("acknowledging the failure claimed the snapshot is authoritative")
	}
	wakeFence, err := controller.BeginWake()
	if err != nil || wakeFence != next+1 {
		t.Fatalf("wake fence=%d err=%v", wakeFence, err)
	}
	recovered := &coordinatorSnapshots{manifest: published, verified: map[uint64]Manifest{5: published}}
	coordinator, err := newTestCoordinator(controller, &coordinatorDriver{}, recovered)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), wakeFence); err != nil {
		t.Fatal(err)
	}
	if len(recovered.restored) != 0 {
		t.Fatalf("restored=%v, want the acknowledged failure to leave the live volume intact", recovered.restored)
	}
	for _, call := range recovered.calls {
		if call == "restore" || call == "current" {
			t.Fatalf("snapshot calls=%v, want no snapshot selected after an acknowledged mid-hibernation failure", recovered.calls)
		}
	}
}

// Hole 2: no operator required at all.
//
// recoverInterruptedHibernate persists WAKING before the live-volume start runs.
// A second crash inside that window leaves a durable WAKING record, and a fresh
// process resuming it took the ordinary wake path and restored a snapshot over
// the volume. The interleaving is exercised directly: the first coordinator is
// parked inside the stage that touches the volume and never returns, exactly as a
// killed task would not, while a second controller loaded from the same durable
// record resumes the wake.
func TestCrashDuringLiveVolumeWakeDoesNotRestoreOverTheVolume(t *testing.T) {
	backend := &testStateStore{record: servingRecord(5)}
	first, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := first.BeginHibernate(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.BeginSnapshot(fence); err != nil {
		t.Fatal(err)
	}
	published := Manifest{Generation: 5, Backend: "sqlite", SchemaVersion: 1}
	parked := &coordinatorDriver{startPersistenceEntered: make(chan struct{}), blockStartPersistence: make(chan struct{})}
	abandoned, err := newTestCoordinator(first, parked, &coordinatorSnapshots{manifest: published, verified: map[uint64]Manifest{5: published}})
	if err != nil {
		t.Fatal(err)
	}
	interrupted := make(chan error, 1)
	go func() { interrupted <- abandoned.Recover(context.Background()) }()
	<-parked.startPersistenceEntered
	// The durable record now reads WAKING at the hibernation fence, which is the
	// state the replacement process finds.
	if record := backend.load(); record.State != StateWaking || record.Generation != fence {
		t.Fatalf("durable record=%+v, want a persisted WAKING recovery", record)
	}

	second, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	resumed := &coordinatorSnapshots{manifest: published, verified: map[uint64]Manifest{5: published}}
	replacement, err := newTestCoordinator(second, &coordinatorDriver{}, resumed)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if generations := resumed.restoredGenerations(); len(generations) != 0 {
		t.Fatalf("restored=%v, want the replacement process to leave the live volume intact", generations)
	}
	for _, call := range resumed.callNames() {
		if call == "restore" || call == "current" {
			t.Fatalf("snapshot calls=%v, want no snapshot selected while resuming an interrupted hibernation", resumed.callNames())
		}
	}
	if state, generation := second.Snapshot(); state != StateActive || generation != fence {
		t.Fatalf("state=%s generation=%d, want the stack active at the recovery fence", state, generation)
	}
	close(parked.blockStartPersistence)
	<-interrupted
}

// The mirror case: a wake elected out of an ordinary hibernation must restore.
// Snapshot authority is what distinguishes the two, so both directions are pinned.
func TestWakeAfterCompletedHibernationRestoresThePublishedSnapshot(t *testing.T) {
	backend := &testStateStore{record: servingRecord(5)}
	controller, err := NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	driver := &coordinatorDriver{}
	published := Manifest{Generation: 6, Backend: "sqlite", SchemaVersion: 1}
	snapshots := &coordinatorSnapshots{manifest: published, verified: map[uint64]Manifest{6: published}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Hibernate(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if !backend.load().SnapshotAuthoritative {
		t.Fatal("a completed hibernation did not record its published snapshot as authoritative")
	}
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	if len(snapshots.restored) != 1 || snapshots.restored[0] != 6 {
		t.Fatalf("restored=%v, want the published generation 6 restored", snapshots.restored)
	}
	if err := controller.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if backend.load().SnapshotAuthoritative {
		t.Fatal("a serving stack must not leave a snapshot marked authoritative")
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
	controller, err := NewPersistent(&testStateStore{record: servingRecord(5)})
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
	own := Manifest{Generation: fence, Backend: "sqlite", SchemaVersion: 1}
	snapshots := &coordinatorSnapshots{manifest: own, verified: map[uint64]Manifest{
		fence - 1: {Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1},
		fence:     own,
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

// A crash in STOPPING whose own published manifest cannot be selected must fail
// closed.
//
// This test previously asserted that the coordinator silently fell back to the
// live volume. That is a weaker contract than the one specs/scale-to-zero.md:180
// states — "Restore failure MUST NOT be converted into an implicit fallback" —
// and it was unsafe here in particular: STOPPING is the one state in which the
// active storage may already have been released, so "recover from the volume" can
// mean "recover from a volume that no longer exists" and start the stack on an
// empty database. FAILED plus an explicit operator selection is the answer the
// spec requires and the only one that cannot lose data.
func TestCoordinatorInterruptedStopWithoutItsOwnSnapshotFailsClosed(t *testing.T) {
	controller, err := NewPersistent(&testStateStore{record: servingRecord(5)})
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
	snapshots := &coordinatorSnapshots{current: ErrNoVerifiedSnapshot, verified: map[uint64]Manifest{fence - 1: {Generation: fence - 1, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := newTestCoordinator(controller, driver, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(context.Background()); !errors.Is(err, ErrNoVerifiedSnapshot) {
		t.Fatalf("recover error=%v, want the unselectable manifest surfaced", err)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want no older generation written over the volume", snapshots.restored)
	}
	if state, _ := controller.Snapshot(); state != StateFailed {
		t.Fatalf("state=%s, want failed pending an operator decision", state)
	}
}

// specs/scale-to-zero.md:180-183 forbids converting a restore failure into an
// implicit fallback: the stack MUST enter FAILED and an older generation MUST be
// an explicit operator action.
//
// This test previously asserted the opposite — that a failed restore silently
// walked back up to four generations — and cited an older revision of the same
// spec as requiring it. Correcting it is not an accommodation: a workspace that
// silently rolls back by hours or days on a corrupt artifact, reporting ACTIVE
// and incrementing only a counter, is the same class of surprise as restoring
// over a live volume.
func TestWakeDoesNotSilentlyFallBackToAnOlderGenerationOnRestoreFailure(t *testing.T) {
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
	if err := coordinator.WakeAt(context.Background(), fence); err == nil {
		t.Fatal("a failed restore silently selected another generation")
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want no implicit fallback", snapshots.restored)
	}
	if err := controller.Fail(fence); err != nil {
		t.Fatal(err)
	}

	// The mandated replacement: an explicit, fenced, authenticated selection of a
	// named known-good generation.
	restoreFence, err := controller.BeginOperatorRestore(fence)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RestoreAt(context.Background(), restoreFence, 2); err != nil {
		t.Fatalf("operator restore=%v", err)
	}
	if len(snapshots.restored) != 1 || snapshots.restored[0] != 2 {
		t.Fatalf("restored=%v, want the operator-selected generation 2", snapshots.restored)
	}
	if err := controller.Activate(restoreFence); err != nil {
		t.Fatal(err)
	}
}

// An operator naming a generation that is not itself verified must be refused
// rather than rounded down to the nearest one that is; rounding down is the
// implicit fallback wearing an operator's clothes.
func TestOperatorRestoreRefusesAGenerationThatIsNotKnownGood(t *testing.T) {
	controller := New(StateHibernated)
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Fail(fence); err != nil {
		t.Fatal(err)
	}
	snapshots := &coordinatorSnapshots{verified: map[uint64]Manifest{2: {Generation: 2, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := newTestCoordinator(controller, &coordinatorDriver{}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	restoreFence, err := controller.BeginOperatorRestore(fence)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RestoreAt(context.Background(), restoreFence, 3); !errors.Is(err, ErrGenerationNotRestorable) {
		t.Fatalf("restore error=%v, want %v", err, ErrGenerationNotRestorable)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want nothing written for an unverified selection", snapshots.restored)
	}
}

// A restore driven from an ordinary wake fence must be refused. Only
// BeginOperatorRestore records the consent that grants snapshot authority, so
// without this guard the explicit endpoint would be a second way to overwrite a
// live volume.
func TestOperatorRestoreRefusesAFenceWithoutRecordedConsent(t *testing.T) {
	controller := New(StateActive)
	hibernateFence, err := controller.BeginHibernate(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginRecovery(hibernateFence); err != nil {
		t.Fatal(err)
	}
	snapshots := &coordinatorSnapshots{verified: map[uint64]Manifest{1: {Generation: 1, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := newTestCoordinator(controller, &coordinatorDriver{}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RestoreAt(context.Background(), hibernateFence, 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("restore error=%v, want %v", err, ErrInvalidTransition)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want the live volume untouched", snapshots.restored)
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

// specs/scale-to-zero.md requires operator-selected restore generation counts to
// be published. The counter that existed measured implicit fallbacks, which can
// no longer happen, so it is replaced rather than kept alongside.
func TestOperatorRestoreIsCounted(t *testing.T) {
	controller := New(StateHibernated)
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Fail(fence); err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewRegistry()
	snapshots := &coordinatorSnapshots{verified: map[uint64]Manifest{2: {Generation: 2, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := NewCoordinator(controller, &coordinatorDriver{}, snapshots, metrics, testWakeSafetyMargin)
	if err != nil {
		t.Fatal(err)
	}
	restoreFence, err := controller.BeginOperatorRestore(fence)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.RestoreAt(context.Background(), restoreFence, 2); err != nil {
		t.Fatal(err)
	}
	values := metrics.Snapshot()
	if values.Counters["sameoldchat_operator_selected_restore_total"] != 1 {
		t.Fatalf("counters=%+v, want one operator-selected restore recorded", values.Counters)
	}
	if values.Counters["sameoldchat_restore_selected_older_generation_total"] != 0 {
		t.Fatalf("counters=%+v, want the implicit-fallback counter gone", values.Counters)
	}
}
