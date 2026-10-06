package grpc

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatv1 "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc/gen/sameoldchat/chat/v1"
)

// The Saved section of Home, the To-dos tab and channel reminders across the
// seam. Instants travel in nanoseconds so the seam is lossless: the stores
// keep whole seconds, and what a local call answers before storage rounds it
// is what a remote call answers too.

// --- Remote: Saved ---

func (r Remote) AddToSaved(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, conversationID domain.ConversationID, timestamp domain.MessageTimestamp) (domain.SavedItem, error) {
	out, err := r.savedItems.AddToSaved(ctx, &chatv1.AddToSavedRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ConversationId: string(conversationID), Timestamp: string(timestamp)})
	if err != nil {
		return domain.SavedItem{}, err
	}
	return decodeProtoSavedItem(out)
}

func (r Remote) SavedItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.SavedItemPage, error) {
	out, err := r.savedItems.SavedItems(ctx, &chatv1.SavedItemsRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending})
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	return decodeProtoSavedItemPage(out)
}

func (r Remote) ClearSavedItems(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (int, error) {
	out, err := r.savedItems.ClearSavedItems(ctx, &chatv1.ClearSavedItemsRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return 0, err
	}
	return int(out.GetCleared()), nil
}

func (r Remote) MoveSavedItemToTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.SavedItemID) (domain.Todo, error) {
	out, err := r.savedItems.MoveSavedItemToTodo(ctx, &chatv1.MoveSavedItemToTodoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), SavedItemId: string(id)})
	if err != nil {
		return domain.Todo{}, err
	}
	return decodeProtoTodo(out)
}

// --- Remote: To-dos ---

func (r Remote) CreateTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.TodoRequest) (domain.Todo, error) {
	out, err := r.todos.CreateTodo(ctx, &chatv1.CreateTodoRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Title: request.Title, Details: request.Details,
		SourceChannelId: string(request.SourceChannel), SourceTimestamp: string(request.SourceTimestamp),
		Reminder: encodeProtoReminderTiming(request.Reminder),
	})
	if err != nil {
		return domain.Todo{}, err
	}
	return decodeProtoTodo(out)
}

func (r Remote) TodoInfo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID) (domain.Todo, error) {
	out, err := r.todos.TodoInfo(ctx, &chatv1.TodoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TodoId: string(id)})
	if err != nil {
		return domain.Todo{}, err
	}
	return decodeProtoTodo(out)
}

func (r Remote) Todos(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, query domain.TodoQuery) (domain.TodoPage, error) {
	reminders := make([]string, 0, len(query.Reminders))
	for _, status := range query.Reminders {
		reminders = append(reminders, string(status))
	}
	out, err := r.todos.Todos(ctx, &chatv1.TodosRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), Done: query.Done, Reminders: reminders,
		Sort: string(query.Sort), NowUnixNano: optionalUnixNano(query.Now),
		Limit: int32(query.Page.Limit), Cursor: string(query.Page.Cursor),
	})
	if err != nil {
		return domain.TodoPage{}, err
	}
	page := domain.TodoPage{Items: make([]domain.Todo, 0, len(out.GetItems())), NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}
	for _, item := range out.GetItems() {
		todo, err := decodeProtoTodo(item)
		if err != nil {
			return domain.TodoPage{}, err
		}
		page.Items = append(page.Items, todo)
	}
	return page, nil
}

func (r Remote) EditTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, edit domain.TodoEdit) (domain.Todo, error) {
	out, err := r.todos.EditTodo(ctx, &chatv1.EditTodoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TodoId: string(id), Title: edit.Title, Details: edit.Details})
	if err != nil {
		return domain.Todo{}, err
	}
	return decodeProtoTodo(out)
}

func (r Remote) SetTodoReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, timing domain.ReminderTiming) (domain.Todo, error) {
	out, err := r.todos.SetTodoReminder(ctx, &chatv1.SetTodoReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TodoId: string(id), Reminder: encodeProtoReminderTiming(timing)})
	if err != nil {
		return domain.Todo{}, err
	}
	return decodeProtoTodo(out)
}

func (r Remote) SetTodoDone(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID, done bool) error {
	out, err := r.todos.SetTodoDone(ctx, &chatv1.SetTodoDoneRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TodoId: string(id), Done: done})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "to-do completion")
}

func (r Remote) DeleteTodo(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.TodoID) error {
	out, err := r.todos.DeleteTodo(ctx, &chatv1.TodoRequest{WorkspaceId: string(workspaceID), UserId: string(userID), TodoId: string(id)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "to-do deletion")
}

func (r Remote) AcknowledgeTodoReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	out, err := r.todos.AcknowledgeTodoReminders(ctx, &chatv1.AcknowledgeTodoRemindersRequest{WorkspaceId: string(workspaceID), UserId: string(userID)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "to-do reminder acknowledgement")
}

// --- Remote: channel reminders ---

func (r Remote) CreateChannelReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.ChannelReminderRequest) (domain.ChannelReminder, error) {
	out, err := r.reminders.CreateChannelReminder(ctx, &chatv1.CreateChannelReminderRequest{
		WorkspaceId: string(workspaceID), UserId: string(userID), ChannelId: string(request.Channel), Text: request.Text,
		DueAtUnixNano: optionalUnixNano(request.Reminder.DueAt), Timezone: request.Reminder.TimeZone,
		Recurrence: string(request.Reminder.Recurrence), RecurrenceAnchorUnixNano: optionalUnixNano(request.Reminder.RecurrenceAnchor),
	})
	if err != nil {
		return domain.ChannelReminder{}, err
	}
	return decodeProtoChannelReminder(out)
}

func (r Remote) ChannelReminders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.PageRequest) (domain.ChannelReminderPage, error) {
	out, err := r.reminders.ChannelReminders(ctx, &chatv1.ChannelRemindersRequest{WorkspaceId: string(workspaceID), UserId: string(userID), Limit: int32(request.Limit), Cursor: string(request.Cursor), Descending: request.Descending})
	if err != nil {
		return domain.ChannelReminderPage{}, err
	}
	page := domain.ChannelReminderPage{Items: make([]domain.ChannelReminder, 0, len(out.GetReminders())), NextCursor: domain.Cursor(out.GetNextCursor()), HasMore: out.GetHasMore()}
	for _, value := range out.GetReminders() {
		reminder, err := decodeProtoChannelReminder(value)
		if err != nil {
			return domain.ChannelReminderPage{}, err
		}
		page.Items = append(page.Items, reminder)
	}
	return page, nil
}

func (r Remote) DeleteChannelReminder(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.ChannelReminderID) error {
	out, err := r.reminders.DeleteChannelReminder(ctx, &chatv1.DeleteChannelReminderRequest{WorkspaceId: string(workspaceID), UserId: string(userID), ReminderId: string(id)})
	if err != nil {
		return err
	}
	return requireAcknowledgement(out.GetOk(), "channel reminder deletion")
}

// --- Server ---

func (s *Server) AddToSaved(ctx context.Context, input *chatv1.AddToSavedRequest) (*chatv1.SavedItem, error) {
	item, err := s.implementation.AddToSaved(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ConversationID(input.GetConversationId()), domain.MessageTimestamp(input.GetTimestamp()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSavedItem(item), nil
}

func (s *Server) SavedItems(ctx context.Context, input *chatv1.SavedItemsRequest) (*chatv1.SavedItemPage, error) {
	page, err := s.implementation.SavedItems(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), protoDirectionalPageRequest(input.GetLimit(), input.GetCursor(), input.GetDescending()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoSavedItemPage(page), nil
}

func (s *Server) ClearSavedItems(ctx context.Context, input *chatv1.ClearSavedItemsRequest) (*chatv1.ClearSavedItemsResponse, error) {
	cleared, err := s.implementation.ClearSavedItems(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()))
	if err != nil {
		return nil, mapError(err)
	}
	return &chatv1.ClearSavedItemsResponse{Cleared: int32(cleared)}, nil
}

func (s *Server) MoveSavedItemToTodo(ctx context.Context, input *chatv1.MoveSavedItemToTodoRequest) (*chatv1.Todo, error) {
	todo, err := s.implementation.MoveSavedItemToTodo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.SavedItemID(input.GetSavedItemId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoTodo(todo), nil
}

func (s *Server) CreateTodo(ctx context.Context, input *chatv1.CreateTodoRequest) (*chatv1.Todo, error) {
	todo, err := s.implementation.CreateTodo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.TodoRequest{
		Title: input.GetTitle(), Details: input.GetDetails(),
		SourceChannel: domain.ConversationID(input.GetSourceChannelId()), SourceTimestamp: domain.MessageTimestamp(input.GetSourceTimestamp()),
		Reminder: decodeProtoReminderTiming(input.GetReminder()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoTodo(todo), nil
}

func (s *Server) TodoInfo(ctx context.Context, input *chatv1.TodoRequest) (*chatv1.Todo, error) {
	todo, err := s.implementation.TodoInfo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.TodoID(input.GetTodoId()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoTodo(todo), nil
}

func (s *Server) Todos(ctx context.Context, input *chatv1.TodosRequest) (*chatv1.TodoPage, error) {
	query := domain.TodoQuery{
		Done: input.GetDone(), Sort: domain.TodoSort(input.GetSort()), Now: optionalTimeFromUnixNano(input.GetNowUnixNano()),
		Page: protoPageRequest(input.GetLimit(), input.GetCursor()),
	}
	for _, status := range input.GetReminders() {
		query.Reminders = append(query.Reminders, domain.TodoReminderGroup(status))
	}
	page, err := s.implementation.Todos(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), query)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.Todo, 0, len(page.Items))
	for _, todo := range page.Items {
		items = append(items, encodeProtoTodo(todo))
	}
	return &chatv1.TodoPage{Items: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) EditTodo(ctx context.Context, input *chatv1.EditTodoRequest) (*chatv1.Todo, error) {
	todo, err := s.implementation.EditTodo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.TodoID(input.GetTodoId()), domain.TodoEdit{Title: input.GetTitle(), Details: input.GetDetails()})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoTodo(todo), nil
}

func (s *Server) SetTodoReminder(ctx context.Context, input *chatv1.SetTodoReminderRequest) (*chatv1.Todo, error) {
	todo, err := s.implementation.SetTodoReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.TodoID(input.GetTodoId()), decodeProtoReminderTiming(input.GetReminder()))
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoTodo(todo), nil
}

func (s *Server) SetTodoDone(ctx context.Context, input *chatv1.SetTodoDoneRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.SetTodoDone(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.TodoID(input.GetTodoId()), input.GetDone()); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) DeleteTodo(ctx context.Context, input *chatv1.TodoRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteTodo(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.TodoID(input.GetTodoId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) AcknowledgeTodoReminders(ctx context.Context, input *chatv1.AcknowledgeTodoRemindersRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.AcknowledgeTodoReminders(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

func (s *Server) CreateChannelReminder(ctx context.Context, input *chatv1.CreateChannelReminderRequest) (*chatv1.ChannelReminder, error) {
	reminder, err := s.implementation.CreateChannelReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ChannelReminderRequest{
		Channel: domain.ConversationID(input.GetChannelId()), Text: input.GetText(),
		Reminder: domain.ReminderTiming{
			DueAt: optionalTimeFromUnixNano(input.GetDueAtUnixNano()), TimeZone: input.GetTimezone(),
			Recurrence: domain.ReminderRecurrence(input.GetRecurrence()), RecurrenceAnchor: optionalTimeFromUnixNano(input.GetRecurrenceAnchorUnixNano()),
		},
	})
	if err != nil {
		return nil, mapError(err)
	}
	return encodeProtoChannelReminder(reminder), nil
}

func (s *Server) ChannelReminders(ctx context.Context, input *chatv1.ChannelRemindersRequest) (*chatv1.ChannelReminderPage, error) {
	page, err := s.implementation.ChannelReminders(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), protoDirectionalPageRequest(input.GetLimit(), input.GetCursor(), input.GetDescending()))
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*chatv1.ChannelReminder, 0, len(page.Items))
	for _, reminder := range page.Items {
		items = append(items, encodeProtoChannelReminder(reminder))
	}
	return &chatv1.ChannelReminderPage{Reminders: items, NextCursor: string(page.NextCursor), HasMore: page.HasMore}, nil
}

func (s *Server) DeleteChannelReminder(ctx context.Context, input *chatv1.DeleteChannelReminderRequest) (*chatv1.MutationResponse, error) {
	if err := s.implementation.DeleteChannelReminder(ctx, domain.WorkspaceID(input.GetWorkspaceId()), domain.UserID(input.GetUserId()), domain.ChannelReminderID(input.GetReminderId())); err != nil {
		return nil, mapError(err)
	}
	return &chatv1.MutationResponse{Ok: true}, nil
}

// --- codecs ---

func encodeProtoSavedItem(value domain.SavedItem) *chatv1.SavedItem {
	item := &chatv1.SavedItem{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		MessageId: string(value.MessageID), ConversationId: string(value.Conversation),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), SourceAvailable: value.SourceAvailable,
	}
	if value.SourceAvailable {
		item.Message = encodeProtoMessage(value.Message)
	}
	return item
}

func decodeProtoSavedItem(value *chatv1.SavedItem) (domain.SavedItem, error) {
	if value == nil || value.GetId() == "" || value.GetWorkspaceId() == "" || value.GetUserId() == "" || value.GetMessageId() == "" || value.GetConversationId() == "" {
		return domain.SavedItem{}, errors.New("typed saved item is incomplete")
	}
	item := domain.SavedItem{
		ID: domain.SavedItemID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		UserID: domain.UserID(value.GetUserId()), MessageID: domain.MessageID(value.GetMessageId()),
		Conversation: domain.ConversationID(value.GetConversationId()),
		CreatedAt:    optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), SourceAvailable: value.GetSourceAvailable(),
	}
	if item.SourceAvailable {
		message, err := decodeProtoMessage(value.GetMessage())
		if err != nil {
			return domain.SavedItem{}, err
		}
		item.Message = message
	}
	return item, nil
}

func encodeProtoReminderTiming(value domain.ReminderTiming) *chatv1.ReminderTiming {
	if !value.Scheduled() && value.Recurrence == domain.ReminderOnce && value.TimeZone == "" && value.RecurrenceAnchor.IsZero() {
		return nil
	}
	return &chatv1.ReminderTiming{
		DueAtUnixNano: optionalUnixNano(value.DueAt), Timezone: value.TimeZone,
		Recurrence: string(value.Recurrence), RecurrenceAnchorUnixNano: optionalUnixNano(value.RecurrenceAnchor),
	}
}

func decodeProtoReminderTiming(value *chatv1.ReminderTiming) domain.ReminderTiming {
	if value == nil {
		return domain.ReminderTiming{}
	}
	return domain.ReminderTiming{
		DueAt: optionalTimeFromUnixNano(value.GetDueAtUnixNano()), TimeZone: value.GetTimezone(),
		Recurrence: domain.ReminderRecurrence(value.GetRecurrence()), RecurrenceAnchor: optionalTimeFromUnixNano(value.GetRecurrenceAnchorUnixNano()),
	}
}

func encodeProtoReminderDelivery(value domain.ReminderDelivery) *chatv1.ReminderDelivery {
	return &chatv1.ReminderDelivery{
		LastDeliveredAtUnixNano: optionalUnixNano(value.LastDeliveredAt), AcknowledgedAtUnixNano: optionalUnixNano(value.AcknowledgedAt),
		FailedAtUnixNano: optionalUnixNano(value.FailedAt), FailureCode: value.FailureCode,
	}
}

func decodeProtoReminderDelivery(value *chatv1.ReminderDelivery) domain.ReminderDelivery {
	return domain.ReminderDelivery{
		LastDeliveredAt: optionalTimeFromUnixNano(value.GetLastDeliveredAtUnixNano()), AcknowledgedAt: optionalTimeFromUnixNano(value.GetAcknowledgedAtUnixNano()),
		FailedAt: optionalTimeFromUnixNano(value.GetFailedAtUnixNano()), FailureCode: value.GetFailureCode(),
	}
}

func encodeProtoTodo(value domain.Todo) *chatv1.Todo {
	return &chatv1.Todo{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), UserId: string(value.UserID),
		Title: value.Title, Details: value.Details,
		SourceMessageId: string(value.Source.MessageID), SourceConversationId: string(value.Source.Conversation), SourceTimestamp: string(value.Source.Timestamp),
		Reminder: encodeProtoReminderTiming(value.Reminder), Delivery: encodeProtoReminderDelivery(value.Delivery),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt), CompletedAtUnixNano: optionalUnixNano(value.CompletedAt),
	}
}

// decodeProtoTodo refuses a to-do the stores would refuse, so a malformed
// record from across the seam fails here rather than in a template.
func decodeProtoTodo(value *chatv1.Todo) (domain.Todo, error) {
	if value == nil {
		return domain.Todo{}, errors.New("typed to-do is required")
	}
	todo := domain.Todo{
		ID: domain.TodoID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()), UserID: domain.UserID(value.GetUserId()),
		Title: value.GetTitle(), Details: value.GetDetails(),
		Source: domain.TodoSource{
			MessageID: domain.MessageID(value.GetSourceMessageId()), Conversation: domain.ConversationID(value.GetSourceConversationId()),
			Timestamp: domain.MessageTimestamp(value.GetSourceTimestamp()),
		},
		Reminder: decodeProtoReminderTiming(value.GetReminder()), Delivery: decodeProtoReminderDelivery(value.GetDelivery()),
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
		CompletedAt: optionalTimeFromUnixNano(value.GetCompletedAtUnixNano()),
	}
	if !todo.Valid() || todo.CreatedAt.IsZero() {
		return domain.Todo{}, errors.New("typed to-do is incomplete")
	}
	return todo, nil
}

func encodeProtoChannelReminder(value domain.ChannelReminder) *chatv1.ChannelReminder {
	return &chatv1.ChannelReminder{
		Id: string(value.ID), WorkspaceId: string(value.WorkspaceID), CreatorId: string(value.Creator),
		ChannelId: string(value.Channel), Text: value.Text,
		DueAtUnixNano: optionalUnixNano(value.Reminder.DueAt), Timezone: value.Reminder.TimeZone,
		Recurrence: string(value.Reminder.Recurrence), RecurrenceAnchorUnixNano: optionalUnixNano(value.Reminder.RecurrenceAnchor),
		CreatedAtUnixNano: optionalUnixNano(value.CreatedAt), UpdatedAtUnixNano: optionalUnixNano(value.UpdatedAt),
		LastDeliveredAtUnixNano: optionalUnixNano(value.Delivery.LastDeliveredAt), FailedAtUnixNano: optionalUnixNano(value.Delivery.FailedAt),
		FailureCode: value.Delivery.FailureCode,
	}
}

func decodeProtoChannelReminder(value *chatv1.ChannelReminder) (domain.ChannelReminder, error) {
	if value == nil {
		return domain.ChannelReminder{}, errors.New("typed channel reminder is required")
	}
	reminder := domain.ChannelReminder{
		ID: domain.ChannelReminderID(value.GetId()), WorkspaceID: domain.WorkspaceID(value.GetWorkspaceId()),
		Creator: domain.UserID(value.GetCreatorId()), Channel: domain.ConversationID(value.GetChannelId()), Text: value.GetText(),
		Reminder: domain.ReminderTiming{
			DueAt: optionalTimeFromUnixNano(value.GetDueAtUnixNano()), TimeZone: value.GetTimezone(),
			Recurrence: domain.ReminderRecurrence(value.GetRecurrence()), RecurrenceAnchor: optionalTimeFromUnixNano(value.GetRecurrenceAnchorUnixNano()),
		},
		Delivery: domain.ReminderDelivery{
			LastDeliveredAt: optionalTimeFromUnixNano(value.GetLastDeliveredAtUnixNano()), FailedAt: optionalTimeFromUnixNano(value.GetFailedAtUnixNano()),
			FailureCode: value.GetFailureCode(),
		},
		CreatedAt: optionalTimeFromUnixNano(value.GetCreatedAtUnixNano()), UpdatedAt: optionalTimeFromUnixNano(value.GetUpdatedAtUnixNano()),
	}
	if !reminder.Valid() || reminder.CreatedAt.IsZero() {
		return domain.ChannelReminder{}, errors.New("typed channel reminder is incomplete")
	}
	return reminder, nil
}
