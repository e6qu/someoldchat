package activator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/lease"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
)

type WakeFunc func(context.Context, uint64) error

// defaultWakeAttempts bounds the recovery attempts a single wake generation gets
// before the stack is durably recorded FAILED, as specs/scale-to-zero.md
// requires. One transient cold-start error must not be terminal.
const defaultWakeAttempts = 3

type Handler struct {
	context    context.Context
	cancel     context.CancelFunc
	controller *lifecycle.Controller
	wake       WakeFunc
	forward    http.Handler
	spool      Spool
	spoolOwner string
	// drainMu admits one spool drain at a time. It is never held while the
	// stack is serving: once the stack is ACTIVE and the spool is empty,
	// requests are proxied directly and never touch this lock.
	drainMu *sync.Mutex
	// queueMu guards the waiter map, and covers enqueue-then-register as one
	// step so a drain cannot deliver a request before its waiter exists.
	queueMu      *sync.Mutex
	waiters      map[uint64]chan spoolResult
	spoolDrained *atomic.Bool
	maxBody      int64
	wakeWait     time.Duration
	wakeAttempts int
	metrics      *observability.Registry
}

func newHandler(ctx context.Context, controller *lifecycle.Controller, wake WakeFunc, wakeDeadline time.Duration, metrics *observability.Registry) (Handler, error) {
	if ctx == nil {
		return Handler{}, errors.New("activator requires a context")
	}
	if controller == nil {
		return Handler{}, errors.New("activator requires a lifecycle controller")
	}
	if wake == nil {
		return Handler{}, errors.New("activator requires a wake driver")
	}
	if wakeDeadline <= 0 {
		return Handler{}, errors.New("activator requires a positive wake deadline")
	}
	if metrics == nil {
		return Handler{}, errors.New("activator requires metrics")
	}
	operationContext, cancel := context.WithCancel(ctx)
	handler := Handler{
		context: operationContext, cancel: cancel, controller: controller, wake: wake, metrics: metrics,
		drainMu: &sync.Mutex{}, queueMu: &sync.Mutex{}, waiters: make(map[uint64]chan spoolResult),
		spoolDrained: &atomic.Bool{}, wakeWait: wakeDeadline, wakeAttempts: defaultWakeAttempts,
	}
	handler.recordLifecycleState()
	return handler, nil
}

func NewForwardingHandler(ctx context.Context, controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, maxBodyBytes int64, wakeDeadline time.Duration, metrics *observability.Registry) (Handler, error) {
	if forward == nil {
		return Handler{}, errors.New("forwarding activator requires a forward handler")
	}
	if maxBodyBytes <= 0 {
		return Handler{}, errors.New("forwarding activator requires a positive body limit")
	}
	handler, err := newHandler(ctx, controller, wake, wakeDeadline, metrics)
	if err != nil {
		return Handler{}, err
	}
	handler.forward, handler.maxBody = forward, maxBodyBytes
	return handler, nil
}

func NewDurableForwardingHandler(ctx context.Context, controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, spool Spool, spoolOwner string, maxBodyBytes int64, wakeDeadline time.Duration, metrics *observability.Registry) (Handler, error) {
	if spool == nil {
		return Handler{}, errors.New("durable forwarding activator requires a request spool")
	}
	if strings.TrimSpace(spoolOwner) == "" {
		return Handler{}, errors.New("durable forwarding activator requires a spool owner")
	}
	handler, err := NewForwardingHandler(ctx, controller, wake, forward, maxBodyBytes, wakeDeadline, metrics)
	if err != nil {
		return Handler{}, err
	}
	handler.spool, handler.spoolOwner = spool, spoolOwner
	go handler.drainLoop()
	return handler, nil
}

// Close stops activator-owned wake and spool work. Durable requests remain in
// the spool and a replacement activator can reclaim them after their leases
// expire.
func (h Handler) Close() error {
	if h.cancel == nil {
		return errors.New("activator is not initialized")
	}
	h.cancel()
	return nil
}

func (h Handler) RegisterForwarding(mux *http.ServeMux) {
	mux.HandleFunc("POST /activate", h.activate)
	mux.HandleFunc("GET /healthz", h.health)
	mux.Handle("/", h)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.recordLifecycleState()
	if h.forward == nil {
		http.Error(w, "forwarding unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.spool != nil && !h.servingDirectly() {
		h.serveDurable(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	if err := h.ensureActive(r.Context()); err != nil {
		h.recordRejected(0)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "service waking", http.StatusServiceUnavailable)
		return
	}
	h.forward.ServeHTTP(w, r)
}

// servingDirectly reports whether the stack is up and the spool has been fully
// drained, in which case a request must be proxied straight through.
//
// Spooling a request that the stack can serve now would buffer the whole
// response in memory, cap it at the body limit, serialize every request behind
// one drain, and make streaming responses and protocol upgrades impossible. The
// spool exists to hold requests that arrive while the stack is not up, and the
// drained flag preserves the documented ordering guarantee: nothing overtakes a
// request that is still queued.
func (h Handler) servingDirectly() bool {
	if !h.spoolDrained.Load() {
		return false
	}
	state, _ := h.controller.Snapshot()
	return state == lifecycle.StateActive
}

type spoolResult struct {
	response capturedResponse
	err      error
}

func (h Handler) serveDurable(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBody+1))
	if err != nil {
		h.recordRejected(0)
		if isBodyTooLargeError(err) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "request body unavailable", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > h.maxBody {
		h.recordRejected(int64(len(body)))
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	// Enqueue and waiter registration are one step: a drain that claimed the
	// entry before the waiter existed would deliver it and drop the outcome,
	// leaving the client to time out on a request the stack already applied.
	h.queueMu.Lock()
	id, err := h.spool.Enqueue(r.Context(), r, body)
	if err != nil {
		h.queueMu.Unlock()
		h.recordRejected(int64(len(body)))
		w.Header().Set("Retry-After", "1")
		if errors.Is(err, ErrSpoolCapacity) {
			// Overload is a bounded, expected condition. It must be
			// distinguishable in logs and metrics from a broken control store.
			h.metrics.AddCounter("sameoldchat_activator_spool_overflow_total", 1)
			http.Error(w, "request spool is full", http.StatusServiceUnavailable)
			return
		}
		h.metrics.AddCounter("sameoldchat_activator_spool_failures_total", 1)
		http.Error(w, "request spool unavailable", http.StatusServiceUnavailable)
		return
	}
	h.spoolDrained.Store(false)
	h.metrics.AddCounter("sameoldchat_activator_buffered_requests_total", 1)
	h.metrics.AddCounter("sameoldchat_activator_buffered_bytes_total", uint64(len(body)))
	result := make(chan spoolResult, 1)
	h.waiters[id] = result
	h.queueMu.Unlock()
	defer h.removeSpoolWaiter(id, result)
	go func() {
		if err := h.processSpool(h.context); err != nil {
			h.completeSpoolRequest(id, spoolResult{err: err})
		}
	}()
	// The wait is bounded like every other activator limit. Without it a
	// durable request whose completion is owned by another replica waits
	// forever, holding a server goroutine and connection.
	waitContext, cancel := context.WithTimeout(r.Context(), h.wakeWait)
	defer cancel()
	select {
	case value := <-result:
		if value.err != nil && value.response.status == 0 {
			h.recordRejected(int64(len(body)))
			w.Header().Set("Retry-After", "1")
			http.Error(w, "service waking", http.StatusServiceUnavailable)
			return
		}
		writeCapturedResponse(w, value.response)
	case <-waitContext.Done():
		if r.Context().Err() != nil {
			return
		}
		// The entry stays spooled for the drain worker; the client is told to
		// retry rather than held open indefinitely.
		h.recordRejected(int64(len(body)))
		w.Header().Set("Retry-After", "1")
		http.Error(w, "service waking", http.StatusServiceUnavailable)
	}
}

// drainLoop is the single owned drain worker. Without it the only drain trigger
// is an inbound request, so a request accepted just before an activator restart
// sits in the control store until some unrelated client happens to arrive.
func (h Handler) drainLoop() {
	interval := h.wakeWait / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-h.context.Done():
			return
		case <-timer.C:
		}
		timer.Reset(interval)
		pending, err := h.spool.Pending(h.context)
		if err != nil {
			h.metrics.AddCounter("sameoldchat_activator_spool_failures_total", 1)
			continue
		}
		if pending == 0 {
			// Nothing is queued, so there is no reason to wake the stack.
			h.spoolDrained.Store(true)
			continue
		}
		if err := h.processSpool(h.context); err != nil && h.context.Err() == nil {
			h.metrics.AddCounter("sameoldchat_activator_drain_failures_total", 1)
		}
	}
}

func (h Handler) processSpool(ctx context.Context) error {
	if ctx == nil {
		return errors.New("activator spool processor requires a context")
	}
	if h.drainMu == nil {
		return errors.New("activator spool processor is not initialized")
	}
	h.drainMu.Lock()
	defer h.drainMu.Unlock()
	if err := h.ensureActive(ctx); err != nil {
		return err
	}
	for {
		requests, err := h.spool.Claim(ctx, h.spoolOwner, 1, h.wakeWait)
		if err != nil {
			return err
		}
		if len(requests) == 0 {
			h.spoolDrained.Store(true)
			return nil
		}
		if err := h.deliverSpooledRequest(ctx, requests[0]); err != nil {
			return err
		}
	}
}

// deliverSpooledRequest forwards one spooled request and always removes it.
//
// The application handler runs to completion before the outcome is known, so its
// mutations are already applied by the time a capture failure or a 5xx status is
// observed. Keeping the entry in that case does not retry anything safely: it
// re-executes an applied, possibly non-idempotent request every time the lease
// expires, and blocks every later entry behind it forever. The delivery is
// therefore recorded once and the caller is told what happened.
func (h Handler) deliverSpooledRequest(ctx context.Context, request SpooledRequest) error {
	replay, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
	if err != nil {
		return err
	}
	replay.Header = request.Header.Clone()
	removeHopByHopHeaders(replay.Header)
	replay.Host = request.Host
	if replay.Header.Get("Idempotency-Key") == "" {
		replay.Header.Set("Idempotency-Key", "sameoldchat-spool-"+strconv.FormatUint(request.ID, 10))
	}
	capture := newCapturedResponse(h.maxBody)
	deliveryErr := h.forwardSpoolRequest(ctx, replay, request.ID, capture)
	if deliveryErr != nil {
		// The delivery never completed, so the entry stays claimable and is
		// retried by the drain worker after the lease expires.
		h.completeSpoolRequest(request.ID, spoolResult{err: deliveryErr})
		return deliveryErr
	}
	response := capture.response()
	if capture.err != nil {
		h.metrics.AddCounter("sameoldchat_activator_capture_failures_total", 1)
		response = capturedResponse{header: make(http.Header), status: http.StatusBadGateway}
	}
	deleteErr := h.spool.Delete(ctx, h.spoolOwner, request.ID)
	if deleteErr != nil {
		h.metrics.AddCounter("sameoldchat_activator_spool_delete_failures_total", 1)
	}
	h.completeSpoolRequest(request.ID, spoolResult{response: response, err: errors.Join(capture.err, deleteErr)})
	if deleteErr != nil {
		return deleteErr
	}
	return nil
}

// forwardSpoolRequest replays one spooled request while holding its lease. A lost
// lease means another owner is running the same work, which is the condition
// behind duplicate delivery, so lease.While always surfaces it.
//
// A capture failure is not a delivery failure: the application ran to completion,
// so the caller decides what to do with an applied request whose response could
// not be held.
func (h Handler) forwardSpoolRequest(ctx context.Context, request *http.Request, id uint64, capture *capturedWriter) error {
	err := lease.While(ctx, h.wakeWait,
		func(renewContext context.Context) error {
			return h.spool.Renew(renewContext, h.spoolOwner, []uint64{id}, h.wakeWait)
		},
		func(deliveryContext context.Context) error {
			h.forward.ServeHTTP(capture, request.WithContext(deliveryContext))
			return nil
		},
	)
	if err != nil {
		h.metrics.AddCounter("sameoldchat_activator_lease_lost_total", 1)
		return errors.Join(err, capture.err)
	}
	return nil
}

func (h Handler) completeSpoolRequest(id uint64, result spoolResult) {
	if h.queueMu == nil {
		return
	}
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	waiter, ok := h.waiters[id]
	if !ok {
		return
	}
	delete(h.waiters, id)
	waiter <- result
}

func (h Handler) removeSpoolWaiter(id uint64, waiter chan spoolResult) {
	if h.queueMu == nil {
		return
	}
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	if current, ok := h.waiters[id]; ok && current == waiter {
		delete(h.waiters, id)
	}
}

// removeHopByHopHeaders keeps replayed requests semantically identical while
// preventing one transport's connection metadata from crossing the boundary.
func removeHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Proxy-Connection", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}

type capturedResponse struct {
	header http.Header
	status int
	body   []byte
}

type capturedWriter struct {
	capturedResponse
	max int64
	err error
}

func (w *capturedWriter) Header() http.Header { return w.header }

func newCapturedResponse(max int64) *capturedWriter {
	return &capturedWriter{capturedResponse: capturedResponse{header: make(http.Header)}, max: max}
}

func (w *capturedWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *capturedWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if int64(len(w.body)+len(body)) > w.max {
		w.err = errors.New("forwarded response exceeds capture limit")
		return 0, w.err
	}
	w.body = append(w.body, body...)
	return len(body), nil
}

func (w *capturedWriter) response() capturedResponse {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.capturedResponse
}

func writeCapturedResponse(w http.ResponseWriter, response capturedResponse) {
	for key, values := range response.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	status := response.status
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.body)
}

func (h Handler) ensureActive(ctx context.Context) error {
	started := time.Now()
	defer func() { h.metrics.ObserveDuration("sameoldchat_request_to_active_duration", time.Since(started)) }()
	waitContext, cancel := context.WithTimeout(ctx, h.wakeWait)
	defer cancel()
	for {
		// Cheap cached fast path: a serving stack needs no control-store read.
		if state, _ := h.controller.Snapshot(); state == lifecycle.StateActive {
			return nil
		}
		fence, err := h.controller.BeginWake()
		if err == nil {
			select {
			case err := <-h.startWake(fence):
				return err
			case <-waitContext.Done():
				return waitContext.Err()
			}
		}
		// Only a nil error grants this caller the wake generation. An
		// already-active stack is simply ready to serve.
		if errors.Is(err, lifecycle.ErrAlreadyActive) {
			return nil
		}
		// A lost compare-and-swap means another replica advanced the durable
		// record. The controller has adopted it, so the next attempt sees
		// reality; failing the request here would 503 the caller for a stack
		// that is coming up.
		if !errors.Is(err, lifecycle.ErrWakeInProgress) && !errors.Is(err, lifecycle.ErrStateConflict) {
			return err
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		select {
		case <-waitContext.Done():
			ticker.Stop()
			return waitContext.Err()
		case <-ticker.C:
			ticker.Stop()
		}
	}
}

func (h Handler) health(w http.ResponseWriter, _ *http.Request) {
	state, generation := h.controller.Snapshot()
	h.recordLifecycleState()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"state":"` + string(state) + `","generation":` + strconv.FormatUint(generation, 10) + `}`))
}

func (h Handler) activate(w http.ResponseWriter, r *http.Request) {
	h.recordLifecycleState()
	fence, err := h.controller.BeginWake()
	if err != nil {
		if errors.Is(err, lifecycle.ErrAlreadyActive) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if errors.Is(err, lifecycle.ErrWakeInProgress) || errors.Is(err, lifecycle.ErrStateConflict) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.Error(w, "activation unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case err := <-h.startWake(fence):
		if err != nil {
			http.Error(w, "activation failed", http.StatusBadGateway)
			return
		}
	case <-r.Context().Done():
		return
	}
	w.Header().Set("X-Lifecycle-Generation", strconv.FormatUint(fence, 10))
	w.WriteHeader(http.StatusNoContent)
}

// Wake drives one fenced wake to completion through the same election and
// bounded-retry path as an inbound request. It is the entry point for scheduled
// activation, so a timer and a request cannot diverge in how they wake the stack.
func (h Handler) Wake(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scheduled wake requires a context")
	}
	fence, err := h.controller.BeginWake()
	if err != nil {
		if errors.Is(err, lifecycle.ErrAlreadyActive) {
			return nil
		}
		return err
	}
	select {
	case err := <-h.startWake(fence):
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h Handler) startWake(fence uint64) <-chan error {
	result := make(chan error, 1)
	go func() {
		started := time.Now()
		defer func() { h.metrics.ObserveDuration("sameoldchat_wake_driver_duration", time.Since(started)) }()
		err := h.driveWake(fence)
		switch {
		case err == nil:
			if activateErr := h.controller.Activate(fence); activateErr != nil {
				result <- activateErr
				return
			}
			h.recordLifecycleState()
			result <- nil
		case h.context.Err() != nil:
			// The activator itself is shutting down. The wake is abandoned, not
			// unrecoverable: the generation stays WAKING so the replacement
			// process resumes it through lifecycle recovery. Recording FAILED
			// here turns every rolling restart that lands during a cold start
			// into an outage that only an operator can clear.
			h.metrics.AddCounter("sameoldchat_activator_wake_abandoned_total", 1)
			result <- err
		default:
			result <- errors.Join(err, h.controller.Fail(fence))
		}
	}()
	return result
}

// driveWake spends the wake generation's bounded attempt budget before the
// caller declares the generation failed.
func (h Handler) driveWake(fence uint64) error {
	var last error
	for attempt := 0; attempt < h.wakeAttempts; attempt++ {
		if attempt > 0 {
			if err := h.backoff(attempt); err != nil {
				return errors.Join(last, err)
			}
		}
		wakeContext, cancel := context.WithTimeout(h.context, h.wakeWait)
		err := h.wake(wakeContext, fence)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		if h.context.Err() != nil {
			return err
		}
		// A rejected transition or a stale fence is not transient: no number of
		// retries makes this caller the owner of the generation.
		if errors.Is(err, lifecycle.ErrInvalidTransition) || errors.Is(err, lifecycle.ErrStaleFence) {
			return err
		}
		h.metrics.AddCounter("sameoldchat_activator_wake_attempt_failures_total", 1)
	}
	return last
}

func (h Handler) backoff(attempt int) error {
	delay := h.wakeWait / 20
	if delay <= 0 {
		delay = time.Millisecond
	}
	delay <<= attempt - 1
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-h.context.Done():
		return h.context.Err()
	case <-timer.C:
		return nil
	}
}

func isBodyTooLargeError(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

func (h Handler) recordRejected(bodyBytes int64) {
	h.metrics.AddCounter("sameoldchat_activator_rejected_requests_total", 1)
	if bodyBytes > 0 {
		h.metrics.AddCounter("sameoldchat_activator_rejected_bytes_total", uint64(bodyBytes))
	}
}

func (h Handler) recordLifecycleState() {
	state, generation := h.controller.Snapshot()
	h.metrics.SetGauge("sameoldchat_lifecycle_generation", int64(generation))
	for _, candidate := range []lifecycle.State{
		lifecycle.StateActive,
		lifecycle.StateHibernated,
		lifecycle.StateWaking,
		lifecycle.StateQuiescing,
		lifecycle.StateSnapshot,
		lifecycle.StateStopping,
		lifecycle.StateFailed,
	} {
		value := int64(0)
		if state == candidate {
			value = 1
		}
		h.metrics.SetGauge("sameoldchat_lifecycle_state_"+string(candidate), value)
	}
}
