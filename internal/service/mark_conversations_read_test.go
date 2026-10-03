package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A section's "Mark all as read" moves only the conversations it names. A
// private channel the member is not in is passed over rather than read on
// their behalf, an empty list marks nothing, and an outsider is refused.
func TestMarkConversationsReadMovesOnlyTheNamedConversations(t *testing.T) {
	s := memory.New()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "one"})
	s.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "two"})
	s.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
	for _, id := range []domain.ConversationID{"C1", "C2", "C3"} {
		if err := s.SeedConversationMember(id, "U2"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []domain.ConversationID{"C1", "C2"} {
		if err := s.SeedConversationMember(id, "U1"); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: s}
	ctx := context.Background()
	newest := map[domain.ConversationID]domain.MessageTimestamp{}
	for _, id := range []domain.ConversationID{"C1", "C2", "C3"} {
		message, err := messages.Post(ctx, "T1", "U2", id, "unread", "", "")
		if err != nil {
			t.Fatal(err)
		}
		newest[id] = domain.NewMessageTimestamp(message.CreatedAt)
	}
	unread := func(id domain.ConversationID) bool {
		t.Helper()
		cursor, err := messages.ReadCursor(ctx, "T1", "U1", id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		return cursor.LastRead != newest[id]
	}

	if moved, err := messages.MarkConversationsRead(ctx, "T1", "U1", nil); err != nil || moved != 0 || !unread("C1") || !unread("C2") {
		t.Fatalf("an empty list moved=%d err=%v", moved, err)
	}
	moved, err := messages.MarkConversationsRead(ctx, "T1", "U1", []domain.ConversationID{"C1", "C3", "Cmissing"})
	if err != nil || moved != 1 {
		t.Fatalf("moved=%d err=%v, want only C1", moved, err)
	}
	if unread("C1") || !unread("C2") {
		t.Fatalf("C1 unread=%v C2 unread=%v", unread("C1"), unread("C2"))
	}
	if again, err := messages.MarkConversationsRead(ctx, "T1", "U1", []domain.ConversationID{"C1"}); err != nil || again != 0 {
		t.Fatalf("a read conversation moved again: %d %v", again, err)
	}
	if _, err := messages.MarkConversationsRead(ctx, "T1", "Ustranger", []domain.ConversationID{"C1"}); err == nil {
		t.Fatal("a caller outside the workspace was allowed")
	}
}
