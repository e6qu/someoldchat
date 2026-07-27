package qualification

import (
	"context"
	"errors"
	"fmt"
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
	connection := domain.SocketModeConnection{ID: "socket-revive-" + f.suffix, AppID: appID, ExpiresAt: time.Now().UTC().Add(80 * time.Millisecond)}
	if err := f.repository.CreateSocketModeConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.ConsumeSocketModeConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(160 * time.Millisecond)
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
