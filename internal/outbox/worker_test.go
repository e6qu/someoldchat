package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestWorkerClaimsDeliversAndAcknowledges(t *testing.T) {
	ctx := context.Background()
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "T1"})
	selected.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	selected.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Now().UTC()}
	if err := selected.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: message.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	var delivered []uint64
	worker, err := NewWorker(selected, "worker-1", 10, time.Minute, func(_ context.Context, record events.Record) error {
		delivered = append(delivered, record.Sequence)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(ctx, "T1")
	if err != nil || count != 1 || len(delivered) != 1 {
		t.Fatalf("count=%d delivered=%v err=%v", count, delivered, err)
	}
	count, err = worker.RunOnce(ctx, "T1")
	if err != nil || count != 0 {
		t.Fatalf("second count=%d err=%v", count, err)
	}
}

func TestWorkerLeavesLeaseUnacknowledgedOnDeliveryError(t *testing.T) {
	ctx := context.Background()
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "T1"})
	selected.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	selected.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Now().UTC()}
	if err := selected.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: message.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(selected, "worker-1", 10, time.Millisecond, func(context.Context, events.Record) error { return errors.New("delivery failed") })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(ctx, "T1"); err == nil {
		t.Fatal("delivery failure was hidden")
	}
	time.Sleep(2 * time.Millisecond)
	claimed, err := selected.ClaimEvents(ctx, "T1", "worker-2", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
}

func TestWorkerRenewsLeaseDuringLongDelivery(t *testing.T) {
	ctx := context.Background()
	selected := memory.New()
	selected.SeedWorkspace(domain.Workspace{ID: "T1"})
	selected.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	selected.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: time.Now().UTC()}
	if err := selected.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: message.CreatedAt}, ""); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first, err := NewWorker(selected, "worker-1", 1, 30*time.Millisecond, func(context.Context, events.Record) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := first.RunOnce(ctx, "T1")
		result <- err
	}()
	<-started
	time.Sleep(80 * time.Millisecond)
	second, err := NewWorker(selected, "worker-2", 1, time.Minute, func(context.Context, events.Record) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := second.RunOnce(ctx, "T1"); err != nil || count != 0 {
		t.Fatalf("lease was lost during delivery: count=%d err=%v", count, err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
