package socketmode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

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
	if err := processor.ProcessOnce(ctx, now, func(_ context.Context, value domain.SocketModeResponse) error {
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
	err := processor.ProcessOnce(ctx, now, func(_ context.Context, value domain.SocketModeResponse) error {
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
