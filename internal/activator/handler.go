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
	"time"

	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/observability"
)

type WakeFunc func(context.Context, uint64) error

type Handler struct {
	context    context.Context
	cancel     context.CancelFunc
	controller *lifecycle.Controller
	wake       WakeFunc
	forward    http.Handler
	spool      Spool
	spoolOwner string
	processMu  *sync.Mutex
	queueMu    *sync.Mutex
	waiters    map[uint64]chan spoolResult
	maxBody    int64
	wakeWait   time.Duration
	metrics    *observability.Registry
}

func NewHandler(ctx context.Context, controller *lifecycle.Controller, wake WakeFunc, metrics *observability.Registry) (Handler, error) {
	if ctx == nil {
		return Handler{}, errors.New("activator requires a context")
	}
	if controller == nil {
		return Handler{}, errors.New("activator requires a lifecycle controller")
	}
	if wake == nil {
		return Handler{}, errors.New("activator requires a wake driver")
	}
	if metrics == nil {
		return Handler{}, errors.New("activator requires metrics")
	}
	operationContext, cancel := context.WithCancel(ctx)
	handler := Handler{context: operationContext, cancel: cancel, controller: controller, wake: wake, metrics: metrics, processMu: &sync.Mutex{}, queueMu: &sync.Mutex{}, waiters: make(map[uint64]chan spoolResult)}
	handler.recordLifecycleState()
	return handler, nil
}

func NewForwardingHandler(ctx context.Context, controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, maxBodyBytes int64, wakeDeadline time.Duration, metrics *observability.Registry) (Handler, error) {
	if forward == nil {
		return Handler{}, errors.New("forwarding activator requires a forward handler")
	}
	if maxBodyBytes <= 0 || wakeDeadline <= 0 || metrics == nil {
		return Handler{}, errors.New("forwarding activator requires positive body, wake, and metrics settings")
	}
	handler, err := NewHandler(ctx, controller, wake, metrics)
	if err != nil {
		return Handler{}, err
	}
	handler.forward, handler.maxBody, handler.wakeWait, handler.metrics = forward, maxBodyBytes, wakeDeadline, metrics
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

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /activate", h.activate)
	mux.HandleFunc("GET /healthz", h.health)
}

func (h Handler) RegisterForwarding(mux *http.ServeMux) {
	h.Register(mux)
	mux.Handle("/", h)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.recordLifecycleState()
	if h.forward == nil {
		http.Error(w, "forwarding unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.spool != nil {
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
	h.queueMu.Lock()
	id, err := h.spool.Enqueue(r.Context(), r, body)
	if err != nil {
		h.queueMu.Unlock()
		h.recordRejected(int64(len(body)))
		w.Header().Set("Retry-After", "1")
		http.Error(w, "request spool unavailable", http.StatusServiceUnavailable)
		return
	}
	h.metrics.AddCounter("sameoldchat_activator_buffered_requests_total", 1)
	h.metrics.AddCounter("sameoldchat_activator_buffered_bytes_total", uint64(len(body)))
	result := make(chan spoolResult, 1)
	h.waiters[id] = result
	h.queueMu.Unlock()
	defer h.removeSpoolWaiter(id, result)
	go func() {
		err := h.processSpool(h.context)
		if err != nil {
			h.completeSpoolRequest(id, spoolResult{err: err})
		}
	}()
	select {
	case value := <-result:
		if value.err != nil && value.response.status == 0 {
			h.recordRejected(int64(len(body)))
			http.Error(w, "service waking", http.StatusServiceUnavailable)
			return
		}
		writeCapturedResponse(w, value.response)
	case <-r.Context().Done():
		return
	}
}

func (h Handler) processSpool(ctx context.Context) error {
	if ctx == nil {
		return errors.New("activator spool processor requires a context")
	}
	if h.processMu == nil {
		return errors.New("activator spool processor is not initialized")
	}
	h.processMu.Lock()
	defer h.processMu.Unlock()
	if err := h.ensureActive(ctx); err != nil {
		return err
	}
	h.queueMu.Lock()
	requests, err := h.spool.Claim(ctx, h.spoolOwner, 64, h.wakeWait)
	h.queueMu.Unlock()
	if err != nil {
		return err
	}
	for _, request := range requests {
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
		h.forward.ServeHTTP(capture, replay)
		if capture.err != nil {
			h.completeSpoolRequest(request.ID, spoolResult{err: capture.err})
			return capture.err
		}
		if capture.status < http.StatusInternalServerError {
			if err := h.spool.Delete(ctx, h.spoolOwner, request.ID); err != nil {
				h.completeSpoolRequest(request.ID, spoolResult{err: err})
				return err
			}
			h.completeSpoolRequest(request.ID, spoolResult{response: capture.response()})
			continue
		}
		h.completeSpoolRequest(request.ID, spoolResult{response: capture.response()})
		return errors.New("spooled request delivery returned a server error")
	}
	return nil
}

func (h Handler) completeSpoolRequest(id uint64, result spoolResult) {
	if h.processMu == nil {
		return
	}
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	h.completeSpoolRequestLocked(id, result)
}

func (h Handler) completeSpoolRequestLocked(id uint64, result spoolResult) {
	waiter, ok := h.waiters[id]
	if !ok {
		return
	}
	delete(h.waiters, id)
	waiter <- result
}

func (h Handler) removeSpoolWaiter(id uint64, waiter chan spoolResult) {
	if h.processMu == nil {
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
		state, _ := h.controller.Snapshot()
		if state == lifecycle.StateActive {
			return nil
		}
		fence, err := h.controller.BeginWake()
		if err == nil {
			result := h.startWake(fence)
			select {
			case err := <-result:
				return err
			case <-waitContext.Done():
				return waitContext.Err()
			}
		}
		if !errors.Is(err, lifecycle.ErrWakeInProgress) {
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
	_, _ = w.Write([]byte(`{"ok":true,"state":"` + string(state) + `","generation":` + itoa(generation) + `}`))
}

func (h Handler) activate(w http.ResponseWriter, r *http.Request) {
	state, _ := h.controller.Snapshot()
	h.recordLifecycleState()
	if state == lifecycle.StateActive {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	fence, err := h.controller.BeginWake()
	if err != nil {
		if errors.Is(err, lifecycle.ErrWakeInProgress) {
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
	w.Header().Set("X-Lifecycle-Generation", itoa(fence))
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) startWake(fence uint64) <-chan error {
	result := make(chan error, 1)
	go func() {
		started := time.Now()
		defer func() { h.metrics.ObserveDuration("sameoldchat_wake_driver_duration", time.Since(started)) }()
		wakeContext, cancel := context.WithTimeout(h.context, h.wakeWait)
		defer cancel()
		if err := h.wake(wakeContext, fence); err != nil {
			result <- errors.Join(err, h.controller.Fail(fence))
			return
		}
		if err := h.controller.Activate(fence); err != nil {
			result <- err
			return
		}
		h.recordLifecycleState()
		result <- nil
	}()
	return result
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

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for value > 0 {
		i--
		b[i] = byte('0' + value%10)
		value /= 10
	}
	return string(b[i:])
}
