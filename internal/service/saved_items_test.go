package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func savedFixture(t *testing.T) (Messages, *memory.Store) {
	t.Helper()
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
	return Messages{Store: repository}, repository
}

func TestSavedItemsArePrivateIdempotentAndRetainNoInaccessibleContent(t *testing.T) {
	ctx := context.Background()
	messages, _ := savedFixture(t)
	message, err := messages.Post(ctx, "T1", "U1", "C1", "private source", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	first, err := messages.AddToSaved(ctx, "T1", "U1", "C1", timestamp)
	if err != nil {
		t.Fatal(err)
	}
	second, err := messages.AddToSaved(ctx, "T1", "U1", "C1", timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotent save created %q after %q", second.ID, first.ID)
	}
	starred, err := messages.Stars(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if stars := starred.Stars; len(stars) != 0 {
		t.Fatalf("a saved item leaked into deprecated stars.* state: %+v", starred)
	}
	if _, err := messages.SavedItemForMessage(ctx, "T1", "U2", message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member read the saved state: %v", err)
	}
	if page, err := messages.SavedItems(ctx, "T1", "U2", domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("another member's Saved = %+v err=%v", page, err)
	}
	page, err := messages.SavedItems(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].SourceAvailable || page.Items[0].Message.Text != "private source" {
		t.Fatalf("saved page = %+v", page)
	}
	if err := messages.LeaveConversation(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	redacted, err := messages.SavedItems(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(redacted.Items) != 1 || redacted.Items[0].SourceAvailable || redacted.Items[0].Message.Text != "" {
		t.Fatalf("inaccessible saved source leaked: %+v", redacted.Items)
	}
	// A message the member can no longer read cannot become a to-do: it
	// would link something they may not see.
	if _, err := messages.MoveSavedItemToTodo(ctx, "T1", "U1", first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("moved an inaccessible saved item to To-dos: %v", err)
	}
	if err := messages.RemoveSavedItem(ctx, "T1", "U1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SavedItemForMessage(ctx, "T1", "U1", message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed saved item remained readable: %v", err)
	}
}

// Saved lists newest first and pages without repeating or skipping, and the
// clean-up action removes one member's items and nobody else's.
func TestSavedItemsPageNewestFirstAndCleanUpIsTheMembersOwn(t *testing.T) {
	ctx := context.Background()
	messages, _ := savedFixture(t)
	var saved []domain.SavedItemID
	for _, text := range []string{"first", "second", "third"} {
		message, err := messages.Post(ctx, "T1", "U1", "C1", text, "", "")
		if err != nil {
			t.Fatal(err)
		}
		item, err := messages.AddToSaved(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt))
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, item.ID)
		if _, err := messages.AddToSaved(ctx, "T1", "U2", "C1", domain.NewMessageTimestamp(message.CreatedAt)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	var seen []domain.SavedItemID
	request := domain.PageRequest{Limit: 2}
	for {
		page, err := messages.SavedItems(ctx, "T1", "U1", request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		if !page.HasMore {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(seen) != 3 || seen[0] != saved[2] || seen[1] != saved[1] || seen[2] != saved[0] {
		t.Fatalf("Saved order = %v, want newest first of %v", seen, saved)
	}
	cleared, err := messages.ClearSavedItems(ctx, "T1", "U1")
	if err != nil || cleared != 3 {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
	if again, err := messages.ClearSavedItems(ctx, "T1", "U1"); err != nil || again != 0 {
		t.Fatalf("clearing an empty Saved = %d err=%v", again, err)
	}
	if page, err := messages.SavedItems(ctx, "T1", "U2", domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 3 {
		t.Fatalf("clean-up reached another member's Saved: %+v err=%v", page, err)
	}
}

// Moving a saved item to To-dos is one step: the item leaves Saved and a
// to-do linked to its message, titled after it, takes its place.
func TestMoveSavedItemToTodoKeepsTheSourceAndLeavesSaved(t *testing.T) {
	ctx := context.Background()
	messages, _ := savedFixture(t)
	message, err := messages.Post(ctx, "T1", "U1", "C1", "review the draft\nwith details below", "", "")
	if err != nil {
		t.Fatal(err)
	}
	item, err := messages.AddToSaved(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.MoveSavedItemToTodo(ctx, "T1", "U2", item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member moved the saved item: %v", err)
	}
	todo, err := messages.MoveSavedItemToTodo(ctx, "T1", "U1", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Title != "review the draft" || todo.Source.MessageID != message.ID || todo.Source.Conversation != "C1" ||
		todo.Source.Timestamp != domain.NewMessageTimestamp(message.CreatedAt) || todo.Reminder.Scheduled() || todo.Done() {
		t.Fatalf("moved to-do = %+v", todo)
	}
	if _, err := messages.SavedItemForMessage(ctx, "T1", "U1", message.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the moved item is still saved: %v", err)
	}
	page, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != todo.ID {
		t.Fatalf("To-dos after the move = %+v err=%v", page, err)
	}
	if _, err := messages.MoveSavedItemToTodo(ctx, "T1", "U1", item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("moving twice = %v, want not found", err)
	}
}
