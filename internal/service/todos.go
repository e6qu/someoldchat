package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// This file is Slack's replacement for Later: the Saved section of Home (a
// member's private bookmarks on messages and files), the To-dos tab (work
// with an optional reminder and a done state), and the channel reminders
// /remind sets. None of them has a Slack app API — "There are no direct APIs
// for Save it for Later to integrate with" — so they are first-party state
// only, distinct from the deprecated stars.* and reminders.* app contracts.

// --- Saved ---

func savedItemPayload(topic string, item domain.SavedItem) events.Payload {
	return events.NewPayload(topic,
		events.String("saved_item_id", string(item.ID)),
		events.String("message_id", string(item.MessageID)),
		events.String("channel_id", string(item.Conversation)),
		events.String("user_id", string(item.UserID)),
	)
}

// AddToSaved is the "Add to saved" message action. It must not call AddStar:
// Slack retired that relationship in 2023 and saved items are neither written
// nor returned through the deprecated stars.* methods. Saving a message that
// is already saved answers the existing item.
func (m Messages) AddToSaved(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.SavedItem, error) {
	message, err := m.messageForTimestamp(ctx, workspaceID, userID, conversationID, timestamp)
	if err != nil {
		return domain.SavedItem{}, err
	}
	id, err := domain.NewSavedItemID()
	if err != nil {
		return domain.SavedItem{}, err
	}
	now := time.Now().UTC()
	item := domain.SavedItem{
		ID: id, WorkspaceID: workspaceID, UserID: userID, MessageID: message.ID,
		Conversation: conversationID, CreatedAt: now,
	}
	event, err := newEvent(workspaceID, userID, savedItemPayload("saved_item.created", item), now)
	if err != nil {
		return domain.SavedItem{}, err
	}
	item, _, err = m.Store.CreateSavedItem(ctx, item, event)
	if err != nil {
		return domain.SavedItem{}, err
	}
	item.Message = message
	item.SourceAvailable = !message.Deleted
	return item, nil
}

func (m Messages) SavedItemForMessage(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, messageID domain.MessageID) (domain.SavedItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.SavedItem{}, err
	}
	item, err := m.Store.GetSavedItemByMessage(ctx, workspaceID, userID, messageID)
	if err != nil {
		return domain.SavedItem{}, err
	}
	return m.savedItemWithSource(ctx, item)
}

func (m Messages) SavedItemsForMessages(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, messageIDs []domain.MessageID) ([]domain.SavedItem, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if len(messageIDs) > 200 {
		return nil, store.InvalidArgument("too many saved item message ids")
	}
	return m.Store.ListSavedItemsForMessages(ctx, workspaceID, userID, messageIDs)
}

// SavedItems lists the Saved section of Home, newest first.
func (m Messages) SavedItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.SavedItemPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.SavedItemPage{}, err
	}
	page, err := m.Store.ListSavedItems(ctx, workspaceID, userID, request)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	for index := range page.Items {
		page.Items[index], err = m.savedItemWithSource(ctx, page.Items[index])
		if err != nil {
			return domain.SavedItemPage{}, err
		}
	}
	return page, nil
}

func (m Messages) RemoveSavedItem(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	item, err := m.Store.GetSavedItem(ctx, workspaceID, userID, id)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, savedItemPayload("saved_item.removed", item), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteSavedItem(ctx, workspaceID, userID, id, event)
}

func (m Messages) ClearSavedItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (int, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return 0, err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("saved_item.cleared", events.String("user_id", string(userID))), time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return m.Store.ClearSavedItems(ctx, workspaceID, userID, event)
}

// MoveSavedItemToTodo makes the saved message a to-do titled after it, with
// no reminder yet; the member adds one from To-dos.
func (m Messages) MoveSavedItemToTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID) (domain.Todo, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Todo{}, err
	}
	item, err := m.Store.GetSavedItem(ctx, workspaceID, userID, id)
	if err != nil {
		return domain.Todo{}, err
	}
	item, err = m.savedItemWithSource(ctx, item)
	if err != nil {
		return domain.Todo{}, err
	}
	if !item.SourceAvailable {
		// The member can no longer read the message, so a to-do linking it
		// would carry nothing they may see.
		return domain.Todo{}, store.ErrNotFound
	}
	todoID, err := domain.NewTodoID()
	if err != nil {
		return domain.Todo{}, err
	}
	now := time.Now().UTC()
	todo := domain.Todo{
		ID: todoID, WorkspaceID: workspaceID, UserID: userID,
		Title: domain.TodoTitleFromMessage(item.Message.Text),
		Source: domain.TodoSource{
			MessageID: item.MessageID, Conversation: item.Conversation,
			Timestamp: domain.NewMessageTimestamp(item.Message.CreatedAt),
		},
		CreatedAt: now, UpdatedAt: now,
	}
	event, err := newEvent(workspaceID, userID, todoPayload("todo.created", todo), now)
	if err != nil {
		return domain.Todo{}, err
	}
	if err := m.Store.MoveSavedItemToTodo(ctx, workspaceID, userID, id, todo, event); err != nil {
		return domain.Todo{}, err
	}
	return todo, nil
}

func (m Messages) savedItemWithSource(ctx context.Context, item domain.SavedItem) (domain.SavedItem, error) {
	item.Message = domain.Message{}
	item.SourceAvailable = false
	if err := m.authorizeConversation(ctx, item.WorkspaceID, item.UserID, item.Conversation); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return item, nil
		}
		return domain.SavedItem{}, err
	}
	message, err := m.Store.GetMessage(ctx, item.MessageID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return item, nil
		}
		return domain.SavedItem{}, err
	}
	if message.WorkspaceID != item.WorkspaceID || message.Conversation != item.Conversation || message.Deleted {
		return item, nil
	}
	item.Message = message
	item.SourceAvailable = true
	return item, nil
}

// --- To-dos ---

func todoPayload(topic string, todo domain.Todo) events.Payload {
	return events.NewPayload(topic,
		events.String("todo_id", string(todo.ID)),
		events.String("user_id", string(todo.UserID)),
	)
}

// normalizeReminderTiming validates a reminder a member chose. The zero timing
// is no reminder at all.
func normalizeReminderTiming(timing domain.ReminderTiming, now time.Time) (domain.ReminderTiming, error) {
	if !timing.Scheduled() {
		if timing.Recurrence != domain.ReminderOnce || !timing.RecurrenceAnchor.IsZero() {
			return domain.ReminderTiming{}, domain.ErrInvalidReminderRequest
		}
		return domain.ReminderTiming{}, nil
	}
	timing.DueAt = timing.DueAt.UTC()
	timing.TimeZone = strings.TrimSpace(timing.TimeZone)
	if timing.TimeZone == "" {
		timing.TimeZone = "UTC"
	}
	if !timing.Recurrence.Valid() {
		return domain.ReminderTiming{}, domain.ErrInvalidReminderRequest
	}
	if !timing.DueAt.After(now) {
		return domain.ReminderTiming{}, domain.ErrReminderTimeInPast
	}
	location, err := time.LoadLocation(timing.TimeZone)
	if err != nil {
		return domain.ReminderTiming{}, domain.ErrInvalidReminderRequest
	}
	anchor, ok := domain.ReminderSeriesAnchor(timing.Recurrence, nil, timing.RecurrenceAnchor.UTC(), timing.DueAt, location)
	if !ok {
		return domain.ReminderTiming{}, domain.ErrInvalidReminderRequest
	}
	timing.RecurrenceAnchor = anchor
	return timing, nil
}

func normalizeTodoText(title, details string) (string, string, error) {
	title = strings.TrimSpace(title)
	details = strings.TrimSpace(details)
	if title == "" || len(title) > domain.TodoTitleLimit || len(details) > domain.TodoDetailsLimit {
		return "", "", domain.ErrInvalidTodo
	}
	return title, details, nil
}

// CreateTodo is Add To-do and "Remind me about this". A to-do made from a
// message keeps the message as its source; the member must be able to read
// it.
func (m Messages) CreateTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.TodoRequest) (domain.Todo, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Todo{}, err
	}
	now := time.Now().UTC()
	if (request.SourceChannel == "") != (request.SourceTimestamp == "") {
		return domain.Todo{}, domain.ErrInvalidTodo
	}
	timing, err := normalizeReminderTiming(request.Reminder, now)
	if err != nil {
		return domain.Todo{}, err
	}
	var source domain.TodoSource
	if request.SourceTimestamp != "" {
		message, messageErr := m.messageForTimestamp(ctx, workspaceID, userID, request.SourceChannel, request.SourceTimestamp)
		if messageErr != nil {
			return domain.Todo{}, messageErr
		}
		source = domain.TodoSource{MessageID: message.ID, Conversation: message.Conversation, Timestamp: request.SourceTimestamp}
		// "Remind me about this" asks only when: the to-do is named after
		// the message it reminds the member of.
		if strings.TrimSpace(request.Title) == "" {
			request.Title = domain.TodoTitleFromMessage(message.Text)
		}
	}
	title, details, err := normalizeTodoText(request.Title, request.Details)
	if err != nil {
		return domain.Todo{}, err
	}
	id, err := domain.NewTodoID()
	if err != nil {
		return domain.Todo{}, err
	}
	todo := domain.Todo{
		ID: id, WorkspaceID: workspaceID, UserID: userID, Title: title, Details: details,
		Source: source, Reminder: timing, CreatedAt: now, UpdatedAt: now,
	}
	event, err := newEvent(workspaceID, userID, todoPayload("todo.created", todo), now)
	if err != nil {
		return domain.Todo{}, err
	}
	if err := m.Store.CreateTodo(ctx, todo, event); err != nil {
		return domain.Todo{}, err
	}
	return todo, nil
}

func (m Messages) TodoInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID) (domain.Todo, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Todo{}, err
	}
	return m.Store.GetTodo(ctx, workspaceID, userID, id)
}

// Todos lists the To-dos tab. A query without a sort sorts by due date, and
// one without an instant divides overdue from upcoming at the present.
func (m Messages) Todos(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query domain.TodoQuery) (domain.TodoPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.TodoPage{}, err
	}
	if query.Sort == "" {
		query.Sort = domain.TodoSortDueDate
	}
	if query.Now.IsZero() {
		query.Now = time.Now().UTC()
	}
	if !query.Valid() {
		return domain.TodoPage{}, domain.ErrInvalidTodo
	}
	return m.Store.ListTodos(ctx, workspaceID, userID, query)
}

func (m Messages) EditTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, edit domain.TodoEdit) (domain.Todo, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Todo{}, err
	}
	title, details, err := normalizeTodoText(edit.Title, edit.Details)
	if err != nil {
		return domain.Todo{}, err
	}
	return m.updateTodo(ctx, workspaceID, userID, id, func(todo *domain.Todo) error {
		todo.Title, todo.Details = title, details
		return nil
	})
}

// SetTodoReminder is Edit reminder, and with the zero timing Clear due date.
// A new reminder starts its delivery afresh, so the badge for one that
// already fired goes with it.
func (m Messages) SetTodoReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, timing domain.ReminderTiming) (domain.Todo, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Todo{}, err
	}
	normalized, err := normalizeReminderTiming(timing, time.Now().UTC())
	if err != nil {
		return domain.Todo{}, err
	}
	return m.updateTodo(ctx, workspaceID, userID, id, func(todo *domain.Todo) error {
		todo.Reminder = normalized
		return nil
	})
}

func (m Messages) updateTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, change func(*domain.Todo) error) (domain.Todo, error) {
	todo, err := m.Store.GetTodo(ctx, workspaceID, userID, id)
	if err != nil {
		return domain.Todo{}, err
	}
	if err := change(&todo); err != nil {
		return domain.Todo{}, err
	}
	todo.UpdatedAt = time.Now().UTC()
	event, err := newEvent(workspaceID, userID, todoPayload("todo.changed", todo), todo.UpdatedAt)
	if err != nil {
		return domain.Todo{}, err
	}
	return m.Store.UpdateTodo(ctx, todo, event)
}

// SetTodoDone marks a to-do done or not done. It is idempotent: marking a
// done to-do done again keeps the instant it was first finished.
func (m Messages) SetTodoDone(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, done bool) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	completed := time.Time{}
	if done {
		completed = now
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("todo.changed",
		events.String("todo_id", string(id)),
		events.String("user_id", string(userID)),
	), now)
	if err != nil {
		return err
	}
	return m.Store.SetTodoCompletion(ctx, workspaceID, userID, id, completed, event)
}

func (m Messages) DeleteTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("todo.deleted",
		events.String("todo_id", string(id)),
		events.String("user_id", string(userID)),
	), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteTodo(ctx, workspaceID, userID, id, event)
}

func (m Messages) AcknowledgeTodoReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload("todo.acknowledged", events.String("user_id", string(userID))), now)
	if err != nil {
		return err
	}
	return m.Store.AcknowledgeTodoReminders(ctx, workspaceID, userID, now, event)
}

// --- channel reminders ---

func channelReminderPayload(topic string, reminder domain.ChannelReminder) events.Payload {
	return events.NewPayload(topic,
		events.String("reminder_id", string(reminder.ID)),
		events.String("channel_id", string(reminder.Channel)),
		events.String("user_id", string(reminder.Creator)),
	)
}

// CreateChannelReminder is /remind #channel. "Guests can't create channel
// reminders", and the member must belong to the channel Slackbot will post
// in.
func (m Messages) CreateChannelReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ChannelReminderRequest) (domain.ChannelReminder, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ChannelReminder{}, err
	}
	membership, err := m.activeWorkspaceMembership(ctx, workspaceID, userID)
	if err != nil {
		return domain.ChannelReminder{}, err
	}
	if membership.Guest() {
		return domain.ChannelReminder{}, domain.ErrInvalidReminderRequest
	}
	now := time.Now().UTC()
	text := strings.TrimSpace(request.Text)
	if text == "" || len(text) > domain.TodoTitleLimit || request.Channel == "" || !request.Reminder.Scheduled() {
		return domain.ChannelReminder{}, domain.ErrInvalidReminderRequest
	}
	timing, err := normalizeReminderTiming(request.Reminder, now)
	if err != nil {
		return domain.ChannelReminder{}, err
	}
	if err := m.requireConversationMembership(ctx, workspaceID, userID, request.Channel); err != nil {
		return domain.ChannelReminder{}, err
	}
	id, err := domain.NewChannelReminderID()
	if err != nil {
		return domain.ChannelReminder{}, err
	}
	reminder := domain.ChannelReminder{
		ID: id, WorkspaceID: workspaceID, Creator: userID, Channel: request.Channel, Text: text,
		Reminder: timing, CreatedAt: now, UpdatedAt: now,
	}
	event, err := newEvent(workspaceID, userID, channelReminderPayload("channel_reminder.created", reminder), now)
	if err != nil {
		return domain.ChannelReminder{}, err
	}
	if err := m.Store.CreateChannelReminder(ctx, reminder, event); err != nil {
		return domain.ChannelReminder{}, err
	}
	return reminder, nil
}

// ChannelReminders is /remind list: the channel reminders the member created.
func (m Messages) ChannelReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ChannelReminderPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.ChannelReminderPage{}, err
	}
	return m.Store.ListChannelReminders(ctx, workspaceID, userID, request)
}

// DeleteChannelReminder is the only change Slack allows a channel reminder:
// "Reminders in channels can't be edited, but you can delete and recreate
// them."
func (m Messages) DeleteChannelReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ChannelReminderID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	reminder, err := m.Store.GetChannelReminder(ctx, workspaceID, userID, id)
	if err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, channelReminderPayload("channel_reminder.deleted", reminder), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteChannelReminder(ctx, workspaceID, userID, id, event)
}
