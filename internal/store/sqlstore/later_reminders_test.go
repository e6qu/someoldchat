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

func TestLaterReminderLeaseRecurrenceAndCancellationAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "later-reminders.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	reminder := domain.LaterReminder{
		ID: "later_reminder_sql", WorkspaceID: "T1", Creator: "U1", UserID: "U1",
		Target: domain.LaterReminderPersonal, Text: "weekly review", DueAt: now.Add(-time.Minute),
		TimeZone: "Europe/Bucharest", Recurrence: domain.ReminderWeekly,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := s.CreateLaterReminder(ctx, reminder, laterStoreEvent("created", reminder.ID, now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	earliest, err := s.EarliestLaterReminder(ctx, "T1")
	if err != nil || !earliest.Equal(reminder.DueAt) {
		t.Fatalf("earliest=%s err=%v", earliest, err)
	}
	claimed, err := s.ClaimDueLaterReminders(ctx, "T1", "worker-1", 10, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := s.DeleteLaterReminder(ctx, "T1", "U1", reminder.ID, laterStoreEvent("deleted", reminder.ID, now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete under lease=%v, want not found", err)
	}
	nextDue := reminder.DueAt.AddDate(0, 0, 7)
	if err := s.MarkLaterReminderDelivered(ctx, "worker-1", reminder.ID, now, nextDue, laterStoreEvent("delivered", reminder.ID, now)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetLaterReminder(ctx, "T1", "U1", reminder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LastDeliveredAt.Equal(now) || !stored.DueAt.Equal(nextDue) || !stored.CompletedAt.IsZero() {
		t.Fatalf("recurring delivery state=%+v", stored)
	}
	if due, err := s.ClaimDueLaterReminders(ctx, "T1", "worker-2", 10, time.Minute, now); err != nil || len(due) != 0 {
		t.Fatalf("future recurrence was claimed: %+v err=%v", due, err)
	}
}

func TestLaterReminderTerminalFailurePersistsAndLeavesTheQueue(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "later-reminder-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	reminder := domain.LaterReminder{
		ID: "later_reminder_failed", WorkspaceID: "T1", Creator: "U1", UserID: "U1",
		Target: domain.LaterReminderPersonal, Text: "cannot deliver", DueAt: now.Add(-time.Minute),
		TimeZone: "UTC", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := s.CreateLaterReminder(ctx, reminder, laterStoreEvent("created-failure", reminder.ID, now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDueLaterReminders(ctx, "T1", "worker", 1, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := s.MarkLaterReminderFailed(ctx, "worker", reminder.ID, "channel_not_found", now, laterStoreEvent("failed", reminder.ID, now)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetLaterReminder(ctx, "T1", "U1", reminder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FailureCode != "channel_not_found" || !stored.FailedAt.Equal(now) {
		t.Fatalf("terminal failure=%+v", stored)
	}
	if earliest, err := s.EarliestLaterReminder(ctx, "T1"); err != nil || !earliest.IsZero() {
		t.Fatalf("failed reminder remained queued: earliest=%s err=%v", earliest, err)
	}
}

func laterStoreEvent(id string, reminderID domain.LaterReminderID, at time.Time) events.Event {
	return events.Event{
		ID: domain.EventID("event-" + id), WorkspaceID: "T1", ActorID: "U1",
		Topic: "later_reminder." + id, Payload: string(reminderID), CreatedAt: at,
	}
}
