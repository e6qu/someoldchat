package socketmode

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

type responseRenewalTrackingQueue struct {
	*memory.Store
	renewed chan struct{}
	once    sync.Once
}

func (q *responseRenewalTrackingQueue) RenewSocketModeResponses(ctx context.Context, owner string, values []domain.SocketModeResponse, lease time.Duration) error {
	q.once.Do(func() { close(q.renewed) })
	return q.Store.RenewSocketModeResponses(ctx, owner, values, lease)
}

func TestResponseProcessorAcknowledgesSuccessfulResponses(t *testing.T) {
	ctx := context.Background()
	queue := memory.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	response := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{}`, ReceivedAt: now}
	if err := queue.RecordSocketModeResponse(ctx, response); err != nil {
		t.Fatal(err)
	}
	processor := ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 10, Lease: time.Minute, RetryDelay: time.Second}
	called := false
	if err := processor.ProcessOnce(ctx, func(_ context.Context, value domain.SocketModeResponse) error {
		called = value.EnvelopeID == "env-1"
		return nil
	}); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	claimed, err := queue.ClaimSocketModeResponses(ctx, "A1", "worker-2", 10, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("claimed after acknowledgement=%+v err=%v", claimed, err)
	}
}

func TestResponseProcessorReleasesUnprocessedResponses(t *testing.T) {
	ctx := context.Background()
	queue := memory.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"env-1", "env-2"} {
		if err := queue.RecordSocketModeResponse(ctx, domain.SocketModeResponse{AppID: "A1", EnvelopeID: id, Payload: `{}`, ReceivedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	processor := ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 10, Lease: time.Minute, RetryDelay: time.Minute}
	wantErr := errors.New("handler failed")
	err := processor.ProcessOnce(ctx, func(_ context.Context, value domain.SocketModeResponse) error {
		if value.EnvelopeID == "env-2" {
			return wantErr
		}
		return nil
	})
	var deliveryErr ResponseDeliveryError
	if !errors.As(err, &deliveryErr) || !errors.Is(err, wantErr) {
		t.Fatalf("err=%v", err)
	}
	claimed, err := queue.ClaimSocketModeResponses(ctx, "A1", "worker-2", 10, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("claimed before retry deadline=%+v err=%v", claimed, err)
	}
}

// The retry delay has to be measured from the failure. Measuring it from the
// clock read that started the batch releases a slow batch's later failures into
// the past, so the failing response is retried in a tight loop with no backoff.
func TestResponseProcessorMeasuresRetryDelayFromTheFailure(t *testing.T) {
	ctx := context.Background()
	queue := memory.New()
	batchStart := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := queue.RecordSocketModeResponse(ctx, domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{}`, ReceivedAt: batchStart}); err != nil {
		t.Fatal(err)
	}
	processor := ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 10, Lease: time.Minute, RetryDelay: 30 * time.Second}
	err := processor.ProcessOnce(ctx, func(context.Context, domain.SocketModeResponse) error {
		return errors.New("handler failed")
	})
	var deliveryErr ResponseDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("err=%v", err)
	}
	claimed, claimErr := queue.ClaimSocketModeResponses(ctx, "A1", "worker-2", 10, time.Minute)
	if claimErr != nil {
		t.Fatal(claimErr)
	}
	if len(claimed) != 0 {
		t.Fatalf("the failed response was immediately claimable again: %+v", claimed)
	}
}

// The retry deadline is now assertable rather than inferable, because the clock
// is a field instead of a parameter the processor never read. The old signature
// took a now the caller had to supply and the processor only validated, which
// invited a reader to believe backoff was injectable when it was not.
func TestResponseProcessorRetriesAFullDelayAfterTheFailure(t *testing.T) {
	failure := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	queue := &releaseRecordingQueue{value: domain.SocketModeResponse{AppID: "A1", EnvelopeID: "env-1", Payload: `{}`, ReceivedAt: failure.Add(-time.Hour)}}
	processor := ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 10, Lease: time.Minute, RetryDelay: 30 * time.Second, Now: func() time.Time { return failure }}
	err := processor.ProcessOnce(context.Background(), func(context.Context, domain.SocketModeResponse) error {
		return errors.New("handler failed")
	})
	var deliveryErr ResponseDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("err=%v", err)
	}
	if want := failure.Add(30 * time.Second); !queue.retryAt.Equal(want) {
		t.Fatalf("retry deadline=%s, want %s measured from the failure", queue.retryAt, want)
	}
}

// releaseRecordingQueue hands out one response and records the retry deadline
// the processor releases it with.
type releaseRecordingQueue struct {
	value    domain.SocketModeResponse
	claimed  bool
	retryAt  time.Time
	released bool
}

func (q *releaseRecordingQueue) ClaimSocketModeResponses(context.Context, domain.AppID, string, int, time.Duration) ([]domain.SocketModeResponse, error) {
	if q.claimed {
		return nil, nil
	}
	q.claimed = true
	return []domain.SocketModeResponse{q.value}, nil
}

func (q *releaseRecordingQueue) RenewSocketModeResponses(context.Context, string, []domain.SocketModeResponse, time.Duration) error {
	return nil
}

func (q *releaseRecordingQueue) AckSocketModeResponses(context.Context, string, []domain.SocketModeResponse) error {
	return nil
}

func (q *releaseRecordingQueue) ReleaseSocketModeResponses(_ context.Context, _ string, _ []domain.SocketModeResponse, retryAt time.Time) error {
	q.retryAt = retryAt
	q.released = true
	return nil
}

func TestResponseProcessorRenewsLeaseDuringSlowHandler(t *testing.T) {
	ctx := context.Background()
	queue := &responseRenewalTrackingQueue{Store: memory.New(), renewed: make(chan struct{})}
	now := time.Now().UTC().Truncate(time.Microsecond)
	response := domain.SocketModeResponse{AppID: "A1", EnvelopeID: "slow", Payload: `{}`, ReceivedAt: now}
	if err := queue.RecordSocketModeResponse(ctx, response); err != nil {
		t.Fatal(err)
	}
	processor := ResponseProcessor{Queue: queue, AppID: "A1", Owner: "worker-1", BatchSize: 1, Lease: 60 * time.Millisecond, RetryDelay: time.Second}
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- processor.ProcessOnce(ctx, func(_ context.Context, _ domain.SocketModeResponse) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	select {
	case <-queue.renewed:
	case <-time.After(time.Second):
		t.Fatal("response processor did not renew the slow handler lease")
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.ClaimSocketModeResponses(ctx, "A1", "worker-2", 1, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("slow response was not acknowledged: claimed=%+v err=%v", claimed, err)
	}
}
