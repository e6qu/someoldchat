package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
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
func (s *coordinatorSnapshots) Select(_ context.Context, maxGeneration uint64) (Manifest, error) {
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

// realLifecycle builds the durable control store, the real filesystem
// snapshotter, and the coordinator over one temporary root, so a test can drive
// the actual sequence an operator or an inbound request drives rather than a
// fake of it. The volume is both the snapshot source and the restore
// destination, exactly as cmd/activator's -snapshot-source/-snapshot-output pair
// is in the file profile.
func realLifecycle(t *testing.T, seed StateRecord) (string, *Controller, FileSnapshotter, Coordinator) {
	t.Helper()
	root := t.TempDir()
	volume := filepath.Join(root, "chat.db")
	manager, err := NewSnapshotManager(filepath.Join(root, "snapshots"), bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{9}, 32), "test-key", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := NewFileSnapshotter(manager, volume, volume, Manifest{
		Backend: "sqlite", SchemaVersion: 1, ApplicationVersion: "test", MinRestorerVersion: "test", MaxRestorerVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteStateStore(filepath.Join(root, "lifecycle.db"), seed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	controller, err := NewPersistent(store)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := newTestCoordinator(controller, &coordinatorDriver{}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	return volume, controller, snapshots, coordinator
}

func writeVolume(t *testing.T, volume, contents string) {
	t.Helper()
	if err := os.WriteFile(volume, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readVolume(t *testing.T, volume string) string {
	t.Helper()
	body, err := os.ReadFile(volume)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A brand-new deployment must cold-start. The control store has never been
// written, so nothing has published a manifest and the snapshot root is empty:
// the only copy of the data is the volume the deployment is about to create.
//
// Seeding derived snapshot authority from the seeded state, so a fresh store was
// HIBERNATED *with* authority. The first ever request took the restore branch,
// found no current.json, exhausted the wake attempts and went FAILED — and the
// documented recovery, POST /recover, carried the flag forward so the next wake
// failed identically. There was no runbook out of it: POST /restore needs a
// generation that does not exist. The deployment was unbootable.
func TestFreshDeploymentReachesActiveFromTheLiveVolume(t *testing.T) {
	volume, controller, _, coordinator := realLifecycle(t, StateRecord{State: StateHibernated})
	if seeded := controller.Metadata(); seeded.SnapshotAuthoritative {
		t.Fatalf("seeded control row=%+v: a store that has never run claims its snapshot is authoritative", seeded)
	}
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), fence); err != nil {
		t.Fatalf("the first ever wake of a fresh deployment failed: %v", err)
	}
	if err := controller.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if state, generation := controller.Snapshot(); state != StateActive || generation != 1 {
		t.Fatalf("state=%s generation=%d, want a serving stack at generation 1", state, generation)
	}
	// Nothing was restored over the volume, so the deployment's own database is
	// whatever the start commands made of it — here, still absent.
	if _, err := os.Stat(volume); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat volume err=%v, want the wake to have written nothing over it", err)
	}
}

// The recorded destruction path, end to end against the real store, the real
// snapshotter and the real coordinator.
//
// The stack crashed during SNAPSHOTTING, so it is FAILED with the volume holding
// writes no snapshot has and snapshot authority correctly false. An operator
// types a generation that does not exist into POST /restore. The restore is
// refused before a byte is written and the handler fails the attempt; the
// operator then follows docs/operations.md and calls POST /recover. The next
// unrelated inbound request must not destroy the volume.
//
// Consent granted at the transition made that impossible: BeginOperatorRestore
// set snapshot authority before the selection was even validated, and Fail and
// AcknowledgeFailure both carried it forward, so the ordinary wake restored the
// last published snapshot over strictly newer data with no operator involved.
func TestRefusedOperatorRestoreDoesNotLetTheNextRequestDestroyTheVolume(t *testing.T) {
	volume, controller, snapshots, coordinator := realLifecycle(t, servingRecord(5))
	const published = "the bytes generation 5 captured"
	const live = "every write made since generation 5"
	writeVolume(t, volume, published)
	if _, err := snapshots.Create(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	writeVolume(t, volume, live)

	// The crash during SNAPSHOTTING: nothing was published for this fence.
	fence, err := controller.BeginHibernate(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginSnapshot(fence); err != nil {
		t.Fatal(err)
	}
	if err := controller.Fail(fence); err != nil {
		t.Fatal(err)
	}

	// POST /restore?generation=99 — a generation this deployment does not have.
	restoreFence, err := controller.BeginOperatorRestore(fence, 99)
	if err != nil {
		t.Fatal(err)
	}
	restoreErr := coordinator.RestoreAt(context.Background(), restoreFence, 99)
	if !errors.Is(restoreErr, ErrNoVerifiedSnapshot) && !errors.Is(restoreErr, ErrGenerationNotRestorable) {
		t.Fatalf("operator restore error=%v, want the selection refused", restoreErr)
	}
	// restoreHandler joins the refusal with Fail on the restore fence.
	if err := controller.Fail(restoreFence); err != nil {
		t.Fatal(err)
	}
	if record := controller.Metadata(); record.SnapshotAuthoritative {
		t.Fatalf("record=%+v: a restore that wrote nothing left permission to overwrite the volume", record)
	}

	// POST /recover, exactly as the runbook says.
	if _, err := controller.AcknowledgeFailure(restoreFence); err != nil {
		t.Fatal(err)
	}
	// Any inbound HTTP request.
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
	if got := readVolume(t, volume); got != live {
		t.Fatalf("volume=%q after an ordinary request, want %q: a snapshot was restored over strictly newer data with no operator involved", got, live)
	}
}

// A crash during an operator restore must resume that operator's selection.
//
// Once Restore may have written a byte the volume is a ruin and only a snapshot
// is complete, so the durable record must already say so — before the write, not
// after. And the record must still name the generation the operator chose: the
// reason they chose an older one is that the current manifest is not what they
// want, so resuming "whatever is current" silently restores the wrong data.
//
// The crash is injected, not slept for: the restore parks in the middle of the
// write and never returns, exactly as a killed task would not, while a second
// controller loaded from the same durable record resumes.
func TestCrashDuringOperatorRestoreResumesTheSelectedGeneration(t *testing.T) {
	volume, controller, snapshots, _ := realLifecycle(t, servingRecord(20))
	const chosen = "the bytes of the generation the operator chose"
	const rejected = "the newer generation the operator does not want"
	writeVolume(t, volume, chosen)
	if _, err := snapshots.Create(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	writeVolume(t, volume, rejected)
	if _, err := snapshots.Create(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	writeVolume(t, volume, "live writes the operator has decided to discard")

	fence, err := controller.BeginHibernate(20)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginSnapshot(fence); err != nil {
		t.Fatal(err)
	}
	if err := controller.Fail(fence); err != nil {
		t.Fatal(err)
	}
	restoreFence, err := controller.BeginOperatorRestore(fence, 5)
	if err != nil {
		t.Fatal(err)
	}
	dying := &haltingSnapshotter{Snapshotter: snapshots, volume: volume, entered: make(chan struct{}), release: make(chan struct{})}
	abandoned, err := newTestCoordinator(controller, &coordinatorDriver{}, dying)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := make(chan error, 1)
	go func() { interrupted <- abandoned.RestoreAt(context.Background(), restoreFence, 5) }()
	<-dying.entered

	// This is the record a replacement process finds. Authority must already be
	// granted, because the volume is now a partial write of the snapshot, and the
	// operator's selection must still be named.
	store, err := OpenSQLiteStateStore(filepath.Join(filepath.Dir(volume), "lifecycle.db"), StateRecord{State: StateActive, Generation: 99})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StateWaking || record.Generation != restoreFence {
		t.Fatalf("durable record=%+v, want a persisted WAKING restore at fence %d", record, restoreFence)
	}
	if !record.SnapshotAuthoritative {
		t.Fatalf("durable record=%+v, want the half-written volume reported as incomplete", record)
	}
	if record.RestoreGeneration != 5 {
		t.Fatalf("durable record=%+v, want the operator's selection of generation 5 to survive the crash", record)
	}

	second, err := NewPersistent(store)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := newTestCoordinator(second, &coordinatorDriver{}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readVolume(t, volume); got != chosen {
		t.Fatalf("volume=%q after resuming the interrupted restore, want the operator-selected %q", got, chosen)
	}
	if state, _ := second.Snapshot(); state != StateActive {
		t.Fatalf("state=%s, want the resumed restore to reach active", state)
	}
	if second.Metadata().RestoreGeneration != 0 {
		t.Fatalf("metadata=%+v, want the completed selection spent", second.Metadata())
	}
	close(dying.release)
	<-interrupted
}

// haltingSnapshotter parks forever inside Restore, after the write has begun. It
// stands in for a process death partway through a restore: the caller never
// returns from the stage and never persists another transition.
type haltingSnapshotter struct {
	Snapshotter
	volume  string
	entered chan struct{}
	release chan struct{}
}

func (s *haltingSnapshotter) Restore(_ context.Context, _ Manifest) error {
	// The volume is left as a partial write, which is the whole reason the record
	// must already say the snapshot is the only complete copy.
	if err := os.WriteFile(s.volume, []byte("half a"), 0o600); err != nil {
		return err
	}
	close(s.entered)
	<-s.release
	return errors.New("the process died during the restore")
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

// A wake persisted by one process is resumed by its replacement.
//
// The control store here is created by the test, so it is a *fresh* one: it has
// published no manifest, which is why the resumed wake starts from the live
// volume and selects no snapshot at all. That assertion used to read "two
// snapshot calls", which was the seeding defect showing through — the seed
// claimed a hibernated stack whose snapshot was authoritative, and the first ever
// wake of a real deployment therefore looked for a current.json that cannot
// exist. Seeding is exercised end to end in
// TestFreshDeploymentReachesActiveFromTheLiveVolume.
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
	if state != StateActive || generation != fence || len(driver.calls) != 5 || len(snapshots.calls) != 1 {
		t.Fatalf("state=%s generation=%d driver=%v snapshots=%v", state, generation, driver.calls, snapshots.calls)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want a store that has published nothing to restore nothing", snapshots.restored)
	}
}

// TestCoordinatorRecoversInterruptedQuiescenceWithoutRestoringOlderSnapshot
// replaces an earlier test that asserted the interrupted-hibernation path
// restores the newest previously verified snapshot. That assertion described
// data loss: quiescence publishes no manifest, so that snapshot is from the
// *previous* cycle and
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
			// snapshots, so an older generation exists to tempt recovery.
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
	// Authority is dropped as soon as persistence is up, not at Activate. The
	// migration and the workers ran inside WakeAt and they write; a crash in that
	// window used to leave a durable record that restored the snapshot over those
	// writes, which contradicts the invariant the field is documented with.
	if backend.load().SnapshotAuthoritative {
		t.Fatal("workers and servers started with the snapshot still marked authoritative")
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
	restoreFence, err := controller.BeginOperatorRestore(fence, 2)
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
	restoreFence, err := controller.BeginOperatorRestore(fence, 3)
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

// A restore driven from a fence that recorded no operator selection must be
// refused, so the explicit endpoint cannot become a second way to overwrite a
// live volume.
//
// The guard used to test SnapshotAuthoritative, which an ordinary wake out of a
// hibernated stack also carries: it admitted any wake fence whose stack had
// hibernated normally, which is every healthy deployment. Matching the recorded
// selection against the generation being restored is what actually distinguishes
// "an operator asked for this" from "a request happened to arrive".
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

	// The same refusal for the fence an ordinary wake out of a normal hibernation
	// owns, which carries snapshot authority and no consent whatsoever.
	hibernated := New(StateHibernated)
	wakeFence, err := hibernated.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if !hibernated.Metadata().SnapshotAuthoritative {
		t.Fatal("setup is wrong: an ordinary wake out of hibernation must carry snapshot authority")
	}
	ordinary := &coordinatorSnapshots{verified: map[uint64]Manifest{1: {Generation: 1, Backend: "sqlite", SchemaVersion: 1}}}
	wake, err := newTestCoordinator(hibernated, &coordinatorDriver{}, ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := wake.RestoreAt(context.Background(), wakeFence, 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("restore on an ordinary wake fence error=%v, want %v", err, ErrInvalidTransition)
	}
	if len(ordinary.restored) != 0 {
		t.Fatalf("restored=%v, want nothing restored for a fence nobody consented on", ordinary.restored)
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

// A deadline that has already passed demands no future wake, so it must not
// refuse hibernation.
//
// The eligibility check tested "is the deadline within the safety margin", which
// an elapsed deadline satisfies forever. BeginWake preserves the deadline across
// the wake it triggered and nothing on the activator side clears it, so a
// deployment whose worker is down — or which never wired the publisher at all —
// woke once for a scheduled job and could never hibernate again. A scale-to-zero
// deployment that stops scaling to zero, reported only as repeated 409s.
func TestHibernateIsNotRefusedForeverByADeadlineThatHasPassed(t *testing.T) {
	controller := New(StateActive)
	if err := controller.SetWakeDeadline(0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	snapshots := &coordinatorSnapshots{manifest: Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1}}
	coordinator, err := newTestCoordinator(controller, &coordinatorDriver{}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Hibernate(context.Background(), 0); err != nil {
		t.Fatalf("hibernate error=%v, want an elapsed deadline to block nothing", err)
	}
	if state, _ := controller.Snapshot(); state != StateHibernated {
		t.Fatalf("state=%s, want hibernated", state)
	}
	// And the wake that the elapsed deadline then triggers must consume it, or
	// the activator's scheduled wake loop fires at a stack it has just woken and
	// the pair oscillates: wake, hibernate, wake.
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	if err := controller.Activate(fence); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.IsZero() {
		t.Fatalf("wake deadline=%s after the wake it demanded, want it consumed", got)
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
	restoreFence, err := controller.BeginOperatorRestore(fence, 2)
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
