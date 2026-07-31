package scheduler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestWorkerDeliversScheduledFileOnlyMessageAfterUploadTicketExpiry(t *testing.T) {
	ctx := context.Background()
	target := memory.New()
	target.SeedWorkspace(domain.Workspace{ID: "T1"})
	target.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	target.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	target.SeedConversationMember("C1", "U1")
	now := time.Now().UTC()
	upload := domain.ExternalUpload{
		ID: "scheduled-upload", WorkspaceID: "T1", Uploader: "U1", Name: "evidence.txt", Title: "evidence.txt",
		MIMEType: "text/plain", BlobKey: "T1/external/scheduled-upload", Size: 8,
		Status: domain.ExternalUploadUploaded, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute), UploadedAt: now.Add(-time.Hour),
	}
	if err := target.CreateExternalUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}
	scheduled := domain.ScheduledMessage{
		WorkspaceID: "T1", ID: "Q-file", Channel: "C1", Author: "U1", PostAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour),
		FileAttachments: []domain.DraftAttachment{{
			UploadID: upload.ID, Name: upload.Name, Title: "Evidence", MIMEType: upload.MIMEType, Size: upload.Size,
		}},
	}
	if err := target.CreateScheduledMessage(ctx, scheduled, events.Event{ID: "scheduled-file", WorkspaceID: "T1", Topic: "message.scheduled", CreatedAt: scheduled.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(target, service.Messages{Store: target}, "file-worker", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 1 {
		t.Fatalf("run count=%d err=%v", count, err)
	}
	history, err := target.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Text != "" ||
		len(history.Messages[0].Files) != 1 || history.Messages[0].Files[0].ID != domain.FileID(upload.ID) {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 0 {
		t.Fatalf("replay count=%d err=%v", count, err)
	}
}

func TestWorkerExecutesEveryWorkspaceAndPreservesThreadAndAppAttribution(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		user := domain.UserID("U-" + string(workspace))
		channel := domain.ConversationID("C-" + string(workspace))
		source.SeedWorkspace(domain.Workspace{ID: workspace})
		source.SeedUser(domain.User{ID: user, WorkspaceID: workspace, Name: string(user)})
		source.SeedConversation(domain.Conversation{ID: channel, WorkspaceID: workspace, Name: "general"})
		source.SeedConversationMember(channel, user)
		parent, err := (service.Messages{Store: source}).Post(ctx, workspace, user, channel, "parent", "", "")
		if err != nil {
			t.Fatal(err)
		}
		due := time.Now().UTC().Add(-time.Minute)
		value := domain.ScheduledMessage{
			WorkspaceID: workspace, ID: domain.ScheduledMessageID("Q-" + string(workspace)),
			Channel: channel, Author: user, AppID: domain.AppID("A-" + string(workspace)),
			Text: "reply", ThreadTimestamp: domain.NewMessageTimestamp(parent.CreatedAt), PostAt: due, CreatedAt: due,
		}
		if err := source.CreateScheduledMessage(ctx, value, events.Event{ID: domain.EventID("event-" + string(workspace)), WorkspaceID: workspace, Topic: "message.scheduled", Payload: string(value.ID), CreatedAt: due}); err != nil {
			t.Fatal(err)
		}
	}
	worker, err := NewWorker(source, service.Messages{Store: source}, "multi-workspace-worker", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, ""); err != nil || count != 2 {
		t.Fatalf("multi-workspace count=%d err=%v", count, err)
	}
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		channel := domain.ConversationID("C-" + string(workspace))
		page, err := source.ListMessages(ctx, channel, domain.PageRequest{Limit: 10})
		if err != nil || len(page.Messages) != 2 {
			t.Fatalf("%s messages=%+v err=%v", workspace, page.Messages, err)
		}
		reply := page.Messages[1]
		if reply.Text != "reply" || reply.AppID != domain.AppID("A-"+string(workspace)) || reply.ThreadTimestamp == "" {
			t.Fatalf("%s scheduled reply lost attribution/thread: %+v", workspace, reply)
		}
	}
}

func TestWorkerReportsRenewalFailureThatArrivesAfterPosting(t *testing.T) {
	source := &lateRenewalFailureSource{Store: memory.New(), renewStarted: make(chan struct{}), postingReturned: make(chan struct{}), releaseRenewal: make(chan struct{})}
	source.SeedWorkspace(domain.Workspace{ID: "T1"})
	source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	source.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	source.SeedConversationMember("C1", "U1")
	item := domain.ScheduledMessage{WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", Text: "scheduled", PostAt: time.Now().UTC()}
	if err := source.CreateScheduledMessage(context.Background(), item, events.Event{
		ID: "event-Q1", WorkspaceID: "T1", Topic: "message.scheduled", Payload: "Q1", CreatedAt: item.PostAt,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(source, service.Messages{Store: source}, "worker-1", 1, 3*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
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

func (s *lateRenewalFailureSource) CreateScheduledMessagePost(ctx context.Context, id domain.ScheduledMessageID, message domain.Message, event events.Event) error {
	err := s.Store.CreateScheduledMessagePost(ctx, id, message, event)
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
	if err != nil {
		t.Fatalf("permanent delivery failure was not terminally handled: %v", err)
	}
	if count != 2 {
		t.Fatalf("processed=%d err=%v, want the posted and terminally failed items handled", count, err)
	}
	page, err := store.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Text != "deliverable" {
		t.Fatalf("messages=%+v, want the rest of the batch delivered", page.Messages)
	}
	claimed, err := store.ClaimScheduledMessages(ctx, "T1", "worker-2", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("terminally handled schedule was reclaimed: %+v", claimed)
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
	item := domain.ScheduledMessage{WorkspaceID: "T1", ID: "Q1", Channel: "C1", Author: "U1", Text: "scheduled", PostAt: time.Now().UTC()}
	if err := source.CreateScheduledMessage(context.Background(), item, events.Event{
		ID: "event-Q1", WorkspaceID: "T1", Topic: "message.scheduled", Payload: "Q1", CreatedAt: item.PostAt,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(source, service.Messages{Store: source}, "worker-1", 1, 3*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
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

func (s *failingPostLateRenewalSource) CreateScheduledMessagePost(context.Context, domain.ScheduledMessageID, domain.Message, events.Event) error {
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

// A missing fencing generation is an error, never zero.
//
// Fence read the unauthenticated GET /healthz, and cmd/ecs-ws-activator's probe
// answers {"ok":true}: encoding/json decodes that into a uint64 field as 0 with
// no error at all. deploy/ecs-scale-zero calls both processes "the activator", so
// an operator pointing -activator-url at the WebSocket edge published every wake
// deadline against fence 0, which the lifecycle activator refuses with 409
// forever. The scheduled-wake feature was inert with a log line as its only
// symptom, so the absence has to be reported rather than defaulted.
func TestActivatorFenceRejectsAResponseWithoutAGeneration(t *testing.T) {
	for _, probe := range []struct {
		name string
		body string
	}{
		{name: "websocket edge", body: `{"ok":true}`},
		{name: "empty object", body: `{}`},
		{name: "not json", body: `no fence here`},
	} {
		t.Run(probe.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, probe.body)
			}))
			defer server.Close()
			publisher, err := NewActivatorDeadlinePublisher(server.URL, "secret", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			fence, err := publisher.Fence(context.Background())
			if err == nil {
				t.Fatalf("fence=%d err=nil, want the absent generation reported", fence)
			}
			if fence != 0 {
				t.Fatalf("fence=%d on error, want the zero value", fence)
			}
		})
	}
}

// The generation is read from the token-protected lifecycle route, not from the
// public probe, so the value the publisher fences against is the one only an
// authenticated caller can see.
func TestActivatorFenceReadsTheAuthenticatedLifecycleRoute(t *testing.T) {
	var requested, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested, authorization = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"state":"active","generation":12}`)
	}))
	defer server.Close()
	publisher, err := NewActivatorDeadlinePublisher(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	fence, err := publisher.Fence(context.Background())
	if err != nil || fence != 12 {
		t.Fatalf("fence=%d err=%v, want 12", fence, err)
	}
	if requested != "/lifecycle" || authorization != "Bearer secret" {
		t.Fatalf("requested %q with %q, want the token-protected lifecycle route", requested, authorization)
	}
}
