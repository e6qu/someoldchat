package activator

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
)

// fencedWake is the wake driver every test uses. It asserts the contract
// lifecycle.Coordinator.WakeAt enforces in production: the caller must own the
// wake generation, so the controller must be WAKING at exactly this fence.
//
// The previous fakes returned nil for any fence. That is what hid the defect
// where a caller who lost the election still believed it had won: a spurious
// second wake was indistinguishable from a legitimate one, so no test could see
// the running stack being driven into FAILED.
type fencedWake struct {
	controller *lifecycle.Controller
	mu         sync.Mutex
	calls      int
	block      chan struct{}
	failures   int
	failWith   error
}

func newFencedWake(controller *lifecycle.Controller) *fencedWake {
	return &fencedWake{controller: controller}
}

func (w *fencedWake) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

func (w *fencedWake) wake(ctx context.Context, fence uint64) error {
	w.mu.Lock()
	w.calls++
	block := w.block
	fail := w.failures > 0
	if fail {
		w.failures--
	}
	w.mu.Unlock()
	state, generation := w.controller.Snapshot()
	if state != lifecycle.StateWaking || generation != fence {
		return errors.New("wake was not fenced by activator: state=" + string(state))
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail {
		if w.failWith != nil {
			return w.failWith
		}
		return errors.New("transient cold start failure")
	}
	return nil
}

func newTestHandler(t *testing.T, controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, wakeDeadline time.Duration) Handler {
	t.Helper()
	handler, err := NewForwardingHandler(context.Background(), controller, wake, forward, 1024, wakeDeadline, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	return handler
}

func createdHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
}

func TestActivateWakesExactlyOnce(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	h := newTestHandler(t, controller, driver.wake, createdHandler(), time.Second)
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/activate", nil))
	if res.Code != http.StatusNoContent || driver.count() != 1 {
		t.Fatalf("status=%d calls=%d", res.Code, driver.count())
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/activate", nil))
	if res.Code != http.StatusNoContent || driver.count() != 1 {
		t.Fatalf("second status=%d calls=%d", res.Code, driver.count())
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want the serving stack intact", state)
	}
}

// A caller that reaches the election after the stack came up owns no wake
// generation. Treating the already-active answer as a won election drove the
// running stack to FAILED, which 503s the whole deployment until an operator
// posts /recover.
func TestWakeOnAnActiveStackDoesNotFailTheRunningStack(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	h := newTestHandler(t, controller, driver.wake, createdHandler(), time.Second)
	if err := h.Wake(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want active", state)
	}
	if err := h.Wake(context.Background()); err != nil {
		t.Fatalf("wake of an already-active stack err=%v, want it to proceed", err)
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want the serving stack untouched", state)
	}
	if driver.count() != 1 {
		t.Fatalf("wake calls=%d, want exactly one restoration", driver.count())
	}
	// The stack must still serve traffic rather than answer 503 forever.
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d, want the running stack to keep serving", res.Code)
	}
}

func TestActivateWithoutDriverFailsClosed(t *testing.T) {
	if _, err := NewForwardingHandler(context.Background(), lifecycle.New(lifecycle.StateHibernated), nil, createdHandler(), 1024, time.Second, observability.NewRegistry()); err == nil {
		t.Fatal("expected missing driver error")
	}
}

func TestHandlerRequiresExplicitContext(t *testing.T) {
	if _, err := NewForwardingHandler(nil, lifecycle.New(lifecycle.StateHibernated), func(context.Context, uint64) error { return nil }, createdHandler(), 1024, time.Second, observability.NewRegistry()); err == nil {
		t.Fatal("nil context was accepted")
	}
}

// A zero wake deadline produced an already-expired wake context, so the wake
// failed instantly and the generation was recorded FAILED.
func TestHandlerRequiresPositiveWakeDeadline(t *testing.T) {
	if _, err := NewForwardingHandler(context.Background(), lifecycle.New(lifecycle.StateHibernated), func(context.Context, uint64) error { return nil }, createdHandler(), 1024, 0, observability.NewRegistry()); err == nil {
		t.Fatal("zero wake deadline was accepted")
	}
}

func TestFailedLifecycleDoesNotImplicitlyRetryWake(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Fail(fence); err != nil {
		t.Fatal(err)
	}
	driver := newFencedWake(controller)
	h := newTestHandler(t, controller, driver.wake, createdHandler(), time.Second)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusServiceUnavailable || driver.count() != 0 {
		t.Fatalf("status=%d wake calls=%d, want explicit recovery", res.Code, driver.count())
	}
}

func TestActivatorOwnsWakeFenceBeforeDriver(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	h := newTestHandler(t, controller, driver.wake, createdHandler(), time.Second)
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/activate", nil))
	if res.Code != http.StatusNoContent || driver.count() != 1 {
		t.Fatalf("status=%d calls=%d", res.Code, driver.count())
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want active", state)
	}
}

func TestForwardingActivatorWakesThenForwards(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	forwarded := 0
	h := newTestHandler(t, controller, driver.wake, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusCreated)
	}), time.Second)
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusCreated || forwarded != 1 {
		t.Fatalf("status=%d forwarded=%d", res.Code, forwarded)
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want active", state)
	}
}

func TestConcurrentFirstRequestsShareOneWake(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	driver.block = make(chan struct{})
	var mu sync.Mutex
	forwarded := 0
	h := newTestHandler(t, controller, driver.wake, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		forwarded++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}), time.Second)

	results := make(chan int, 2)
	for range 2 {
		go func() {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
			results <- response.Code
		}()
	}
	// Both requests are in the wait loop before the wake is released, so the
	// loser observes the election it lost.
	waitForState(t, controller, lifecycle.StateWaking)
	close(driver.block)
	for range 2 {
		if code := <-results; code != http.StatusCreated {
			t.Fatalf("status=%d, want forwarded response", code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if driver.count() != 1 || forwarded != 2 {
		t.Fatalf("wake calls=%d forwarded=%d, want one wake and two forwards", driver.count(), forwarded)
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want the shared wake to leave the stack active", state)
	}
}

// specs/scale-to-zero.md:145 requires FAILED only after bounded recovery
// attempts. A single blipping cold start must not wedge the deployment.
func TestWakeRetriesTransientFailuresBeforeRecordingFailure(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	driver.failures = 2
	h := newTestHandler(t, controller, driver.wake, createdHandler(), 200*time.Millisecond)
	if err := h.Wake(context.Background()); err != nil {
		t.Fatalf("wake=%v, want the bounded retry budget to absorb transient failures", err)
	}
	if driver.count() != 3 {
		t.Fatalf("wake calls=%d, want the attempt budget spent", driver.count())
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateActive {
		t.Fatalf("state=%s, want active", state)
	}
}

func TestWakeRecordsFailureAfterTheAttemptBudgetIsSpent(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	driver.failures = 100
	h := newTestHandler(t, controller, driver.wake, createdHandler(), 100*time.Millisecond)
	if err := h.Wake(context.Background()); err == nil {
		t.Fatal("wake succeeded despite every attempt failing")
	}
	if state, _ := controller.Snapshot(); state != lifecycle.StateFailed {
		t.Fatalf("state=%s, want failed after the bounded budget", state)
	}
}

func TestForwardingActivatorRejectsMutationDuringQuiescence(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateActive)
	activeFence, err := controller.BeginHibernate(0)
	if err != nil || activeFence == 0 {
		t.Fatal("expected hibernation to enter quiescence with a new fence")
	}
	driver := newFencedWake(controller)
	forwarded := 0
	h := newTestHandler(t, controller, driver.wake, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusCreated)
	}), 20*time.Millisecond)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusServiceUnavailable || driver.count() != 0 || forwarded != 0 {
		t.Fatalf("status=%d wake calls=%d forwarded=%d, want explicit quiescence rejection", res.Code, driver.count(), forwarded)
	}
}

func TestForwardingActivatorRejectsOversizedBody(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateActive)
	handler, err := NewForwardingHandler(context.Background(), controller, func(context.Context, uint64) error { return nil }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}), 8, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	mux := http.NewServeMux()
	handler.RegisterForwarding(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("oversized body")))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want body-limit rejection", res.Code)
	}
}

func newTestSpool(t *testing.T) *SQLiteSpool {
	t.Helper()
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	return spool
}

func newDurableTestHandler(t *testing.T, controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, spool Spool, owner string, maxBody int64, wakeDeadline time.Duration) Handler {
	t.Helper()
	handler, err := NewDurableForwardingHandler(context.Background(), controller, wake, forward, spool, owner, maxBody, wakeDeadline, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	return handler
}

func TestDurableForwardingRejectsMalformedBodyAsBadRequest(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	h := newDurableTestHandler(t, controller, newFencedWake(controller).wake, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), newTestSpool(t), "activator-a", 1024, time.Second)
	request := httptest.NewRequest(http.MethodPost, "/api/message", &failingReader{})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want malformed body rejection", response.Code)
	}
}

// An activator shutting down mid-wake must not record the generation FAILED. A
// rolling restart that lands during a cold start would otherwise take the whole
// deployment down until a human posts /recover.
func TestHandlerCloseCancelsWakeDriverWithoutRecordingFailure(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	wakeStarted := make(chan struct{})
	wakeCanceled := make(chan struct{})
	h, err := NewForwardingHandler(context.Background(), controller, func(ctx context.Context, _ uint64) error {
		close(wakeStarted)
		<-ctx.Done()
		close(wakeCanceled)
		return ctx.Err()
	}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
		requestDone <- response.Code
	}()
	<-wakeStarted
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakeCanceled:
	case <-time.After(time.Second):
		t.Fatal("wake driver was not canceled")
	}
	if code := <-requestDone; code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want wake failure", code)
	}
	// WAKING is resumable by lifecycle recovery on the next boot; FAILED is not.
	waitForState(t, controller, lifecycle.StateWaking)
}

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, errors.New("malformed request body") }
func (*failingReader) Close() error             { return nil }

func TestClientTimeoutDoesNotCancelSharedWake(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := newFencedWake(controller)
	driver.block = make(chan struct{})
	h := newTestHandler(t, controller, driver.wake, createdHandler(), time.Second)
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")).WithContext(requestContext)
	result := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		result <- response.Code
	}()
	waitForState(t, controller, lifecycle.StateWaking)
	cancel()
	if code := <-result; code != http.StatusServiceUnavailable {
		t.Fatalf("timed out request status=%d", code)
	}
	close(driver.block)
	waitForState(t, controller, lifecycle.StateActive)
}

func waitForState(t *testing.T, controller *lifecycle.Controller, want lifecycle.State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ := controller.Snapshot(); state == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	state, _ := controller.Snapshot()
	t.Fatalf("state=%s, want %s", state, want)
}

func TestDurableForwardingSpoolsBeforeWakeAndDeletesAfterDelivery(t *testing.T) {
	spool := newTestSpool(t)
	controller := lifecycle.New(lifecycle.StateHibernated)
	var delivered, idempotencyKey string
	var hopByHopHeadersForwarded bool
	h := newDurableTestHandler(t, controller, newFencedWake(controller).wake, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read replay body: %v", err)
			return
		}
		delivered = string(body)
		idempotencyKey = r.Header.Get("Idempotency-Key")
		hopByHopHeadersForwarded = r.Header.Get("Connection") != "" || r.Header.Get("Keep-Alive") != "" || r.Header.Get("X-Per-Hop") != ""
		w.Header().Set("X-Replayed", "true")
		w.WriteHeader(http.StatusCreated)
	}), spool, "activator-a", 1024, time.Second)
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("durable body"))
	request.Header.Set("Connection", "keep-alive, X-Per-Hop")
	request.Header.Set("Keep-Alive", "timeout=5")
	request.Header.Set("X-Per-Hop", "must-not-cross")
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, request)
	if result.Code != http.StatusCreated || delivered != "durable body" || idempotencyKey == "" || hopByHopHeadersForwarded || result.Header().Get("X-Replayed") != "true" {
		t.Fatalf("status=%d delivered=%q idempotency=%q hop-by-hop=%t headers=%v", result.Code, delivered, idempotencyKey, hopByHopHeadersForwarded, result.Header())
	}
	remaining, err := spool.List(context.Background(), 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
	}
}

// This test keeps the stack hibernated so the delivery genuinely goes through the
// spool. It previously started ACTIVE, which now proxies directly and would no
// longer exercise lease renewal at all.
func TestDurableForwardingRenewsLeaseDuringSlowDelivery(t *testing.T) {
	const leaseDuration = 300 * time.Millisecond
	spool := newTestSpool(t)
	controller := lifecycle.New(lifecycle.StateHibernated)
	h := newDurableTestHandler(t, controller, newFencedWake(controller).wake, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * leaseDuration)
		w.WriteHeader(http.StatusCreated)
	}), spool, "slow-owner", 1024, 4*leaseDuration)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("slow body")))
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d, want created", response.Code)
	}
	remaining, err := spool.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("slow request remained in spool: %+v", remaining)
	}
}

func TestDurableForwardingRejectsQueueOverflowWithRetryAfter(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 8, MaxQueuedRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	queued := httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("queued"))
	if _, err := spool.Enqueue(context.Background(), queued, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	controller := lifecycle.New(lifecycle.StateFailed)
	h := newDurableTestHandler(t, controller, func(context.Context, uint64) error { return nil }, createdHandler(), spool, "activator-a", 1024, time.Second)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("overflow")))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("status=%d retry-after=%q, want bounded overflow rejection", response.Code, response.Header().Get("Retry-After"))
	}
	if response.Body.String() == "" || !strings.Contains(response.Body.String(), "full") {
		t.Fatalf("body=%q, want overload distinguishable from a storage failure", response.Body.String())
	}
}

// A response larger than the capture limit used to leave the entry in the spool
// forever: the application had already applied the mutation, the drain aborted,
// and the same entry was re-delivered every time its lease expired, blocking the
// queue head permanently.
func TestOversizedCapturedResponseIsDeliveredOnceAndRemoved(t *testing.T) {
	spool := newTestSpool(t)
	controller := lifecycle.New(lifecycle.StateHibernated)
	var mu sync.Mutex
	executions := 0
	h := newDurableTestHandler(t, controller, newFencedWake(controller).wake, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		executions++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 64))
	}), spool, "activator-a", 16, 300*time.Millisecond)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/files.upload", strings.NewReader("body")))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want a single gateway error for an uncapturable response", response.Code)
	}
	remaining, err := spool.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("pending=%d, want the applied request removed instead of replayed forever", remaining)
	}
	// A second queued request must not be blocked behind it.
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/tiny", strings.NewReader("x")))
	if second.Code == http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want the queue head unblocked", second.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions > 2 {
		t.Fatalf("application executions=%d, want no re-execution of an applied request", executions)
	}
}

// Once the stack is up and the spool is drained, requests must be proxied
// straight through. Buffering every response and holding one process-wide lock
// across the forward serialized all traffic and made streaming impossible.
func TestActiveStackProxiesConcurrentlyWithoutBuffering(t *testing.T) {
	spool := newTestSpool(t)
	controller := lifecycle.New(lifecycle.StateHibernated)
	release := make(chan struct{})
	var mu sync.Mutex
	inFlight, peak := 0, 0
	h := newDurableTestHandler(t, controller, newFencedWake(controller).wake, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		concurrent := inFlight
		mu.Unlock()
		if r.URL.Path == "/api/slow" && concurrent == 1 {
			<-release
		}
		w.WriteHeader(http.StatusOK)
		// Larger than the capture limit: a buffered path could not answer this.
		_, _ = w.Write(make([]byte, 4096))
		mu.Lock()
		inFlight--
		mu.Unlock()
	}), spool, "activator-a", 1024, time.Second)
	// Wake the stack and drain the spool through one ordinary request.
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/warm", nil))
	waitForState(t, controller, lifecycle.StateActive)
	waitForDirectServing(t, h)

	slowDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/slow", nil))
		slowDone <- response.Code
	}()
	// The fast request must complete while the slow one is still in flight.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("slow request never entered the application handler")
		}
		mu.Lock()
		started := inFlight
		mu.Unlock()
		if started > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	fast := httptest.NewRecorder()
	h.ServeHTTP(fast, httptest.NewRequest(http.MethodGet, "/api/fast", nil))
	if fast.Code != http.StatusOK || fast.Body.Len() != 4096 {
		t.Fatalf("fast status=%d bytes=%d, want an unbuffered response past the capture limit", fast.Code, fast.Body.Len())
	}
	close(release)
	if code := <-slowDone; code != http.StatusOK {
		t.Fatalf("slow status=%d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak < 2 {
		t.Fatalf("peak concurrency=%d, want requests proxied concurrently", peak)
	}
}

func waitForDirectServing(t *testing.T, h Handler) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.servingDirectly() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("activator never switched to direct proxying after the spool drained")
}

// Nothing drained the spool except a live request, so a request accepted just
// before a restart sat in the control store until unrelated traffic arrived.
func TestBackgroundDrainDeliversSpooledRequestWithoutAWaitingClient(t *testing.T) {
	spool := newTestSpool(t)
	// The request is spooled by a handler that is then discarded, exactly as a
	// restart would leave it.
	if _, err := spool.Enqueue(context.Background(), httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("orphaned")), []byte("orphaned")); err != nil {
		t.Fatal(err)
	}
	controller := lifecycle.New(lifecycle.StateHibernated)
	delivered := make(chan string, 1)
	newDurableTestHandler(t, controller, newFencedWake(controller).wake, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		delivered <- string(body)
		w.WriteHeader(http.StatusCreated)
	}), spool, "activator-a", 1024, 200*time.Millisecond)
	select {
	case body := <-delivered:
		if body != "orphaned" {
			t.Fatalf("delivered=%q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the background drain never delivered the spooled request")
	}
}

// A durable request must not wait forever. The reachable case is a completion
// owned by another replica: this activator's own drain claims nothing, returns
// no error, and never completes the waiter. Only the request's own bound rescues
// it, so it answers 503 and leaves the entry for the drain worker.
func TestDurableWaiterIsBoundedWhenAnotherOwnerHoldsTheEntry(t *testing.T) {
	spool := &noClaimSpool{SQLiteSpool: newTestSpool(t)}
	controller := lifecycle.New(lifecycle.StateHibernated)
	h := newDurableTestHandler(t, controller, newFencedWake(controller).wake, createdHandler(), spool, "activator-a", 1024, 150*time.Millisecond)
	done := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("held")))
		done <- response.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d, want a bounded wait", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("durable request waited without a bound of its own")
	}
	// The request stays durable: it was accepted, so it must not be dropped.
	pending, err := spool.Pending(context.Background())
	if err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v, want the accepted request still queued", pending, err)
	}
}

// noClaimSpool models an entry whose lease is held by another activator replica.
type noClaimSpool struct {
	*SQLiteSpool
}

func (*noClaimSpool) Claim(context.Context, string, int, time.Duration) ([]SpooledRequest, error) {
	return nil, nil
}

// The unauthenticated probe must reveal nothing about the lifecycle, and the
// fencing generation must be served on its own token-protected route.
//
// Two defects met on this endpoint. The probe published the lifecycle state and
// the fencing generation on the same public listener as forwarded application
// traffic — two of the exact three things /metrics was protected for, and what
// specs/scale-to-zero.md:189 forbids. And because cmd/ecs-ws-activator's probe
// answers {"ok":true}, a wake-deadline publisher pointed at the WebSocket edge
// decoded a generation of 0 with no error and published every deadline against a
// fence the activator refuses forever. One shape for the public probe fixes both:
// the edge already answers it, and the generation now has a route of its own.
func TestPublicProbeRevealsNoLifecycleStateAndTheFenceHasItsOwnRoute(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	fence, err := controller.BeginWake()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewForwardingHandler(
		context.Background(), controller, newFencedWake(controller).wake,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		1<<20, time.Minute, observability.NewRegistry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)

	probe := httptest.NewRecorder()
	mux.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if body := strings.TrimSpace(probe.Body.String()); body != `{"ok":true}` {
		t.Fatalf("probe body=%q, want the same shape cmd/ecs-ws-activator answers and nothing more", body)
	}

	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/lifecycle", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("lifecycle status=%d, want 200", status.Code)
	}
	if body := strings.TrimSpace(status.Body.String()); !strings.Contains(body, `"generation":`+strconv.FormatUint(fence, 10)) || !strings.Contains(body, `"state":"waking"`) {
		t.Fatalf("lifecycle body=%q, want the state and the fencing generation", body)
	}
}
