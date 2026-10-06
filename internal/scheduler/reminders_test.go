package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// "Reminders attach a due date to your to-dos": the reminder coming due is
// a notification and a badge, not the member finishing the work. Delivery
// used to complete a one-time reminder, which made an overdue to-do
// impossible to express.
func TestReminderWorkerDeliversATodoReminderPrivatelyAndLeavesItOverdue(t *testing.T) {
	ctx := context.Background()
	source := reminderStore(t)
	due := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	seedTodo(t, source, domain.Todo{
		ID: "todo_personal", WorkspaceID: "T1", UserID: "U1", Title: "submit expenses",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "Europe/Bucharest", RecurrenceAnchor: due},
		CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
	})
	now := due.Add(time.Minute)
	worker, err := NewReminderWorker(source, service.Messages{Store: source}, "reminder-worker", 10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 1 {
		t.Fatalf("deliver to-do reminder count=%d err=%v", count, err)
	}
	delivered, err := source.GetTodo(ctx, "T1", "U1", "todo_personal")
	if err != nil {
		t.Fatal(err)
	}
	if delivered.Done() || !delivered.Delivery.LastDeliveredAt.Equal(now) || !delivered.Reminder.DueAt.Equal(due) ||
		delivered.ReminderGroup(now) != domain.TodoOverdue || !delivered.Badged() {
		t.Fatalf("to-do after its reminder came due = %+v", delivered)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 0 {
		t.Fatalf("a delivered one-time reminder fired again: count=%d err=%v", count, err)
	}
	activity, err := source.ListActivity(ctx, "T1", "U1", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(activity.Items) != 1 || activity.Items[0].TodoID != "todo_personal" || activity.Items[0].Todo.Title != "submit expenses" {
		t.Fatalf("Activity after the reminder = %+v", activity.Items)
	}
	page, err := source.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("a to-do reminder leaked into a channel: %+v", page.Messages)
	}
}

// A done to-do's reminder does not fire: finishing the work is the member's
// answer to it.
func TestReminderWorkerSkipsADoneTodo(t *testing.T) {
	ctx := context.Background()
	source := reminderStore(t)
	due := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	seedTodo(t, source, domain.Todo{
		ID: "todo_done", WorkspaceID: "T1", UserID: "U1", Title: "already done",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "UTC", RecurrenceAnchor: due},
		CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour), CompletedAt: due.Add(-time.Minute),
	})
	worker, err := NewReminderWorker(source, service.Messages{Store: source}, "reminder-worker", 10, time.Minute, func() time.Time { return due.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 0 {
		t.Fatalf("done to-do reminder count=%d err=%v", count, err)
	}
	if earliest, err := source.EarliestTodoReminder(ctx, "T1"); err != nil || !earliest.IsZero() {
		t.Fatalf("a done to-do still wakes the workspace at %s (err %v)", earliest, err)
	}
}

func TestReminderWorkerAdvancesMonthEndRecurrenceToTheClampedDay(t *testing.T) {
	ctx := context.Background()
	source := reminderStore(t)
	anchor := time.Date(2026, time.January, 31, 9, 0, 0, 0, time.UTC)
	seedTodo(t, source, domain.Todo{
		ID: "todo_monthly", WorkspaceID: "T1", UserID: "U1", Title: "rent",
		Reminder:  domain.ReminderTiming{DueAt: anchor, RecurrenceAnchor: anchor, Recurrence: domain.ReminderMonthly, TimeZone: "UTC"},
		CreatedAt: anchor.Add(-time.Hour), UpdatedAt: anchor.Add(-time.Hour),
	})
	now := anchor.Add(time.Minute)
	worker, err := NewReminderWorker(source, service.Messages{Store: source}, "reminder-worker", 10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 1 {
		t.Fatalf("deliver monthly reminder count=%d err=%v", count, err)
	}
	delivered, err := source.GetTodo(ctx, "T1", "U1", "todo_monthly")
	if err != nil {
		t.Fatal(err)
	}
	// February has no 31st, so the next occurrence clamps to the 28th rather than
	// overflowing to March 3rd, and the reminder stays recurring rather than
	// completing. The anchor survived the store round-trip to make that possible.
	if want := time.Date(2026, time.February, 28, 9, 0, 0, 0, time.UTC); !delivered.Reminder.DueAt.Equal(want) {
		t.Fatalf("next due = %s, want %s", delivered.Reminder.DueAt.UTC(), want)
	}
	if delivered.Done() {
		t.Fatalf("recurring reminder was completed instead of rescheduled: %+v", delivered)
	}
	if !delivered.Reminder.RecurrenceAnchor.Equal(anchor) {
		t.Fatalf("anchor changed on delivery: got %s, want %s", delivered.Reminder.RecurrenceAnchor.UTC(), anchor)
	}
}

func TestReminderWorkerChannelRetryUsesOneMessageForTheOccurrence(t *testing.T) {
	ctx := context.Background()
	base := reminderStore(t)
	due := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	seedChannelReminder(t, base, domain.ChannelReminder{
		ID: "channel_reminder_retry", WorkspaceID: "T1", Creator: "U1", Channel: "C1", Text: "stand-up",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "UTC", RecurrenceAnchor: due},
		CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
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
	page, err := base.ListMessages(ctx, "C1", domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Text != "Reminder: stand-up." || page.Messages[0].AuthorID != domain.SlackbotUserID {
		t.Fatalf("channel reminder messages = %+v", page.Messages)
	}
	posted, err := base.GetChannelReminder(ctx, "T1", "U1", "channel_reminder_retry")
	if err != nil || !posted.Finished() {
		t.Fatalf("posted channel reminder = %+v err=%v", posted, err)
	}
	if count, err := worker.RunOnce(ctx, "T1"); err != nil || count != 0 {
		t.Fatalf("a posted one-time channel reminder fired again: count=%d err=%v", count, err)
	}
}

func TestTodoCannotBeDeletedWhileDeliveryOwnsTheLease(t *testing.T) {
	ctx := context.Background()
	source := reminderStore(t)
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	seedTodo(t, source, domain.Todo{
		ID: "todo_race", WorkspaceID: "T1", UserID: "U1", Title: "race",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "UTC", RecurrenceAnchor: due},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	})
	claimed, err := source.ClaimDueTodoReminders(ctx, "T1", "worker", 1, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	event := events.Event{ID: "delete-race", WorkspaceID: "T1", Topic: "todo.deleted", Payload: "{}", CreatedAt: now}
	if err := source.DeleteTodo(ctx, "T1", "U1", "todo_race", event); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete during delivery = %v, want not found", err)
	}
	if err := source.ReleaseTodoReminder(ctx, "worker", "todo_race", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := source.DeleteTodo(ctx, "T1", "U1", "todo_race", event); err != nil {
		t.Fatalf("delete after release: %v", err)
	}
}

func TestNextReminderDuePreservesLocalWallClockAcrossDST(t *testing.T) {
	bucharest, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, time.March, 28, 9, 30, 0, 0, bucharest)
	next, err := NextReminderDue(domain.ReminderTiming{
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

func TestNextReminderDueMonthlyClampsToMonthEndWithoutDrifting(t *testing.T) {
	anchor := time.Date(2026, time.January, 31, 9, 0, 0, 0, time.UTC)
	// Each step feeds the prior result back as the current due instant, exactly
	// as delivery does, and asserts the month advances without the day drifting
	// off the anchored 31st once a short month has clamped it.
	want := []time.Time{
		time.Date(2026, time.February, 28, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 31, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.April, 30, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.May, 31, 9, 0, 0, 0, time.UTC),
	}
	due := anchor
	for i, expected := range want {
		next, err := NextReminderDue(domain.ReminderTiming{
			DueAt: due.UTC(), RecurrenceAnchor: anchor.UTC(), TimeZone: "UTC", Recurrence: domain.ReminderMonthly,
		}, due)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !next.Equal(expected) {
			t.Fatalf("step %d: next = %s, want %s", i, next.UTC(), expected)
		}
		due = next
	}
}

// "every month" and "every year" set on a day some months lack, after today's
// time has passed, used to step the first occurrence with AddDate: October
// 31st became December 1st, and that date became the anchor, so the series
// lived on the 1st. The first occurrence now clamps to the next month's last
// day, the series keeps the day it was set on, and every later occurrence -
// computed by the delivery worker from what the service stored - follows it.
func TestEveryMonthPhraseClampsFirstOccurrenceAndKeepsItsDay(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	day := func(location *time.Location, year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, 9, 0, 0, 0, location)
	}
	// The years are far ahead so the service's due-in-the-future rule holds
	// on any test clock; 2096 is a leap year and 2100 is not.
	for _, testCase := range []struct {
		name     string
		now      time.Time
		phrase   string
		location *time.Location
		want     []time.Time
	}{
		{"Jan 31 to Feb 28 to Mar 31", time.Date(2097, time.January, 31, 10, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2097, time.February, 28), day(time.UTC, 2097, time.March, 31), day(time.UTC, 2097, time.April, 30), day(time.UTC, 2097, time.May, 31)}},
		{"Jan 31 to Feb 29 in a leap year", time.Date(2096, time.January, 31, 10, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2096, time.February, 29), day(time.UTC, 2096, time.March, 31)}},
		{"Oct 31 to Nov 30 to Dec 31", time.Date(2097, time.October, 31, 10, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2097, time.November, 30), day(time.UTC, 2097, time.December, 31), day(time.UTC, 2098, time.January, 31), day(time.UTC, 2098, time.February, 28)}},
		{"Oct 31 in a member's zone", time.Date(2097, time.October, 31, 10, 0, 0, 0, newYork), "pay rent every month at 9am", newYork,
			[]time.Time{day(newYork, 2097, time.November, 30), day(newYork, 2097, time.December, 31)}},
		{"Jan 30 to Feb 28 to Mar 30", time.Date(2097, time.January, 30, 10, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2097, time.February, 28), day(time.UTC, 2097, time.March, 30)}},
		{"Jan 29 to Feb 28 to Mar 29", time.Date(2097, time.January, 29, 10, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2097, time.February, 28), day(time.UTC, 2097, time.March, 29)}},
		{"Jan 29 to Feb 29 in a leap year", time.Date(2096, time.January, 29, 10, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2096, time.February, 29), day(time.UTC, 2096, time.March, 29)}},
		{"Jan 31 before its time is today", time.Date(2097, time.January, 31, 8, 0, 0, 0, time.UTC), "pay rent every month at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2097, time.January, 31), day(time.UTC, 2097, time.February, 28), day(time.UTC, 2097, time.March, 31)}},
		{"Feb 29 every year", time.Date(2096, time.February, 29, 10, 0, 0, 0, time.UTC), "file taxes every year at 9am", time.UTC,
			[]time.Time{day(time.UTC, 2097, time.February, 28), day(time.UTC, 2098, time.February, 28), day(time.UTC, 2099, time.February, 28),
				day(time.UTC, 2100, time.February, 28), day(time.UTC, 2101, time.February, 28), day(time.UTC, 2102, time.February, 28),
				day(time.UTC, 2103, time.February, 28), day(time.UTC, 2104, time.February, 29)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, occurrence, err := domain.ParseReminderExpression(testCase.phrase, testCase.now, testCase.location)
			if err != nil {
				t.Fatal(err)
			}
			if !occurrence.Due.Equal(testCase.want[0]) {
				t.Fatalf("first occurrence = %s, want %s", occurrence.Due.In(testCase.location), testCase.want[0])
			}
			// What the service stores is what delivery steps from.
			chat := service.Messages{Store: reminderStore(t)}
			stored, err := chat.CreateTodo(context.Background(), "T1", "U1", domain.TodoRequest{
				Title: "pay rent", Reminder: domain.ReminderTiming{
					DueAt: occurrence.Due, TimeZone: testCase.location.String(), Recurrence: occurrence.Recurrence, RecurrenceAnchor: occurrence.Anchor,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			for i, expected := range testCase.want[1:] {
				next, err := NextReminderDue(stored.Reminder, stored.Reminder.DueAt)
				if err != nil {
					t.Fatalf("step %d: %v", i+1, err)
				}
				if !next.Equal(expected) {
					t.Fatalf("step %d: next = %s, want %s", i+1, next.In(testCase.location), expected)
				}
				stored.Reminder.DueAt = next
			}
		})
	}
}

func TestNextReminderDueYearlyKeepsLeapDayAnchor(t *testing.T) {
	anchor := time.Date(2024, time.February, 29, 9, 0, 0, 0, time.UTC)
	want := []time.Time{
		time.Date(2025, time.February, 28, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 28, 9, 0, 0, 0, time.UTC),
		time.Date(2027, time.February, 28, 9, 0, 0, 0, time.UTC),
		time.Date(2028, time.February, 29, 9, 0, 0, 0, time.UTC), // leap year restores the 29th
	}
	due := anchor
	for i, expected := range want {
		next, err := NextReminderDue(domain.ReminderTiming{
			DueAt: due.UTC(), RecurrenceAnchor: anchor.UTC(), TimeZone: "UTC", Recurrence: domain.ReminderYearly,
		}, due)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !next.Equal(expected) {
			t.Fatalf("step %d: next = %s, want %s", i, next.UTC(), expected)
		}
		due = next
	}
}

// A reminder written before the anchor column existed carries a zero anchor;
// recurrence must still advance, falling back to the due instant.
func TestNextReminderDueWithoutAnchorFallsBackToDueInstant(t *testing.T) {
	due := time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC)
	next, err := NextReminderDue(domain.ReminderTiming{
		DueAt: due.UTC(), TimeZone: "UTC", Recurrence: domain.ReminderMonthly,
	}, due)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.February, 15, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next.UTC(), want)
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
	todo := domain.Todo{
		ID: "todo_wake", WorkspaceID: "T2", UserID: "U-T2", Title: "wake first",
		Reminder:  domain.ReminderTiming{DueAt: early, TimeZone: "UTC", RecurrenceAnchor: early},
		CreatedAt: early.Add(-time.Hour), UpdatedAt: early.Add(-time.Hour),
	}
	if err := source.CreateTodo(ctx, todo, events.Event{ID: "reminder-wake", WorkspaceID: "T2", Topic: "todo.created", Payload: "{}", CreatedAt: todo.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	// A channel reminder is a timer too, and an earlier one wins.
	channelDue := early.Add(-30 * time.Minute)
	channelReminder := domain.ChannelReminder{
		ID: "channel_reminder_wake", WorkspaceID: "T1", Creator: "U-T1", Channel: "C-T1", Text: "wake earliest",
		Reminder:  domain.ReminderTiming{DueAt: channelDue, TimeZone: "UTC", RecurrenceAnchor: channelDue},
		CreatedAt: early.Add(-time.Hour), UpdatedAt: early.Add(-time.Hour),
	}
	if err := source.CreateChannelReminder(ctx, channelReminder, events.Event{ID: "channel-wake", WorkspaceID: "T1", Topic: "channel_reminder.created", Payload: "{}", CreatedAt: channelReminder.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	publisher := &recordingProductDeadline{fence: 7}
	if err := PublishEarliestProductWakeDeadline(ctx, source, source, publisher); err != nil {
		t.Fatal(err)
	}
	if publisher.publishedFence != 7 || !publisher.deadline.Equal(channelDue) {
		t.Fatalf("published fence=%d deadline=%s, want fence 7 and %s", publisher.publishedFence, publisher.deadline, channelDue)
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

func seedTodo(t *testing.T, source *memory.Store, todo domain.Todo) {
	t.Helper()
	event := events.Event{
		ID: domain.EventID("created_" + todo.ID), WorkspaceID: todo.WorkspaceID,
		ActorID: todo.UserID, Topic: "todo.created", Payload: "{}", CreatedAt: todo.CreatedAt,
	}
	if err := source.CreateTodo(context.Background(), todo, event); err != nil {
		t.Fatal(err)
	}
}

func seedChannelReminder(t *testing.T, source *memory.Store, reminder domain.ChannelReminder) {
	t.Helper()
	event := events.Event{
		ID: domain.EventID("created_" + reminder.ID), WorkspaceID: reminder.WorkspaceID,
		ActorID: reminder.Creator, Topic: "channel_reminder.created", Payload: "{}", CreatedAt: reminder.CreatedAt,
	}
	if err := source.CreateChannelReminder(context.Background(), reminder, event); err != nil {
		t.Fatal(err)
	}
}

var errReminderAcknowledgement = errors.New("simulated acknowledgement loss")

type failFirstReminderAcknowledgement struct {
	*memory.Store
	failed bool
}

func (s *failFirstReminderAcknowledgement) MarkChannelReminderDelivered(ctx context.Context, owner string, id domain.ChannelReminderID, deliveredAt, nextDue time.Time, event events.Event) error {
	if !s.failed {
		s.failed = true
		return errReminderAcknowledgement
	}
	return s.Store.MarkChannelReminderDelivered(ctx, owner, id, deliveredAt, nextDue, event)
}

// A weekly reminder that names its weekdays steps from one named day to the
// next at the anchor's local time, wrapping into the following week, rather
// than a whole week from its last occurrence.
func TestNextRecurrenceStepsThroughTheNamedWeekdays(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tz database unavailable")
	}
	// Wednesday 2026-10-07 09:30 local.
	anchor := time.Date(2026, 10, 7, 9, 30, 0, 0, location)
	days := []time.Weekday{time.Monday, time.Wednesday, time.Friday}
	due := anchor.UTC()
	var got []string
	for range 4 {
		next, err := nextRecurrence(domain.ReminderWeekly, days, "America/New_York", anchor.UTC(), due, due)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, next.In(location).Format("Mon 01-02 15:04"))
		due = next
	}
	if want := "[Fri 10-09 09:30 Mon 10-12 09:30 Wed 10-14 09:30 Fri 10-16 09:30]"; fmt.Sprint(got) != want {
		t.Fatalf("occurrences = %v, want %s", got, want)
	}
	// Without weekdays a weekly reminder still advances a week at a time.
	if next, err := nextRecurrence(domain.ReminderWeekly, nil, "America/New_York", anchor.UTC(), anchor.UTC(), anchor.UTC()); err != nil || !next.Equal(anchor.AddDate(0, 0, 7).UTC()) {
		t.Fatalf("weekly without weekdays = %v err=%v", next, err)
	}
}

// "in a month" and "in a year" are one-time calendar steps that keep the time
// of day and clamp a day the target month lacks to its last day: January 31st
// plus a month is February 28th (29th in a leap year), not AddDate's March
// 3rd, and February 29th plus a year is February 28th.
func TestInMonthsPhraseClampsToTheTargetMonth(t *testing.T) {
	at := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, 10, 30, 0, 0, time.UTC)
	}
	for _, testCase := range []struct {
		phrase string
		now    time.Time
		want   time.Time
	}{
		{"pay rent in a month", at(2097, time.January, 31), at(2097, time.February, 28)},
		{"pay rent in 1 month", at(2096, time.January, 31), at(2096, time.February, 29)},
		{"pay rent in 3 months", at(2097, time.November, 30), at(2098, time.February, 28)},
		{"pay rent in 13 months", at(2097, time.January, 31), at(2098, time.February, 28)},
		{"pay rent in 2 months", at(2097, time.January, 15), at(2097, time.March, 15)},
		{"file taxes in a year", at(2096, time.February, 29), at(2097, time.February, 28)},
		{"file taxes in 4 years", at(2096, time.February, 29), at(2100, time.February, 28)},
	} {
		t.Run(testCase.phrase+" from "+testCase.now.Format("2006-01-02"), func(t *testing.T) {
			_, occurrence, err := domain.ParseReminderExpression(testCase.phrase, testCase.now, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if !occurrence.Due.Equal(testCase.want) || occurrence.Recurrence != domain.ReminderOnce {
				t.Fatalf("due = %s (%s), want %s once", occurrence.Due, occurrence.Recurrence, testCase.want)
			}
		})
	}
}
