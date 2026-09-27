package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// OwnMessageUnreadRepository is the part of store.Store the own-message
// unread check drives.
type OwnMessageUnreadRepository interface {
	CreateMessage(context.Context, domain.Message, events.Event, string, ...events.Event) error
	ListConversations(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationListRequest) (domain.ConversationPage, error)
	GetReadCursor(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) (domain.ReadCursor, error)
	SetReadCursor(context.Context, domain.ReadCursor, events.Event) error
	SetThreadFollowed(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID, domain.MessageTimestamp, bool, events.Event) error
	ListFollowedThreads(context.Context, domain.WorkspaceID, domain.UserID, domain.PageRequest) (domain.FollowedThreadPage, error)
}

// CheckOwnMessagesAreNeverUnread requires that a member's own messages never
// raise their own unread counts, whichever way they were posted: a top-level
// post reads the conversation up to it (and never moves the cursor
// backwards), and a thread reply, which does not move the cursor, is still not
// counted. The repository must hold workspace T1, members U1 and U2, and
// public conversation C1 both belong to.
func CheckOwnMessagesAreNeverUnread(t *testing.T, repository OwnMessageUnreadRepository) {
	t.Helper()
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	post := func(id domain.MessageID, author domain.UserID, offset time.Duration, thread domain.MessageTimestamp) domain.Message {
		t.Helper()
		created := base.Add(offset)
		message := domain.Message{ID: id, WorkspaceID: "T1", Conversation: "C1", AuthorID: author, Text: string(id), ThreadTimestamp: thread, CreatedAt: created}
		if err := repository.CreateMessage(ctx, message, events.Event{ID: domain.EventID("E" + string(id)), WorkspaceID: "T1", Topic: "message.created", Payload: string(id), CreatedAt: created}, ""); err != nil {
			t.Fatal(err)
		}
		return message
	}
	unread := func(user domain.UserID) int {
		t.Helper()
		page, err := repository.ListConversations(ctx, "T1", user, domain.ConversationListRequest{Limit: 10, MemberUserID: user})
		if err != nil {
			t.Fatal(err)
		}
		for _, conversation := range page.Conversations {
			if conversation.ID == "C1" {
				return conversation.UnreadCount
			}
		}
		t.Fatalf("C1 is not listed for %s", user)
		return 0
	}

	root := post("M1", "U2", 0, "")
	if got := unread("U1"); got != 1 {
		t.Fatalf("U1 unread after U2 posted = %d, want 1", got)
	}
	mine := post("M2", "U1", time.Second, "")
	if got := unread("U1"); got != 0 {
		t.Fatalf("U1 unread after posting at top level = %d, want 0: posting reads the conversation", got)
	}
	if got := unread("U2"); got != 1 {
		t.Fatalf("U2 unread = %d, want 1: another member's post is still unread to them", got)
	}
	cursor, err := repository.GetReadCursor(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if want := domain.NewMessageTimestamp(mine.CreatedAt); cursor.LastRead != want {
		t.Fatalf("U1 cursor = %s, want %s", cursor.LastRead, want)
	}

	rootTS := domain.NewMessageTimestamp(root.CreatedAt)
	if err := repository.SetThreadFollowed(ctx, "T1", "U1", "C1", rootTS, true, events.Event{ID: "Efollow", WorkspaceID: "T1", Topic: "thread.followed", Payload: "{}", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	post("R1", "U2", 2*time.Second, rootTS)
	post("R2", "U1", 3*time.Second, rootTS)
	if got := unread("U1"); got != 1 {
		t.Fatalf("U1 unread after a reply of their own = %d, want 1 (only U2's reply)", got)
	}
	threads, err := repository.ListFollowedThreads(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, thread := range threads.Threads {
		if thread.Root == rootTS {
			found = true
			if thread.UnreadReplies != 1 {
				t.Fatalf("followed thread unread replies = %d, want 1 (only U2's reply)", thread.UnreadReplies)
			}
		}
	}
	if !found {
		t.Fatalf("followed threads = %+v, want the thread U1 followed", threads.Threads)
	}
	cursor, err = repository.GetReadCursor(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if want := domain.NewMessageTimestamp(mine.CreatedAt); cursor.LastRead != want {
		t.Fatalf("a thread reply moved U1's cursor to %s, want it left at %s", cursor.LastRead, want)
	}

	ahead := domain.NewMessageTimestamp(base.Add(10 * time.Second))
	if err := repository.SetReadCursor(ctx, domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: ahead, UpdatedAt: base.Add(10 * time.Second)}, events.Event{ID: "Eread", WorkspaceID: "T1", Topic: "conversation.read", Payload: "{}", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	post("M3", "U1", 5*time.Second, "")
	cursor, err = repository.GetReadCursor(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastRead != ahead {
		t.Fatalf("an older post moved U1's cursor back to %s, want %s", cursor.LastRead, ahead)
	}
}
