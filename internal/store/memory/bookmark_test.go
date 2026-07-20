package memory

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestBookmarkLifecyclePersistsAndPublishesEvents(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1"})
	now := time.Now().UTC()
	bookmark := domain.Bookmark{ID: "Bk1", WorkspaceID: "T1", Conversation: "C1", Title: "Docs", Type: "link", Link: "https://docs.example", CreatedAt: now, UpdatedAt: now, UpdatedBy: "U1"}
	if err := s.CreateBookmark(ctx, bookmark, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "bookmark.created"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBookmark(ctx, bookmark, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "bookmark.created"}); err != store.ErrAlreadyExists {
		t.Fatalf("duplicate error=%v", err)
	}
	bookmark.Title = "Updated"
	bookmark.UpdatedAt = now.Add(time.Minute)
	if updated, err := s.UpdateBookmark(ctx, bookmark, events.Event{ID: "E3", WorkspaceID: "T1", Topic: "bookmark.updated"}); err != nil || updated.Title != "Updated" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	items, err := s.ListBookmarks(ctx, "T1", "C1")
	if err != nil || len(items) != 1 || items[0].Title != "Updated" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := s.DeleteBookmark(ctx, "T1", "C1", "Bk1", events.Event{ID: "E4", WorkspaceID: "T1", Topic: "bookmark.removed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBookmark(ctx, "T1", "C1", "Bk1", events.Event{ID: "E5", WorkspaceID: "T1", Topic: "bookmark.removed"}); err != store.ErrNotFound {
		t.Fatalf("missing delete error=%v", err)
	}
}
