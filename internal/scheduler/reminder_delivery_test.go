package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
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

	worker, err := NewReminderDeliveryWorker(store, 10)
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
	first, err := NewReminderDeliveryWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewReminderDeliveryWorker(store, 10)
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
