package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestTodoLifecycleIsPrivateAndKeepsReminderAndDoneApart(t *testing.T) {
	ctx := context.Background()
	messages, _ := savedFixture(t)
	due := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	todo, err := messages.CreateTodo(ctx, "T1", "U1", domain.TodoRequest{
		Title: "  write the report  ", Details: "sections 2 and 3",
		Reminder: domain.ReminderTiming{DueAt: due, TimeZone: "Europe/Bucharest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if todo.Title != "write the report" || todo.Details != "sections 2 and 3" || !todo.Reminder.DueAt.Equal(due) ||
		!todo.Reminder.RecurrenceAnchor.Equal(due) || todo.Reminder.TimeZone != "Europe/Bucharest" || todo.Source.Set() {
		t.Fatalf("created to-do = %+v", todo)
	}
	if _, err := messages.TodoInfo(ctx, "T1", "U2", todo.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member read the to-do: %v", err)
	}
	if _, err := messages.EditTodo(ctx, "T1", "U2", todo.ID, domain.TodoEdit{Title: "taken"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member edited the to-do: %v", err)
	}
	if err := messages.SetTodoDone(ctx, "T1", "U2", todo.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member finished the to-do: %v", err)
	}
	if err := messages.DeleteTodo(ctx, "T1", "U2", todo.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member deleted the to-do: %v", err)
	}

	edited, err := messages.EditTodo(ctx, "T1", "U1", todo.ID, domain.TodoEdit{Title: "write the final report"})
	if err != nil || edited.Title != "write the final report" || edited.Details != "" || !edited.Reminder.DueAt.Equal(due) {
		t.Fatalf("edited to-do = %+v err=%v", edited, err)
	}
	cleared, err := messages.SetTodoReminder(ctx, "T1", "U1", todo.ID, domain.ReminderTiming{})
	if err != nil || cleared.Reminder.Scheduled() || cleared.Title != "write the final report" {
		t.Fatalf("Clear due date = %+v err=%v", cleared, err)
	}
	later := due.Add(24 * time.Hour)
	rescheduled, err := messages.SetTodoReminder(ctx, "T1", "U1", todo.ID, domain.ReminderTiming{DueAt: later, TimeZone: "UTC", Recurrence: domain.ReminderWeekly})
	if err != nil || !rescheduled.Reminder.DueAt.Equal(later) || rescheduled.Reminder.Recurrence != domain.ReminderWeekly {
		t.Fatalf("Edit reminder = %+v err=%v", rescheduled, err)
	}
	if _, err := messages.SetTodoReminder(ctx, "T1", "U1", todo.ID, domain.ReminderTiming{DueAt: time.Now().Add(-time.Minute), TimeZone: "UTC"}); !errors.Is(err, domain.ErrReminderTimeInPast) {
		t.Fatalf("a reminder in the past = %v", err)
	}

	if err := messages.SetTodoDone(ctx, "T1", "U1", todo.ID, true); err != nil {
		t.Fatal(err)
	}
	first, err := messages.TodoInfo(ctx, "T1", "U1", todo.ID)
	if err != nil || !first.Done() || !first.Reminder.DueAt.Equal(later) {
		t.Fatalf("done to-do = %+v err=%v", first, err)
	}
	if err := messages.SetTodoDone(ctx, "T1", "U1", todo.ID, true); err != nil {
		t.Fatal(err)
	}
	again, err := messages.TodoInfo(ctx, "T1", "U1", todo.ID)
	if err != nil || !again.CompletedAt.Equal(first.CompletedAt) {
		t.Fatalf("marking done twice moved the done instant: %+v err=%v", again, err)
	}
	open, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(open.Items) != 0 {
		t.Fatalf("open to-dos include a done one: %+v err=%v", open, err)
	}
	done, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Done: true, Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(done.Items) != 1 {
		t.Fatalf("done to-dos = %+v err=%v", done, err)
	}
	if err := messages.SetTodoDone(ctx, "T1", "U1", todo.ID, false); err != nil {
		t.Fatal(err)
	}
	if reopened, err := messages.TodoInfo(ctx, "T1", "U1", todo.ID); err != nil || reopened.Done() {
		t.Fatalf("reopened to-do = %+v err=%v", reopened, err)
	}
	if err := messages.DeleteTodo(ctx, "T1", "U1", todo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.TodoInfo(ctx, "T1", "U1", todo.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted to-do is still readable: %v", err)
	}
}

func TestTodoRequestsAreValidatedBeforeAnythingIsStored(t *testing.T) {
	ctx := context.Background()
	messages, _ := savedFixture(t)
	future := time.Now().UTC().Add(time.Hour)
	for name, request := range map[string]domain.TodoRequest{
		"no title":                     {Title: "   "},
		"a title that is too long":     {Title: string(make([]byte, domain.TodoTitleLimit+1))},
		"a source without a timestamp": {Title: "x", SourceChannel: "C1"},
		"a repeat without a due date":  {Title: "x", Reminder: domain.ReminderTiming{Recurrence: domain.ReminderDaily}},
		"an unknown repeat":            {Title: "x", Reminder: domain.ReminderTiming{DueAt: future, Recurrence: "hourly"}},
		"an unknown zone":              {Title: "x", Reminder: domain.ReminderTiming{DueAt: future, TimeZone: "Mars/Olympus"}},
	} {
		if _, err := messages.CreateTodo(ctx, "T1", "U1", request); !errors.Is(err, domain.ErrInvalidTodo) && !errors.Is(err, domain.ErrInvalidReminderRequest) {
			t.Errorf("%s: err=%v, want an invalid-request error", name, err)
		}
	}
	if _, err := messages.CreateTodo(ctx, "T1", "U1", domain.TodoRequest{Title: "x", Reminder: domain.ReminderTiming{DueAt: time.Now().Add(-time.Minute)}}); !errors.Is(err, domain.ErrReminderTimeInPast) {
		t.Errorf("a reminder in the past: err=%v", err)
	}
	if page, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Page: domain.PageRequest{Limit: 10}}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a refused request stored a to-do: %+v err=%v", page, err)
	}
}

// "Remind me about this" names the to-do after the message, keeps the
// message as its source, and needs a message the member can read.
func TestRemindMeAboutThisMakesATodoLinkedToTheMessage(t *testing.T) {
	ctx := context.Background()
	messages, repository := savedFixture(t)
	message, err := messages.Post(ctx, "T1", "U1", "C1", "ship the release notes\nand tell the team", "", "")
	if err != nil {
		t.Fatal(err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	due := time.Now().UTC().Add(20 * time.Minute)
	todo, err := messages.CreateTodo(ctx, "T1", "U2", domain.TodoRequest{SourceChannel: "C1", SourceTimestamp: timestamp, Reminder: domain.ReminderTiming{DueAt: due, TimeZone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	if todo.Title != "ship the release notes" || todo.Source.MessageID != message.ID || todo.Source.Timestamp != timestamp {
		t.Fatalf("message to-do = %+v", todo)
	}
	if err := repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateTodo(ctx, "T1", "U3", domain.TodoRequest{SourceChannel: "C1", SourceTimestamp: timestamp, Reminder: domain.ReminderTiming{DueAt: due, TimeZone: "UTC"}}); err == nil {
		t.Fatal("a member outside the private channel made a to-do of its message")
	}
}

// The To-dos filter groups by reminder (overdue, upcoming, none) and the
// three sorts page completely, in order, without repeating a to-do.
func TestTodosFilterAndSortPageEveryToDoOnce(t *testing.T) {
	ctx := context.Background()
	messages, repository := savedFixture(t)
	now := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	created := now.Add(-48 * time.Hour)
	seed := func(id string, createdOffset time.Duration, due time.Time) {
		t.Helper()
		todo := domain.Todo{ID: domain.TodoID(id), WorkspaceID: "T1", UserID: "U1", Title: id, CreatedAt: created.Add(createdOffset), UpdatedAt: created.Add(createdOffset)}
		if !due.IsZero() {
			todo.Reminder = domain.ReminderTiming{DueAt: due, TimeZone: "UTC", RecurrenceAnchor: due}
		}
		if err := repository.CreateTodo(ctx, todo, events.Event{ID: domain.EventID("E-" + id), WorkspaceID: "T1", Topic: "todo.created", CreatedAt: todo.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	seed("a-overdue-late", 1*time.Hour, now.Add(-time.Hour))
	seed("b-overdue-early", 2*time.Hour, now.Add(-3*time.Hour))
	seed("c-upcoming", 3*time.Hour, now.Add(time.Hour))
	seed("d-none", 4*time.Hour, time.Time{})
	seed("e-none", 5*time.Hour, time.Time{})
	seed("f-upcoming-tie", 5*time.Hour, now.Add(time.Hour))
	if err := repository.CreateTodo(ctx, domain.Todo{ID: "z-other", WorkspaceID: "T1", UserID: "U2", Title: "not yours", CreatedAt: created, UpdatedAt: created}, events.Event{ID: "E-other", WorkspaceID: "T1", Topic: "todo.created"}); err != nil {
		t.Fatal(err)
	}
	walk := func(query domain.TodoQuery) []domain.TodoID {
		t.Helper()
		query.Now = now
		query.Page = domain.PageRequest{Limit: 2}
		var ids []domain.TodoID
		for {
			page, err := messages.Todos(ctx, "T1", "U1", query)
			if err != nil {
				t.Fatal(err)
			}
			for _, todo := range page.Items {
				ids = append(ids, todo.ID)
			}
			if !page.HasMore {
				return ids
			}
			query.Page.Cursor = page.NextCursor
		}
	}
	equal := func(name string, got []domain.TodoID, want ...domain.TodoID) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("%s = %v, want %v", name, got, want)
			}
		}
	}
	equal("due date", walk(domain.TodoQuery{Sort: domain.TodoSortDueDate}), "b-overdue-early", "a-overdue-late", "c-upcoming", "f-upcoming-tie", "d-none", "e-none")
	equal("earliest created", walk(domain.TodoQuery{Sort: domain.TodoSortEarliestFirst}), "a-overdue-late", "b-overdue-early", "c-upcoming", "d-none", "e-none", "f-upcoming-tie")
	equal("latest created", walk(domain.TodoQuery{Sort: domain.TodoSortLatestFirst}), "f-upcoming-tie", "e-none", "d-none", "c-upcoming", "b-overdue-early", "a-overdue-late")
	equal("overdue", walk(domain.TodoQuery{Sort: domain.TodoSortDueDate, Reminders: []domain.TodoReminderGroup{domain.TodoOverdue}}), "b-overdue-early", "a-overdue-late")
	equal("upcoming and none", walk(domain.TodoQuery{Sort: domain.TodoSortEarliestFirst, Reminders: []domain.TodoReminderGroup{domain.TodoUpcoming, domain.TodoNoReminder}}), "c-upcoming", "d-none", "e-none", "f-upcoming-tie")

	page, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Sort: domain.TodoSortDueDate, Now: now, Page: domain.PageRequest{Limit: 2}})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page = %+v err=%v", page, err)
	}
	if _, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Sort: domain.TodoSortLatestFirst, Now: now, Page: domain.PageRequest{Limit: 2, Cursor: page.NextCursor}}); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("a due-date cursor under another sort = %v, want an invalid cursor", err)
	}
	if _, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Sort: "priority", Page: domain.PageRequest{Limit: 2}}); !errors.Is(err, domain.ErrInvalidTodo) {
		t.Fatalf("an unknown sort = %v", err)
	}
}

func TestChannelRemindersAreListedAndDeletedByTheirCreatorOnly(t *testing.T) {
	ctx := context.Background()
	messages, repository := savedFixture(t)
	due := time.Now().UTC().Add(time.Hour)
	reminder, err := messages.CreateChannelReminder(ctx, "T1", "U1", domain.ChannelReminderRequest{Channel: "C1", Text: "stand-up", Reminder: domain.ReminderTiming{DueAt: due, TimeZone: "UTC", Recurrence: domain.ReminderWeekly}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateChannelReminder(ctx, "T1", "U1", domain.ChannelReminderRequest{Channel: "C1", Text: "no date"}); !errors.Is(err, domain.ErrInvalidReminderRequest) {
		t.Fatalf("a channel reminder without a date = %v", err)
	}
	if err := repository.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateChannelReminder(ctx, "T1", "U3", domain.ChannelReminderRequest{Channel: "C1", Text: "outsider", Reminder: domain.ReminderTiming{DueAt: due, TimeZone: "UTC"}}); err == nil {
		t.Fatal("a member outside the channel set a reminder in it")
	}
	if page, err := messages.ChannelReminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 1 || page.Items[0].ID != reminder.ID {
		t.Fatalf("/remind list = %+v err=%v", page, err)
	}
	if page, err := messages.ChannelReminders(ctx, "T1", "U2", domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("another member's /remind list = %+v err=%v", page, err)
	}
	if todos, err := messages.Todos(ctx, "T1", "U1", domain.TodoQuery{Page: domain.PageRequest{Limit: 10}}); err != nil || len(todos.Items) != 0 {
		t.Fatalf("a channel reminder appeared in To-dos: %+v err=%v", todos, err)
	}
	if err := messages.DeleteChannelReminder(ctx, "T1", "U2", reminder.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member deleted the channel reminder: %v", err)
	}
	if err := messages.DeleteChannelReminder(ctx, "T1", "U1", reminder.ID); err != nil {
		t.Fatal(err)
	}
	if page, err := messages.ChannelReminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("/remind list after delete = %+v err=%v", page, err)
	}
}
