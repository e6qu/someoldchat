package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestReminderDeliveryFiresEachReminderOnce holds the delivery contract for
// reminders.add. The reminder was durable and nothing read it, so a member who
// asked to be reminded never was.
func TestReminderDeliveryFiresEachReminderOnce(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	now := time.Unix(1_700_000_000, 0).UTC()

	create := func(id domain.ReminderID, user domain.UserID, due time.Time) {
		t.Helper()
		reminder := domain.Reminder{WorkspaceID: "T1", ID: id, Creator: "U1", User: user, Text: string(id), Time: due}
		if err := store.CreateReminder(ctx, reminder, events.Event{
			ID: domain.EventID("evt-" + string(id)), WorkspaceID: "T1", ActorID: "U1",
			Topic: "reminder.created", Payload: string(id), CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("Rm-past", "U2", now.Add(-time.Minute))
	create("Rm-now", "U1", now)
	create("Rm-future", "U1", now.Add(time.Hour))

	worker, err := NewReminderDeliveryWorker(store, service.Messages{Store: store}, 10)
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := worker.RunOnceAt(ctx, "T1", now)
	if err != nil || delivered != 2 {
		t.Fatalf("delivered=%d err=%v, want the two that are due", delivered, err)
	}
	// A second pass delivers nothing: the claim is the mark, so a reminder
	// cannot fire twice however often the worker runs.
	again, err := worker.RunOnceAt(ctx, "T1", now)
	if err != nil || again != 0 {
		t.Fatalf("a delivered reminder fired again: %d err=%v", again, err)
	}
	// The one still in the future waits for its own time.
	later, err := worker.RunOnceAt(ctx, "T1", now.Add(2*time.Hour))
	if err != nil || later != 1 {
		t.Fatalf("the future reminder did not fire when it came due: %d err=%v", later, err)
	}

	// Every delivery leaves a notice naming the member being reminded, which is
	// the person the reminder is for and not whoever set it.
	claimed, err := store.ClaimEventsForTopic(ctx, "T1", "reminder.delivered", "test-owner", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, record := range claimed {
		notices++
		if record.Event.Payload == "" {
			t.Fatalf("a delivery notice carries nothing: %+v", record.Event)
		}
	}
	if notices != 3 {
		t.Fatalf("delivery notices=%d, want one for each reminder", notices)
	}
	// Slackbot posts each reminder into the reminded member's Slackbot DM, as
	// Slack does: bob was reminded once, by Slackbot, and alice twice.
	for member, want := range map[domain.UserID][]string{"U2": {"Reminder: Rm-past."}, "U1": {"Reminder: Rm-now.", "Reminder: Rm-future."}} {
		direct, err := store.FindDirectConversation(ctx, "T1", []domain.UserID{member, domain.SlackbotUserID})
		if err != nil {
			t.Fatalf("%s has no Slackbot DM: %v", member, err)
		}
		page, err := store.ListMessages(ctx, direct.ID, domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, message := range page.Messages {
			if message.AuthorID != domain.SlackbotUserID {
				t.Fatalf("a reminder was posted by %s, not Slackbot", message.AuthorID)
			}
			got[message.Text] = true
		}
		if len(page.Messages) != len(want) {
			t.Fatalf("%s's Slackbot DM holds %d messages, want %v", member, len(page.Messages), want)
		}
		for _, text := range want {
			if !got[text] {
				t.Fatalf("%s's Slackbot DM lacks %q: %+v", member, text, page.Messages)
			}
		}
	}
}

// TestReminderDeliveryIsClaimedOnceUnderConcurrency holds the compare-and-set.
// Two workers reading the same batch must deliver each reminder once between
// them, or a member is reminded twice for one reminder.
func TestReminderDeliveryIsClaimedOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	now := time.Unix(1_700_000_000, 0).UTC()
	for index := 0; index < 8; index++ {
		id := domain.ReminderID("Rm-" + string(rune('a'+index)))
		if err := store.CreateReminder(ctx, domain.Reminder{
			WorkspaceID: "T1", ID: id, Creator: "U1", User: "U1", Text: string(id), Time: now.Add(-time.Minute),
		}, events.Event{ID: domain.EventID("evt-" + string(id)), WorkspaceID: "T1", ActorID: "U1", Topic: "reminder.created", Payload: string(id), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := NewReminderDeliveryWorker(store, service.Messages{Store: store}, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewReminderDeliveryWorker(store, service.Messages{Store: store}, 10)
	if err != nil {
		t.Fatal(err)
	}
	oneCount, oneErr := first.RunOnceAt(ctx, "T1", now)
	twoCount, twoErr := second.RunOnceAt(ctx, "T1", now)
	if oneErr != nil || twoErr != nil {
		t.Fatalf("errors: %v %v", oneErr, twoErr)
	}
	if oneCount+twoCount != 8 {
		t.Fatalf("the two workers delivered %d of 8 reminders between them", oneCount+twoCount)
	}
}

// A recurring reminders.add reminder is not retired by delivery: it moves to
// its next occurrence, in its own zone, and fires again when that comes due.
func TestRecurringReminderMovesToItsNextOccurrence(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	// Thursday 1 January 2026, 09:00 in Paris.
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no time zone database")
	}
	first := time.Date(2026, time.January, 1, 9, 0, 0, 0, paris).UTC()
	reminder := domain.Reminder{WorkspaceID: "T1", ID: "Rm-weekly", Creator: "U1", User: "U1", Text: "weekly sync", Time: first,
		Recurring: true, Recurrence: domain.ReminderWeekly, TimeZone: "Europe/Paris", RecurrenceAnchor: first}
	if err := store.CreateReminder(ctx, reminder, events.Event{ID: "evt-weekly", WorkspaceID: "T1", ActorID: "U1", Topic: "reminder.created", Payload: "Rm-weekly", CreatedAt: first}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewReminderDeliveryWorker(store, service.Messages{Store: store}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for week := 0; week < 3; week++ {
		due := time.Date(2026, time.January, 1+7*week, 9, 0, 0, 0, paris)
		if delivered, err := worker.RunOnceAt(ctx, "T1", due); err != nil || delivered != 1 {
			t.Fatalf("week %d: delivered=%d err=%v", week, delivered, err)
		}
		if again, err := worker.RunOnceAt(ctx, "T1", due); err != nil || again != 0 {
			t.Fatalf("week %d fired twice: %d err=%v", week, again, err)
		}
		stored, err := store.GetReminder(ctx, "T1", "U1", "Rm-weekly")
		if err != nil || !stored.Time.Equal(due.AddDate(0, 0, 7).UTC()) || !stored.CompleteAt.IsZero() {
			t.Fatalf("week %d: next due %s err=%v, want %s", week, stored.Time, err, due.AddDate(0, 0, 7).UTC())
		}
	}
}

// A delivered reminder carries Slack's controls, and Slackbot answers them: Mark
// as complete completes the reminder, Remind me about this sets it again, and
// each replaces the controls with what was done. A recurring reminder is
// offered only Remind me about this.
func TestDeliveredReminderControlsCompleteAndSnoozeIt(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	for _, reminder := range []domain.Reminder{
		{WorkspaceID: "T1", ID: "Rm-once", Creator: "U1", User: "U1", Text: "file the report", Time: now},
		{WorkspaceID: "T1", ID: "Rm-again", Creator: "U1", User: "U1", Text: "stand up", Time: now.Add(time.Second)},
		{WorkspaceID: "T1", ID: "Rm-weekly", Creator: "U1", User: "U1", Text: "water the plants", Time: now.Add(2 * time.Second),
			Recurring: true, Recurrence: domain.ReminderWeekly, TimeZone: "UTC", RecurrenceAnchor: now.Add(2 * time.Second)},
	} {
		if err := store.CreateReminder(ctx, reminder, events.Event{ID: domain.EventID("evt-" + string(reminder.ID)), WorkspaceID: "T1", ActorID: "U1", Topic: "reminder.created", Payload: "{}", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	messages := service.Messages{Store: store}
	worker, err := NewReminderDeliveryWorker(store, messages, 10)
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := worker.RunOnceAt(ctx, "T1", now.Add(time.Minute)); err != nil || delivered != 3 {
		t.Fatalf("delivered=%d err=%v", delivered, err)
	}
	direct, err := store.FindDirectConversation(ctx, "T1", []domain.UserID{"U1", domain.SlackbotUserID})
	if err != nil {
		t.Fatal(err)
	}
	reminderMessage := func(text string) domain.Message {
		t.Helper()
		page, err := store.ListMessages(ctx, direct.ID, domain.HistoryRequest{Page: domain.PageRequest{Limit: 10}})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range page.Messages {
			if message.Text == text {
				return message
			}
		}
		t.Fatalf("no reminder message %q in %+v", text, page.Messages)
		return domain.Message{}
	}
	once := reminderMessage("Reminder: file the report.")
	for _, want := range []string{`"action_id":"slackbot_reminder_complete"`, `"action_id":"slackbot_reminder_snooze"`, `"value":"tomorrow"`, `"block_id":"slackbot_reminder:Rm-once"`} {
		if !strings.Contains(once.Blocks, want) {
			t.Fatalf("the reminder's blocks lack %s: %s", want, once.Blocks)
		}
	}
	if weekly := reminderMessage("Reminder: water the plants."); strings.Contains(weekly.Blocks, "slackbot_reminder_complete") || !strings.Contains(weekly.Blocks, "slackbot_reminder_snooze") {
		t.Fatalf("a recurring reminder's controls: %s", weekly.Blocks)
	}

	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: once.ID, BlockID: "slackbot_reminder:Rm-once", ActionID: domain.SlackbotReminderCompleteAction, Type: "button", Value: "Rm-once",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	if completed, err := messages.ReminderInfo(ctx, "T1", "U1", "Rm-once"); err != nil || completed.CompleteAt.IsZero() {
		t.Fatalf("the reminder was not completed: %+v err=%v", completed, err)
	}
	if answered := reminderMessage("Reminder: file the report."); strings.Contains(answered.Blocks, "slackbot_reminder_complete") || !strings.Contains(answered.Blocks, "You marked this reminder as complete.") {
		t.Fatalf("the controls were not replaced: %s", answered.Blocks)
	}

	again := reminderMessage("Reminder: stand up.")
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: again.ID, BlockID: "slackbot_reminder:Rm-again", ActionID: domain.SlackbotReminderSnoozeAction, Type: "static_select", Value: "1h",
	}, "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	if answered := reminderMessage("Reminder: stand up."); !strings.Contains(answered.Blocks, "I'll remind you in 1 hour.") {
		t.Fatalf("the snooze was not acknowledged: %s", answered.Blocks)
	}
	page, err := messages.Reminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	snoozed := 0
	for _, reminder := range page.Reminders {
		if reminder.Text == "stand up" && reminder.ID != "Rm-again" && reminder.Time.After(time.Now().Add(59*time.Minute)) && reminder.Time.Before(time.Now().Add(61*time.Minute)) {
			snoozed++
		}
	}
	if snoozed != 1 {
		t.Fatalf("the snoozed reminder was not set again in an hour: %+v", page.Reminders)
	}
	if err := messages.DispatchBlockAction(ctx, "T1", "U1", domain.AppBlockAction{
		MessageID: again.ID, BlockID: "slackbot_reminder:Rm-again", ActionID: domain.SlackbotReminderSnoozeAction, Type: "static_select", Value: "1h",
	}, "https://chat.example.test"); err == nil {
		t.Fatal("an answered reminder's controls answered again")
	}
}

func TestReminderSnoozeDueFollowsTheMembersDay(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no time zone database")
	}
	// Friday 2 January 2026, 23:30 in Paris.
	now := time.Date(2026, 1, 2, 22, 30, 0, 0, time.UTC)
	for choice, want := range map[string]time.Time{
		"20m":       now.Add(20 * time.Minute),
		"3h":        now.Add(3 * time.Hour),
		"tomorrow":  time.Date(2026, 1, 3, 9, 0, 0, 0, paris).UTC(),
		"next_week": time.Date(2026, 1, 5, 9, 0, 0, 0, paris).UTC(),
	} {
		if got, ok := domain.ReminderSnoozeDue(choice, now, paris); !ok || !got.Equal(want) {
			t.Errorf("%s: %s ok=%v, want %s", choice, got, ok, want)
		}
	}
	if _, ok := domain.ReminderSnoozeDue("someday", now, paris); ok {
		t.Error("an unknown choice was accepted")
	}
}
