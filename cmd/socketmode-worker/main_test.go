package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
)

// A single ClaimSocketModeResponses timeout during a database failover, a single
// acknowledgement conflict, or a failed release — which is the recovery path and
// is reported as a joined error rather than a ResponseDeliveryError — used to
// exit this process, while the outbox worker tolerated twenty consecutive
// failures for exactly the same conditions. One policy now covers both.
func TestTransientStoreFailuresAreToleratedUpToTheBudget(t *testing.T) {
	queue := &flakyResponseQueue{failures: 3, value: domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{}`, ReceivedAt: time.Unix(1700000000, 0).UTC()}}
	processor := socketmode.ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 10, Lease: time.Minute, RetryDelay: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := 0
	cycle := func(cycleContext context.Context) (bool, error) {
		before := delivered
		err := processor.ProcessOnce(cycleContext, func(context.Context, domain.SocketModeResponse) error {
			delivered++
			return nil
		})
		if delivered > 0 {
			cancel()
		}
		return delivered > before, err
	}
	if code := pollWithinFailureBudget(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cycle, time.Microsecond, 20); code != 0 {
		t.Fatalf("exit code=%d after %d transient store failures, want the worker to keep running", code, queue.failures)
	}
	if delivered != 1 {
		t.Fatalf("delivered=%d, want the response delivered once the store recovered", delivered)
	}
}

// A store that never recovers must not leave the process reporting itself
// healthy while nothing drains.
func TestPermanentStoreFailureExhaustsTheBudget(t *testing.T) {
	queue := &flakyResponseQueue{failures: 1 << 30}
	processor := socketmode.ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 10, Lease: time.Minute, RetryDelay: time.Second}
	cycle := func(cycleContext context.Context) (bool, error) {
		return false, processor.ProcessOnce(cycleContext, func(context.Context, domain.SocketModeResponse) error { return nil })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if code := pollWithinFailureBudget(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cycle, time.Microsecond, 5); code != exitRuntime {
		t.Fatalf("exit code=%d against a store that never answers, want %d", code, exitRuntime)
	}
}

// A response destination that has been retired fails, is released with a retry
// deadline in the future, and then produces empty cycles until that deadline. If
// those empty cycles reset the counter, the budget never fires.
func TestFailureBudgetIsResetByProgressRatherThanByAQuietCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cycles := 0
	cycle := func(context.Context) (bool, error) {
		cycles++
		if cycles%10 == 0 {
			return false, errors.New("response destination refused the connection")
		}
		return false, nil
	}
	if code := pollWithinFailureBudget(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), cycle, time.Microsecond, 3); code != exitRuntime {
		t.Fatalf("exit code=%d after %d cycles with a failure every tenth, want %d", code, cycles, exitRuntime)
	}
}

// flakyResponseQueue fails its claim the first failures times, then hands out one
// response.
type flakyResponseQueue struct {
	failures int
	calls    int
	value    domain.SocketModeResponse
	claimed  bool
}

func (q *flakyResponseQueue) ClaimSocketModeResponses(context.Context, domain.AppID, string, int, time.Duration) ([]domain.SocketModeResponse, error) {
	q.calls++
	if q.calls <= q.failures {
		return nil, errors.New("claim Socket Mode responses: context deadline exceeded")
	}
	if q.claimed {
		return nil, nil
	}
	q.claimed = true
	return []domain.SocketModeResponse{q.value}, nil
}

func (q *flakyResponseQueue) RenewSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Duration) error {
	return nil
}

func (q *flakyResponseQueue) AckSocketModeResponses(context.Context, string, []domain.SocketModeResponse) error {
	return nil
}

func (q *flakyResponseQueue) ReleaseSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Time) error {
	return nil
}

type responseDoer func(*http.Request) (*http.Response, error)

func (f responseDoer) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestHTTPResponseDeliveryRequiresAbsoluteURLAndClient(t *testing.T) {
	if _, err := newHTTPResponseDelivery("/responses", responseDoer(func(*http.Request) (*http.Response, error) { return nil, nil })); err == nil {
		t.Fatal("relative response URL succeeded")
	}
	if _, err := newHTTPResponseDelivery("https://example.test/responses", nil); err == nil {
		t.Fatal("nil HTTP client succeeded")
	}
}

func TestHTTPResponseDeliverySendsDurableResponseMetadata(t *testing.T) {
	var received *http.Request
	delivery, err := newHTTPResponseDelivery("https://example.test/responses", responseDoer(func(request *http.Request) (*http.Response, error) {
		received = request
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{"ok":true}`}
	if err := delivery(context.Background(), response); err != nil {
		t.Fatal(err)
	}
	if received == nil || received.Method != http.MethodPost || received.Header.Get("Content-Type") != "application/json" || received.Header.Get("Idempotency-Key") != "A1:env-1" || received.Header.Get("X-SameOldChat-App-ID") != "A1" || received.Header.Get("X-SameOldChat-Envelope-ID") != "env-1" {
		t.Fatalf("request=%+v", received)
	}
	body, err := io.ReadAll(received.Body)
	if err != nil || string(body) != response.Payload {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestHTTPResponseDeliveryFailsForNonSuccessStatus(t *testing.T) {
	delivery, err := newHTTPResponseDelivery("https://example.test/responses", responseDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery(context.Background(), domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{}`}); err == nil {
		t.Fatal("non-success response was accepted")
	}
}
