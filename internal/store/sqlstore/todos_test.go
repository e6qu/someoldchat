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

func openTodoStore(t *testing.T, name string) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, seed := range []error{
		s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}),
		s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}),
		s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		s.SeedConversationMember(ctx, "C1", "U1"),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	return s
}

func todoStoreEvent(id string, at time.Time) events.Event {
	return events.Event{ID: domain.EventID("event-" + id), WorkspaceID: "T1", ActorID: "U1", Topic: "todo.changed", Payload: "{}", CreatedAt: at}
}

func TestTodoReminderLeaseRecurrenceAndCancellationAreAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTodoStore(t, "todos.db")
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	due := now.Add(-time.Minute)
	todo := domain.Todo{
		ID: "todo_sql", WorkspaceID: "T1", UserID: "U1", Title: "weekly review",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "Europe/Bucharest", Recurrence: domain.ReminderWeekly, RecurrenceAnchor: due},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := s.CreateTodo(ctx, todo, todoStoreEvent("created", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	earliest, err := s.EarliestTodoReminder(ctx, "T1")
	if err != nil || !earliest.Equal(due) {
		t.Fatalf("earliest=%s err=%v", earliest, err)
	}
	claimed, err := s.ClaimDueTodoReminders(ctx, "T1", "worker-1", 10, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := s.DeleteTodo(ctx, "T1", "U1", todo.ID, todoStoreEvent("deleted", now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete under lease=%v, want not found", err)
	}
	if err := s.SetTodoCompletion(ctx, "T1", "U1", todo.ID, now, todoStoreEvent("done", now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("complete under lease=%v, want not found", err)
	}
	nextDue := due.AddDate(0, 0, 7)
	if err := s.MarkTodoReminderDelivered(ctx, "worker-1", todo.ID, now, nextDue, todoStoreEvent("delivered", now)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetTodo(ctx, "T1", "U1", todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Delivery.LastDeliveredAt.Equal(now) || !stored.Reminder.DueAt.Equal(nextDue) || stored.Done() || !stored.Badged() {
		t.Fatalf("recurring delivery state=%+v", stored)
	}
	if err := s.AcknowledgeTodoReminders(ctx, "T1", "U1", now.Add(time.Second), todoStoreEvent("acknowledged", now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetTodo(ctx, "T1", "U1", todo.ID)
	if err != nil || stored.Badged() || !stored.Delivery.AcknowledgedAt.Equal(stored.Delivery.LastDeliveredAt) {
		t.Fatalf("acknowledged delivery=%+v err=%v", stored, err)
	}
	if due, err := s.ClaimDueTodoReminders(ctx, "T1", "worker-2", 10, time.Minute, now); err != nil || len(due) != 0 {
		t.Fatalf("future recurrence was claimed: %+v err=%v", due, err)
	}
}

// Clearing a to-do's due date takes it out of the delivery queue, and a new
// due date starts delivery afresh.
func TestTodoReminderEditRestartsDelivery(t *testing.T) {
	ctx := context.Background()
	s := openTodoStore(t, "todo-edit.db")
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	due := now.Add(-time.Minute)
	todo := domain.Todo{
		ID: "todo_edit", WorkspaceID: "T1", UserID: "U1", Title: "edit me",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "UTC", RecurrenceAnchor: due},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := s.CreateTodo(ctx, todo, todoStoreEvent("created", now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDueTodoReminders(ctx, "T1", "worker", 1, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTodoReminderDelivered(ctx, "worker", todo.ID, now, time.Time{}, todoStoreEvent("delivered", now)); err != nil {
		t.Fatal(err)
	}
	cleared := todo
	cleared.Reminder = domain.ReminderTiming{}
	cleared.UpdatedAt = now
	updated, err := s.UpdateTodo(ctx, cleared, todoStoreEvent("cleared", now))
	if err != nil || updated.Reminder.Scheduled() || !updated.Delivery.LastDeliveredAt.IsZero() || updated.ReminderGroup(now) != domain.TodoNoReminder {
		t.Fatalf("cleared due date = %+v err=%v", updated, err)
	}
	if earliest, err := s.EarliestTodoReminder(ctx, "T1"); err != nil || !earliest.IsZero() {
		t.Fatalf("a to-do without a reminder is queued at %s (err %v)", earliest, err)
	}
	later := now.Add(time.Hour)
	rescheduled := cleared
	rescheduled.Reminder = domain.ReminderTiming{DueAt: later, TimeZone: "UTC", RecurrenceAnchor: later}
	if _, err := s.UpdateTodo(ctx, rescheduled, todoStoreEvent("rescheduled", now)); err != nil {
		t.Fatal(err)
	}
	if earliest, err := s.EarliestTodoReminder(ctx, "T1"); err != nil || !earliest.Equal(later) {
		t.Fatalf("rescheduled to-do earliest=%s err=%v", earliest, err)
	}
}

func TestChannelReminderTerminalFailurePersistsAndLeavesTheQueue(t *testing.T) {
	ctx := context.Background()
	s := openTodoStore(t, "channel-reminder-failure.db")
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	due := now.Add(-time.Minute)
	reminder := domain.ChannelReminder{
		ID: "channel_reminder_failed", WorkspaceID: "T1", Creator: "U1", Channel: "C1", Text: "cannot deliver",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "UTC", RecurrenceAnchor: due},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := s.CreateChannelReminder(ctx, reminder, todoStoreEvent("created-failure", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDueChannelReminders(ctx, "T1", "worker", 1, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := s.MarkChannelReminderFailed(ctx, "worker", reminder.ID, "channel_not_found", now, todoStoreEvent("failed", now)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetChannelReminder(ctx, "T1", "U1", reminder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Delivery.FailureCode != "channel_not_found" || !stored.Delivery.FailedAt.Equal(now) {
		t.Fatalf("terminal failure=%+v", stored)
	}
	if earliest, err := s.EarliestChannelReminder(ctx, "T1"); err != nil || !earliest.IsZero() {
		t.Fatalf("failed reminder remained queued: earliest=%s err=%v", earliest, err)
	}
}

// TestSchema218MovesLaterIntoTodosAndSaved upgrades a database holding
// Later's records and checks each lands where the migration says it does.
func TestSchema218MovesLaterIntoTodosAndSaved(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "later-legacy.db")
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []error{
		s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}),
		s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}),
		s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		s.SeedConversationMember(ctx, "C1", "U1"),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	sent := time.Date(2026, time.July, 1, 9, 0, 0, 0, time.UTC)
	messages := []struct{ id, text string }{
		{"M-progress", "keep me"}, {"M-reminded", "remind me"}, {"M-archived", "archived one"},
		{"M-done", "finished work\nsecond line"}, {"M-file", ""},
	}
	for offset, value := range messages {
		message := domain.Message{ID: domain.MessageID(value.id), WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: value.text, CreatedAt: sent.Add(time.Duration(offset+1) * time.Minute)}
		if err := s.CreateMessage(ctx, message, events.Event{ID: domain.EventID("E-" + value.id), WorkspaceID: "T1", Topic: "message.created"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	at := string(domain.NewStoredTime(sent))
	statements := []string{
		`DROP TABLE saved_items`,
		`CREATE TABLE saved_items (
		 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
		 message_id TEXT NOT NULL REFERENCES messages(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
		 state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		 UNIQUE (workspace_id, user_id, message_id))`,
		`CREATE INDEX saved_items_user_state_updated ON saved_items(workspace_id, user_id, state, updated_at, id)`,
		`CREATE TABLE later_reminders (
		 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
		 user_id TEXT NOT NULL DEFAULT '', channel_id TEXT NOT NULL DEFAULT '', source_message_id TEXT NOT NULL DEFAULT '',
		 source_conversation_id TEXT NOT NULL DEFAULT '', source_timestamp TEXT NOT NULL DEFAULT '', target TEXT NOT NULL, text TEXT NOT NULL, due_at INTEGER NOT NULL,
		 timezone TEXT NOT NULL, recurrence TEXT NOT NULL DEFAULT '', recurrence_anchor INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		 completed_at INTEGER NOT NULL DEFAULT 0, last_delivered_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0,
		 failure_code TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
		 next_attempt_at INTEGER NOT NULL DEFAULT 0)`,
		`DELETE FROM schema_migrations WHERE version >= 218`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (217, '')`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	due := sent.Add(48 * time.Hour).Unix()
	for _, row := range []struct {
		id, message, state string
	}{
		{"saved-progress", "M-progress", "in_progress"},
		{"saved-reminded", "M-reminded", "in_progress"},
		{"saved-archived", "M-archived", "archived"},
		{"saved-done", "M-done", "completed"},
		{"saved-file", "M-file", "completed"},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO saved_items(id, workspace_id, user_id, message_id, conversation_id, state, created_at, updated_at) VALUES (?, 'T1', 'U1', ?, 'C1', ?, ?, ?)`,
			row.id, row.message, row.state, at, at); err != nil {
			t.Fatal(err)
		}
	}
	remindedTS := string(domain.NewMessageTimestamp(sent.Add(2 * time.Minute)))
	if _, err := s.db.ExecContext(ctx, `INSERT INTO later_reminders(id, workspace_id, creator_id, user_id, source_message_id, source_conversation_id, source_timestamp, target, text, due_at, timezone, recurrence_anchor, created_at, updated_at)
		VALUES ('later_reminder_personal', 'T1', 'U1', 'U1', 'M-reminded', 'C1', ?, 'personal', 'Message reminder', ?, 'UTC', ?, ?, ?)`, remindedTS, due, due, sent.Unix(), sent.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO later_reminders(id, workspace_id, creator_id, user_id, target, text, due_at, timezone, created_at, updated_at, completed_at, last_delivered_at)
		VALUES ('later_reminder_fired', 'T1', 'U1', 'U1', 'personal', 'already fired', ?, 'UTC', ?, ?, ?, ?)`, sent.Unix(), sent.Unix(), sent.Unix(), sent.Unix(), sent.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO later_reminders(id, workspace_id, creator_id, channel_id, target, text, due_at, timezone, recurrence, recurrence_anchor, created_at, updated_at)
		VALUES ('later_reminder_channel', 'T1', 'U1', 'C1', 'channel', 'stand-up', ?, 'UTC', 'weekly', ?, ?, ?)`, due, due, sent.Unix(), sent.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	reminded, err := s.GetTodo(ctx, "T1", "U1", "later_reminder_personal")
	if err != nil || reminded.Title != "Message reminder" || reminded.Source.MessageID != "M-reminded" || reminded.Source.Timestamp != domain.MessageTimestamp(remindedTS) ||
		reminded.Reminder.DueAt.Unix() != due || reminded.Done() {
		t.Fatalf("personal Later reminder became %+v err=%v", reminded, err)
	}
	fired, err := s.GetTodo(ctx, "T1", "U1", "later_reminder_fired")
	if err != nil || !fired.Done() {
		t.Fatalf("a reminder Later showed as completed became %+v err=%v", fired, err)
	}
	channel, err := s.GetChannelReminder(ctx, "T1", "U1", "later_reminder_channel")
	if err != nil || channel.Channel != "C1" || channel.Text != "stand-up" || channel.Reminder.Recurrence != domain.ReminderWeekly {
		t.Fatalf("channel reminder became %+v err=%v", channel, err)
	}
	done, err := s.GetTodo(ctx, "T1", "U1", "todo_saved-done")
	if err != nil || !done.Done() || done.Title != "finished work" || done.Source.MessageID != "M-done" ||
		done.Source.Timestamp != domain.NewMessageTimestamp(sent.Add(4*time.Minute)) {
		t.Fatalf("completed saved item became %+v err=%v", done, err)
	}
	file, err := s.GetTodo(ctx, "T1", "U1", "todo_saved-file")
	if err != nil || file.Title != "Saved message" {
		t.Fatalf("completed saved file became %+v err=%v", file, err)
	}
	page, err := s.ListSavedItems(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	kept := map[domain.SavedItemID]bool{}
	for _, item := range page.Items {
		kept[item.ID] = true
	}
	if len(kept) != 2 || !kept["saved-progress"] || !kept["saved-archived"] {
		t.Fatalf("Saved after the upgrade = %+v", page.Items)
	}
	columns, err := s.tableColumns(ctx, s.db, "saved_items")
	if err != nil || columns["state"] || columns["updated_at"] {
		t.Fatalf("saved_items kept Later's columns: %v err=%v", columns, err)
	}
	var legacy int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'later_reminders'`).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatalf("later_reminders remains=%d err=%v", legacy, err)
	}
}
