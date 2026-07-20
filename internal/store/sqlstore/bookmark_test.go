package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestSQLiteBookmarkLifecycleSurvivesStoreOpen(t *testing.T) {
	ctx := context.Background()
	dsn := "file:bookmark-lifecycle?mode=memory&cache=shared"
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"})
	s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "Alice"})
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	bookmark := domain.Bookmark{ID: "Bk1", WorkspaceID: "T1", Conversation: "C1", Title: "Docs", Type: "link", Link: "https://docs.example", CreatedAt: now, UpdatedAt: now, UpdatedBy: "U1"}
	if err := s.CreateBookmark(ctx, bookmark, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "bookmark.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBookmark(ctx, "T1", "C1", "Bk1"); err != nil {
		t.Fatal(err)
	}
	bookmark.Title = "Updated"
	bookmark.UpdatedAt = now.Add(time.Minute)
	if _, err := s.UpdateBookmark(ctx, bookmark, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "bookmark.updated", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListBookmarks(ctx, "T1", "C1")
	if err != nil || len(items) != 1 || items[0].Title != "Updated" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := s.DeleteBookmark(ctx, "T1", "C1", "Bk1", events.Event{ID: "E3", WorkspaceID: "T1", Topic: "bookmark.removed", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBookmark(ctx, "T1", "C1", "Bk1"); err != store.ErrNotFound {
		t.Fatalf("missing bookmark error=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
