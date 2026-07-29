package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestReminderWorkerDeliversPersonalReminderPrivately(t *testing.T) {
	ctx := context.Background()
	source := reminderStore(t)
	due := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	seedLaterReminder(t, source, domain.LaterReminder{
		ID: "later_reminder_personal", WorkspaceID: "T1", Creator: "U1", UserID: "U1",
		Target: domain.LaterReminderPersonal, Text: "submit expenses", DueAt: due,
		TimeZone: "Europe/Bucharest", CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
	})
	now := due.Add(time.Minute)
	worker, err := NewReminderWorker(source, service.Messages{Store: source}, "reminder-worker", 10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 1 {
		t.Fatalf("deliver personal reminder count=%d err=%v", count, err)
	}
	delivered, err := source.GetLaterReminder(ctx, "T1", "U1", "later_reminder_personal")
	if err != nil {
		t.Fatal(err)
	}
	if !delivered.CompletedAt.Equal(now) || !delivered.LastDeliveredAt.Equal(now) {
		t.Fatalf("personal reminder delivery state = %+v", delivered)
	}
	page, err := source.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("personal reminder leaked into a channel: %+v", page.Messages)
	}
}

func TestReminderWorkerChannelRetryUsesOneMessageForTheOccurrence(t *testing.T) {
	ctx := context.Background()
	base := reminderStore(t)
	due := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	seedLaterReminder(t, base, domain.LaterReminder{
		ID: "later_reminder_channel", WorkspaceID: "T1", Creator: "U1", Channel: "C1",
		Target: domain.LaterReminderChannel, Text: "stand-up", DueAt: due,
		TimeZone: "UTC", CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
	})
	source := &failFirstReminderAcknowledgement{Store: base}
	now := due.Add(time.Minute)
	worker, err := NewReminderWorker(source, service.Messages{Store: base}, "reminder-worker", 10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); count != 0 || !errors.Is(err, errReminderAcknowledgement) {
		t.Fatalf("first delivery count=%d err=%v", count, err)
	}
	now = now.Add(2 * time.Minute)
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 1 {
		t.Fatalf("retried delivery count=%d err=%v", count, err)
	}
	page, err := base.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Text != "Reminder: stand-up" {
		t.Fatalf("channel reminder messages = %+v", page.Messages)
	}
}

func TestLaterReminderCannotBeDeletedWhileDeliveryOwnsTheLease(t *testing.T) {
	ctx := context.Background()
	source := reminderStore(t)
	now := time.Now().UTC()
	seedLaterReminder(t, source, domain.LaterReminder{
		ID: "later_reminder_race", WorkspaceID: "T1", Creator: "U1", UserID: "U1",
		Target: domain.LaterReminderPersonal, Text: "race", DueAt: now.Add(-time.Minute),
		TimeZone: "UTC", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	})
	claimed, err := source.ClaimDueLaterReminders(ctx, "T1", "worker", 1, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	event := events.Event{ID: "delete-race", WorkspaceID: "T1", Topic: "later_reminder.deleted", Payload: "{}", CreatedAt: now}
	if err := source.DeleteLaterReminder(ctx, "T1", "U1", "later_reminder_race", event); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete during delivery = %v, want not found", err)
	}
	if err := source.ReleaseLaterReminder(ctx, "worker", "later_reminder_race", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := source.DeleteLaterReminder(ctx, "T1", "U1", "later_reminder_race", event); err != nil {
		t.Fatalf("delete after release: %v", err)
	}
}

func TestNextReminderDuePreservesLocalWallClockAcrossDST(t *testing.T) {
	bucharest, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, time.March, 28, 9, 30, 0, 0, bucharest)
	next, err := NextReminderDue(domain.LaterReminder{
		DueAt: due.UTC(), TimeZone: "Europe/Bucharest", Recurrence: domain.ReminderDaily,
	}, due.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	local := next.In(bucharest)
	if local.Day() != 29 || local.Hour() != 9 || local.Minute() != 30 {
		t.Fatalf("next local delivery = %s, want 2026-03-29 09:30", local)
	}
}

func TestProductWakeDeadlineUsesRemindersAndEveryWorkspace(t *testing.T) {
	ctx := context.Background()
	source := memory.New()
	early := time.Date(2026, time.July, 29, 15, 0, 0, 0, time.UTC)
	late := early.Add(2 * time.Hour)
	for _, workspace := range []domain.WorkspaceID{"T1", "T2"} {
		user := domain.UserID("U-" + workspace)
		channel := domain.ConversationID("C-" + workspace)
		if err := source.SeedWorkspace(domain.Workspace{ID: workspace}); err != nil {
			t.Fatal(err)
		}
		if err := source.SeedUser(domain.User{ID: user, WorkspaceID: workspace, Name: string(user)}); err != nil {
			t.Fatal(err)
		}
		if err := source.SeedConversation(domain.Conversation{ID: channel, WorkspaceID: workspace, Name: "general"}); err != nil {
			t.Fatal(err)
		}
		if err := source.SeedConversationMember(channel, user); err != nil {
			t.Fatal(err)
		}
	}
	scheduled := domain.ScheduledMessage{
		ID: "Q-late", WorkspaceID: "T1", Channel: "C-T1", Author: "U-T1",
		Text: "later", PostAt: late, CreatedAt: early,
	}
	if err := source.CreateScheduledMessage(ctx, scheduled, events.Event{ID: "scheduled-wake", WorkspaceID: "T1", Topic: "message.scheduled", Payload: "{}", CreatedAt: early}); err != nil {
		t.Fatal(err)
	}
	reminder := domain.LaterReminder{
		ID: "later_reminder_wake", WorkspaceID: "T2", Creator: "U-T2", UserID: "U-T2",
		Target: domain.LaterReminderPersonal, Text: "wake first", DueAt: early,
		TimeZone: "UTC", CreatedAt: early.Add(-time.Hour), UpdatedAt: early.Add(-time.Hour),
	}
	if err := source.CreateLaterReminder(ctx, reminder, events.Event{ID: "reminder-wake", WorkspaceID: "T2", Topic: "later_reminder.created", Payload: "{}", CreatedAt: reminder.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	publisher := &recordingProductDeadline{fence: 7}
	if err := PublishEarliestProductWakeDeadline(ctx, source, source, publisher); err != nil {
		t.Fatal(err)
	}
	if publisher.publishedFence != 7 || !publisher.deadline.Equal(early) {
		t.Fatalf("published fence=%d deadline=%s, want fence 7 and %s", publisher.publishedFence, publisher.deadline, early)
	}
}

type recordingProductDeadline struct {
	fence          uint64
	publishedFence uint64
	deadline       time.Time
}

func (p *recordingProductDeadline) Fence(context.Context) (uint64, error) {
	return p.fence, nil
}

func (p *recordingProductDeadline) SetWakeDeadline(fence uint64, deadline time.Time) error {
	p.publishedFence = fence
	p.deadline = deadline
	return nil
}

func reminderStore(t *testing.T) *memory.Store {
	t.Helper()
	source := memory.New()
	for _, err := range []error{
		source.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}),
		source.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}),
		source.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		source.SeedConversationMember("C1", "U1"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return source
}

func seedLaterReminder(t *testing.T, source *memory.Store, reminder domain.LaterReminder) {
	t.Helper()
	event := events.Event{
		ID: domain.EventID("created_" + reminder.ID), WorkspaceID: reminder.WorkspaceID,
		ActorID: reminder.Creator, Topic: "later_reminder.created", Payload: "{}", CreatedAt: reminder.CreatedAt,
	}
	if err := source.CreateLaterReminder(context.Background(), reminder, event); err != nil {
		t.Fatal(err)
	}
}

var errReminderAcknowledgement = errors.New("simulated acknowledgement loss")

type failFirstReminderAcknowledgement struct {
	*memory.Store
	failed bool
}

func (s *failFirstReminderAcknowledgement) MarkLaterReminderDelivered(ctx context.Context, owner string, id domain.LaterReminderID, deliveredAt, nextDue time.Time, event events.Event) error {
	if !s.failed {
		s.failed = true
		return errReminderAcknowledgement
	}
	return s.Store.MarkLaterReminderDelivered(ctx, owner, id, deliveredAt, nextDue, event)
}
