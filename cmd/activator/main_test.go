package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/activator"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
)

func TestValidControlTokenRequiresBearerSchemeAndExactToken(t *testing.T) {
	for _, test := range []struct {
		header string
		want   bool
	}{
		{header: "Bearer secret", want: true},
		{header: "Bearer  secret ", want: true},
		{header: "secret", want: false},
		{header: "bearer secret", want: false},
		{header: "Bearer", want: false},
		{header: "Bearer other", want: false},
		{header: "Bearer secret extra", want: false},
	} {
		if got := validControlToken(test.header, "secret"); got != test.want {
			t.Fatalf("validControlToken(%q)=%t, want %t", test.header, got, test.want)
		}
	}
}

func TestValidControlTokenRejectsEmptyExpectedToken(t *testing.T) {
	if validControlToken("Bearer secret", "") {
		t.Fatal("empty expected control token was accepted")
	}
}

// /metrics is served on the same public listener as forwarded application
// traffic and publishes lifecycle state, snapshot sizes, and queue depths. It was
// reachable without the control-plane token.
func TestControlTokenGuardsMetricsAlongsideLifecycleEndpoints(t *testing.T) {
	forwarded := false
	serve := func(w http.ResponseWriter, _ *http.Request) {
		forwarded = true
		w.WriteHeader(http.StatusNoContent)
	}
	// Registered with the same patterns as run(), so the guard is exercised
	// through the pattern lookup it actually uses in production rather than
	// through a bare handler that never matches a route.
	mux := http.NewServeMux()
	mux.HandleFunc("/", serve)
	mux.HandleFunc("GET /healthz", serve)
	mux.HandleFunc("GET /lifecycle", serve)
	mux.HandleFunc("GET /metrics", serve)
	mux.HandleFunc("POST /hibernate", serve)
	mux.HandleFunc("POST /recover", serve)
	mux.HandleFunc("POST /restore", serve)
	guarded := requireControlToken(mux, "secret")

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/metrics"},
		// The lifecycle state and the fencing generation are two of the three
		// things whose presence on the public listener is why /metrics was
		// protected; specs/scale-to-zero.md:189 forbids public lifecycle status
		// from revealing topology. They must not come back on the probe.
		{http.MethodGet, "/lifecycle"},
		{http.MethodPost, "/hibernate"},
		{http.MethodPost, "/recover"},
		{http.MethodPost, "/restore"},
	} {
		forwarded = false
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusUnauthorized || forwarded {
			t.Fatalf("%s %s status=%d forwarded=%t, want the control token required", route.method, route.path, response.Code, forwarded)
		}
		authorized := httptest.NewRequest(route.method, route.path, nil)
		authorized.Header.Set("Authorization", "Bearer secret")
		response = httptest.NewRecorder()
		guarded.ServeHTTP(response, authorized)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s %s authorized status=%d", route.method, route.path, response.Code)
		}
	}

	// Application traffic and the liveness probe are the only unauthenticated
	// patterns; everything else is protected by omission from the allow-list.
	for _, path := range []string{"/api/message", "/healthz"} {
		forwarded = false
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if !forwarded || response.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d forwarded=%t, want it served without the control token", path, response.Code, forwarded)
		}
	}
}

// A route added to the mux without being added to the allow-list must be
// protected, which is the property the previous deny-list did not have.
func TestUnlistedControlRouteIsProtectedByDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /newly-added-control-endpoint", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an endpoint absent from the allow-list was served without the control token")
	})
	guarded := requireControlToken(mux, "secret")
	response := httptest.NewRecorder()
	guarded.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/newly-added-control-endpoint", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want the control token required", response.Code)
	}
}

// The lifecycle coordinator no longer walks back to older snapshot generations on
// its own, because specs/scale-to-zero.md:180-183 forbids converting a restore
// failure into an implicit fallback. This is the explicit operator action the
// same paragraph mandates and which had no implementation: docs/operations.md
// promised it and cmd/activator registered only /activate, /hibernate, /recover
// and /metrics.
func TestOperatorRestoreSelectsTheNamedGenerationAndFencesIt(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateFailed)
	snapshots := &stubSnapshotter{verified: map[uint64]lifecycle.Manifest{
		2: {Generation: 2, Backend: "sqlite", SchemaVersion: 1},
		4: {Generation: 4, Backend: "sqlite", SchemaVersion: 1},
	}}
	coordinator, err := lifecycle.NewCoordinator(controller, &stubDriver{}, snapshots, observability.NewRegistry(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := restoreHandler(controller, coordinator, slog.New(slog.NewTextHandler(io.Discard, nil)))

	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/restore", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing generation status=%d, want 400", response.Code)
	}

	response = httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/restore?generation=3", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("unverified generation status=%d, want 409", response.Code)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want nothing written for a generation that is not known-good", snapshots.restored)
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateFailed {
		t.Fatalf("state=%s, want the stack still failed after a rejected selection", state)
	}

	_, fence := controller.Snapshot()
	response = httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodPost, "/restore?generation=2", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q, want 204", response.Code, response.Body.String())
	}
	if len(snapshots.restored) != 1 || snapshots.restored[0] != 2 {
		t.Fatalf("restored=%v, want exactly the operator-selected generation 2", snapshots.restored)
	}
	state, generation := controller.Snapshot()
	if state != lifecycle.StateActive || generation != fence+1 {
		t.Fatalf("state=%s generation=%d, want an active stack at a new fence", state, generation)
	}
	if got := response.Header().Get("X-Lifecycle-Generation"); got != strconv.FormatUint(generation, 10) {
		t.Fatalf("generation header=%q, want %d", got, generation)
	}
}

// An operator restore must never be reachable on a serving stack: it records
// consent to overwrite the volume, so on an ACTIVE stack it would agree, on the
// operator's behalf, to discarding a volume nobody said was expendable.
func TestOperatorRestoreIsRefusedOnAServingStack(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateActive)
	snapshots := &stubSnapshotter{verified: map[uint64]lifecycle.Manifest{2: {Generation: 2, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := lifecycle.NewCoordinator(controller, &stubDriver{}, snapshots, observability.NewRegistry(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	restoreHandler(controller, coordinator, slog.New(slog.NewTextHandler(io.Discard, nil)))(response, httptest.NewRequest(http.MethodPost, "/restore?generation=2", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 on a serving stack", response.Code)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want the serving volume untouched", snapshots.restored)
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want the serving stack intact", state)
	}
}

// The documented runbook, driven through the handlers an operator actually
// calls: POST /restore with a generation this deployment does not have, then
// POST /recover, then any inbound request.
//
// The stack failed mid-hibernation, so the volume holds writes no snapshot has.
// The restore is refused before a byte is written, but the transition that began
// it granted snapshot authority, and both Fail and AcknowledgeFailure carried
// that forward — so the next unrelated request restored the last published
// snapshot over strictly newer data. One typo in the runbook destroyed the
// workspace.
func TestRefusedOperatorRestoreLeavesTheNextWakeOnTheLiveVolume(t *testing.T) {
	backend := &recordingStateStore{record: lifecycle.StateRecord{State: lifecycle.StateActive, Generation: 5}}
	controller, err := lifecycle.NewPersistent(backend)
	if err != nil {
		t.Fatal(err)
	}
	// A crash during SNAPSHOTTING: this fence published nothing.
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
	snapshots := &stubSnapshotter{verified: map[uint64]lifecycle.Manifest{5: {Generation: 5, Backend: "sqlite", SchemaVersion: 1}}}
	coordinator, err := lifecycle.NewCoordinator(controller, &stubDriver{}, snapshots, observability.NewRegistry(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	response := httptest.NewRecorder()
	restoreHandler(controller, coordinator, logger)(response, httptest.NewRequest(http.MethodPost, "/restore?generation=99", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("mistyped generation status=%d, want 409", response.Code)
	}
	if backend.load().SnapshotAuthoritative {
		t.Fatalf("record=%+v: a refused restore left permission to overwrite the volume", backend.load())
	}

	response = httptest.NewRecorder()
	recoverHandler(controller, logger)(response, httptest.NewRequest(http.MethodPost, "/recover", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("recover status=%d, want 204", response.Code)
	}
	if backend.load().SnapshotAuthoritative {
		t.Fatalf("record=%+v: the runbook recovery claimed the snapshot is newer than the volume", backend.load())
	}

	wakeFence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.WakeAt(context.Background(), wakeFence); err != nil {
		t.Fatal(err)
	}
	if len(snapshots.restored) != 0 {
		t.Fatalf("restored=%v, want the ordinary request to leave the live volume intact", snapshots.restored)
	}
}

// recordingStateStore is the durable control record, guarded because a wake and
// an operator action can reach it concurrently.
type recordingStateStore struct {
	mu     sync.Mutex
	record lifecycle.StateRecord
}

func (s *recordingStateStore) Load() (lifecycle.StateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}

func (s *recordingStateStore) load() lifecycle.StateRecord {
	record, _ := s.Load()
	return record
}

func (s *recordingStateStore) CompareAndSwap(expected, next lifecycle.StateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record != expected {
		return lifecycle.ErrStateConflict
	}
	s.record = next
	return nil
}

type stubDriver struct{}

func (stubDriver) Inspect(context.Context, uint64) error                              { return nil }
func (stubDriver) StartPersistence(context.Context, uint64, lifecycle.Manifest) error { return nil }
func (stubDriver) RunMigration(context.Context, uint64, int) error                    { return nil }
func (stubDriver) StartWorkers(context.Context, uint64) error                         { return nil }
func (stubDriver) StartServers(context.Context, uint64) error                         { return nil }
func (stubDriver) DrainServers(context.Context, uint64) error                         { return nil }
func (stubDriver) StopWorkers(context.Context, uint64) error                          { return nil }
func (stubDriver) StopPersistence(context.Context, uint64) error                      { return nil }
func (stubDriver) ReleaseActiveStorage(context.Context, uint64) error                 { return nil }

type stubSnapshotter struct {
	verified map[uint64]lifecycle.Manifest
	restored []uint64
}

func (s *stubSnapshotter) Create(context.Context, uint64) (lifecycle.Manifest, error) {
	return lifecycle.Manifest{}, errors.New("not used")
}

func (s *stubSnapshotter) Current(context.Context, uint64) (lifecycle.Manifest, error) {
	return lifecycle.Manifest{}, lifecycle.ErrNoVerifiedSnapshot
}

func (s *stubSnapshotter) Select(_ context.Context, maxGeneration uint64) (lifecycle.Manifest, error) {
	var newest lifecycle.Manifest
	for generation, manifest := range s.verified {
		if generation <= maxGeneration && generation > newest.Generation {
			newest = manifest
		}
	}
	if newest.Generation == 0 {
		return lifecycle.Manifest{}, lifecycle.ErrNoVerifiedSnapshot
	}
	return newest, nil
}

func (s *stubSnapshotter) LiveState(_ context.Context, generation uint64) (lifecycle.Manifest, error) {
	return lifecycle.Manifest{Generation: generation, Backend: "sqlite", SchemaVersion: 1}, nil
}

func (s *stubSnapshotter) Restore(_ context.Context, manifest lifecycle.Manifest) error {
	s.restored = append(s.restored, manifest.Generation)
	return nil
}

// specs/scale-to-zero.md requires the active stack to publish its earliest
// necessary wake time before shutdown. The consume side was wired — the
// scheduled wake loop and the hibernation safety-window check both read it — but
// nothing in production ever produced the value, so the wake timer was inert and
// a hibernated stack fired a due scheduled message only when unrelated traffic
// happened to wake it. The producer lives in a different process from the record,
// so the hint crosses the authenticated control boundary.
func TestPublishedWakeDeadlineReachesTheDurableRecordAndIsFenced(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateActive)
	forwarding, err := activator.NewForwardingHandler(
		context.Background(), controller,
		func(context.Context, uint64) error { return nil },
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		1<<20, time.Minute, observability.NewRegistry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer forwarding.Close()
	mux := http.NewServeMux()
	forwarding.RegisterForwarding(mux)
	mux.HandleFunc("POST /wake-deadline", wakeDeadlineHandler(controller, slog.New(slog.NewTextHandler(io.Discard, nil))))
	server := httptest.NewServer(requireControlToken(mux, "secret"))
	defer server.Close()

	publisher, err := scheduler.NewActivatorDeadlinePublisher(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	fence, err := publisher.Fence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2026, time.August, 2, 9, 30, 0, 0, time.UTC)
	if err := publisher.SetWakeDeadline(fence, deadline); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.Equal(deadline) {
		t.Fatalf("durable wake deadline=%s, want %s", got, deadline)
	}
	// Clearing is the ordinary case once the work has run. A served deadline left
	// behind would refuse every later hibernation inside its safety window.
	if err := publisher.SetWakeDeadline(fence, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.IsZero() {
		t.Fatalf("durable wake deadline=%s, want it cleared", got)
	}
	// A worker from a generation the stack has moved past must not be able to
	// reinstate a deadline the current generation cleared.
	if err := publisher.SetWakeDeadline(fence+1, deadline); err == nil {
		t.Fatal("a stale generation published a wake deadline")
	}
	if got := controller.Metadata().WakeDeadline; !got.IsZero() {
		t.Fatalf("durable wake deadline=%s after a stale publish, want it still cleared", got)
	}
}

// The wake deadline is lifecycle control, so it is protected by omission from the
// unauthenticated allow-list like every other activator-owned route.
func TestWakeDeadlineRequiresTheControlToken(t *testing.T) {
	if unauthenticatedPatterns["POST /wake-deadline"] {
		t.Fatal("the wake deadline endpoint is on the unauthenticated allow-list")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /wake-deadline", func(http.ResponseWriter, *http.Request) {
		t.Error("the wake deadline endpoint was served without the control token")
	})
	response := httptest.NewRecorder()
	requireControlToken(mux, "secret").ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/wake-deadline?generation=1", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want the control token required", response.Code)
	}
}
