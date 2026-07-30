package memory

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

func TestActivityPersistsOverlappingFiltersAndIndependentTriage(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "D1", WorkspaceID: "T1", Name: "dm", IsDirect: true})
	s.SeedConversationMember("D1", "U1")
	s.SeedConversationMember("D1", "U2")

	created := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "D1", AuthorID: "U1", Text: "hello <@U2>", CreatedAt: created}
	if err := s.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created"}, ""); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityMention}, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("mention activity=%+v err=%v", page, err)
	}
	item := page.Items[0]
	if !item.SourceAvailable || item.Message.ID != "M1" || len(item.Kinds) != 2 || item.Kinds[0] != domain.ActivityDM || item.Kinds[1] != domain.ActivityMention {
		t.Fatalf("activity item=%+v", item)
	}
	if err := s.MutateActivity(ctx, "T1", "U2", []domain.ActivityID{item.ID}, domain.ActivityClear, created.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if active, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{Page: domain.PageRequest{Limit: 20}}); err != nil || len(active.Items) != 0 {
		t.Fatalf("active after clear=%+v err=%v", active, err)
	}
	cleared, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{ClearedOnly: true, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(cleared.Items) != 1 || cleared.Items[0].ReadAt.IsZero() {
		t.Fatalf("cleared=%+v err=%v", cleared, err)
	}
	if err := s.MutateActivity(ctx, "T1", "U2", []domain.ActivityID{item.ID}, domain.ActivityRestore, created.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if unread, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{UnreadOnly: true, Page: domain.PageRequest{Limit: 20}}); err != nil || len(unread.Items) != 0 {
		t.Fatalf("restoring a cleared item incorrectly made it unread: %+v err=%v", unread, err)
	}

	preferences, err := s.GetActivityPreferences(ctx, "T1", "U2")
	if err != nil || preferences.Layout != domain.ActivityDetailed {
		t.Fatalf("default preferences=%+v err=%v", preferences, err)
	}
	preferences.Layout = domain.ActivityDense
	if err := s.SetActivityPreferences(ctx, preferences); err != nil {
		t.Fatal(err)
	}
	if stored, err := s.GetActivityPreferences(ctx, "T1", "U2"); err != nil || stored.Layout != domain.ActivityDense {
		t.Fatalf("stored preferences=%+v err=%v", stored, err)
	}
}

func TestActivityReactionLifecycleAndConversationReadCursor(t *testing.T) {
	ctx := context.Background()
	s := New()
	s.SeedWorkspace(domain.Workspace{ID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1"})
	s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	s.SeedConversationMember("C1", "U1")
	s.SeedConversationMember("C1", "U2")
	created := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "<@U2>", CreatedAt: created}
	if err := s.CreateMessage(ctx, message, events.Event{}, ""); err != nil {
		t.Fatal(err)
	}
	reaction := domain.Reaction{Message: "M1", Name: "eyes", UserID: "U2", CreatedAt: created.Add(time.Minute)}
	if err := s.AddReaction(ctx, reaction, events.Event{}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListActivity(ctx, "T1", "U1", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReaction}, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReactionName != "eyes" {
		t.Fatalf("reaction activity=%+v err=%v", page, err)
	}
	if err := s.RemoveReaction(ctx, reaction, events.Event{}); err != nil {
		t.Fatal(err)
	}
	if page, err = s.ListActivity(ctx, "T1", "U1", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReaction}, Page: domain.PageRequest{Limit: 20}}); err != nil || len(page.Items) != 0 {
		t.Fatalf("removed reaction activity=%+v err=%v", page, err)
	}
	cursor := domain.ReadCursor{WorkspaceID: "T1", UserID: "U2", Conversation: "C1", LastRead: domain.NewMessageTimestamp(created), UpdatedAt: created.Add(2 * time.Minute)}
	if err := s.SetReadCursor(ctx, cursor, events.Event{}); err != nil {
		t.Fatal(err)
	}
	if page, err = s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{UnreadOnly: true, Page: domain.PageRequest{Limit: 20}}); err != nil || len(page.Items) != 0 {
		t.Fatalf("read cursor left message activity unread=%+v err=%v", page, err)
	}
}
