package activator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
)

type WakeFunc func(context.Context, uint64) error

type Handler struct {
	controller *lifecycle.Controller
	wake       WakeFunc
	forward    http.Handler
	spool      Spool
	spoolOwner string
	maxBody    int64
	wakeWait   time.Duration
}

func NewHandler(controller *lifecycle.Controller, wake WakeFunc) (Handler, error) {
	if controller == nil {
		return Handler{}, errors.New("activator requires a lifecycle controller")
	}
	if wake == nil {
		return Handler{}, errors.New("activator requires a wake driver")
	}
	return Handler{controller: controller, wake: wake}, nil
}

func NewForwardingHandler(controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, maxBodyBytes int64, wakeDeadline time.Duration) (Handler, error) {
	if forward == nil {
		return Handler{}, errors.New("forwarding activator requires a forward handler")
	}
	if maxBodyBytes <= 0 || wakeDeadline <= 0 {
		return Handler{}, errors.New("forwarding activator requires positive body and wake limits")
	}
	handler, err := NewHandler(controller, wake)
	if err != nil {
		return Handler{}, err
	}
	handler.forward, handler.maxBody, handler.wakeWait = forward, maxBodyBytes, wakeDeadline
	return handler, nil
}

func NewDurableForwardingHandler(controller *lifecycle.Controller, wake WakeFunc, forward http.Handler, spool Spool, spoolOwner string, maxBodyBytes int64, wakeDeadline time.Duration) (Handler, error) {
	if spool == nil {
		return Handler{}, errors.New("durable forwarding activator requires a request spool")
	}
	if strings.TrimSpace(spoolOwner) == "" {
		return Handler{}, errors.New("durable forwarding activator requires a spool owner")
	}
	handler, err := NewForwardingHandler(controller, wake, forward, maxBodyBytes, wakeDeadline)
	if err != nil {
		return Handler{}, err
	}
	handler.spool, handler.spoolOwner = spool, spoolOwner
	return handler, nil
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
		http.Error(w, "request body unavailable", http.StatusRequestEntityTooLarge)
		return
	}
	if int64(len(body)) > h.maxBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	id, err := h.spool.Enqueue(context.Background(), r, body)
	if err != nil {
		http.Error(w, "request spool unavailable", http.StatusServiceUnavailable)
		return
	}
	result := make(chan spoolResult, 1)
	go func() {
		response, err := h.processSpool(context.Background(), id)
		result <- spoolResult{response: response, err: err}
	}()
	select {
	case value := <-result:
		if value.err != nil && value.response.status == 0 {
			http.Error(w, "service waking", http.StatusServiceUnavailable)
			return
		}
		writeCapturedResponse(w, value.response)
	case <-r.Context().Done():
		return
	}
}

func (h Handler) processSpool(ctx context.Context, currentID uint64) (capturedResponse, error) {
	if err := h.ensureActive(ctx); err != nil {
		return capturedResponse{}, err
	}
	requests, err := h.spool.Claim(ctx, h.spoolOwner, 64, h.wakeWait)
	if err != nil {
		return capturedResponse{}, err
	}
	for _, request := range requests {
		replay, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
		if err != nil {
			return capturedResponse{}, err
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
			return capturedResponse{}, capture.err
		}
		if capture.status < http.StatusInternalServerError {
			if err := h.spool.Delete(ctx, h.spoolOwner, request.ID); err != nil {
				return capturedResponse{}, err
			}
		} else if request.ID == currentID {
			return capture.response(), nil
		} else {
			return capturedResponse{}, errors.New("spooled request delivery returned a server error")
		}
		if request.ID == currentID {
			return capture.response(), nil
		}
	}
	return capturedResponse{}, errors.New("spooled request was not delivered")
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
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"state":"` + string(state) + `","generation":` + itoa(generation) + `}`))
}

func (h Handler) activate(w http.ResponseWriter, r *http.Request) {
	state, _ := h.controller.Snapshot()
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
		wakeContext, cancel := context.WithTimeout(context.Background(), h.wakeWait)
		defer cancel()
		if err := h.wake(wakeContext, fence); err != nil {
			result <- errors.Join(err, h.controller.Fail(fence))
			return
		}
		if err := h.controller.Activate(fence); err != nil {
			result <- err
			return
		}
		result <- nil
	}()
	return result
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
