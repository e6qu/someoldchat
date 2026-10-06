package qualification

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// savedItemsAgreeAcrossProfiles is the Saved section of Home: newest first,
// paged without repeats, idempotent per message, cleared and moved to To-dos
// in one step, and private to its member on every profile.
func savedItemsAgreeAcrossProfiles(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()
	other := f.secondMember(t, ctx)
	base := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	var messages []domain.Message
	var saved []domain.SavedItemID
	for index := range 3 {
		message := f.message(t, ctx, fmt.Sprintf("saved-%d", index), base.Add(time.Duration(index)*time.Minute))
		messages = append(messages, message)
		item := domain.SavedItem{
			ID: domain.SavedItemID(fmt.Sprintf("SV-%d-%s", index, f.suffix)), WorkspaceID: f.workspaceID, UserID: f.userID,
			MessageID: message.ID, Conversation: f.channelID, CreatedAt: base.Add(time.Duration(index) * time.Hour),
		}
		created, isNew, err := f.repository.CreateSavedItem(ctx, item, f.event(fmt.Sprintf("saved-%d", index), "saved_item.created", string(item.ID)))
		if err != nil || !isNew || created.ID != item.ID || created.SourceAvailable {
			t.Fatalf("create saved item=%+v new=%v err=%v", created, isNew, err)
		}
		saved = append(saved, item.ID)
	}
	again, isNew, err := f.repository.CreateSavedItem(ctx, domain.SavedItem{
		ID: domain.SavedItemID("SV-again-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID,
		MessageID: messages[0].ID, Conversation: f.channelID, CreatedAt: base,
	}, f.event("saved-again", "saved_item.created", "again"))
	if err != nil || isNew || again.ID != saved[0] {
		t.Fatalf("saving a saved message again = %+v new=%v err=%v, want the existing item", again, isNew, err)
	}
	if _, _, err := f.repository.CreateSavedItem(ctx, domain.SavedItem{
		ID: domain.SavedItemID("SV-other-" + f.suffix), WorkspaceID: f.workspaceID, UserID: other,
		MessageID: messages[0].ID, Conversation: f.channelID, CreatedAt: base,
	}, f.event("saved-other", "saved_item.created", "other")); err != nil {
		t.Fatal(err)
	}

	var listed []domain.SavedItemID
	request := domain.PageRequest{Limit: 2}
	for {
		page, err := f.repository.ListSavedItems(ctx, f.workspaceID, f.userID, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			listed = append(listed, item.ID)
			if !item.CreatedAt.Equal(base.Add(time.Duration(len(saved)-len(listed)) * time.Hour)) {
				t.Fatalf("saved item %s reads back created at %s", item.ID, item.CreatedAt)
			}
		}
		if !page.HasMore {
			break
		}
		request.Cursor = page.NextCursor
	}
	if fmt.Sprint(listed) != fmt.Sprint([]domain.SavedItemID{saved[2], saved[1], saved[0]}) {
		t.Fatalf("Saved lists %v, want newest first %v", listed, []domain.SavedItemID{saved[2], saved[1], saved[0]})
	}
	if _, err := f.repository.ListSavedItems(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 2, Descending: true}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("a descending Saved page = %v, want invalid argument: Saved has one order", err)
	}
	if byMessage, err := f.repository.GetSavedItemByMessage(ctx, f.workspaceID, f.userID, messages[1].ID); err != nil || byMessage.ID != saved[1] {
		t.Fatalf("saved by message=%+v err=%v", byMessage, err)
	}
	batch, err := f.repository.ListSavedItemsForMessages(ctx, f.workspaceID, f.userID, []domain.MessageID{messages[0].ID, messages[2].ID, "missing"})
	if err != nil || len(batch) != 2 {
		t.Fatalf("saved batch=%+v err=%v", batch, err)
	}

	todo := domain.Todo{
		ID: domain.TodoID("TD-moved-" + f.suffix), WorkspaceID: f.workspaceID, UserID: f.userID, Title: "from saved",
		Source:    domain.TodoSource{MessageID: messages[1].ID, Conversation: f.channelID, Timestamp: domain.NewMessageTimestamp(messages[1].CreatedAt)},
		CreatedAt: base, UpdatedAt: base,
	}
	wrongSource := todo
	wrongSource.Source.MessageID = messages[2].ID
	if err := f.repository.MoveSavedItemToTodo(ctx, f.workspaceID, f.userID, saved[1], wrongSource, f.event("move-wrong", "todo.created", "wrong")); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("moving to a to-do linking another message = %v, want invalid argument", err)
	}
	if err := f.repository.MoveSavedItemToTodo(ctx, f.workspaceID, other, saved[1], todo, f.event("move-other", "todo.created", "other")); !errors.Is(err, store.ErrInvalidArgument) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member moved the saved item: %v", err)
	}
	if err := f.repository.MoveSavedItemToTodo(ctx, f.workspaceID, f.userID, saved[1], todo, f.event("move", "todo.created", "move")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.GetSavedItem(ctx, f.workspaceID, f.userID, saved[1]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the moved item is still saved: %v", err)
	}
	if moved, err := f.repository.GetTodo(ctx, f.workspaceID, f.userID, todo.ID); err != nil || moved.Source != todo.Source || moved.Title != "from saved" {
		t.Fatalf("moved to-do=%+v err=%v", moved, err)
	}
	if err := f.repository.MoveSavedItemToTodo(ctx, f.workspaceID, f.userID, saved[1], todo, f.event("move-twice", "todo.created", "twice")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("moving a moved item = %v, want not found", err)
	}

	cleared, err := f.repository.ClearSavedItems(ctx, f.workspaceID, f.userID, f.event("clear", "saved_item.cleared", "clear"))
	if err != nil || cleared != 2 {
		t.Fatalf("cleared=%d err=%v, want the two items left", cleared, err)
	}
	if cleared, err := f.repository.ClearSavedItems(ctx, f.workspaceID, f.userID, f.event("clear-again", "saved_item.cleared", "again")); err != nil || cleared != 0 {
		t.Fatalf("clearing an empty Saved=%d err=%v", cleared, err)
	}
	if page, err := f.repository.ListSavedItems(ctx, f.workspaceID, other, domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 1 {
		t.Fatalf("clean-up reached another member's Saved: %+v err=%v", page, err)
	}
}

// todosAgreeAcrossProfiles is the To-dos tab's storage: validation, the
// reminder groups and the three sorts, edits that keep or restart delivery,
// done and not done, and the delivery lease, on every profile.
func todosAgreeAcrossProfiles(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()
	now := time.Now().UTC().Truncate(time.Second)
	created := now.Add(-48 * time.Hour)
	id := func(name string) domain.TodoID { return domain.TodoID("TD-" + name + "-" + f.suffix) }
	seed := func(name string, offset time.Duration, due time.Time) domain.Todo {
		t.Helper()
		todo := domain.Todo{ID: id(name), WorkspaceID: f.workspaceID, UserID: f.userID, Title: name, CreatedAt: created.Add(offset), UpdatedAt: created.Add(offset)}
		if !due.IsZero() {
			todo.Reminder = domain.ReminderTiming{DueAt: due, TimeZone: "Europe/Bucharest", RecurrenceAnchor: due}
		}
		if err := f.repository.CreateTodo(ctx, todo, f.event("todo-"+name, "todo.created", name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return todo
	}
	if err := f.repository.CreateTodo(ctx, domain.Todo{ID: id("untitled"), WorkspaceID: f.workspaceID, UserID: f.userID, Title: " ", CreatedAt: created, UpdatedAt: created}, f.event("todo-untitled", "todo.created", "x")); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("an untitled to-do = %v, want invalid argument", err)
	}
	if err := f.repository.CreateTodo(ctx, domain.Todo{ID: id("lost-source"), WorkspaceID: f.workspaceID, UserID: f.userID, Title: "x",
		Source:    domain.TodoSource{MessageID: "missing-" + domain.MessageID(f.suffix), Conversation: f.channelID, Timestamp: "1.000001"},
		CreatedAt: created, UpdatedAt: created}, f.event("todo-lost", "todo.created", "x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a to-do linking a missing message = %v, want not found", err)
	}
	overdue := seed("overdue", time.Hour, now.Add(-time.Hour))
	seed("upcoming", 2*time.Hour, now.Add(time.Hour))
	seed("none", 3*time.Hour, time.Time{})
	seed("tie", 3*time.Hour, now.Add(time.Hour))
	if err := f.repository.CreateTodo(ctx, overdue, f.event("todo-duplicate", "todo.created", "dup")); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("a duplicate to-do = %v, want already exists", err)
	}

	walk := func(query domain.TodoQuery) []string {
		t.Helper()
		query.Now = now
		query.Page = domain.PageRequest{Limit: 1}
		var titles []string
		for {
			page, err := f.repository.ListTodos(ctx, f.workspaceID, f.userID, query)
			if err != nil {
				t.Fatal(err)
			}
			for _, todo := range page.Items {
				titles = append(titles, todo.Title)
			}
			if !page.HasMore {
				return titles
			}
			query.Page.Cursor = page.NextCursor
		}
	}
	// The tie on creation is broken by identifier, and "tie" sorts after
	// "none" in the identifier because both carry the same suffix.
	for name, testCase := range map[string]struct {
		query domain.TodoQuery
		want  string
	}{
		"due date":         {domain.TodoQuery{Sort: domain.TodoSortDueDate}, "[overdue tie upcoming none]"},
		"earliest created": {domain.TodoQuery{Sort: domain.TodoSortEarliestFirst}, "[overdue upcoming none tie]"},
		"latest created":   {domain.TodoQuery{Sort: domain.TodoSortLatestFirst}, "[tie none upcoming overdue]"},
		"overdue":          {domain.TodoQuery{Sort: domain.TodoSortDueDate, Reminders: []domain.TodoReminderGroup{domain.TodoOverdue}}, "[overdue]"},
		"upcoming or none": {domain.TodoQuery{Sort: domain.TodoSortEarliestFirst, Reminders: []domain.TodoReminderGroup{domain.TodoUpcoming, domain.TodoNoReminder}}, "[upcoming none tie]"},
	} {
		if got := fmt.Sprint(walk(testCase.query)); got != testCase.want {
			t.Errorf("%s = %s, want %s", name, got, testCase.want)
		}
	}
	if _, err := f.repository.ListTodos(ctx, f.workspaceID, f.userID, domain.TodoQuery{Sort: "priority", Now: now, Page: domain.PageRequest{Limit: 1}}); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("an unknown sort = %v, want invalid argument", err)
	}

	edited := overdue
	edited.Title, edited.Details, edited.UpdatedAt = "overdue, renamed", "details", now
	if stored, err := f.repository.UpdateTodo(ctx, edited, f.event("todo-edit", "todo.changed", "edit")); err != nil || stored.Title != "overdue, renamed" || !stored.Reminder.DueAt.Equal(overdue.Reminder.DueAt) {
		t.Fatalf("edited to-do=%+v err=%v", stored, err)
	}
	moved := edited
	moved.Source = domain.TodoSource{MessageID: "M-x", Conversation: f.channelID, Timestamp: "1.000001"}
	if _, err := f.repository.UpdateTodo(ctx, moved, f.event("todo-move", "todo.changed", "move")); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("changing a to-do's source = %v, want invalid argument", err)
	}

	claimed, err := f.repository.ClaimDueTodoReminders(ctx, f.workspaceID, "worker", 10, time.Minute, now)
	if err != nil || len(claimed) != 1 || claimed[0].ID != overdue.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := f.repository.DeleteTodo(ctx, f.workspaceID, f.userID, overdue.ID, f.event("todo-delete-leased", "todo.deleted", "leased")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete under the delivery lease = %v, want not found", err)
	}
	if err := f.repository.RenewTodoReminder(ctx, "someone-else", overdue.ID, time.Minute, now); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("another owner renewed the lease: %v", err)
	}
	if err := f.repository.MarkTodoReminderDelivered(ctx, "worker", overdue.ID, now, time.Time{}, f.event("todo-delivered", "todo.reminder_delivered", "delivered")); err != nil {
		t.Fatal(err)
	}
	delivered, err := f.repository.GetTodo(ctx, f.workspaceID, f.userID, overdue.ID)
	if err != nil || delivered.Done() || !delivered.Reminder.DueAt.Equal(overdue.Reminder.DueAt) || !delivered.Delivery.LastDeliveredAt.Equal(now) || !delivered.Badged() {
		t.Fatalf("delivered to-do=%+v err=%v: its reminder came due, it is not done", delivered, err)
	}
	if again, err := f.repository.ClaimDueTodoReminders(ctx, f.workspaceID, "worker", 10, time.Minute, now.Add(time.Minute)); err != nil || len(again) != 0 {
		t.Fatalf("a delivered one-time reminder was claimed again: %+v err=%v", again, err)
	}
	activity, err := f.repository.ListActivity(ctx, f.workspaceID, f.userID, domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReminder}, Page: domain.PageRequest{Limit: 10}})
	if err != nil || len(activity.Items) != 1 || activity.Items[0].TodoID != overdue.ID || activity.Items[0].Todo.Title != "overdue, renamed" {
		t.Fatalf("reminder Activity=%+v err=%v", activity.Items, err)
	}
	if err := f.repository.AcknowledgeTodoReminders(ctx, f.workspaceID, f.userID, now.Add(time.Second), f.event("todo-ack", "todo.acknowledged", "ack")); err != nil {
		t.Fatal(err)
	}
	if acknowledged, err := f.repository.GetTodo(ctx, f.workspaceID, f.userID, overdue.ID); err != nil || acknowledged.Badged() {
		t.Fatalf("acknowledged to-do=%+v err=%v", acknowledged, err)
	}

	rescheduled := edited
	later := now.Add(3 * time.Hour)
	rescheduled.Reminder = domain.ReminderTiming{DueAt: later, TimeZone: "UTC", Recurrence: domain.ReminderDaily, RecurrenceAnchor: later}
	if stored, err := f.repository.UpdateTodo(ctx, rescheduled, f.event("todo-reschedule", "todo.changed", "reschedule")); err != nil ||
		!stored.Reminder.DueAt.Equal(later) || !stored.Delivery.LastDeliveredAt.IsZero() || stored.Reminder.Recurrence != domain.ReminderDaily {
		t.Fatalf("a new reminder did not restart delivery: %+v err=%v", stored, err)
	}
	if err := f.repository.SetTodoCompletion(ctx, f.workspaceID, f.userID, overdue.ID, now, f.event("todo-done", "todo.changed", "done")); err != nil {
		t.Fatal(err)
	}
	if err := f.repository.SetTodoCompletion(ctx, f.workspaceID, f.userID, overdue.ID, now.Add(time.Hour), f.event("todo-done-again", "todo.changed", "again")); err != nil {
		t.Fatal(err)
	}
	if done, err := f.repository.GetTodo(ctx, f.workspaceID, f.userID, overdue.ID); err != nil || !done.CompletedAt.Equal(now) {
		t.Fatalf("done to-do=%+v err=%v: marking it done again must keep the first instant", done, err)
	}
	if earliest, err := f.repository.EarliestTodoReminder(ctx, f.workspaceID); err != nil || !earliest.Equal(now.Add(time.Hour)) {
		t.Fatalf("earliest to-do reminder=%s err=%v, want the upcoming one and not the done one", earliest, err)
	}
	if done := walk(domain.TodoQuery{Done: true, Sort: domain.TodoSortDueDate}); fmt.Sprint(done) != "[overdue, renamed]" {
		t.Fatalf("done to-dos=%v", done)
	}
	if err := f.repository.SetTodoCompletion(ctx, f.workspaceID, f.userID, overdue.ID, time.Time{}, f.event("todo-undone", "todo.changed", "undone")); err != nil {
		t.Fatal(err)
	}
	if reopened, err := f.repository.GetTodo(ctx, f.workspaceID, f.userID, overdue.ID); err != nil || reopened.Done() {
		t.Fatalf("reopened to-do=%+v err=%v", reopened, err)
	}
	if err := f.repository.DeleteTodo(ctx, f.workspaceID, f.userID, overdue.ID, f.event("todo-delete", "todo.deleted", "delete")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repository.GetTodo(ctx, f.workspaceID, f.userID, overdue.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted to-do err=%v", err)
	}
	if activity, err := f.repository.ListActivity(ctx, f.workspaceID, f.userID, domain.ActivityQuery{Kinds: []domain.ActivityKind{domain.ActivityReminder}, Page: domain.PageRequest{Limit: 10}}); err != nil || len(activity.Items) != 0 {
		t.Fatalf("a deleted to-do left its Activity row: %+v err=%v", activity.Items, err)
	}
}

// channelRemindersAgreeAcrossProfiles is /remind #channel's storage: listed
// by creator, delivered on a recurrence without completing, and deleted
// with the channel.
func channelRemindersAgreeAcrossProfiles(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()
	other := f.secondMember(t, ctx)
	now := time.Now().UTC().Truncate(time.Second)
	due := now.Add(-time.Minute)
	reminder := domain.ChannelReminder{
		ID: domain.ChannelReminderID("CR-" + f.suffix), WorkspaceID: f.workspaceID, Creator: f.userID, Channel: f.channelID, Text: "stand-up",
		Reminder:  domain.ReminderTiming{DueAt: due, TimeZone: "UTC", Recurrence: domain.ReminderWeekly, RecurrenceAnchor: due},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	undated := reminder
	undated.ID, undated.Reminder = domain.ChannelReminderID("CR-undated-"+f.suffix), domain.ReminderTiming{}
	if err := f.repository.CreateChannelReminder(ctx, undated, f.event("channel-undated", "channel_reminder.created", "x")); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("a channel reminder without a due date = %v, want invalid argument", err)
	}
	nowhere := reminder
	nowhere.ID, nowhere.Channel = domain.ChannelReminderID("CR-nowhere-"+f.suffix), domain.ConversationID("C-missing-"+f.suffix)
	if err := f.repository.CreateChannelReminder(ctx, nowhere, f.event("channel-nowhere", "channel_reminder.created", "x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a channel reminder for a missing channel = %v, want not found", err)
	}
	if err := f.repository.CreateChannelReminder(ctx, reminder, f.event("channel", "channel_reminder.created", "x")); err != nil {
		t.Fatal(err)
	}
	if page, err := f.repository.ListChannelReminders(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 1 || page.Items[0].Text != "stand-up" {
		t.Fatalf("/remind list=%+v err=%v", page, err)
	}
	if page, err := f.repository.ListChannelReminders(ctx, f.workspaceID, other, domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("another member's /remind list=%+v err=%v", page, err)
	}
	if earliest, err := f.repository.EarliestChannelReminder(ctx, f.workspaceID); err != nil || !earliest.Equal(due) {
		t.Fatalf("earliest channel reminder=%s err=%v", earliest, err)
	}
	claimed, err := f.repository.ClaimDueChannelReminders(ctx, f.workspaceID, "worker", 10, time.Minute, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := f.repository.DeleteChannelReminder(ctx, f.workspaceID, f.userID, reminder.ID, f.event("channel-delete-leased", "channel_reminder.deleted", "x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete under the delivery lease = %v, want not found", err)
	}
	next := due.AddDate(0, 0, 7)
	if err := f.repository.MarkChannelReminderDelivered(ctx, "worker", reminder.ID, now, next, f.event("channel-delivered", "channel_reminder.delivered", "x")); err != nil {
		t.Fatal(err)
	}
	stored, err := f.repository.GetChannelReminder(ctx, f.workspaceID, f.userID, reminder.ID)
	if err != nil || !stored.Reminder.DueAt.Equal(next) || !stored.Delivery.LastDeliveredAt.Equal(now) || stored.Finished() {
		t.Fatalf("delivered recurring channel reminder=%+v err=%v", stored, err)
	}
	if err := f.repository.DeleteChannelReminder(ctx, f.workspaceID, other, reminder.ID, f.event("channel-delete-other", "channel_reminder.deleted", "x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member deleted the channel reminder: %v", err)
	}
	if err := f.repository.DeleteConversation(ctx, f.workspaceID, f.channelID, f.event("channel-gone", "conversation.deleted", string(f.channelID))); err != nil {
		t.Fatal(err)
	}
	if page, err := f.repository.ListChannelReminders(ctx, f.workspaceID, f.userID, domain.PageRequest{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a deleted channel kept its reminders: %+v err=%v", page, err)
	}
}
