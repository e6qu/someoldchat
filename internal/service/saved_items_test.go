package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestSavedItemsArePrivateIdempotentAndRetainNoInaccessibleContent(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	for _, err := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}),
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}),
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate}),
		repository.SeedConversationMember("C1", "U1"),
		repository.SeedConversationMember("C1", "U2"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: repository}
	message, err := messages.Post(ctx, "T1", "U1", "C1", "private source", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	first, err := messages.SaveForLater(ctx, "T1", "U1", "C1", timestamp)
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.SaveForLater(ctx, "T1", "U1", "C1", timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent save created %q after %q", second.ID, first.ID)
	}
	stars, _, _, err := messages.Stars(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(stars) != 0 {
		t.Fatalf("current Later save leaked into deprecated stars.* state: %+v", stars)
	}
	if _, err := messages.SavedItemForMessage(ctx, "T1", "U2", message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member read the saved state: %v", err)
	}
	page, err := messages.SavedItems(ctx, "T1", "U1", domain.SavedItemInProgress, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].SourceAvailable || page.Items[0].Message.Text != "private source" {
		t.Fatalf("saved page = %+v", page)
	}
	if _, err := messages.SetSavedItemState(ctx, "T1", "U1", first.ID, domain.SavedItemCompleted); err != nil {
		t.Fatal(err)
	}
	completed, err := messages.SavedItems(ctx, "T1", "U1", domain.SavedItemCompleted, domain.PageRequest{Limit: 10})
	if err != nil || len(completed.Items) != 1 {
		t.Fatalf("completed page = %+v, %v", completed, err)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	redacted, err := messages.SavedItems(ctx, "T1", "U1", domain.SavedItemCompleted, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(redacted.Items) != 1 || redacted.Items[0].SourceAvailable || redacted.Items[0].Message.Text != "" {
		t.Fatalf("inaccessible saved source leaked: %+v", redacted.Items)
	}
	if err := messages.RemoveSavedItem(ctx, "T1", "U1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SavedItemForMessage(ctx, "T1", "U1", message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed saved item remained readable: %v", err)
	}
}
