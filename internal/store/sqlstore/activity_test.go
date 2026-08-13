package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestSQLitePrivateChannelInvitationActivitySurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "invitation-activity.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []func() error{
		func() error { return s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1"}) },
		func() error {
			return s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "private", Kind: domain.ConversationTypePrivate})
		},
		func() error { return s.SeedConversationMember(ctx, "C1", "U1") },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	event := events.Event{ID: "E-invite", WorkspaceID: "T1", ActorID: "U1", Topic: "conversation.members_invited", CreatedAt: created}
	if err := s.InviteConversationMembers(ctx, "C1", []domain.UserID{"U2"}, event); err != nil {
		t.Fatal(err)
	}
	if err := s.InviteConversationMembers(ctx, "C1", []domain.UserID{"U2"}, events.Event{ID: "E-duplicate", WorkspaceID: "T1", ActorID: "U1", CreatedAt: created.Add(time.Minute)}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate invite err=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{
		Kinds: []domain.ActivityKind{domain.ActivityInvitation}, Page: domain.PageRequest{Limit: 20},
	})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("persisted invitation activity=%+v err=%v", page, err)
	}
	item := page.Items[0]
	if !item.SourceAvailable || item.ActorID != "U1" || item.Conversation != "C1" || !item.OccurredAt.Equal(created) {
		t.Fatalf("persisted invitation item=%+v", item)
	}
}

func TestSQLiteActivitySurvivesReopenWithFiltersAndTriage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "activity.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}))
	seed(s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}))
	seed(s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}))
	seed(s.SeedConversation(ctx, domain.Conversation{ID: "D1", WorkspaceID: "T1", Name: "dm", Kind: domain.ConversationTypeIM}))
	seed(s.SeedConversationMember(ctx, "D1", "U1"))
	seed(s.SeedConversationMember(ctx, "D1", "U2"))
	created := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	seed(s.CreateMessage(ctx, domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "D1", AuthorID: "U1", Text: "hello <@U2>", CreatedAt: created}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created"}, ""))
	page, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityMention}, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(page.Items) != 1 || !page.Items[0].SourceAvailable || len(page.Items[0].Kinds) != 2 {
		t.Fatalf("activity=%+v err=%v", page, err)
	}
	id := page.Items[0].ID
	seed(s.MutateActivity(ctx, "T1", "U2", []domain.ActivityID{id}, domain.ActivityClear, created.Add(time.Minute)))
	seed(s.SetActivityPreferences(ctx, domain.ActivityPreferences{WorkspaceID: "T1", UserID: "U2", Layout: domain.ActivityDense}))
	seed(s.Close())

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cleared, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{ClearedOnly: true, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(cleared.Items) != 1 || cleared.Items[0].ID != id || cleared.Items[0].ReadAt.IsZero() {
		t.Fatalf("persisted cleared activity=%+v err=%v", cleared, err)
	}
	preferences, err := s.GetActivityPreferences(ctx, "T1", "U2")
	if err != nil || preferences.Layout != domain.ActivityDense {
		t.Fatalf("preferences=%+v err=%v", preferences, err)
	}
}

func TestSQLiteActivityReactionReadCursorAndReminderDeliveryAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, seed := range []func() error{
		func() error { return s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}) },
		func() error { return s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}) },
		func() error {
			return s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
		},
		func() error { return s.SeedConversationMember(ctx, "C1", "U1") },
		func() error { return s.SeedConversationMember(ctx, "C1", "U2") },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	message := domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello <@U2>", CreatedAt: created}
	if err := s.CreateMessage(ctx, message, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created"}, ""); err != nil {
		t.Fatal(err)
	}
	reaction := domain.Reaction{Message: "M1", Name: "eyes", UserID: "U2", CreatedAt: created.Add(time.Minute)}
	if err := s.AddReaction(ctx, reaction, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "reaction.added"}); err != nil {
		t.Fatal(err)
	}
	reactions, err := s.ListActivity(ctx, "T1", "U1", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReaction}, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(reactions.Items) != 1 || reactions.Items[0].ReactionName != "eyes" {
		t.Fatalf("reaction activity=%+v err=%v", reactions, err)
	}
	if err := s.RemoveReaction(ctx, reaction, events.Event{ID: "E3", WorkspaceID: "T1", Topic: "reaction.removed"}); err != nil {
		t.Fatal(err)
	}
	reactions, err = s.ListActivity(ctx, "T1", "U1", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReaction}, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(reactions.Items) != 0 {
		t.Fatalf("removed reaction activity=%+v err=%v", reactions, err)
	}
	if err := s.SetReadCursor(ctx, domain.ReadCursor{
		WorkspaceID: "T1", UserID: "U2", Conversation: "C1",
		LastRead: domain.NewMessageTimestamp(created), UpdatedAt: created.Add(2 * time.Minute),
	}, events.Event{ID: "E4", WorkspaceID: "T1", Topic: "conversation.read"}); err != nil {
		t.Fatal(err)
	}
	unread, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{UnreadOnly: true, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(unread.Items) != 0 {
		t.Fatalf("read cursor left activity unread=%+v err=%v", unread, err)
	}

	reminder := domain.LaterReminder{
		ID: "later-1", WorkspaceID: "T1", Creator: "U2", UserID: "U2",
		Target: domain.LaterReminderPersonal, Text: "remember", DueAt: created,
		TimeZone: "UTC", CreatedAt: created.Add(-time.Hour), UpdatedAt: created.Add(-time.Hour),
	}
	if err := s.CreateLaterReminder(ctx, reminder, events.Event{ID: "E5", WorkspaceID: "T1", Topic: "later_reminder.created"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDueLaterReminders(ctx, "T1", "worker", 1, time.Minute, created.Add(time.Minute))
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	delivered := created.Add(90 * time.Second)
	if err := s.MarkLaterReminderDelivered(ctx, "worker", reminder.ID, delivered, time.Time{}, events.Event{ID: "E6", WorkspaceID: "T1", Topic: "later_reminder.delivered"}); err != nil {
		t.Fatal(err)
	}
	reminders, err := s.ListActivity(ctx, "T1", "U2", domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReminder}, Page: domain.PageRequest{Limit: 20}})
	if err != nil || len(reminders.Items) != 1 || reminders.Items[0].Reminder.Text != "remember" {
		t.Fatalf("reminder activity=%+v err=%v", reminders, err)
	}
}

func TestSQLiteNotificationPreferencesAndThreadFollowsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "notifications.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}))
	seed(s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}))
	seed(s.SeedUser(ctx, domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}))
	seed(s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}))
	seed(s.SeedConversationMember(ctx, "C1", "U1"))
	seed(s.SeedConversationMember(ctx, "C1", "U2"))
	workspacePreferences := domain.DefaultWorkspaceNotificationPreferences("T1", "U2")
	workspacePreferences.Level = domain.NotificationAll
	workspacePreferences.Keywords = []string{"release"}
	workspacePreferences.ActivityReminders = false
	seed(s.SetWorkspaceNotificationPreferences(ctx, workspacePreferences, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "notification.preferences_changed"}))
	conversationPreferences := domain.DefaultConversationNotificationPreferences("T1", "U2", "C1")
	conversationPreferences.FollowEveryThread = true
	seed(s.SetConversationNotificationPreferences(ctx, conversationPreferences, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "conversation.notification_preferences_changed"}))
	created := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	rootTimestamp := domain.NewMessageTimestamp(created)
	seed(s.CreateMessage(ctx, domain.Message{
		ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "root", CreatedAt: created,
	}, events.Event{ID: "E3", WorkspaceID: "T1", Topic: "message.created"}, ""))
	seed(s.SetThreadFollowed(ctx, "T1", "U2", "C1", rootTimestamp, true, events.Event{ID: "E4", WorkspaceID: "T1", Topic: "thread.follow_changed"}))
	seed(s.Close())

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	storedWorkspace, err := s.GetWorkspaceNotificationPreferences(ctx, "T1", "U2")
	if err != nil || storedWorkspace.Level != domain.NotificationAll || len(storedWorkspace.Keywords) != 1 || storedWorkspace.Keywords[0] != "release" || storedWorkspace.ActivityReminders {
		t.Fatalf("workspace preferences=%+v err=%v", storedWorkspace, err)
	}
	storedConversation, err := s.GetConversationNotificationPreferences(ctx, "T1", "U2", "C1")
	if err != nil || storedConversation.Level != domain.NotificationInherit || !storedConversation.FollowEveryThread {
		t.Fatalf("conversation preferences=%+v err=%v", storedConversation, err)
	}
	followed, err := s.IsThreadFollowed(ctx, "T1", "U2", "C1", rootTimestamp)
	if err != nil || !followed {
		t.Fatalf("thread followed=%v err=%v", followed, err)
	}
}

func TestSchema108MigrationCreatesDurableNotificationTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "activity-migration.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE thread_follows`,
		`DROP TABLE conversation_notification_preferences`,
		`DROP TABLE notification_preferences`,
		`UPDATE schema_migrations SET version = 107 WHERE version = 108`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"notification_preferences", "conversation_notification_preferences", "thread_follows"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("schema 108 did not create %s", table)
		}
	}
}

func TestSchema107MigrationCreatesDurableActivityTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "activity-migration.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE activity_preferences`,
		`DROP TABLE activity_item_kinds`,
		`DROP TABLE activity_items`,
		`UPDATE schema_migrations SET version = 106 WHERE version = 107`,
		`DELETE FROM schema_migrations WHERE version = 108`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"activity_items", "activity_item_kinds", "activity_preferences"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("schema 107 did not create %s", table)
		}
	}
}
