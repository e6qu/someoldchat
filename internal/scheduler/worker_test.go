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
	if err := PublishWakeDeadline(ctx, store, controller, 0, "T1"); err != nil {
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
	if err := PublishWakeDeadline(ctx, store, controller, 0, "T1"); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.IsZero() {
		t.Fatalf("wake deadline=%s after delivery, want zero", got)
	}
}

// One item that cannot be posted used to abandon the rest of its own claimed
// batch: RunOnce returned on the first error, leaving every later item leased to
// this owner until the lease expired. The whole workspace's schedule stalled for
// a lease period on every cycle, and the next cycle re-claimed the same batch and
// stalled again on the same item.
func TestRunOnceCompletesTheBatchAroundAnItemThatCannotBePosted(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	// A private conversation the author is not a member of. That is an ordinary
	// durable state: the author was removed after the message was scheduled.
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "private", IsPrivate: true})
	due := time.Now().UTC().Add(-time.Hour)
	for _, value := range []domain.ScheduledMessage{
		{WorkspaceID: "T1", ID: "Q1", Channel: "C2", Author: "U1", Text: "undeliverable", PostAt: due, CreatedAt: due},
		{WorkspaceID: "T1", ID: "Q2", Channel: "C1", Author: "U1", Text: "deliverable", PostAt: due, CreatedAt: due},
	} {
		if err := store.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("event-" + string(value.ID)), WorkspaceID: "T1", Topic: "message.scheduled", Payload: string(value.ID), CreatedAt: value.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	worker, err := NewWorker(store, service.Messages{Store: store}, "worker-1", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	count, err := worker.RunOnce(ctx, "T1")
	if err == nil {
		t.Fatal("the undeliverable item was reported as delivered")
	}
	if count != 1 {
		t.Fatalf("completed=%d err=%v, want the deliverable item posted despite its neighbour", count, err)
	}
	page, err := store.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Text != "deliverable" {
		t.Fatalf("messages=%+v, want the rest of the batch delivered", page.Messages)
	}
}

// The wake hint is a property of the stack, not of a tenant. Publishing one
// workspace's earliest job overwrote another's, so a multi-tenant deployment
// hibernated straight through every workspace's jobs but the last published one.
func TestPublishWakeDeadlineUsesTheEarliestAcrossEveryWorkspace(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	early := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	late := early.Add(2 * time.Hour)
	for workspace, postAt := range map[domain.WorkspaceID]time.Time{"T1": late, "T2": early} {
		store.SeedWorkspace(domain.Workspace{ID: workspace})
		user := domain.UserID("U-" + workspace)
		conversation := domain.ConversationID("C-" + workspace)
		store.SeedUser(domain.User{ID: user, WorkspaceID: workspace})
		store.SeedConversation(domain.Conversation{ID: conversation, WorkspaceID: workspace})
		store.SeedConversationMember(conversation, user)
		value := domain.ScheduledMessage{WorkspaceID: workspace, ID: domain.ScheduledMessageID("Q-" + workspace), Channel: conversation, Author: user, Text: "later", PostAt: postAt, CreatedAt: postAt}
		if err := store.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("event-" + string(value.ID)), WorkspaceID: workspace, Topic: "message.scheduled", Payload: string(value.ID), CreatedAt: postAt}); err != nil {
			t.Fatal(err)
		}
	}
	controller := lifecycle.New(lifecycle.StateActive)
	if err := PublishWakeDeadline(ctx, store, controller, 0, "T1", "T2"); err != nil {
		t.Fatal(err)
	}
	if got := controller.Metadata().WakeDeadline; !got.Equal(early) {
		t.Fatalf("wake deadline=%s, want the earliest across every workspace, %s", got, early)
	}
}

// LC-49: the renewal-error suppression that was fixed in internal/activator and
// internal/blob but missed here. A lost lease was reported only when the post had
// succeeded, so the one combination that matters most — the post failed *and*
// another owner held the lease — was reported as an ordinary post failure. The
// caller then released the item for a third replica believing nothing else was
// running it.
func TestWorkerReportsALostLeaseEvenWhenThePostAlsoFailed(t *testing.T) {
	source := &failingPostLateRenewalSource{Store: memory.New(), renewStarted: make(chan struct{}), postingReturned: make(chan struct{}), releaseRenewal: make(chan struct{})}
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
	err = <-result
	if !errors.Is(err, errScheduledLeaseLost) {
		t.Fatalf("worker error=%v, want the lost lease reported alongside the failed post", err)
	}
}

var errScheduledPostFailed = errors.New("scheduled post failed")

type failingPostLateRenewalSource struct {
	*memory.Store
	renewStarted    chan struct{}
	postingReturned chan struct{}
	releaseRenewal  chan struct{}
}

func (s *failingPostLateRenewalSource) CreateMessage(context.Context, domain.Message, events.Event, string) error {
	<-s.renewStarted
	close(s.postingReturned)
	return errScheduledPostFailed
}

func (s *failingPostLateRenewalSource) RenewScheduledMessage(context.Context, string, domain.ScheduledMessageID, time.Duration) error {
	select {
	case <-s.renewStarted:
	default:
		close(s.renewStarted)
	}
	<-s.releaseRenewal
	return errScheduledLeaseLost
}
