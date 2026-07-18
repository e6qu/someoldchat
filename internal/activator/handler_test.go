package activator

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
)

type coordinatorWake struct {
	controller *lifecycle.Controller
	started    bool
}

func (w *coordinatorWake) wake(_ context.Context, fence uint64) error {
	state, generation := w.controller.Snapshot()
	if state != lifecycle.StateWaking || generation != fence {
		return errors.New("wake was not fenced by activator")
	}
	w.started = true
	return nil
}

func TestActivateWakesExactlyOnce(t *testing.T) {
	calls := 0
	h, err := NewHandler(lifecycle.New(lifecycle.StateHibernated), func(_ context.Context, _ uint64) error { calls++; return nil }, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/activate", nil))
	if res.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("status=%d calls=%d", res.Code, calls)
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/activate", nil))
	if res.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("second status=%d calls=%d", res.Code, calls)
	}
}

func TestActivateWithoutDriverFailsClosed(t *testing.T) {
	if _, err := NewHandler(lifecycle.New(lifecycle.StateHibernated), nil, observability.NewRegistry()); err == nil {
		t.Fatal("expected missing driver error")
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
	calls := 0
	h, err := NewForwardingHandler(controller, func(context.Context, uint64) error {
		calls++
		return nil
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}), 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusServiceUnavailable || calls != 0 {
		t.Fatalf("status=%d wake calls=%d, want explicit recovery", res.Code, calls)
	}
}

func TestActivatorOwnsWakeFenceBeforeDriver(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	driver := &coordinatorWake{controller: controller}
	h, err := NewHandler(controller, driver.wake, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/activate", nil))
	if res.Code != http.StatusNoContent || !driver.started {
		t.Fatalf("status=%d started=%t", res.Code, driver.started)
	}
	state, _ := controller.Snapshot()
	if state != lifecycle.StateActive {
		t.Fatalf("state=%s, want active", state)
	}
}

func TestForwardingActivatorWakesThenForwards(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	forwarded := 0
	h, err := NewForwardingHandler(controller, func(_ context.Context, _ uint64) error { return nil }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusCreated)
	}), 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusCreated || forwarded != 1 {
		t.Fatalf("status=%d forwarded=%d", res.Code, forwarded)
	}
	state, _ := controller.Snapshot()
	if state != lifecycle.StateActive {
		t.Fatalf("state=%s, want active", state)
	}
}

func TestConcurrentFirstRequestsShareOneWake(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	wakeStarted := make(chan struct{})
	releaseWake := make(chan struct{})
	var wakeCalls, forwarded int
	var mu sync.Mutex
	h, err := NewForwardingHandler(controller, func(ctx context.Context, _ uint64) error {
		mu.Lock()
		wakeCalls++
		mu.Unlock()
		close(wakeStarted)
		select {
		case <-releaseWake:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		forwarded++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}), 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan int, 2)
	for range 2 {
		go func() {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
			results <- response.Code
		}()
	}
	<-wakeStarted
	close(releaseWake)
	for range 2 {
		if code := <-results; code != http.StatusCreated {
			t.Fatalf("status=%d, want forwarded response", code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if wakeCalls != 1 || forwarded != 2 {
		t.Fatalf("wake calls=%d forwarded=%d, want one wake and two forwards", wakeCalls, forwarded)
	}
}

func TestForwardingActivatorRejectsMutationDuringQuiescence(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateActive)
	activeFence, err := controller.BeginHibernate(0)
	if err != nil || activeFence == 0 {
		t.Fatal("expected hibernation to enter quiescence with a new fence")
	}
	wakeCalls := 0
	forwarded := 0
	h, err := NewForwardingHandler(controller, func(context.Context, uint64) error {
		wakeCalls++
		return nil
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusCreated)
	}), 1024, 20*time.Millisecond, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")))
	if res.Code != http.StatusServiceUnavailable || wakeCalls != 0 || forwarded != 0 {
		t.Fatalf("status=%d wake calls=%d forwarded=%d, want explicit quiescence rejection", res.Code, wakeCalls, forwarded)
	}
}

func TestForwardingActivatorRejectsOversizedBody(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateActive)
	h, err := NewForwardingHandler(controller, func(context.Context, uint64) error { return nil }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}), 8, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterForwarding(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("oversized body")))
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want body-limit rejection", res.Code)
	}
}

func TestClientTimeoutDoesNotCancelSharedWake(t *testing.T) {
	controller := lifecycle.New(lifecycle.StateHibernated)
	started := make(chan struct{})
	release := make(chan struct{})
	h, err := NewForwardingHandler(controller, func(ctx context.Context, _ uint64) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }), 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("hello")).WithContext(requestContext)
	result := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		result <- response.Code
	}()
	<-started
	cancel()
	if code := <-result; code != http.StatusServiceUnavailable {
		t.Fatalf("timed out request status=%d", code)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, _ := controller.Snapshot()
		if state == lifecycle.StateActive {
			return
		}
		time.Sleep(time.Millisecond)
	}
	state, _ := controller.Snapshot()
	t.Fatalf("wake state=%s, want active", state)
}

func TestDurableForwardingSpoolsBeforeWakeAndDeletesAfterDelivery(t *testing.T) {
	spool, err := OpenSQLiteSpool(filepath.Join(t.TempDir(), "control.db"), []byte("01234567890123456789012345678901"), SpoolLimits{MaxBodyBytes: 1024, MaxQueuedBytes: 4096, MaxQueuedRequests: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	controller := lifecycle.New(lifecycle.StateHibernated)
	var delivered, idempotencyKey string
	var hopByHopHeadersForwarded bool
	h, err := NewDurableForwardingHandler(controller, func(context.Context, uint64) error { return nil }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}), spool, "activator-a", 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
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
	h, err := NewDurableForwardingHandler(controller, func(context.Context, uint64) error { return nil }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}), spool, "activator-a", 1024, time.Second, observability.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/message", strings.NewReader("overflow")))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("status=%d retry-after=%q, want bounded overflow rejection", response.Code, response.Header().Get("Retry-After"))
	}
}
