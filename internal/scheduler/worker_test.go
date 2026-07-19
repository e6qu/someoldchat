package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/lifecycle"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestWorkerPostsDueMessageExactlyOnceAcrossClaimReplay(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	id, err := domain.NewScheduledMessageID()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Hour)
	if err := store.CreateScheduledMessage(ctx, domain.ScheduledMessage{WorkspaceID: "T1", ID: id, Channel: "C1", Author: "U1", Text: "due", PostAt: created, CreatedAt: created}, events.Event{ID: "scheduled-created", WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(id), CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(store, service.Messages{Store: store}, "worker-1", 10, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(ctx, "T1")
	if err != nil || count != 1 {
		t.Fatalf("first run count=%d err=%v", count, err)
	}
	count, err = worker.RunOnce(ctx, "T1")
	if err != nil || count != 0 {
		t.Fatalf("replay run count=%d err=%v", count, err)
	}
	page, err := store.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "due" {
		t.Fatalf("messages=%+v err=%v", page.Messages, err)
	}
}

func TestWorkerReportsRenewalFailureThatArrivesAfterPosting(t *testing.T) {
	source := &lateRenewalFailureSource{Store: memory.New(), renewStarted: make(chan struct{}), postingReturned: make(chan struct{}), releaseRenewal: make(chan struct{})}
	source.SeedWorkspace(domain.Workspace{ID: "T1"})
	source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	source.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	source.SeedConversationMember("C1", "U1")
	worker, err := NewWorker(source, service.Messages{Store: source}, "worker-1", 1, 3*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	item := domain.ScheduledMessage{WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", Text: "scheduled", PostAt: time.Now().UTC()}
	result := make(chan error, 1)
	go func() { result <- worker.postWithLease(context.Background(), item) }()
	<-source.postingReturned
	close(source.releaseRenewal)
	if err := <-result; !errors.Is(err, errScheduledLeaseLost) {
		t.Fatalf("worker error=%v, want %v", err, errScheduledLeaseLost)
	}
}

var errScheduledLeaseLost = errors.New("scheduled lease lost")

type lateRenewalFailureSource struct {
	*memory.Store
	renewStarted    chan struct{}
	postingReturned chan struct{}
	releaseRenewal  chan struct{}
}

func (s *lateRenewalFailureSource) CreateMessage(ctx context.Context, message domain.Message, event events.Event, idempotencyKey string) error {
	err := s.Store.CreateMessage(ctx, message, event, idempotencyKey)
	<-s.renewStarted
	close(s.postingReturned)
	return err
}

func (s *lateRenewalFailureSource) RenewScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Duration) error {
	select {
	case <-s.renewStarted:
	default:
		close(s.renewStarted)
	}
	<-s.releaseRenewal
	return errScheduledLeaseLost
}

func TestPublishWakeDeadlineUsesEarliestPendingMessage(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	store.SeedConversationMember("C1", "U1")
	first := time.Now().UTC().Add(-2 * time.Second).Truncate(time.Second)
	second := first.Add(time.Second)
	for _, value := range []domain.ScheduledMessage{
		{WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", Text: "first", PostAt: first, CreatedAt: first},
		{WorkspaceID: "T1", ID: "Q2", Channel: "C1", Author: "U1", Text: "second", PostAt: second, CreatedAt: second},
	} {
		if err := store.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("event-" + string(value.ID)), WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(value.ID), CreatedAt: value.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	controller := lifecycle.New(lifecycle.StateActive)
	if err := PublishWakeDeadline(ctx, store, controller, "T1", 0); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.Equal(first) {
		t.Fatalf("wake deadline=%s, want %s", got, first)
	}
	worker, err := NewWorker(store, service.Messages{Store: store}, "worker-1", 10, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 2 {
		t.Fatalf("scheduled execution count=%d err=%v, want two", count, err)
	}
	if err := PublishWakeDeadline(ctx, store, controller, "T1", 0); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.IsZero() {
		t.Fatalf("wake deadline=%s after delivery, want zero", got)
	}
}
