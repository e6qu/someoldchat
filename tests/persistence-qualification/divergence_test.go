package qualification

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The contracts below are the ones the storage profiles were verified to
// disagree on. They live in the shared suite so a profile cannot answer
// differently again: message ordering, unread counting and lease fencing were
// wrong on every SQL profile and right in memory, while validation, referential
// checks and sentinel errors were right on the SQL profiles and absent in memory.

// trailingZeroFractions are instants whose time.RFC3339Nano rendering has a
// variable-width fraction, so one encoding is a byte prefix of another and text
// comparison disagrees with chronological comparison.
func trailingZeroFractions(base time.Time) []time.Time {
	return []time.Time{
		base.Add(50 * time.Millisecond),
		base.Add(100 * time.Millisecond),
		base.Add(120 * time.Millisecond),
		base.Add(123456 * time.Microsecond),
		base.Add(200 * time.Millisecond),
		base.Add(1500 * time.Millisecond),
		base.Add(1500001 * time.Microsecond),
	}
}

type fixture struct {
	repository  qualificationStore
	workspaceID domain.WorkspaceID
	userID      domain.UserID
	channelID   domain.ConversationID
	suffix      string
}

func newFixture(t *testing.T, ctx context.Context, open opener) (fixture, func()) {
	t.Helper()
	repository, closeRepository := open(t, ctx)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	value := fixture{
		repository:  repository,
		workspaceID: domain.WorkspaceID("T-divergence-" + suffix),
		userID:      domain.UserID("U-divergence-" + suffix),
		channelID:   domain.ConversationID("C-divergence-" + suffix),
		suffix:      suffix,
	}
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: value.workspaceID, Name: "Divergence"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: value.userID, WorkspaceID: value.workspaceID, Email: "divergence-" + suffix + "@example.com", Name: "divergence"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversation(ctx, domain.Conversation{ID: value.channelID, WorkspaceID: value.workspaceID, Name: "divergence"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember(ctx, value.channelID, value.userID); err != nil {
		t.Fatal(err)
	}
	return value, closeRepository
}

func (f fixture) event(name, topic, payload string) events.Event {
	return events.Event{ID: domain.EventID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Topic: topic, Payload: payload, CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func (f fixture) message(t *testing.T, ctx context.Context, name string, createdAt time.Time) domain.Message {
	t.Helper()
	message := domain.Message{ID: domain.MessageID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID, AuthorID: f.userID, Text: "text " + name, CreatedAt: createdAt}
	if err := f.repository.CreateMessage(ctx, message, f.event("event-"+name, "message.created", string(message.ID)), ""); err != nil {
		t.Fatalf("create message %s: %v", name, err)
	}
	return message
}

// reply posts into an existing thread. The thread rule is the one part of
// retention neither profile can infer from the other, so a contract that
// exercises it needs replies as a first-class fixture step.
func (f fixture) reply(t *testing.T, ctx context.Context, name string, root domain.MessageTimestamp, createdAt time.Time) domain.Message {
	t.Helper()
	message := domain.Message{
		ID: domain.MessageID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		AuthorID: f.userID, Text: "reply " + name, ThreadTimestamp: root, Attachments: "[]",
		CreatedAt: domain.MessageInstant(createdAt),
	}
	if err := f.repository.CreateMessage(ctx, message, f.event("event-"+name, "message.created", string(message.ID)), ""); err != nil {
		t.Fatalf("create reply %s: %v", name, err)
	}
	return message
}

func messageOrderIsChronological(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	instants := trailingZeroFractions(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	for index, instant := range instants {
		f.message(t, ctx, fmt.Sprintf("M%d", index), instant)
	}
	page, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: len(instants) + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != len(instants) {
		t.Fatalf("listed %d messages, want %d", len(page.Messages), len(instants))
	}
	for index := 1; index < len(page.Messages); index++ {
		if page.Messages[index].CreatedAt.Before(page.Messages[index-1].CreatedAt) {
			t.Fatalf("message %d (%s) sorted before %d (%s)", index, page.Messages[index].CreatedAt, index-1, page.Messages[index-1].CreatedAt)
		}
	}
	// Keyset pagination compares the same key, so a broken ordering shows up as a
	// skipped or repeated identifier.
	seen := make(map[domain.MessageID]struct{}, len(instants))
	request := domain.PageRequest{Limit: 1}
	for visited := 0; visited <= len(instants); visited++ {
		single, err := f.repository.ListMessages(ctx, f.channelID, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range single.Messages {
			if _, repeated := seen[message.ID]; repeated {
				t.Fatalf("keyset pagination repeated %q", message.ID)
			}
			seen[message.ID] = struct{}{}
		}
		if !single.HasMore {
			break
		}
		request.Cursor = single.NextCursor
	}
	if len(seen) != len(instants) {
		t.Fatalf("keyset pagination visited %d messages, want %d", len(seen), len(instants))
	}
}

func unreadCountFollowsTheReadCursor(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	read := base.Add(500 * time.Millisecond)
	f.message(t, ctx, "M-read", read)
	f.message(t, ctx, "M-unread", base.Add(550*time.Millisecond))
	cursor := domain.ReadCursor{WorkspaceID: f.workspaceID, UserID: f.userID, Conversation: f.channelID, LastRead: domain.NewMessageTimestamp(read), UpdatedAt: read}
	if err := f.repository.SetReadCursor(ctx, cursor, f.event("cursor", "conversation.marked", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	page, err := f.repository.ListConversations(ctx, f.workspaceID, f.userID, domain.ConversationListRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, conversation := range page.Conversations {
		if conversation.ID != f.channelID {
			continue
		}
		if conversation.UnreadCount != 1 {
			t.Fatalf("unread count = %d, want 1 for the message posted after the read cursor", conversation.UnreadCount)
		}
		return
	}
	t.Fatalf("conversation %q missing from %+v", f.channelID, page.Conversations)
}

// Marking everything read is one action to the member and one transaction to
// the store, and it is the only cursor write whose input is derived from the
// store's own answer about what the newest message is. The two profiles have to
// agree on both halves or the sidebar clears differently depending on which
// storage a deployment chose.
func batchReadCursorsAgreeWithTheNewestMessage(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	f.message(t, ctx, "M-batch-first", base)
	newest := base.Add(2 * time.Second)
	f.message(t, ctx, "M-batch-newest", newest)
	// A deleted message must not become the read position: deleting the last
	// message would otherwise park the cursor on something nobody can see.
	deleted := f.message(t, ctx, "M-batch-deleted", base.Add(4*time.Second))
	deleted.Deleted = true
	if err := f.repository.DeleteMessage(ctx, deleted, f.event("batch-delete", "message.deleted", string(deleted.ID)), nil); err != nil {
		t.Fatal(err)
	}

	latest, err := f.repository.LatestMessageTimestamps(ctx, f.workspaceID, []domain.ConversationID{f.channelID})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.NewMessageTimestamp(newest)
	if latest[f.channelID] != want {
		t.Fatalf("latest = %q, want %q — the newest undeleted message", latest[f.channelID], want)
	}
	// A conversation nobody has posted in is absent, not zero.
	empty, err := f.repository.LatestMessageTimestamps(ctx, f.workspaceID, []domain.ConversationID{domain.ConversationID("C-absent-" + f.suffix)})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("latest = %+v, want no entry for a conversation with no messages", empty)
	}

	cursor := domain.ReadCursor{WorkspaceID: f.workspaceID, UserID: f.userID, Conversation: f.channelID, LastRead: want, UpdatedAt: newest}
	if err := f.repository.SetReadCursors(ctx, []domain.ReadCursor{cursor}, []events.Event{f.event("batch-cursor", "conversation.read", string(f.channelID))}); err != nil {
		t.Fatal(err)
	}
	stored, err := f.repository.GetReadCursor(ctx, f.workspaceID, f.userID, f.channelID)
	if err != nil || stored.LastRead != want {
		t.Fatalf("cursor = %+v err = %v, want the batch write to have landed", stored, err)
	}

	// Mismatched lengths are rejected rather than silently truncated: an
	// unpaired cursor would move a read position with nothing in the journal to
	// say it moved.
	if err := f.repository.SetReadCursors(ctx, []domain.ReadCursor{cursor}, nil); err == nil {
		t.Fatal("a cursor with no event was accepted")
	}
}

// The Threads view is the first read of thread_follows there has ever been:
// the table has stored follows since threads were built and nothing listed
// them, so the two profiles had no opportunity to disagree until now.
func followedThreadsAgreeAcrossProfiles(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	root := f.message(t, ctx, "M-thread-root", base)
	rootTimestamp := domain.NewMessageTimestamp(base)
	f.reply(t, ctx, "M-thread-reply-one", rootTimestamp, base.Add(time.Second))
	f.reply(t, ctx, "M-thread-reply-two", rootTimestamp, base.Add(2*time.Second))

	if err := f.repository.SetThreadFollowed(ctx, f.workspaceID, f.userID, f.channelID, rootTimestamp, true,
		f.event("follow", "thread.followed", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	page, err := f.repository.ListFollowedThreads(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Threads) != 1 {
		t.Fatalf("threads = %+v, want the one followed thread", page.Threads)
	}
	thread := page.Threads[0]
	if thread.Conversation != f.channelID || thread.Root != rootTimestamp {
		t.Fatalf("thread = %+v, want the followed root in %q", thread, f.channelID)
	}
	if thread.RootText != root.Text || thread.RootAuthorID != f.userID {
		t.Fatalf("thread root = %q by %q, want %q by %q", thread.RootText, thread.RootAuthorID, root.Text, f.userID)
	}
	if thread.ReplyCount != 2 {
		t.Fatalf("reply count = %d, want 2", thread.ReplyCount)
	}
	// With no read cursor everything is unread, which is what a member who has
	// never opened the conversation should see.
	if thread.UnreadReplies != 2 {
		t.Fatalf("unread replies = %d, want 2 before any read cursor", thread.UnreadReplies)
	}
	if !thread.LastReplyAt.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("last reply = %s, want %s", thread.LastReplyAt, base.Add(2*time.Second))
	}

	// Reading up to the first reply leaves exactly one unread.
	cursor := domain.ReadCursor{WorkspaceID: f.workspaceID, UserID: f.userID, Conversation: f.channelID,
		LastRead: domain.NewMessageTimestamp(base.Add(time.Second)), UpdatedAt: base.Add(time.Second)}
	if err := f.repository.SetReadCursor(ctx, cursor, f.event("thread-cursor", "conversation.read", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	page, err = f.repository.ListFollowedThreads(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Threads) != 1 || page.Threads[0].UnreadReplies != 1 {
		t.Fatalf("threads = %+v err = %v, want one unread reply after the cursor moved", page.Threads, err)
	}

	// A deleted root leaves the view rather than becoming a row that opens onto
	// nothing.
	root.Deleted = true
	if err := f.repository.DeleteMessage(ctx, root, f.event("thread-root-delete", "message.deleted", string(root.ID)), nil); err != nil {
		t.Fatal(err)
	}
	page, err = f.repository.ListFollowedThreads(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Threads) != 0 {
		t.Fatalf("threads = %+v err = %v, want the thread gone once its root was deleted", page.Threads, err)
	}
}

// The Threads view reads its roots in chunks, because how many threads a member
// follows in one conversation is their choice and every SQL engine caps bound
// parameters per statement. This walks more roots than one chunk holds and
// asserts the page is still whole: chunking is the kind of change that loses or
// repeats rows at the seam between batches, and both profiles have to agree it
// does neither.
func followedThreadsSurviveMoreRootsThanOneChunk(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	const roots = 250
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < roots; index++ {
		at := base.Add(time.Duration(index) * time.Second)
		f.message(t, ctx, fmt.Sprintf("M-bulk-%03d", index), at)
		if err := f.repository.SetThreadFollowed(ctx, f.workspaceID, f.userID, f.channelID, domain.NewMessageTimestamp(at), true,
			f.event(fmt.Sprintf("bulk-follow-%03d", index), "thread.followed", string(f.channelID))); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.repository.ListFollowedThreads(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 200})
	if err != nil {
		t.Fatalf("listing %d followed threads failed: %v", roots, err)
	}
	if len(page.Threads) != 200 || !page.HasMore {
		t.Fatalf("threads = %d hasMore = %v, want a full page of 200 and more to come", len(page.Threads), page.HasMore)
	}
	// Every row is distinct and carries the root it names: a chunk boundary
	// that repeated or dropped a batch would still fill the page.
	seen := make(map[domain.MessageTimestamp]struct{}, len(page.Threads))
	for _, thread := range page.Threads {
		if _, repeated := seen[thread.Root]; repeated {
			t.Fatalf("root %q appeared twice across chunk boundaries", thread.Root)
		}
		seen[thread.Root] = struct{}{}
		if thread.RootText == "" {
			t.Fatalf("thread %q came back with no root text, so its chunk did not resolve", thread.Root)
		}
	}
}

// A delay step waits on the clock, so the instant it becomes due is the one
// piece of workflow state that must outlive the process. A duration would start
// again from whenever the process did, turning an hour's wait across a
// deployment into two; both profiles have to store the instant and answer the
// same question about what is due.
func workflowDelaysWaitOnADurableInstant(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	due := time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC)
	step := domain.WorkflowStep{
		ID: domain.WorkflowStepID("Fx-delay-" + f.suffix), WorkflowRunID: domain.WorkflowRunID("Wx-" + f.suffix),
		WorkspaceID: f.workspaceID, UserID: f.userID, EditID: "wait",
		Status: domain.WorkflowStepWaiting, Inputs: `{"builtin":"delay"}`, Outputs: "{}",
		ResumeAt: due, CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
	}
	if err := f.repository.SetWorkflowStep(ctx, step, f.event("delay", "workflow.step_waiting", string(step.ID))); err != nil {
		t.Fatal(err)
	}
	// A step waiting on a person carries no wake time and must never be
	// mistaken for one waiting on the clock.
	person := step
	person.ID = domain.WorkflowStepID("Fx-form-" + f.suffix)
	person.EditID = "form"
	person.ResumeAt = time.Time{}
	if err := f.repository.SetWorkflowStep(ctx, person, f.event("form", "workflow.step_waiting", string(person.ID))); err != nil {
		t.Fatal(err)
	}

	before, err := f.repository.DueWorkflowDelays(ctx, f.workspaceID, due.Add(-time.Minute), 10)
	if err != nil || len(before) != 0 {
		t.Fatalf("due before the instant = %+v err = %v, want none", before, err)
	}
	after, err := f.repository.DueWorkflowDelays(ctx, f.workspaceID, due, 10)
	if err != nil || len(after) != 1 || after[0].ID != step.ID {
		t.Fatalf("due at the instant = %+v err = %v, want the delay and only the delay", after, err)
	}
	if !after[0].ResumeAt.Equal(due) {
		t.Fatalf("resume time came back as %s, want %s — a wait that loses its instant restarts", after[0].ResumeAt, due)
	}

	// Every workspace, which is the shape the global worker queue asks for.
	global, err := f.repository.DueWorkflowDelays(ctx, "", due, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range global {
		if value.ID == step.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the workspace-wide sweep missed the delay: %+v", global)
	}
}

func createMessageValidatesAndIsReferential(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	createdAt := time.Unix(1700000000, 0).UTC()
	malformed := domain.Message{ID: domain.MessageID("M-malformed-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID, AuthorID: f.userID, Text: "bad", Blocks: "{not json", CreatedAt: createdAt}
	if err := f.repository.CreateMessage(ctx, malformed, f.event("malformed", "message.created", string(malformed.ID)), ""); err == nil {
		t.Fatal("malformed blocks were accepted")
	}
	malformedAttachments := malformed
	malformedAttachments.ID = domain.MessageID("M-bad-attachments-" + f.suffix)
	malformedAttachments.Blocks = ""
	malformedAttachments.Attachments = "{not json"
	if err := f.repository.CreateMessage(ctx, malformedAttachments, f.event("bad-attachments", "message.created", string(malformedAttachments.ID)), ""); err == nil {
		t.Fatal("malformed attachments were accepted")
	}

	stored := f.message(t, ctx, "M-normalized", createdAt)
	loaded, err := f.repository.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Attachments != "[]" {
		t.Fatalf("attachments read back as %q, want the normalized empty array", loaded.Attachments)
	}

	duplicate := stored
	duplicate.Text = "second row"
	if err := f.repository.CreateMessage(ctx, duplicate, f.event("duplicate", "message.created", string(duplicate.ID)), ""); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate message identifier error=%v, want %v", err, store.ErrAlreadyExists)
	}

	orphan := domain.Message{ID: domain.MessageID("M-orphan-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: domain.ConversationID("C-missing-" + f.suffix), AuthorID: f.userID, Text: "orphan", CreatedAt: createdAt}
	if err := f.repository.CreateMessage(ctx, orphan, f.event("orphan", "message.created", string(orphan.ID)), ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("message in a missing conversation error=%v, want %v", err, store.ErrNotFound)
	}
}

func expiredOutboxLeaseIsFenced(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	if err := f.repository.AppendEvent(ctx, f.event("fenced", "message.created", "M-fenced")); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.repository.ClaimEvents(ctx, f.workspaceID, "worker-a", 10, 40*time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	sequences := []uint64{claimed[0].Sequence}
	time.Sleep(120 * time.Millisecond)
	if err := f.repository.AckEvents(ctx, "worker-a", sequences); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("expired owner acknowledgement error=%v, want %v", err, store.ErrLeaseConflict)
	}
	if err := f.repository.RenewEvents(ctx, "worker-a", sequences, time.Minute); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("expired owner renewal error=%v, want %v", err, store.ErrLeaseConflict)
	}
	retaken, err := f.repository.ClaimEvents(ctx, f.workspaceID, "worker-b", 10, time.Minute)
	if err != nil || len(retaken) != 1 || retaken[0].Sequence != sequences[0] {
		t.Fatalf("retaken=%+v err=%v", retaken, err)
	}
	if err := f.repository.AckEvents(ctx, "worker-b", sequences); err != nil {
		t.Fatalf("live owner acknowledgement error=%v", err)
	}
}

func internalTopicsStayInternal(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	// The payload of a blob-delete event is an internal storage key. Leaking it
	// into a client replay discloses it, and letting the general worker claim it
	// means the blob is never deleted.
	for _, topic := range []string{events.FileBlobDeleteTopic, events.UserPhotoBlobDeleteTopic} {
		if err := f.repository.AppendEvent(ctx, f.event("internal-"+topic, topic, "internal/storage/key")); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repository.AppendEvent(ctx, f.event("public", "message.created", "M-public")); err != nil {
		t.Fatal(err)
	}
	listed, err := f.repository.ListEventsAfter(ctx, f.workspaceID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range listed {
		if store.InternalTopic(record.Event.Topic) {
			t.Fatalf("client replay exposed internal topic %q with payload %q", record.Event.Topic, record.Event.Payload)
		}
	}
	claimed, err := f.repository.ClaimEvents(ctx, f.workspaceID, "general-worker", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range claimed {
		if store.InternalTopic(record.Event.Topic) {
			t.Fatalf("the general outbox worker claimed internal topic %q", record.Event.Topic)
		}
	}
	// Each internal topic remains claimable by its own worker.
	for _, topic := range []string{events.FileBlobDeleteTopic, events.UserPhotoBlobDeleteTopic} {
		cleanup, err := f.repository.ClaimEventsForTopic(ctx, f.workspaceID, topic, "cleanup-worker", 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleanup) != 1 || cleanup[0].Event.Topic != topic {
			t.Fatalf("topic-specific claim for %q = %+v", topic, cleanup)
		}
	}
}

func eventsRetainTheirActor(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	event := f.event("actor", "message.created", "M-actor")
	event.ActorID = f.userID
	if err := f.repository.AppendEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	listed, err := f.repository.ListEventsAfter(ctx, f.workspaceID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d events, want 1", len(listed))
	}
	if listed[0].Event.ActorID != f.userID {
		t.Fatalf("event actor = %q, want %q", listed[0].Event.ActorID, f.userID)
	}
	if !listed[0].Event.CreatedAt.Equal(event.CreatedAt) {
		t.Fatalf("event timestamp = %s, want %s", listed[0].Event.CreatedAt, event.CreatedAt)
	}
}

func emailIdentityIsCaseFolded(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	first := domain.User{ID: domain.UserID("U-fold-a-" + f.suffix), WorkspaceID: f.workspaceID, Email: "ä-" + f.suffix + "@x.test", Name: "first"}
	if err := f.repository.CreateUser(ctx, first, domain.WorkspaceMembership{WorkspaceID: f.workspaceID, UserID: first.ID, Role: domain.WorkspaceRoleMember, Active: true}, f.event("fold-a", "user.created", string(first.ID))); err != nil {
		t.Fatal(err)
	}
	second := domain.User{ID: domain.UserID("U-fold-b-" + f.suffix), WorkspaceID: f.workspaceID, Email: "Ä-" + f.suffix + "@X.TEST", Name: "second"}
	if err := f.repository.CreateUser(ctx, second, domain.WorkspaceMembership{WorkspaceID: f.workspaceID, UserID: second.ID, Role: domain.WorkspaceRoleMember, Active: true}, f.event("fold-b", "user.created", string(second.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("non-ASCII case variant error=%v, want %v", err, store.ErrAlreadyExists)
	}
	found, err := f.repository.FindUserByEmail(ctx, f.workspaceID, "  Ä-"+f.suffix+"@X.TEST  ")
	if err != nil || found.ID != first.ID {
		t.Fatalf("lookup by a case variant returned %+v err=%v", found, err)
	}
	if found.Email != domain.NormalizeEmail(first.Email) {
		t.Fatalf("stored email %q, want the normalized form %q", found.Email, domain.NormalizeEmail(first.Email))
	}
}

func conversationSearchTreatsMetacharactersLiterally(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	literal := domain.Conversation{ID: domain.ConversationID("C-literal-" + f.suffix), WorkspaceID: f.workspaceID, Name: "100%-coverage"}
	if err := f.repository.SeedConversation(ctx, literal); err != nil {
		t.Fatal(err)
	}
	underscore := domain.Conversation{ID: domain.ConversationID("C-underscore-" + f.suffix), WorkspaceID: f.workspaceID, Name: "snake_case"}
	if err := f.repository.SeedConversation(ctx, underscore); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]domain.ConversationID{"%": literal.ID, "_": underscore.ID} {
		page, err := f.repository.SearchConversations(ctx, f.workspaceID, query, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(page.Conversations) != 1 || page.Conversations[0].ID != want {
			t.Fatalf("search %q matched %+v, want only %q", query, page.Conversations, want)
		}
	}
}

func searchFoldsUnicodeIdentically(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	conversation := domain.Conversation{
		ID:          domain.ConversationID("C-unicode-" + f.suffix),
		WorkspaceID: f.workspaceID,
		Name:        "ÄPFEL",
	}
	if err := f.repository.CreateConversation(ctx, conversation, f.userID, f.event("unicode-conversation", "conversation.created", string(conversation.ID))); err != nil {
		t.Fatal(err)
	}
	assertConversation := func(query string) {
		t.Helper()
		page, err := f.repository.SearchConversations(ctx, f.workspaceID, query, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("conversation search %q: %v", query, err)
		}
		if len(page.Conversations) != 1 || page.Conversations[0].ID != conversation.ID {
			t.Fatalf("conversation search %q returned %+v, want %s", query, page.Conversations, conversation.ID)
		}
	}
	assertConversation("äpfel")
	if _, err := f.repository.RenameConversation(ctx, conversation.ID, "ÜBER", f.event("unicode-rename", "conversation.renamed", string(conversation.ID))); err != nil {
		t.Fatal(err)
	}
	assertConversation("über")
	if _, err := f.repository.SetConversationTopic(ctx, conversation.ID, "ÉCOLE", f.event("unicode-topic", "conversation.topic_changed", string(conversation.ID))); err != nil {
		t.Fatal(err)
	}
	assertConversation("école")
	if _, err := f.repository.SetConversationPurpose(ctx, conversation.ID, "ÅNGSTRÖM", f.event("unicode-purpose", "conversation.purpose_changed", string(conversation.ID))); err != nil {
		t.Fatal(err)
	}
	assertConversation("ångström")

	createdAt := time.Unix(1700000100, 0).UTC()
	message := domain.Message{ID: domain.MessageID("M-unicode-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: conversation.ID, AuthorID: f.userID, Text: "ÄPFEL", CreatedAt: createdAt}
	if err := f.repository.CreateMessage(ctx, message, f.event("unicode-message", "message.created", string(message.ID)), ""); err != nil {
		t.Fatal(err)
	}
	assertMessage := func(query string) {
		t.Helper()
		page, err := f.repository.SearchMessages(ctx, f.workspaceID, f.userID, domain.MessageSearch{
			Terms: []string{query}, Page: domain.PageRequest{Limit: 10},
		})
		if err != nil {
			t.Fatalf("message search %q: %v", query, err)
		}
		if len(page.Messages) != 1 || page.Messages[0].ID != message.ID {
			t.Fatalf("message search %q returned %+v, want %s", query, page.Messages, message.ID)
		}
	}
	assertMessage("äpfel")
	message.Text = "ÖL"
	if err := f.repository.UpdateMessage(ctx, message, f.event("unicode-message-update", "message.changed", string(message.ID))); err != nil {
		t.Fatal(err)
	}
	assertMessage("öl")
}

func messagesPageInBothDirections(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1700000200, 0).UTC()
	var deleted domain.Message
	for index := 1; index <= 5; index++ {
		message := f.message(t, ctx, fmt.Sprintf("direction-%d", index), base.Add(time.Duration(index)*time.Second))
		if index == 4 {
			deleted = message
		}
	}
	deleted.Deleted = true
	if err := f.repository.UpdateMessage(ctx, deleted, f.event("delete-direction-4", "message.deleted", string(deleted.ID))); err != nil {
		t.Fatal(err)
	}
	first, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 2, Descending: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := []domain.MessageID{first.Messages[0].ID, first.Messages[1].ID}; !strings.Contains(string(got[0]), "direction-5") || !strings.Contains(string(got[1]), "direction-3") || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first descending page=%+v", first)
	}
	second, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 2, Cursor: first.NextCursor, Descending: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || !strings.Contains(string(second.Messages[0].ID), "direction-2") || !strings.Contains(string(second.Messages[1].ID), "direction-1") || second.HasMore {
		t.Fatalf("second descending page=%+v", second)
	}
	forward, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Messages) != 1 || !strings.Contains(string(forward.Messages[0].ID), "direction-5") {
		t.Fatalf("descending cursor did not resume forward from the same row: %+v", forward)
	}
}

func referentialFailuresAreSentinels(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	// A driver-level constraint failure must never escape: the transport maps an
	// unrecognized error to a retryable status carrying the constraint name.
	access := domain.ListAccess{ListID: domain.ListID("F-missing-" + f.suffix), EntityType: "channel", EntityID: string(f.channelID), Access: "read"}
	if err := f.repository.SetListAccess(ctx, access, f.event("missing-list-access", "list.access.set", string(access.ListID))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("access on a missing list error=%v, want %v", err, store.ErrNotFound)
	}
	conversation := domain.Conversation{ID: domain.ConversationID("C-dup-" + f.suffix), WorkspaceID: f.workspaceID, Name: "duplicate"}
	if err := f.repository.CreateConversation(ctx, conversation, f.userID, f.event("create-conversation", "channel.created", string(conversation.ID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CreateConversation(ctx, conversation, f.userID, f.event("create-conversation-again", "channel.created", string(conversation.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate conversation error=%v, want %v", err, store.ErrAlreadyExists)
	}
	nameClash := conversation
	nameClash.ID = domain.ConversationID("C-name-clash-" + f.suffix)
	if err := f.repository.CreateConversation(ctx, nameClash, f.userID, f.event("create-conversation-name-clash", "channel.created", string(nameClash.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate conversation name error=%v, want %v", err, store.ErrAlreadyExists)
	}
	renameTarget := conversation
	renameTarget.ID = domain.ConversationID("C-rename-target-" + f.suffix)
	renameTarget.Name = "rename-target-" + f.suffix
	if err := f.repository.CreateConversation(ctx, renameTarget, f.userID, f.event("create-conversation-rename-target", "channel.created", string(renameTarget.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.RenameConversation(ctx, renameTarget.ID, conversation.Name, f.event("rename-conversation-name-clash", "channel.renamed", string(renameTarget.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("renaming conversation onto a taken name error=%v, want %v", err, store.ErrAlreadyExists)
	}
	group := domain.UserGroup{ID: domain.UserGroupID("S-dup-a-" + f.suffix), WorkspaceID: f.workspaceID, Name: "group a", Handle: "shared_handle_" + f.suffix, Creator: f.userID, UpdatedBy: f.userID, CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC(), Enabled: true}
	if err := f.repository.CreateUserGroup(ctx, group, f.event("group-a", "usergroup.created", string(group.ID))); err != nil {
		t.Fatal(err)
	}
	clash := group
	clash.ID = domain.UserGroupID("S-dup-b-" + f.suffix)
	clash.Name = "group b"
	if err := f.repository.CreateUserGroup(ctx, clash, f.event("group-b", "usergroup.created", string(clash.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate user group handle error=%v, want %v", err, store.ErrAlreadyExists)
	}
	renamed := group
	renamed.Handle = "renamed_handle_" + f.suffix
	if err := f.repository.CreateUserGroup(ctx, domain.UserGroup{ID: domain.UserGroupID("S-third-" + f.suffix), WorkspaceID: f.workspaceID, Name: "group c", Handle: renamed.Handle, Creator: f.userID, UpdatedBy: f.userID, CreatedAt: renamed.CreatedAt, UpdatedAt: renamed.UpdatedAt, Enabled: true}, f.event("group-c", "usergroup.created", "S-third")); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.UpdateUserGroup(ctx, renamed, f.event("group-rename", "usergroup.updated", string(renamed.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("renaming a user group onto a taken handle error=%v, want %v", err, store.ErrAlreadyExists)
	}
}

func expiredSocketModeConnectionIsNotRevived(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	appID := domain.AppID("A-revive-" + f.suffix)
	connection := domain.SocketModeConnection{ID: "socket-revive-" + f.suffix, AppID: appID, ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if err := f.repository.CreateSocketModeConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ConsumeSocketModeConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	// Start the expiry window only after consumption. Under the race detector a
	// contended SQLite operation can take longer than the former 80 ms ticket
	// lifetime, which made the prerequisite consume correctly return NotFound
	// before this contract reached the renewal behavior it exists to exercise.
	expiresAt := time.Now().UTC().Add(2 * time.Second)
	if err := f.repository.RenewSocketModeConnection(ctx, connection.ID, expiresAt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(expiresAt) + 100*time.Millisecond)
	active, err := f.repository.CountSocketModeConnections(ctx, appID)
	if err != nil || active != 0 {
		t.Fatalf("active connections after expiry=%d err=%v, want 0", active, err)
	}
	// The slot is gone, so a replacement may already hold it; reviving this one
	// would put the app over the concurrency limit.
	if err := f.repository.RenewSocketModeConnection(ctx, connection.ID, time.Now().UTC().Add(time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("renewing an expired connection error=%v, want %v", err, store.ErrNotFound)
	}
	if active, err := f.repository.CountSocketModeConnections(ctx, appID); err != nil || active != 0 {
		t.Fatalf("active connections after a rejected renewal=%d err=%v, want 0", active, err)
	}
}

func socketModeBatchesAreAllOrNothing(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	appID := domain.AppID("A-batch-" + f.suffix)
	queued := make([]domain.SocketModeResponse, 0, 3)
	for index := 0; index < 3; index++ {
		value := domain.SocketModeResponse{AppID: appID, EnvelopeID: fmt.Sprintf("envelope-%d-%s", index, f.suffix), Payload: `{"ok":true}`, ReceivedAt: time.Now().UTC().Add(time.Duration(index) * time.Millisecond)}
		if err := f.repository.RecordSocketModeResponse(ctx, value); err != nil {
			t.Fatal(err)
		}
		queued = append(queued, value)
	}
	claimed, err := f.repository.ClaimSocketModeResponses(ctx, appID, "worker-a", 10, time.Minute)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	// The last envelope is not held by this owner, so the whole batch must fail
	// and leave the first two unacknowledged.
	poisoned := append([]domain.SocketModeResponse(nil), claimed...)
	poisoned[2].EnvelopeID = queued[2].EnvelopeID
	if err := f.repository.AckSocketModeResponses(ctx, "worker-b", poisoned); err == nil {
		t.Fatal("a batch acknowledged by the wrong owner succeeded")
	}
	if err := f.repository.AckSocketModeResponses(ctx, "worker-a", claimed); err != nil {
		t.Fatalf("the rightful owner could not acknowledge the batch: %v", err)
	}
	remaining, err := f.repository.ClaimSocketModeResponses(ctx, appID, "worker-c", 10, time.Minute)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("acknowledged responses reclaimed=%+v err=%v", remaining, err)
	}
}

func seedHelpersRejectInvalidInput(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: domain.WorkspaceID("T-bad-" + f.suffix), Name: "bad", Discoverability: "sideways"}); err == nil {
		t.Fatal("an invalid workspace discoverability was accepted")
	}
	if err := f.repository.SeedUser(ctx, domain.User{ID: domain.UserID("U-bad-" + f.suffix), WorkspaceID: f.workspaceID, Name: "bad", Presence: "elsewhere"}); err == nil {
		t.Fatal("an invalid user presence was accepted")
	}
	if _, err := f.repository.SetUserPresence(ctx, f.workspaceID, f.userID, domain.Presence("elsewhere"), f.event("bad-presence", "user.presence_changed", string(f.userID))); err == nil {
		t.Fatal("an invalid presence was stored")
	}
}

// socketModeAdmissionIsAtomicUnderConcurrency drives the real race rather than
// asserting the fixed code back to itself. Consumption is what makes a Socket
// Mode connection active, so it is where the concurrency limit is enforced; the
// SQL repositories read the active count and then wrote in a transaction whose
// first statement was that read, so every concurrent dialler saw the same count
// and every one of them was admitted. Measured before the repair: 64 diallers
// against a limit of 10 admitted between 11 and 15.
//
// The load test that was supposed to cover this exercised only the in-memory
// profile, where a single mutex makes the check-then-act atomic by accident,
// which is exactly why the SQL profiles' version survived.
func socketModeAdmissionIsAtomicUnderConcurrency(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	appID := domain.AppID("A-admission-" + f.suffix)
	dialers := 64
	for index := 0; index < dialers; index++ {
		connection := domain.SocketModeConnection{ID: fmt.Sprintf("socket-admission-%d-%s", index, f.suffix), AppID: appID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		if err := f.repository.CreateSocketModeConnection(ctx, connection); err != nil {
			// Ticket issuance is itself bounded; stop at the bound and dial what
			// was issued, which is still far more than the limit.
			dialers = index
			break
		}
	}
	if dialers <= domain.SocketModeConnectionLimit {
		t.Fatalf("only %d tickets were issued, which cannot exceed the limit of %d", dialers, domain.SocketModeConnectionLimit)
	}

	var admitted, limited int64
	failures := make(chan error, dialers)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < dialers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			_, err := f.repository.ConsumeSocketModeConnection(ctx, fmt.Sprintf("socket-admission-%d-%s", index, f.suffix))
			switch {
			case err == nil:
				atomic.AddInt64(&admitted, 1)
			case errors.Is(err, store.ErrSocketModeConnectionLimit):
				atomic.AddInt64(&limited, 1)
			default:
				failures <- err
			}
		}(index)
	}
	close(start)
	group.Wait()
	close(failures)

	// Every dialer is either admitted or told the limit was reached. Anything
	// else is the repository leaking its own contention to the caller: reading
	// the count and then writing in the same transaction makes the losing writer
	// fail with an engine-level snapshot conflict, which a dialer cannot act on
	// and which is not the reason it was refused.
	for err := range failures {
		t.Errorf("a dialer was refused with %v, want either admission or %v", err, store.ErrSocketModeConnectionLimit)
	}
	if t.Failed() {
		t.FailNow()
	}
	if admitted > int64(domain.SocketModeConnectionLimit) {
		t.Fatalf("%d of %d concurrent dialers were admitted, want at most %d", admitted, dialers, domain.SocketModeConnectionLimit)
	}
	// And the slots are actually filled: an admission that loses a race must be
	// refused for a reason the caller can retry on, not silently dropped.
	if admitted != int64(domain.SocketModeConnectionLimit) {
		t.Fatalf("%d of %d concurrent dialers were admitted, want the full %d", admitted, dialers, domain.SocketModeConnectionLimit)
	}
	if admitted+limited != int64(dialers) {
		t.Fatalf("%d admitted plus %d limited does not account for %d dialers", admitted, limited, dialers)
	}
	active, err := f.repository.CountSocketModeConnections(ctx, appID)
	if err != nil {
		t.Fatal(err)
	}
	if active != int(admitted) {
		t.Fatalf("counted %d active connections, want the %d that were admitted", active, admitted)
	}
}

// blobReferencesTolerateAnArbitraryProfilePhotoURL covers a defect any workspace
// member could trigger from their own profile: users.profile.set accepts
// image_24 as free text, and both repositories treated a URL they did not mint
// as a corrupt database and failed the whole walk. On the in-memory profile the
// failing return happened while the read lock was held, so blob garbage
// collection did not merely fail — it left the lock held and deadlocked every
// subsequent write to the repository, permanently.
func blobReferencesTolerateAnArbitraryProfilePhotoURL(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	profile := domain.UserProfile{DisplayName: "divergence", Image24: "https://example.test/avatar.png"}
	if _, err := f.repository.UpdateUserProfile(ctx, f.workspaceID, f.userID, profile, f.event("photo", "user.profile_changed", string(f.userID))); err != nil {
		t.Fatal(err)
	}

	visited := make([]string, 0)
	if err := f.repository.WalkBlobReferences(ctx, f.workspaceID, func(reference string) error {
		visited = append(visited, reference)
		return nil
	}); err != nil {
		t.Fatalf("a profile photo URL this deployment did not mint stopped blob garbage collection: %v", err)
	}
	for _, reference := range visited {
		if strings.Contains(reference, "example.test") {
			t.Fatalf("an external avatar URL was reported as a blob reference: %q", reference)
		}
	}

	// And the repository is still usable. A held lock is invisible until the next
	// writer arrives, so the walk returning is not on its own proof of anything.
	done := make(chan error, 1)
	go func() {
		_, err := f.repository.SetUserPresence(ctx, f.workspaceID, f.userID, domain.PresenceAway, f.event("presence", "user.presence_changed", string(f.userID)))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the repository deadlocked after walking blob references: the walk returned while still holding its lock")
	}
}

// emailIdentityIsNotUnicodeCaseFolded pins which canonical form the profiles
// agree on. The in-memory repository compared addresses with strings.EqualFold,
// which applies full Unicode simple folding: U+017F LATIN SMALL LETTER LONG S
// folds onto 's', so "ſmith@x.test" resolved to the account owning
// "smith@x.test". That is an account takeover through an identity provider's
// asserted address, and it existed on exactly one storage profile.
func emailIdentityIsNotUnicodeCaseFolded(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	victim := domain.User{ID: domain.UserID("U-fold-victim-" + f.suffix), WorkspaceID: f.workspaceID, Email: "smith-" + f.suffix + "@x.test", Name: "victim"}
	if err := f.repository.CreateUser(ctx, victim, domain.WorkspaceMembership{WorkspaceID: f.workspaceID, UserID: victim.ID, Role: domain.WorkspaceRoleMember, Active: true}, f.event("fold-victim", "user.created", string(victim.ID))); err != nil {
		t.Fatal(err)
	}

	confusable := "ſmith-" + f.suffix + "@x.test"
	found, err := f.repository.FindUserByEmail(ctx, f.workspaceID, confusable)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("looking up %q resolved to %+v err=%v, want %v: a Unicode case fold must not map one identity onto another", confusable, found, err, store.ErrNotFound)
	}

	// ASCII case and surrounding space are still folded, on every profile.
	loaded, err := f.repository.FindUserByEmail(ctx, f.workspaceID, "  SMITH-"+f.suffix+"@X.TEST  ")
	if err != nil || loaded.ID != victim.ID {
		t.Fatalf("case-insensitive lookup returned %+v err=%v, want %s", loaded, err, victim.ID)
	}
	if loaded.Email != domain.NormalizeEmail(victim.Email) {
		t.Fatalf("stored address %q, want the canonical %q", loaded.Email, domain.NormalizeEmail(victim.Email))
	}
}

// starsPageInChronologicalOrder is the round-one ordering defect in the one
// place it survived. The in-memory repository built its star cursor key with
// time.RFC3339Nano, which strips trailing zeros from the fraction, so a star at
// .120000 encoded as "…:00.12Z", one at .123456 as "…:00.123456Z", and
// 'Z' (0x5A) > '3' (0x33) put the earlier star after the later one — and the
// cursor minted from the ".12Z" key skipped every ".1234xx" row on the next page.
func starsPageInChronologicalOrder(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	instants := trailingZeroFractions(base)
	for index, instant := range instants {
		message := f.message(t, ctx, fmt.Sprintf("star-message-%d", index), instant)
		star := domain.Star{Conversation: f.channelID, UserID: f.userID, Message: message, CreatedAt: instant}
		if err := f.repository.AddStar(ctx, star, f.event(fmt.Sprintf("star-%d", index), "star.added", string(message.ID))); err != nil {
			t.Fatal(err)
		}
	}

	page, _, _, err := f.repository.ListStars(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: len(instants) + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != len(instants) {
		t.Fatalf("listed %d stars, want %d", len(page), len(instants))
	}
	for index := 1; index < len(page); index++ {
		if page[index].CreatedAt.Before(page[index-1].CreatedAt) {
			t.Fatalf("star %d (%s) sorted before %d (%s)", index, page[index].CreatedAt, index-1, page[index-1].CreatedAt)
		}
	}

	// Walk one at a time: the same key is the keyset cursor, so a broken encoding
	// shows up as a skipped or repeated identifier rather than as a wrong order.
	seen := make(map[domain.MessageID]struct{}, len(instants))
	request := domain.PageRequest{Limit: 1}
	for {
		single, next, hasMore, err := f.repository.ListStars(ctx, f.workspaceID, f.userID, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, star := range single {
			if _, repeated := seen[star.Message.ID]; repeated {
				t.Fatalf("keyset pagination repeated %q", star.Message.ID)
			}
			seen[star.Message.ID] = struct{}{}
		}
		if !hasMore {
			break
		}
		request.Cursor = next
		if len(seen) > len(instants) {
			t.Fatal("keyset pagination did not terminate")
		}
	}
	if len(seen) != len(instants) {
		t.Fatalf("keyset pagination visited %d stars, want all %d", len(seen), len(instants))
	}
}

// messagesResolveByTheirOwnCreationInstant pins the read half of the invariant
// CreateMessage enforces on the write side. A message is stored truncated to the
// microsecond its public timestamp can express; the SQL repositories did not
// truncate the LOOKUP key, so the same call answered ErrNotFound there and
// returned the message in memory.
func messagesResolveByTheirOwnCreationInstant(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	// 789 nanoseconds past a microsecond boundary: precision the message's own
	// identifier cannot carry.
	created := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	message := f.message(t, ctx, "instant", created)

	found, err := f.repository.GetMessageByCreatedAt(ctx, f.channelID, created)
	if err != nil || found.ID != message.ID {
		t.Fatalf("lookup by the creating instant returned %+v err=%v, want %s", found, err, message.ID)
	}
	truncated, err := f.repository.GetMessageByCreatedAt(ctx, f.channelID, domain.MessageInstant(created))
	if err != nil || truncated.ID != message.ID {
		t.Fatalf("lookup by the truncated instant returned %+v err=%v, want %s", truncated, err, message.ID)
	}
	if _, err := f.repository.GetMessageByCreatedAt(ctx, f.channelID, created.Add(time.Second)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup at a different instant error=%v, want %v", err, store.ErrNotFound)
	}
}

// listsAreCreatedWithTheirItemsOrNotAtAll pins the unit of work the copy_from
// journey needs. Creating the list, announcing it, and then copying items one
// call at a time left a half-copied list that clients had already been told
// about, with no cleanup path.
func listsAreCreatedWithTheirItemsOrNotAtAll(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	now := time.Unix(1700000000, 0).UTC()
	listID := domain.ListID("F-copy-" + f.suffix)
	list := domain.List{ID: listID, WorkspaceID: f.workspaceID, OwnerID: f.userID, Name: "copied", Schema: "[]", CreatedAt: now, UpdatedAt: now}
	item := func(name string) domain.ListItem {
		return domain.ListItem{ID: domain.ListItemID(name + "-" + f.suffix), ListID: listID, WorkspaceID: f.workspaceID, Fields: "[]", CreatedBy: f.userID, UpdatedBy: f.userID, CreatedAt: now, UpdatedAt: now}
	}

	// One item in the batch belongs to a different list, so the whole creation
	// must fail and leave nothing behind.
	stray := item("stray")
	stray.ListID = domain.ListID("F-other-" + f.suffix)
	if err := f.repository.CreateListWithItems(ctx, list, f.event("list-bad", "list.created", string(listID)), []store.ListItemCreation{
		{Item: item("keep"), Event: f.event("item-keep-bad", "list.item.created", "keep")},
		{Item: stray, Event: f.event("item-stray", "list.item.created", "stray")},
	}); err == nil {
		t.Fatal("a batch naming an item of another list was accepted")
	}
	if _, err := f.repository.GetList(ctx, f.workspaceID, listID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a rejected creation left the list behind: err=%v", err)
	}

	// The whole batch, committed together.
	creations := []store.ListItemCreation{
		{Item: item("one"), Event: f.event("item-one", "list.item.created", "one")},
		{Item: item("two"), Event: f.event("item-two", "list.item.created", "two")},
	}
	if err := f.repository.CreateListWithItems(ctx, list, f.event("list-good", "list.created", string(listID)), creations); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.GetList(ctx, f.workspaceID, listID); err != nil {
		t.Fatal(err)
	}
	items, err := f.repository.ListItems(ctx, f.workspaceID, listID, domain.PageRequest{Limit: 10}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != len(creations) {
		t.Fatalf("copied %d items, want %d", len(items.Items), len(creations))
	}
}

// profileChangesCommitWithEveryEventTheyCarry pins the other unit of work: a
// photo replacement changes the profile AND instructs the cleanup worker to
// retire the bytes the old profile referenced. Appending the second through a
// separate call left a window in which the profile no longer names the old blob
// and nothing has been told to delete it.
func profileChangesCommitWithEveryEventTheyCarry(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	announcement := f.event("profile", "user.profile_changed", string(f.userID))
	cleanup := f.event("cleanup", events.UserPhotoBlobDeleteTopic, string(f.workspaceID)+"/users/"+string(f.userID)+"/old")
	if _, err := f.repository.UpdateUserProfile(ctx, f.workspaceID, f.userID, domain.UserProfile{DisplayName: "changed"}, announcement, cleanup); err != nil {
		t.Fatal(err)
	}

	claimed, err := f.repository.ClaimEventsForTopic(ctx, f.workspaceID, events.UserPhotoBlobDeleteTopic, "cleanup-worker", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Event.ID != cleanup.ID {
		t.Fatalf("cleanup events=%+v, want exactly the one committed with the profile change", claimed)
	}
	// A profile change with no event at all is not a change anybody can observe.
	if _, err := f.repository.UpdateUserProfile(ctx, f.workspaceID, f.userID, domain.UserProfile{DisplayName: "silent"}); err == nil {
		t.Fatal("a profile change with no event was accepted")
	}
}

// resolvedAccessNamesOneGrantDeterministically pins the half of the access
// contract the port promises and neither repository kept: "the returned value
// names the grant that decided the outcome, so a caller can report why access
// was allowed". Both kept the first grant of the highest rank they happened to
// see — over a randomised Go map in one and a query with no ORDER BY in the
// other — so a user holding the same level through two channels got a different
// answer on successive identical calls.
func resolvedAccessNamesOneGrantDeterministically(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	now := time.Unix(1700000000, 0).UTC()
	owner := domain.UserID("U-owner-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: owner, WorkspaceID: f.workspaceID, Email: "owner-" + f.suffix + "@example.com", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	listID := domain.ListID("F-grants-" + f.suffix)
	if err := f.repository.CreateList(ctx, domain.List{ID: listID, WorkspaceID: f.workspaceID, OwnerID: owner, Name: "grants", Schema: "[]", CreatedAt: now, UpdatedAt: now}, f.event("grant-list", "list.created", string(listID))); err != nil {
		t.Fatal(err)
	}
	// Two channels the subject belongs to, both granting the same level, so the
	// only thing distinguishing them is the tie-break.
	channels := []domain.ConversationID{domain.ConversationID("C-grant-b-" + f.suffix), domain.ConversationID("C-grant-a-" + f.suffix)}
	for index, channel := range channels {
		if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: channel, WorkspaceID: f.workspaceID, Name: fmt.Sprintf("grant-%d", index)}); err != nil {
			t.Fatal(err)
		}
		if err := f.repository.SeedConversationMember(ctx, channel, f.userID); err != nil {
			t.Fatal(err)
		}
		if err := f.repository.SetListAccess(ctx, domain.ListAccess{ListID: listID, EntityType: "channel", EntityID: string(channel), Access: store.AccessWrite}, f.event(fmt.Sprintf("grant-%d", index), "list.access_set", string(listID))); err != nil {
			t.Fatal(err)
		}
	}

	first, err := f.repository.GetListAccess(ctx, listID, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Access != store.AccessWrite {
		t.Fatalf("resolved level=%q, want %q", first.Access, store.AccessWrite)
	}
	for attempt := 0; attempt < 40; attempt++ {
		again, err := f.repository.GetListAccess(ctx, listID, f.userID)
		if err != nil {
			t.Fatal(err)
		}
		if again.EntityType != first.EntityType || again.EntityID != first.EntityID {
			t.Fatalf("attempt %d named grant %s/%s, first call named %s/%s: the reported reason for access is not stable", attempt, again.EntityType, again.EntityID, first.EntityType, first.EntityID)
		}
	}
}

// mutationsReturnTheValueTheyWrote covers the post-commit re-read. A mutation
// that commits and then reads the row back through the pool returns whatever a
// concurrent writer left behind, so two overlapping bookmarks.edit calls each
// received the OTHER caller's title as the result of their own write.
func mutationsReturnTheValueTheyWrote(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	now := time.Unix(1700000000, 0).UTC()
	bookmark := domain.Bookmark{ID: domain.BookmarkID("Bk-race-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID, Title: "initial", Type: "link", Link: "https://example.test", CreatedAt: now, UpdatedAt: now, UpdatedBy: f.userID}
	if err := f.repository.CreateBookmark(ctx, bookmark, f.event("bookmark", "bookmark.created", string(bookmark.ID))); err != nil {
		t.Fatal(err)
	}

	const writers = 4
	const rounds = 40
	mismatches := make(chan string, writers*rounds)
	var group sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		group.Add(1)
		go func(writer int) {
			defer group.Done()
			for round := 0; round < rounds; round++ {
				title := fmt.Sprintf("writer-%d-round-%d", writer, round)
				update := bookmark
				update.Title = title
				update.UpdatedAt = now
				returned, err := f.repository.UpdateBookmark(ctx, update, f.event(fmt.Sprintf("edit-%d-%d", writer, round), "bookmark.updated", string(bookmark.ID)))
				if err != nil {
					mismatches <- err.Error()
					return
				}
				if returned.Title != title {
					mismatches <- fmt.Sprintf("a writer that stored %q was handed back %q", title, returned.Title)
					return
				}
			}
		}(writer)
	}
	group.Wait()
	close(mismatches)
	for message := range mismatches {
		t.Fatalf("a mutation did not return the value it wrote: %s", message)
	}
}

// endingAnAlreadyEndedCallIsAConflict pins one of the last error-code
// divergences between the profiles: the SQL repositories reported ErrNotFound
// for a call that had already finished, because their guarded UPDATE could not
// tell that apart from a call that does not exist, while the in-memory
// repository reported ErrAlreadyExists. The caller was told its call had
// vanished.
func endingAnAlreadyEndedCallIsAConflict(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	call := domain.Call{ID: domain.CallID("call-end-" + f.suffix), WorkspaceID: f.workspaceID, ExternalUniqueID: "ext-" + f.suffix, JoinURL: "https://example.test/join", CreatedBy: f.userID, StartedAt: time.Unix(1700000000, 0).UTC()}
	if err := f.repository.CreateCall(ctx, call, f.event("call", "call.created", string(call.ID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.EndCall(ctx, f.workspaceID, call.ID, 30, f.event("call-end", "call.ended", string(call.ID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.EndCall(ctx, f.workspaceID, call.ID, 30, f.event("call-end-again", "call.ended", string(call.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("ending an already ended call error=%v, want %v", err, store.ErrAlreadyExists)
	}
	if err := f.repository.EndCall(ctx, f.workspaceID, domain.CallID("call-missing-"+f.suffix), 30, f.event("call-missing", "call.ended", "missing")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ending a call that does not exist error=%v, want %v", err, store.ErrNotFound)
	}
}

// connectedChannelPagesAreFilteredAndBounded covers a repository method the
// suite did not reach at all. The SQL implementation selected every
// conversation-to-team row in the workspace past the cursor and then applied the
// channel filter, the team filter and the page limit in Go, so the cost of one
// page was the size of the workspace; the in-memory implementation filtered and
// paged differently, and nothing compared the two.
func connectedChannelPagesAreFilteredAndBounded(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	partners := []domain.WorkspaceID{domain.WorkspaceID("T-partner-a-" + f.suffix), domain.WorkspaceID("T-partner-b-" + f.suffix)}
	for _, partner := range partners {
		if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: partner, Name: "partner"}); err != nil {
			t.Fatal(err)
		}
	}
	channels := make([]domain.ConversationID, 0, 5)
	// The fixture's own channel is connected to its own workspace, so the
	// assertions below are scoped to the channels this contract creates.
	expected := make(map[domain.ConversationID]int, 5)
	for index := 0; index < 5; index++ {
		channel := domain.ConversationID(fmt.Sprintf("C-connected-%d-%s", index, f.suffix))
		if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: channel, WorkspaceID: f.workspaceID, Name: fmt.Sprintf("connected-%d", index)}); err != nil {
			t.Fatal(err)
		}
		// Even channels reach both partners, odd channels only the first.
		reach := partners
		if index%2 == 1 {
			reach = partners[:1]
		}
		if err := f.repository.SetConversationTeams(ctx, f.workspaceID, channel, reach, false, f.event(fmt.Sprintf("connect-%d", index), "conversation.teams_set", string(channel))); err != nil {
			t.Fatal(err)
		}
		channels = append(channels, channel)
		expected[channel] = len(reach)
	}

	// Page through everything two at a time and require every channel exactly
	// once, in identifier order, with its full team list intact.
	seen := make([]domain.ConversationID, 0, len(channels))
	all := make([]domain.ConversationID, 0, len(channels)+1)
	request := domain.PageRequest{Limit: 2}
	for {
		page, hasMore, next, err := f.repository.ListConnectedChannelInfo(ctx, f.workspaceID, nil, nil, request)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > request.Limit {
			t.Fatalf("a page of %d exceeded the requested limit of %d", len(page), request.Limit)
		}
		for _, info := range page {
			if want, ours := expected[info.ChannelID]; ours {
				if len(info.InternalTeamIDs) != want {
					t.Fatalf("channel %s reported %d teams, want %d: paging must not cut a channel's team list", info.ChannelID, len(info.InternalTeamIDs), want)
				}
				seen = append(seen, info.ChannelID)
			}
			all = append(all, info.ChannelID)
		}
		if !hasMore {
			break
		}
		request.Cursor = next
		if len(all) > len(channels)+8 {
			t.Fatalf("paging did not terminate: %v", all)
		}
	}
	if len(seen) != len(channels) {
		t.Fatalf("paged %d channels, want %d: %v", len(seen), len(channels), seen)
	}
	for index := 1; index < len(all); index++ {
		if all[index] <= all[index-1] {
			t.Fatalf("connected channels are not in identifier order: %v", all)
		}
	}

	// The team filter selects channels, not just their team lists.
	filtered, _, _, err := f.repository.ListConnectedChannelInfo(ctx, f.workspaceID, nil, partners[1:], domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 3 {
		t.Fatalf("filtering by the second partner returned %d channels (%+v), want the 3 that reach it", len(filtered), filtered)
	}
	for _, info := range filtered {
		if len(info.InternalTeamIDs) != 1 || info.InternalTeamIDs[0] != partners[1] {
			t.Fatalf("channel %s reported %v, want only the filtered team", info.ChannelID, info.InternalTeamIDs)
		}
	}

	// And the channel filter narrows to exactly the named channels.
	named, _, _, err := f.repository.ListConnectedChannelInfo(ctx, f.workspaceID, channels[:2], nil, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 2 || named[0].ChannelID != channels[0] || named[1].ChannelID != channels[1] {
		t.Fatalf("filtering by channel returned %+v, want %v", named, channels[:2])
	}
}

// messageTimestampsAreUniquePerConversation is the identity contract behind a
// message's public timestamp.
//
// A message's ts is `<seconds>.<six digits>`, and every write-addressing call —
// chat.update, chat.delete, reactions.add, thread-root resolution — resolves a
// message through it. Two messages on one microsecond therefore share one
// identifier and the second becomes permanently unaddressable, which is what a
// migration that truncated the stored instant in place produced. Both profiles
// refuse the collision, and both name the same sentinel, so the one producer's
// remedy is the same everywhere.
func messageTimestampsAreUniquePerConversation(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	instant := time.Date(2024, 6, 1, 9, 0, 0, 123456000, time.UTC)
	first := f.message(t, ctx, "unique-first", instant)

	// 123 nanoseconds later is the SAME microsecond, so it is the same
	// identifier.
	contested := domain.Message{ID: domain.MessageID("unique-second-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID, AuthorID: f.userID, Text: "contested", CreatedAt: instant.Add(123 * time.Nanosecond)}
	if err := f.repository.CreateMessage(ctx, contested, f.event("event-contested", "message.created", string(contested.ID)), ""); !errors.Is(err, store.ErrMessageTimestampTaken) {
		t.Fatalf("second message on one microsecond err=%v, want store.ErrMessageTimestampTaken", err)
	}
	// The remedy is the next microsecond, and it works.
	contested.CreatedAt = instant.Add(time.Microsecond)
	if err := f.repository.CreateMessage(ctx, contested, f.event("event-contested", "message.created", string(contested.ID)), ""); err != nil {
		t.Fatalf("the next microsecond was also refused: %v", err)
	}
	// Both messages are individually addressable by their own timestamps.
	for _, want := range []domain.Message{first, contested} {
		found, err := f.repository.GetMessageByCreatedAt(ctx, f.channelID, want.CreatedAt)
		if err != nil {
			t.Fatalf("message %s is not addressable by its own instant: %v", want.ID, err)
		}
		if found.ID != want.ID {
			t.Fatalf("instant %s resolves to %s, want %s", want.CreatedAt, found.ID, want.ID)
		}
	}
}

// conversationCreatorIsAMember pins a membership rule that governs writes and
// that both repositories were changed to implement independently, with no shared
// test. Joining only private conversations left the creator of a public channel
// unable to act on the channel they had just made.
func conversationCreatorIsAMember(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	for _, testCase := range []struct {
		name    string
		private bool
	}{{"public", false}, {"private", true}} {
		id := domain.ConversationID("C-creator-" + testCase.name + "-" + f.suffix)
		conversation := domain.Conversation{ID: id, WorkspaceID: f.workspaceID, Name: "creator-" + testCase.name, IsPrivate: testCase.private}
		if err := f.repository.CreateConversation(ctx, conversation, f.userID, f.event("event-creator-"+testCase.name, "conversation.created", string(id))); err != nil {
			t.Fatalf("create %s conversation: %v", testCase.name, err)
		}
		member, err := f.repository.IsConversationMember(ctx, id, f.userID)
		if err != nil || !member {
			t.Fatalf("the creator of a %s conversation is not a member (err=%v)", testCase.name, err)
		}
		members, err := f.repository.ListConversationMembers(ctx, id, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, value := range members.Users {
			if value.ID == f.userID {
				found = true
			}
		}
		if !found {
			t.Fatalf("members of the %s conversation = %+v, want the creator listed", testCase.name, members.Users)
		}
	}
}

// authMethodDefaultsToEnabled pins a security-relevant default that has been
// inverted twice in two releases and had no contract. The table records an
// administrator's decision to turn a provider OFF; a provider with no row is not
// "disabled", it is "not overridden".
func authMethodDefaultsToEnabled(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	method, err := f.repository.GetAuthMethod(ctx, f.workspaceID, "a-provider-nobody-configured")
	if err != nil {
		t.Fatalf("an unwritten provider reported err=%v, want no error", err)
	}
	if !method.Enabled {
		t.Fatalf("an unwritten provider reported %+v, want Enabled: true", method)
	}
}

// Revocation reaches the repositories directly across the auth seam, so the
// tokens_revoked announcement is minted inside the revoking mutation itself.
// Every profile must agree: one announcement per application token, routed to
// the token's app, none for a personal token, and none again on re-revocation.
func revokingAnAppTokenAnnouncesTokensRevokedOnce(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	appToken := "xoxb-revoked-" + f.suffix
	if err := f.repository.SeedToken(ctx, appToken, domain.TokenRecord{WorkspaceID: f.workspaceID, UserID: f.userID, AppID: "A-revoke", TokenType: "bot", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	personal := "xoxp-personal-" + f.suffix
	if err := f.repository.SeedToken(ctx, personal, domain.TokenRecord{WorkspaceID: f.workspaceID, UserID: f.userID, TokenType: "user", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.RevokeToken(ctx, appToken); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.RevokeToken(ctx, personal); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.RevokeToken(ctx, appToken); err != nil {
		t.Fatal(err)
	}
	listed, err := f.repository.ListEventsAfter(ctx, f.workspaceID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	announcements := make([]events.Record, 0, 1)
	for _, record := range listed {
		if record.Event.Topic == "app.tokens_revoked" {
			announcements = append(announcements, record)
		}
	}
	if len(announcements) != 1 {
		t.Fatalf("tokens_revoked announcements = %d, want exactly 1 (app token once; personal and repeat revocations announce nothing)", len(announcements))
	}
	bodies, err := events.SlackEventBodies(announcements[0], "A-revoke")
	if err != nil || len(bodies) != 1 {
		t.Fatalf("owning app bodies=%d err=%v", len(bodies), err)
	}
	if !strings.Contains(string(bodies[0]), `"type":"tokens_revoked"`) || !strings.Contains(string(bodies[0]), `"bot":["`+string(f.userID)+`"]`) {
		t.Fatalf("tokens_revoked body=%s", bodies[0])
	}
	other, err := events.SlackEventBodies(announcements[0], "A-other")
	if err != nil || len(other) != 0 {
		t.Fatalf("another app received the revocation: %q err=%v", other, err)
	}
}

// The app.uninstalled record commits with the uninstall and stays readable
// by the app it announces, even though the read scope for app events is
// otherwise the app's ENABLED installations. Every profile must carve the
// topic out identically, or an app on one storage profile learns why its
// tokens died and an app on another never does.
func uninstallAnnouncementOutlivesTheInstallation(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	appID := domain.AppID("A-gone-" + f.suffix)
	now := time.Now().UTC()
	if err := f.repository.CreateApp(ctx, domain.App{
		ID: appID, DevelopmentWorkspaceID: f.workspaceID, OwnerID: f.userID, Name: "Doomed", ClientID: "client-" + f.suffix,
		SigningSecretHash: "signing", SigningSecretCiphertext: "cipher", VerificationTokenHash: "verify",
		VerificationTokenCiphertext: "cipher", ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: appID, Version: 1, CreatedBy: f.userID, CreatedAt: now,
		Manifest: `{"display_information":{"name":"Doomed"}}`,
	}, domain.OAuthClient{ID: "client-" + f.suffix, SecretHash: "secret", AppID: appID}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: appID, WorkspaceID: f.workspaceID, Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A live connection has claimed before, so its durable cursor exists —
	// the shape an open Socket Mode connection is in when the uninstall lands.
	// Anything the priming claim leased is acknowledged immediately, so no
	// lease can outlive this setup and stall the loop below.
	if record, _, _, claimed, err := f.repository.ClaimAppEvent(ctx, appID, "socket", "conn-1", time.Minute); err != nil {
		t.Fatal(err)
	} else if claimed {
		if err := f.repository.AckAppEvent(ctx, appID, "socket", "conn-1", record.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	announcementID, err := domain.NewEventID()
	if err != nil {
		t.Fatal(err)
	}
	announcement, err := events.New(announcementID, f.workspaceID, "", events.NewPayload("app.uninstalled",
		events.String("app_id", string(appID)),
		events.String("target_app_id", string(appID)),
	), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repository.UninstallApp(ctx, f.workspaceID, appID, announcement); err != nil {
		t.Fatal(err)
	}
	listed, err := f.repository.ListAppEventsAfter(ctx, appID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range listed {
		if record.Event.Topic == "app.uninstalled" {
			found = true
		} else {
			t.Fatalf("a disabled installation leaked topic %s to the app", record.Event.Topic)
		}
	}
	if !found {
		t.Fatal("the uninstall announcement is invisible to the app it announces")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, _, _, claimed, err := f.repository.ClaimAppEvent(ctx, appID, "socket", "conn-1", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if claimed && record.Event.Topic == "app.uninstalled" {
			if err := f.repository.AckAppEvent(ctx, appID, "socket", "conn-1", record.Sequence); err != nil {
				t.Fatal(err)
			}
			break
		}
		if claimed {
			if err := f.repository.AckAppEvent(ctx, appID, "socket", "conn-1", record.Sequence); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("the open connection never received the uninstall announcement")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Slack's channel_join, channel_topic and channel_name messages are the
// visible record of how a channel came to be the way it is. They are written
// by the same store call as the change they describe, so a crash cannot leave
// a renamed channel with no notice — and every profile must agree, including
// on the normalized empty attachment list, or the same notice reads
// differently depending on where it was stored.
func conversationNoticesCommitWithTheirChange(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	joiner := domain.UserID("U-joiner-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: joiner, WorkspaceID: f.workspaceID, Name: "joiner"}); err != nil {
		t.Fatal(err)
	}
	joinNotice := domain.Message{
		ID: domain.MessageID("M-join-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		AuthorID: joiner, Text: "<@" + string(joiner) + "> has joined the channel",
		Subtype: domain.MessageSubtypeChannelJoin, Attachments: "[]",
		CreatedAt: domain.MessageInstant(time.Now()),
	}
	if err := f.repository.AddConversationMember(ctx, f.channelID, joiner, f.event("notice-join", "conversation.member_added", string(f.channelID)), joinNotice); err != nil {
		t.Fatal(err)
	}
	renameNotice := domain.Message{
		ID: domain.MessageID("M-rename-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		AuthorID: f.userID, Text: "<@" + string(f.userID) + "> renamed the channel to renamed-" + f.suffix,
		Subtype: domain.MessageSubtypeChannelName, Attachments: "[]",
		CreatedAt: domain.MessageInstant(time.Now().Add(time.Millisecond)),
	}
	if _, err := f.repository.RenameConversation(ctx, f.channelID, "renamed-"+f.suffix, f.event("notice-rename", "conversation.renamed", string(f.channelID)), renameNotice); err != nil {
		t.Fatal(err)
	}
	page, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := map[domain.MessageSubtype]domain.Message{}
	for _, message := range page.Messages {
		if message.Subtype != "" {
			found[message.Subtype] = message
		}
	}
	for _, want := range []domain.MessageSubtype{domain.MessageSubtypeChannelJoin, domain.MessageSubtypeChannelName} {
		message, ok := found[want]
		if !ok {
			t.Fatalf("the %s notice did not commit with its change: %+v", want, page.Messages)
		}
		if message.Attachments != "[]" {
			t.Fatalf("%s notice attachments=%q, want the normalized empty list every profile writes", want, message.Attachments)
		}
		if message.Text == "" {
			t.Fatalf("%s notice carries no text", want)
		}
	}
}

// Deleting a file must delete it everywhere it was shared. The message that
// shared it survives — Slack keeps the post and marks the attachment gone —
// so every profile has to report the attachment's current state when it hands
// out the message, not the state it had when it was posted. A profile that
// snapshots the file onto the message keeps offering a download link and the
// original title for bytes that are no longer there, and keeps offering the
// uploader a delete control for a file already deleted.
func aDeletedFileIsDeletedOnEveryMessageThatCarriesIt(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	file := domain.File{
		ID: domain.FileID("F-shared-" + f.suffix), WorkspaceID: f.workspaceID, Uploader: f.userID,
		Name: "budget.txt", Title: "Budget", MIMEType: "text/plain",
		BlobKey: string(f.workspaceID) + "/budget", Size: 9, CreatedAt: time.Unix(1_700_000_500, 0).UTC(),
	}
	if err := f.repository.CreateFile(ctx, file, f.event("shared-file", "file.created", string(file.ID))); err != nil {
		t.Fatal(err)
	}
	share := domain.Message{
		ID: domain.MessageID("M-file-share-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		AuthorID: f.userID, Text: "here is the budget", Attachments: "[]",
		CreatedAt: domain.MessageInstant(time.Unix(1_700_000_501, 0).UTC()),
	}
	if err := f.repository.CreateFileShareMessage(ctx, []domain.FileID{file.ID}, share,
		[]events.Event{f.event("shared-file-message", "message.created", string(share.ID))}); err != nil {
		t.Fatal(err)
	}
	attached := func(stage string) domain.File {
		t.Helper()
		page, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		for _, message := range page.Messages {
			if message.ID != share.ID {
				continue
			}
			if len(message.Files) != 1 {
				t.Fatalf("%s: the share message carries %d files, want 1", stage, len(message.Files))
			}
			return message.Files[0]
		}
		t.Fatalf("%s: the share message is gone from the channel", stage)
		return domain.File{}
	}
	before := attached("before deletion")
	if before.Deleted || before.Title != file.Title || !slices.Contains(before.SharedChannels, f.channelID) {
		t.Fatalf("before deletion the attachment reads %+v, want the live file shared into the channel", before)
	}

	if err := f.repository.DeleteFile(ctx, file.ID, f.event("shared-file-delete", "file.deleted", string(file.ID))); err != nil {
		t.Fatal(err)
	}
	after := attached("after deletion")
	if !after.Deleted {
		t.Fatalf("after deletion the attachment still reads as live: %+v", after)
	}
	if _, err := f.repository.GetFile(ctx, file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the deleted file is still readable: %v", err)
	}
}

// A file is visible to whoever can read a conversation it is shared into, and
// the share is a row of its own. Deleting the message that shared it therefore
// has to retract the share, or the file stays readable in that channel — and
// keeps appearing in files.list — with nothing left on screen that put it
// there. The share survives while another live message still carries the file,
// and the retraction is journalled as file.unshared exactly when it happens.
func deletingTheLastCarrierRetractsTheFileShare(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	file := domain.File{
		ID: domain.FileID("F-carried-" + f.suffix), WorkspaceID: f.workspaceID, Uploader: f.userID,
		Name: "plan.txt", Title: "Plan", MIMEType: "text/plain",
		BlobKey: string(f.workspaceID) + "/plan", Size: 4, CreatedAt: time.Unix(1_700_000_600, 0).UTC(),
	}
	if err := f.repository.CreateFile(ctx, file, f.event("carried-file", "file.created", string(file.ID))); err != nil {
		t.Fatal(err)
	}
	carrier := func(name string, at time.Time) domain.Message {
		t.Helper()
		message := domain.Message{
			ID: domain.MessageID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
			AuthorID: f.userID, Text: name, Attachments: "[]", CreatedAt: domain.MessageInstant(at),
		}
		if err := f.repository.CreateFileShareMessage(ctx, []domain.FileID{file.ID}, message,
			[]events.Event{f.event(name, "message.created", string(message.ID))}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return message
	}
	first := carrier("carrier-one", time.Unix(1_700_000_601, 0).UTC())
	second := carrier("carrier-two", time.Unix(1_700_000_602, 0).UTC())

	sharedChannels := func(stage string) []domain.ConversationID {
		t.Helper()
		stored, err := f.repository.GetFile(ctx, file.ID)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		return stored.SharedChannels
	}
	unsharedEvents := func(stage string) int {
		t.Helper()
		records, err := f.repository.ListEventsAfter(ctx, f.workspaceID, 0, 200)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		count := 0
		for _, record := range records {
			if record.Event.Topic == "file.unshared" {
				count++
			}
		}
		return count
	}

	deleteCarrier := func(message domain.Message, name string) {
		t.Helper()
		message.Deleted = true
		unshareEvent := f.event(name+"-unshare", "file.unshared", string(file.ID))
		if err := f.repository.DeleteMessage(ctx, message, f.event(name+"-delete", "message.deleted", string(message.ID)),
			[]store.FileUnshare{{FileID: file.ID, Event: unshareEvent}}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	deleteCarrier(first, "first")
	if channels := sharedChannels("after the first delete"); !slices.Contains(channels, f.channelID) {
		t.Fatalf("the share ended while another message still carried the file: %v", channels)
	}
	if count := unsharedEvents("after the first delete"); count != 0 {
		t.Fatalf("file.unshared was journalled %d times while the share was still open", count)
	}

	deleteCarrier(second, "second")
	if channels := sharedChannels("after the last delete"); slices.Contains(channels, f.channelID) {
		t.Fatalf("the file is still shared into the channel with no message carrying it: %v", channels)
	}
	if count := unsharedEvents("after the last delete"); count != 1 {
		t.Fatalf("file.unshared was journalled %d times for one retracted share", count)
	}
}

// Accepting an invitation creates a member, a membership at the recorded guest
// tier and every channel join the invitation named. All of it commits together
// or none of it does: a partial acceptance would leave a member who cannot see
// the channels they were invited to, or consume an invitation with no member
// behind it, and either state is unrecoverable without an administrator.
func acceptingAnInvitationCommitsTheWholeMembership(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	second := domain.ConversationID("C-invited-" + f.suffix)
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: second, WorkspaceID: f.workspaceID, Name: "invited"}); err != nil {
		t.Fatal(err)
	}
	invitation := domain.InviteRequest{
		ID: domain.InviteRequestID("IR-" + f.suffix), WorkspaceID: f.workspaceID,
		Email: "invited-" + f.suffix + "@example.com", RequestedBy: f.userID,
		ChannelIDs: []domain.ConversationID{f.channelID, second}, RealName: "Invited",
		Restricted: true, Status: domain.InviteRequestPending,
		CreatedAt: time.Unix(1_700_000_700, 0).UTC(), ExpiresAt: time.Unix(1_800_000_000, 0).UTC(),
	}
	if err := f.repository.CreateInviteRequest(ctx, invitation, f.event("invite-create", "invite_request.created", string(invitation.ID))); err != nil {
		t.Fatal(err)
	}

	newcomer := domain.UserID("U-invited-" + f.suffix)
	acceptance := domain.InviteRequestAcceptance{
		WorkspaceID: f.workspaceID, RequestID: invitation.ID,
		User:       domain.User{ID: newcomer, WorkspaceID: f.workspaceID, Email: invitation.Email, Name: "Invited", RealName: "Invited"},
		Membership: domain.WorkspaceMembership{WorkspaceID: f.workspaceID, UserID: newcomer, Role: domain.WorkspaceRoleMember, Active: true, Restricted: true},
		Channels:   invitation.ChannelIDs,
		AcceptedAt: time.Unix(1_700_000_800, 0).UTC(),
	}
	accept := func() error {
		return f.repository.AcceptInviteRequest(ctx, acceptance, []events.Event{f.event("invite-accept", "invite_request.accepted", string(invitation.ID))})
	}

	// Nothing is acceptable until it is approved, and nothing may be written
	// on the way to finding that out.
	if err := accept(); err == nil {
		t.Fatal("an unapproved invitation was accepted")
	}
	if _, err := f.repository.FindUserByEmail(ctx, f.workspaceID, invitation.Email); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused acceptance created a user anyway: %v", err)
	}

	if err := f.repository.SetInviteRequestStatus(ctx, f.workspaceID, invitation.ID, domain.InviteRequestPending, domain.InviteRequestApproved, time.Unix(1_700_000_750, 0).UTC(), f.event("invite-approve", "invite_request.approved", string(invitation.ID))); err != nil {
		t.Fatal(err)
	}
	if err := accept(); err != nil {
		t.Fatal(err)
	}

	membership, err := f.repository.GetWorkspaceMembership(ctx, f.workspaceID, newcomer)
	if err != nil || !membership.Active || !membership.Restricted || membership.UltraRestricted {
		t.Fatalf("membership=%+v err=%v, want an active multi-channel guest", membership, err)
	}
	for _, channelID := range invitation.ChannelIDs {
		members, listErr := f.repository.ListConversationMembers(ctx, channelID, domain.PageRequest{Limit: 50})
		if listErr != nil {
			t.Fatal(listErr)
		}
		joined := false
		for _, member := range members.Users {
			if member.ID == newcomer {
				joined = true
			}
		}
		if !joined {
			t.Fatalf("the accepted member did not join %s", channelID)
		}
	}
	stored, err := f.repository.GetInviteRequest(ctx, f.workspaceID, invitation.ID)
	if err != nil || stored.Status != domain.InviteRequestAccepted || stored.AcceptedBy != newcomer || stored.AcceptedAt.IsZero() {
		t.Fatalf("invitation=%+v err=%v", stored, err)
	}
	// Single use.
	if err := accept(); err == nil {
		t.Fatal("an invitation was accepted twice")
	}
}

// The analytics dashboard is counted from the durable rows on every load, so
// two storage profiles that count differently show an administrator two
// different workspaces. The window is a parameter for the same reason: a store
// that chose its own would make the page and any export from the same call
// describe different spans.
func workspaceAnalyticsCountTheSameOnEveryProfile(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	private := domain.ConversationID("C-private-" + f.suffix)
	archived := domain.ConversationID("C-archived-" + f.suffix)
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: private, WorkspaceID: f.workspaceID, Name: "private", IsPrivate: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: archived, WorkspaceID: f.workspaceID, Name: "archived", Archived: true}); err != nil {
		t.Fatal(err)
	}
	guest := domain.UserID("U-guest-" + f.suffix)
	if err := f.repository.CreateUser(ctx, domain.User{ID: guest, WorkspaceID: f.workspaceID, Email: "guest-" + f.suffix + "@example.com", Name: "guest", RealName: "guest"},
		domain.WorkspaceMembership{WorkspaceID: f.workspaceID, UserID: guest, Role: domain.WorkspaceRoleMember, Active: true, Restricted: true},
		f.event("guest-created", "user.created", string(guest))); err != nil {
		t.Fatal(err)
	}

	window := time.Unix(1_700_000_000, 0).UTC()
	// Two inside the window and one before it, so a store that ignores the
	// window and one that applies it cannot both pass.
	f.message(t, ctx, "analytics-old", window.Add(-48*time.Hour))
	f.message(t, ctx, "analytics-one", window.Add(time.Hour))
	f.message(t, ctx, "analytics-two", window.Add(2*time.Hour))

	analytics, err := f.repository.WorkspaceAnalytics(ctx, f.workspaceID, window, 5)
	if err != nil {
		t.Fatal(err)
	}
	if analytics.Members != 2 || analytics.ActiveMembers != 2 || analytics.Guests != 1 {
		t.Fatalf("people=%+v, want two members of whom one is a guest", analytics)
	}
	if analytics.PublicChannels != 1 || analytics.PrivateChannels != 1 || analytics.ArchivedChannels != 1 {
		t.Fatalf("channels=%+v", analytics)
	}
	if analytics.Messages != 3 || analytics.RecentMessages != 2 {
		t.Fatalf("messages=%d recent=%d, want three of which two are in the window", analytics.Messages, analytics.RecentMessages)
	}
	if len(analytics.BusiestChannels) != 1 || analytics.BusiestChannels[0].ConversationID != f.channelID || analytics.BusiestChannels[0].Messages != 2 {
		t.Fatalf("busiest=%+v", analytics.BusiestChannels)
	}
	// The bound is honoured, and a zero bound asks for no list rather than an
	// unbounded one.
	none, err := f.repository.WorkspaceAnalytics(ctx, f.workspaceID, window, 0)
	if err != nil || len(none.BusiestChannels) != 0 {
		t.Fatalf("bounded=%+v err=%v", none.BusiestChannels, err)
	}
}

// A conversation has at most one running huddle, and everyone who presses
// start joins that one. The convergence is the store's job: a read-then-create
// gives two concurrent starters one huddle each, and the second is the one
// nobody else is in. Every profile must converge the same way, and must end the
// huddle when the last person leaves.
func huddlesConvergeAndEndWithTheirLastParticipant(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	second := domain.UserID("U-huddler-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: second, WorkspaceID: f.workspaceID, Name: "huddler"}); err != nil {
		t.Fatal(err)
	}
	start := func(name string, actor domain.UserID) (domain.Call, bool) {
		t.Helper()
		call := domain.Call{
			ID: domain.CallID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Kind: domain.CallKindHuddle,
			ConversationID: f.channelID, CreatedBy: actor, StartedAt: time.Unix(1_700_000_900, 0).UTC(),
		}
		value, created, err := f.repository.StartHuddle(ctx, call,
			f.event(name+"-started", "huddle.started", string(call.ID)),
			f.event(name+"-joined", "huddle.joined", string(call.ID)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return value, created
	}

	first, created := start("huddle-a", f.userID)
	if !created {
		t.Fatal("the first start did not create the huddle")
	}
	joinedExisting, createdAgain := start("huddle-b", second)
	if createdAgain {
		t.Fatal("a second huddle was started in a conversation that already had one")
	}
	if joinedExisting.ID != first.ID {
		t.Fatalf("the second starter joined %s, want the running huddle %s", joinedExisting.ID, first.ID)
	}
	if len(joinedExisting.Participants) != 2 {
		t.Fatalf("participants=%v, want both starters", joinedExisting.Participants)
	}

	active, err := f.repository.ActiveHuddle(ctx, f.workspaceID, f.channelID)
	if err != nil || active.ID != first.ID {
		t.Fatalf("active=%+v err=%v", active, err)
	}

	after, err := f.repository.LeaveCall(ctx, f.workspaceID, first.ID, second,
		f.event("huddle-left-one", "huddle.left", string(first.ID)),
		f.event("huddle-ended-one", "huddle.ended", string(first.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if !after.Active() || len(after.Participants) != 1 {
		t.Fatalf("after one left the huddle reads %+v, want it still running with one person", after)
	}

	ended, err := f.repository.LeaveCall(ctx, f.workspaceID, first.ID, f.userID,
		f.event("huddle-left-two", "huddle.left", string(first.ID)),
		f.event("huddle-ended-two", "huddle.ended", string(first.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if ended.Active() || len(ended.Participants) != 0 {
		t.Fatalf("the huddle outlived its last participant: %+v", ended)
	}
	if _, err := f.repository.ActiveHuddle(ctx, f.workspaceID, f.channelID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an ended huddle is still the conversation's active one: %v", err)
	}
	// The conversation is free for a new huddle, which is the point of ending
	// it rather than leaving it running and empty.
	if _, createdAfter := start("huddle-c", f.userID); !createdAfter {
		t.Fatal("a new huddle could not be started after the previous one ended")
	}
}

// A user row belongs to one workspace, so the same person in two workspaces is
// two rows sharing an address. The switcher resolves by that address, and both
// profiles must agree on which workspaces it names, in what order, and with
// which local identity — a switcher that listed a workspace on one profile and
// not the other would offer a destination the switch then refuses.
func workspacesForAnAddressAgreeOnEveryProfile(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	address := "shared-" + f.suffix + "@example.com"
	local := domain.UserID("U-local-" + f.suffix)
	if err := f.repository.CreateUser(ctx,
		domain.User{ID: local, WorkspaceID: f.workspaceID, Email: address, Name: "shared", RealName: "Shared"},
		domain.WorkspaceMembership{WorkspaceID: f.workspaceID, UserID: local, Role: domain.WorkspaceRoleAdmin, Active: true},
		f.event("shared-local", "user.created", string(local))); err != nil {
		t.Fatal(err)
	}
	second := domain.WorkspaceID("T-second-" + f.suffix)
	if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: second, Name: "Second"}); err != nil {
		t.Fatal(err)
	}
	elsewhere := domain.UserID("U-elsewhere-" + f.suffix)
	if err := f.repository.CreateUser(ctx,
		domain.User{ID: elsewhere, WorkspaceID: second, Email: address, Name: "shared", RealName: "Shared"},
		domain.WorkspaceMembership{WorkspaceID: second, UserID: elsewhere, Role: domain.WorkspaceRoleMember, Active: true},
		f.event("shared-elsewhere", "user.created", string(elsewhere))); err != nil {
		t.Fatal(err)
	}
	// A third workspace the address was removed from must not be offered.
	third := domain.WorkspaceID("T-third-" + f.suffix)
	if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: third, Name: "Third"}); err != nil {
		t.Fatal(err)
	}
	inactive := domain.UserID("U-inactive-" + f.suffix)
	if err := f.repository.CreateUser(ctx,
		domain.User{ID: inactive, WorkspaceID: third, Email: address, Name: "shared", RealName: "Shared"},
		domain.WorkspaceMembership{WorkspaceID: third, UserID: inactive, Role: domain.WorkspaceRoleMember, Active: true},
		f.event("shared-inactive", "user.created", string(inactive))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SetUserDeleted(ctx, third, inactive, true, f.event("shared-removed", "user.removed", string(inactive))); err != nil {
		t.Fatal(err)
	}

	summaries, err := f.repository.ListWorkspacesForEmail(ctx, strings.ToUpper(address))
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("workspaces=%+v, want the two the address is an active member of", summaries)
	}
	// Ordered by workspace identifier, so two calls and two profiles agree.
	if summaries[0].Workspace.ID >= summaries[1].Workspace.ID {
		t.Fatalf("workspaces are not ordered: %+v", summaries)
	}
	byWorkspace := map[domain.WorkspaceID]domain.WorkspaceMembershipSummary{}
	for _, summary := range summaries {
		byWorkspace[summary.Workspace.ID] = summary
	}
	if got := byWorkspace[f.workspaceID]; got.UserID != local || got.Role != domain.WorkspaceRoleAdmin {
		t.Fatalf("local membership=%+v", got)
	}
	if got := byWorkspace[second]; got.UserID != elsewhere || got.Role != domain.WorkspaceRoleMember {
		t.Fatalf("second membership=%+v, want the identity and role held there", got)
	}
	if _, offered := byWorkspace[third]; offered {
		t.Fatal("a workspace the address was removed from was offered as a destination")
	}
}

// CONNECT-01 forbids promising a place in a Slack Connect channel from a stale
// count, so the capacity is claimed inside the transaction that appends the
// organization. Both profiles must refuse the same acceptance, and must leave
// the refused invitation acceptable: being told there is no room must not
// consume the invitation.
func slackConnectCapacityIsClaimedTransactionally(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	// The host counts towards the capacity, so filling it takes one fewer.
	teams := make([]domain.WorkspaceID, 0, domain.SlackConnectCapacity)
	for index := 0; index < domain.SlackConnectCapacity-1; index++ {
		id := domain.WorkspaceID(fmt.Sprintf("T-seat-%d-%s", index, f.suffix))
		if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: id, Name: "seat"}); err != nil {
			t.Fatal(err)
		}
		teams = append(teams, id)
	}
	if err := f.repository.SetConversationTeams(ctx, f.workspaceID, f.channelID, teams, false, f.event("seats", "conversation.connected", string(f.channelID))); err != nil {
		t.Fatal(err)
	}

	late := domain.WorkspaceID("T-late-" + f.suffix)
	if err := f.repository.SeedWorkspace(ctx, domain.Workspace{ID: late, Name: "late"}); err != nil {
		t.Fatal(err)
	}
	invite := domain.SharedInvite{
		ID: domain.SharedInviteID("SI-" + f.suffix), WorkspaceID: f.workspaceID, ConversationID: f.channelID,
		TargetWorkspaceID: late, InvitedBy: f.userID, Status: domain.SharedInvitePending,
		CreatedAt: time.Unix(1_700_001_000, 0).UTC(), ExpiresAt: time.Unix(1_800_000_000, 0).UTC(),
	}
	if err := f.repository.CreateSharedInvite(ctx, invite, f.event("connect-created", "shared_invite.created", string(invite.ID))); err != nil {
		t.Fatal(err)
	}
	// A second outstanding invitation for the same organization is refused:
	// two would let two acceptances each claim the last place.
	duplicate := invite
	duplicate.ID = domain.SharedInviteID("SI-dup-" + f.suffix)
	if err := f.repository.CreateSharedInvite(ctx, duplicate, f.event("connect-duplicate", "shared_invite.created", string(duplicate.ID))); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("a second outstanding invitation err=%v, want it refused", err)
	}
	if err := f.repository.SetSharedInviteStatus(ctx, invite.ID, domain.SharedInvitePending, domain.SharedInviteApproved, time.Unix(1_700_001_100, 0).UTC(), f.event("connect-approved", "shared_invite.approved", string(invite.ID))); err != nil {
		t.Fatal(err)
	}

	if _, err := f.repository.AcceptSharedInvite(ctx, invite.ID, time.Unix(1_700_001_200, 0).UTC(),
		[]events.Event{f.event("connect-accepted", "shared_invite.accepted", string(invite.ID))}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("acceptance into a full channel err=%v, want a conflict", err)
	}
	// Nothing was consumed by the refusal.
	stored, err := f.repository.GetSharedInvite(ctx, invite.ID)
	if err != nil || stored.Status != domain.SharedInviteApproved {
		t.Fatalf("invitation=%+v err=%v, want it still approved", stored, err)
	}

	// A transition the state machine forbids is refused on both profiles, so
	// no caller can move an invitation somewhere it may not go.
	if err := f.repository.SetSharedInviteStatus(ctx, invite.ID, domain.SharedInviteApproved, domain.SharedInvitePending, time.Unix(1_700_001_300, 0).UTC(), f.event("connect-back", "shared_invite.created", string(invite.ID))); err == nil {
		t.Fatal("an approved invitation was moved back to pending")
	}
}

// Retention deletion is permanent and irreversible, so the two profiles have
// to agree exactly on which rows disappear. This pins the whole cascade: what
// goes, what survives, and the thread rule that decides between them.
//
// The thread rule is the subtle part. A thread is retained until its newest
// reply expires — deleting a root while its replies survive would leave replies
// with no parent to render under — and Slack documents nothing about it, so
// both profiles implement it from this contract rather than from each other.
func retentionDeletesTheSameContentOnEveryProfile(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1_700_000_000, 0).UTC()
	horizon := base.Add(100 * 24 * time.Hour)
	old := func(name string, at time.Time) domain.Message { return f.message(t, ctx, name, at) }

	// Expired and standalone: goes.
	lone := old("retention-lone", base)
	// Expired root, but a reply is still inside the horizon: the whole thread
	// stays, however old the root is.
	activeRoot := old("retention-active-root", base.Add(time.Minute))
	activeRootTS := domain.NewMessageTimestamp(activeRoot.CreatedAt)
	f.reply(t, ctx, "retention-active-reply", activeRootTS, horizon.Add(24*time.Hour))
	// Expired root whose only reply is also expired: both go.
	deadRoot := old("retention-dead-root", base.Add(2*time.Minute))
	deadRootTS := domain.NewMessageTimestamp(deadRoot.CreatedAt)
	deadReply := f.reply(t, ctx, "retention-dead-reply", deadRootTS, base.Add(3*time.Minute))
	// Inside the horizon: stays.
	recent := old("retention-recent", horizon.Add(48*time.Hour))

	// The lone message carries everything that references a message, so the
	// cascade is exercised rather than assumed.
	if err := f.repository.AddReaction(ctx, domain.Reaction{Message: lone.ID, Name: "wave", UserID: f.userID, CreatedAt: base}, f.event("retention-reaction", "reaction.added", string(lone.ID))); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.AddPin(ctx, domain.Pin{Message: lone.ID, UserID: f.userID, CreatedAt: base}, f.event("retention-pin", "pin.added", string(lone.ID))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repository.CreateSavedItem(ctx, domain.SavedItem{
		ID: domain.SavedItemID("SI-retention-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID,
		MessageID: lone.ID, Conversation: f.channelID, State: domain.SavedItemInProgress,
		CreatedAt: base, UpdatedAt: base,
	}, f.event("retention-saved", "saved_item.created", string(lone.ID))); err != nil {
		t.Fatal(err)
	}

	swept, err := f.repository.SweepRetention(ctx, domain.RetentionSweepRequest{
		WorkspaceID: f.workspaceID, ConversationID: f.channelID,
		MessageHorizon: horizon, SweptAt: horizon, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if swept.Messages != 3 || !swept.Complete {
		t.Fatalf("sweep=%+v, want the lone message and the dead thread's two, completely", swept)
	}

	page, err := f.repository.ListMessages(ctx, f.channelID, domain.PageRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	survived := map[domain.MessageID]struct{}{}
	for _, message := range page.Messages {
		survived[message.ID] = struct{}{}
	}
	for _, id := range []domain.MessageID{activeRoot.ID, recent.ID} {
		if _, ok := survived[id]; !ok {
			t.Fatalf("%s was deleted but its thread is still active or it is inside the horizon", id)
		}
	}
	for _, id := range []domain.MessageID{lone.ID, deadRoot.ID, deadReply.ID} {
		if _, ok := survived[id]; ok {
			t.Fatalf("%s is past the horizon with no live reply and should be gone", id)
		}
	}

	// Permanent, not tombstoned: GetMessage still answers for a soft-deleted
	// message, so this is the assertion that separates the two.
	if _, err := f.repository.GetMessage(ctx, lone.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a swept message is still addressable: %v", err)
	}
	// And everything that pointed at it is gone too.
	if saved, err := f.repository.ListSavedItems(ctx, f.workspaceID, f.userID, domain.SavedItemInProgress, domain.PageRequest{Limit: 10}); err != nil || len(saved.Items) != 0 {
		t.Fatalf("saved items=%+v err=%v, want the saved reference swept with its message", saved.Items, err)
	}

	// A second sweep finds nothing: the operation is idempotent, which is what
	// makes the compare-and-set claim safe to lose.
	again, err := f.repository.SweepRetention(ctx, domain.RetentionSweepRequest{
		WorkspaceID: f.workspaceID, ConversationID: f.channelID,
		MessageHorizon: horizon, SweptAt: horizon, Limit: 50,
	})
	if err != nil || again.Messages != 0 {
		t.Fatalf("second sweep=%+v err=%v, want nothing left to do", again, err)
	}
}

// The sweep is claimed by advancing a watermark, so two workers racing over one
// workspace each get a disjoint set of conversations and neither does the
// other's work.
func retentionSweepsAreClaimedExactlyOnce(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	for index := 0; index < 4; index++ {
		id := domain.ConversationID(fmt.Sprintf("C-sweep-%d-%s", index, f.suffix))
		if err := f.repository.SeedConversation(ctx, domain.Conversation{ID: id, WorkspaceID: f.workspaceID, Name: fmt.Sprintf("sweep-%d-%s", index, f.suffix)}); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Unix(1_700_000_000, 0).UTC()
	sweptAt := before.Add(time.Hour)

	first, err := f.repository.ClaimRetentionSweep(ctx, f.workspaceID, before, sweptAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.repository.ClaimRetentionSweep(ctx, f.workspaceID, before, sweptAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("the first claim took nothing")
	}
	if len(second) != 0 {
		t.Fatalf("a second claim over the same window took %v, want nothing", second)
	}
	// Once the interval has passed they are claimable again — that is the daily
	// cadence, not a one-shot.
	third, err := f.repository.ClaimRetentionSweep(ctx, f.workspaceID, sweptAt.Add(time.Minute), sweptAt.Add(time.Hour), 10)
	if err != nil || len(third) != len(first) {
		t.Fatalf("third claim=%d err=%v, want the same conversations available again", len(third), err)
	}
}

// A workspace default governs a channel that has no override; an override wins
// while it exists; removing it returns the channel to the default.
func conversationRetentionOverridesTheWorkspaceDefault(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	if policy, err := f.repository.GetRetentionPolicy(ctx, f.workspaceID); err != nil || policy.MessageDays != 0 || policy.FileDays != 0 {
		t.Fatalf("policy=%+v err=%v, want an unconfigured workspace to keep everything", policy, err)
	}
	if err := f.repository.SetRetentionPolicy(ctx, f.workspaceID, domain.RetentionPolicy{MessageDays: 90, FileDays: 30}, f.event("retention-policy", "retention.policy_changed", string(f.workspaceID))); err != nil {
		t.Fatal(err)
	}
	policy, err := f.repository.GetRetentionPolicy(ctx, f.workspaceID)
	if err != nil || policy.MessageDays != 90 || policy.FileDays != 30 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}

	override, err := f.repository.GetConversationRetention(ctx, f.workspaceID, f.channelID)
	if err != nil || override.DurationDays != 0 {
		t.Fatalf("override=%+v err=%v, want none", override, err)
	}
	if got := override.Effective(policy); got != 90 {
		t.Fatalf("effective=%d, want the workspace default", got)
	}

	if err := f.repository.SetConversationRetention(ctx, f.workspaceID, f.channelID, 7, time.Unix(1_700_000_500, 0).UTC(), f.event("retention-override", "retention.override_changed", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	override, err = f.repository.GetConversationRetention(ctx, f.workspaceID, f.channelID)
	if err != nil || override.DurationDays != 7 {
		t.Fatalf("override=%+v err=%v", override, err)
	}
	if got := override.Effective(policy); got != 7 {
		t.Fatalf("effective=%d, want the override", got)
	}

	if err := f.repository.RemoveConversationRetention(ctx, f.workspaceID, f.channelID, f.event("retention-removed", "retention.override_removed", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	override, err = f.repository.GetConversationRetention(ctx, f.workspaceID, f.channelID)
	if err != nil || override.DurationDays != 0 {
		t.Fatalf("override after removal=%+v err=%v, want the workspace default to govern again", override, err)
	}
	// Removing an override a channel never had is not an error.
	if err := f.repository.RemoveConversationRetention(ctx, f.workspaceID, f.channelID, f.event("retention-removed-again", "retention.override_removed", string(f.channelID))); err != nil {
		t.Fatalf("removing an absent override: %v", err)
	}
	// A duration outside Slack's bounds is refused on both profiles.
	if err := f.repository.SetConversationRetention(ctx, f.workspaceID, f.channelID, domain.RetentionMaximumDays, time.Unix(1_700_000_600, 0).UTC(), f.event("retention-too-long", "retention.override_changed", string(f.channelID))); err == nil {
		t.Fatal("a duration at the maximum was accepted")
	}
}

// A timeline renders many parents at once, so thread summaries are read in
// one batched call rather than one read per parent. Every profile must return
// the same counts, the same participant list in the same order, and the same
// last-reply instant — a summary that differs by storage engine would render
// a different "N replies" line to the same person on two deployments.
func threadSummariesAreBatchedAndIdentical(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	second := domain.UserID("U-replier-" + f.suffix)
	if err := f.repository.SeedUser(ctx, domain.User{ID: second, WorkspaceID: f.workspaceID, Name: "replier"}); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_400, 0).UTC()
	rootA := f.message(t, ctx, "thread-root-a", base)
	rootB := f.message(t, ctx, "thread-root-b", base.Add(time.Second))
	rootATS := domain.NewMessageTimestamp(rootA.CreatedAt)
	rootBTS := domain.NewMessageTimestamp(rootB.CreatedAt)

	reply := func(name string, root domain.MessageTimestamp, author domain.UserID, at time.Time) {
		t.Helper()
		message := domain.Message{
			ID: domain.MessageID(name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
			AuthorID: author, Text: "reply " + name, ThreadTimestamp: root, Attachments: "[]",
			CreatedAt: domain.MessageInstant(at),
		}
		if err := f.repository.CreateMessage(ctx, message, f.event("evt-"+name, "message.created", string(message.ID)), ""); err != nil {
			t.Fatal(err)
		}
	}
	reply("reply-a1", rootATS, f.userID, base.Add(10*time.Second))
	reply("reply-a2", rootATS, second, base.Add(20*time.Second))
	reply("reply-a3", rootATS, second, base.Add(30*time.Second))
	reply("reply-b1", rootBTS, second, base.Add(40*time.Second))

	summaries, err := f.repository.ThreadSummaries(ctx, f.channelID, []domain.MessageTimestamp{rootATS, rootBTS})
	if err != nil {
		t.Fatal(err)
	}
	a := summaries[rootATS]
	if a.ReplyCount != 3 {
		t.Fatalf("root A replies=%d, want 3", a.ReplyCount)
	}
	if len(a.Participants) != 2 || a.Participants[0] > a.Participants[1] {
		t.Fatalf("root A participants=%v, want two distinct authors in sorted order", a.Participants)
	}
	if !a.LastReplyAt.Equal(domain.MessageInstant(base.Add(30 * time.Second))) {
		t.Fatalf("root A last reply=%s, want the newest reply's instant", a.LastReplyAt)
	}
	b := summaries[rootBTS]
	if b.ReplyCount != 1 || len(b.Participants) != 1 || b.Participants[0] != second {
		t.Fatalf("root B summary=%+v", b)
	}
	// A root with no replies is absent rather than zero-valued, and an empty
	// request reads nothing at all.
	unknown := domain.NewMessageTimestamp(base.Add(9 * time.Hour))
	more, err := f.repository.ThreadSummaries(ctx, f.channelID, []domain.MessageTimestamp{unknown})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := more[unknown]; present {
		t.Fatalf("a root with no replies reported a summary: %+v", more)
	}
	empty, err := f.repository.ThreadSummaries(ctx, f.channelID, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty request summaries=%+v err=%v", empty, err)
	}
}

// Moving the read cursor forward closes the Activity items it covers, and
// moving it BACKWARDS — which is how Slack's "mark unread from here" works —
// must reopen them. Only the forward half existed, so marking a conversation
// unread left the sidebar showing unread messages while Activity insisted
// there was nothing to see. MSG-02 requires the two to agree.
func activityFollowsTheReadCursorBothWays(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	// The Activity item is produced by the mention itself, which is how every
	// item in this system comes to exist. Both identifiers are Slack-shaped:
	// the mention parser only recognizes <@Uxxxx> forms, so the fixture's own
	// hyphenated identifiers cannot be mentioned.
	recipient := domain.UserID("U" + f.suffix)
	author := domain.UserID("UA" + f.suffix)
	for _, user := range []domain.UserID{recipient, author} {
		if err := f.repository.SeedUser(ctx, domain.User{ID: user, WorkspaceID: f.workspaceID, Name: string(user)}); err != nil {
			t.Fatal(err)
		}
		if err := f.repository.SeedConversationMember(ctx, f.channelID, user); err != nil {
			t.Fatal(err)
		}
	}
	at := domain.MessageInstant(time.Unix(1_700_000_800, 0).UTC())
	mention := domain.Message{
		ID: domain.MessageID("M-activity-cursor-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
		AuthorID: author, Text: "hello <@" + string(recipient) + ">", Attachments: "[]", CreatedAt: at,
	}
	if err := f.repository.CreateMessage(ctx, mention, f.event("activity-cursor", "message.created", string(mention.ID)), ""); err != nil {
		t.Fatal(err)
	}
	unread := func() int {
		t.Helper()
		page, err := f.repository.ListActivity(ctx, f.workspaceID, recipient, domain.ActivityQuery{UnreadOnly: true, Page: domain.PageRequest{Limit: 10}})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	if unread() != 1 {
		t.Fatalf("a fresh mention is not unread")
	}
	read := domain.ReadCursor{WorkspaceID: f.workspaceID, UserID: recipient, Conversation: f.channelID,
		LastRead: domain.NewMessageTimestamp(mention.CreatedAt), UpdatedAt: time.Now().UTC()}
	if err := f.repository.SetReadCursor(ctx, read, f.event("cursor-read", "conversation.read", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	if unread() != 0 {
		t.Fatalf("marking read left the mention unread")
	}
	// Mark unread from the message itself: the cursor moves to just before it.
	back := read
	back.LastRead = domain.NewMessageTimestamp(mention.CreatedAt.Add(-time.Microsecond))
	back.UpdatedAt = time.Now().UTC()
	if err := f.repository.SetReadCursor(ctx, back, f.event("cursor-unread", "conversation.read", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	if unread() != 1 {
		t.Fatalf("marking unread did not reopen the Activity item: the sidebar and Activity now disagree")
	}
}
