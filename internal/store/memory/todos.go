package memory

import (
	"context"
	"sort"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// --- saved items ---

func (s *Store) CreateSavedItem(_ context.Context, item domain.SavedItem, event events.Event) (domain.SavedItem, bool, error) {
	if item.ID == "" || item.MessageID == "" || item.Conversation == "" {
		return domain.SavedItem{}, false, store.InvalidArgument("saved item is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	message, err := s.messageLocked(item.MessageID)
	if err != nil || message.WorkspaceID != item.WorkspaceID || message.Conversation != item.Conversation {
		return domain.SavedItem{}, false, store.ErrNotFound
	}
	for _, existing := range s.savedItems {
		if existing.WorkspaceID == item.WorkspaceID && existing.UserID == item.UserID && existing.MessageID == item.MessageID {
			return existing, false, nil
		}
	}
	if _, exists := s.savedItems[item.ID]; exists {
		return domain.SavedItem{}, false, store.ErrAlreadyExists
	}
	item.Message = domain.Message{}
	item.SourceAvailable = false
	s.savedItems[item.ID] = item
	s.outbox = append(s.outbox, event)
	return item, true, nil
}

func (s *Store) GetSavedItem(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID) (domain.SavedItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.savedItems[id]
	if !exists || item.WorkspaceID != workspace || item.UserID != user {
		return domain.SavedItem{}, store.ErrNotFound
	}
	return item, nil
}

func (s *Store) GetSavedItemByMessage(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, message domain.MessageID) (domain.SavedItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.savedItems {
		if item.WorkspaceID == workspace && item.UserID == user && item.MessageID == message {
			return item, nil
		}
	}
	return domain.SavedItem{}, store.ErrNotFound
}

func (s *Store) ListSavedItemsForMessages(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, messages []domain.MessageID) ([]domain.SavedItem, error) {
	wanted := make(map[domain.MessageID]struct{}, len(messages))
	for _, message := range messages {
		wanted[message] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.SavedItem, 0, len(messages))
	for _, item := range s.savedItems {
		if item.WorkspaceID != workspace || item.UserID != user {
			continue
		}
		if _, ok := wanted[item.MessageID]; ok {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	return items, nil
}

// savedItemNewer orders saved items newest first by their stored instant,
// as the SQL profile's ORDER BY created_at DESC, id DESC does.
func savedItemNewer(left, right domain.SavedItem) bool {
	leftAt, rightAt := domain.NewStoredTime(left.CreatedAt), domain.NewStoredTime(right.CreatedAt)
	if leftAt != rightAt {
		return leftAt > rightAt
	}
	return left.ID > right.ID
}

func (s *Store) ListSavedItems(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.SavedItemPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.SavedItemPage{}, err
	}
	raw, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	var after domain.SavedItem
	resume := raw != ""
	if resume {
		createdAt, id, ok := cutCursor(raw)
		instant, parseErr := domain.ParseStoredTime(createdAt)
		if !ok || parseErr != nil {
			return domain.SavedItemPage{}, domain.ErrInvalidCursor
		}
		after = domain.SavedItem{ID: domain.SavedItemID(id), CreatedAt: instant}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.SavedItem, 0, request.Limit+1)
	for _, item := range s.savedItems {
		if item.WorkspaceID != workspace || item.UserID != user || resume && !savedItemNewer(after, item) {
			continue
		}
		values = appendSorted(values, item, request.Limit+1, savedItemNewer)
	}
	page := domain.SavedItemPage{Items: values, HasMore: len(values) > request.Limit}
	if page.HasMore {
		page.Items = values[:request.Limit]
		last := page.Items[request.Limit-1]
		page.NextCursor, err = domain.NewListCursor(string(domain.NewStoredTime(last.CreatedAt)) + "\x00" + string(last.ID))
	}
	return page, err
}

func cutCursor(raw string) (string, string, bool) {
	for index := 0; index < len(raw); index++ {
		if raw[index] == 0 {
			return raw[:index], raw[index+1:], index > 0 && index < len(raw)-1
		}
	}
	return "", "", false
}

func (s *Store) DeleteSavedItem(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.savedItems[id]
	if !exists || item.WorkspaceID != workspace || item.UserID != user {
		return store.ErrNotFound
	}
	delete(s.savedItems, id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ClearSavedItems(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, event events.Event) (int, error) {
	if workspace == "" || user == "" {
		return 0, store.InvalidArgument("clearing saved items needs a member")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleared := 0
	for id, item := range s.savedItems {
		if item.WorkspaceID == workspace && item.UserID == user {
			delete(s.savedItems, id)
			cleared++
		}
	}
	if cleared > 0 {
		s.outbox = append(s.outbox, event)
	}
	return cleared, nil
}

func (s *Store) MoveSavedItemToTodo(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID, todo domain.Todo, event events.Event) error {
	if todo.WorkspaceID != workspace || todo.UserID != user || !todo.Valid() {
		return store.InvalidArgument("the to-do a saved item moves to is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.savedItems[id]
	if !exists || item.WorkspaceID != workspace || item.UserID != user {
		return store.ErrNotFound
	}
	if item.MessageID != todo.Source.MessageID {
		return store.InvalidArgument("the to-do must link the saved message")
	}
	if err := s.insertTodoLocked(todo); err != nil {
		return err
	}
	delete(s.savedItems, id)
	s.outbox = append(s.outbox, event)
	return nil
}

// --- to-dos ---

// wholeSeconds is the precision every SQL profile keeps for to-do and
// channel-reminder instants; the memory profile keeps the same, so a value
// reads back alike from all of them.
func wholeSeconds(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Second)
}

func storedTiming(timing domain.ReminderTiming) domain.ReminderTiming {
	timing.DueAt = wholeSeconds(timing.DueAt)
	timing.RecurrenceAnchor = wholeSeconds(timing.RecurrenceAnchor)
	return timing
}

func storedDelivery(delivery domain.ReminderDelivery) domain.ReminderDelivery {
	delivery.LastDeliveredAt = wholeSeconds(delivery.LastDeliveredAt)
	delivery.AcknowledgedAt = wholeSeconds(delivery.AcknowledgedAt)
	delivery.FailedAt = wholeSeconds(delivery.FailedAt)
	return delivery
}

func storedTodo(todo domain.Todo) domain.Todo {
	todo.Reminder = storedTiming(todo.Reminder)
	todo.Delivery = storedDelivery(todo.Delivery)
	todo.CreatedAt = wholeSeconds(todo.CreatedAt)
	todo.UpdatedAt = wholeSeconds(todo.UpdatedAt)
	todo.CompletedAt = wholeSeconds(todo.CompletedAt)
	return todo
}

func (s *Store) insertTodoLocked(todo domain.Todo) error {
	if workspace, ok := s.workspaces[todo.WorkspaceID]; !ok || workspace.ID == "" {
		return store.ErrNotFound
	}
	if user, ok := s.users[todo.UserID]; !ok || user.WorkspaceID != todo.WorkspaceID {
		return store.ErrNotFound
	}
	if todo.Source.Set() {
		message, ok := memoryMessageByID(s.messages[todo.Source.Conversation], todo.Source.MessageID)
		if !ok || message.WorkspaceID != todo.WorkspaceID {
			return store.ErrNotFound
		}
	}
	if _, exists := s.todos[todo.ID]; exists {
		return store.ErrAlreadyExists
	}
	s.todos[todo.ID] = storedTodo(todo)
	return nil
}

func (s *Store) CreateTodo(_ context.Context, todo domain.Todo, event events.Event) error {
	if !todo.Valid() {
		return store.InvalidArgument("to-do is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.insertTodoLocked(todo); err != nil {
		return err
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetTodo(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.TodoID) (domain.Todo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	todo, ok := s.todos[id]
	if !ok || todo.WorkspaceID != workspace || todo.UserID != user {
		return domain.Todo{}, store.ErrNotFound
	}
	return todo, nil
}

func (s *Store) ListTodos(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, query domain.TodoQuery) (domain.TodoPage, error) {
	if err := store.CheckAscendingPage(query.Page); err != nil {
		return domain.TodoPage{}, err
	}
	if !query.Valid() {
		return domain.TodoPage{}, store.InvalidArgument("to-do filter is invalid")
	}
	after, resume, err := domain.DecodeTodoCursor(query.Page.Cursor, query.Sort)
	if err != nil {
		return domain.TodoPage{}, err
	}
	now := wholeSeconds(query.Now)
	precedes := func(left, right domain.Todo) bool {
		return domain.PositionOf(left).Precedes(domain.PositionOf(right), query.Sort)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Todo, 0, query.Page.Limit+1)
	for _, todo := range s.todos {
		if todo.WorkspaceID != workspace || todo.UserID != user || todo.Done() != query.Done || !query.Includes(todo.ReminderGroup(now)) {
			continue
		}
		if resume && !after.Precedes(domain.PositionOf(todo), query.Sort) {
			continue
		}
		values = appendSorted(values, todo, query.Page.Limit+1, precedes)
	}
	return store.TodoPageOf(values, query)
}

func (s *Store) UpdateTodo(_ context.Context, todo domain.Todo, event events.Event) (domain.Todo, error) {
	if !todo.Valid() {
		return domain.Todo{}, store.InvalidArgument("to-do is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.todos[todo.ID]
	if !ok || current.WorkspaceID != todo.WorkspaceID || current.UserID != todo.UserID || s.todoQueue.leased(todo.ID, time.Now().UTC()) {
		return domain.Todo{}, store.ErrNotFound
	}
	if current.Source != todo.Source {
		return domain.Todo{}, store.InvalidArgument("a to-do's source cannot change")
	}
	current.Title = todo.Title
	current.Details = todo.Details
	current.UpdatedAt = wholeSeconds(todo.UpdatedAt)
	if !store.SameReminderTiming(current.Reminder, todo.Reminder) {
		current.Reminder = storedTiming(todo.Reminder)
		current.Delivery = domain.ReminderDelivery{}
		s.todoQueue.forget(todo.ID)
	}
	s.todos[todo.ID] = current
	s.outbox = append(s.outbox, event)
	return current, nil
}

func (s *Store) SetTodoCompletion(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.TodoID, completed time.Time, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	todo, ok := s.todos[id]
	now := time.Now().UTC()
	if !ok || todo.WorkspaceID != workspace || todo.UserID != user || s.todoQueue.leased(id, now) {
		return store.ErrNotFound
	}
	if todo.Done() == !completed.IsZero() {
		return nil
	}
	todo.CompletedAt = wholeSeconds(completed)
	todo.UpdatedAt = wholeSeconds(completed)
	if completed.IsZero() {
		todo.UpdatedAt = wholeSeconds(now)
	}
	s.todos[id] = todo
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) DeleteTodo(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.TodoID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	todo, ok := s.todos[id]
	if !ok || todo.WorkspaceID != workspace || todo.UserID != user || s.todoQueue.leased(id, time.Now().UTC()) {
		return store.ErrNotFound
	}
	delete(s.todos, id)
	s.todoQueue.forget(id)
	for activityID, item := range s.activityItems {
		if item.TodoID == id && item.WorkspaceID == workspace && item.UserID == user {
			delete(s.activityItems, activityID)
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) AcknowledgeTodoReminders(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, acknowledged time.Time, event events.Event) error {
	if workspace == "" || user == "" || acknowledged.IsZero() {
		return store.InvalidArgument("to-do reminder acknowledgement is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for id, todo := range s.todos {
		if todo.WorkspaceID != workspace || todo.UserID != user || !todo.Delivery.LastDeliveredAt.After(todo.Delivery.AcknowledgedAt) {
			continue
		}
		todo.Delivery.AcknowledgedAt = todo.Delivery.LastDeliveredAt
		s.todos[id] = todo
		found = true
	}
	if !found {
		return nil
	}
	for activityID, item := range s.activityItems {
		if item.WorkspaceID == workspace && item.UserID == user && item.TodoID != "" && item.ReadAt.IsZero() {
			item.ReadAt = acknowledged.UTC()
			s.activityItems[activityID] = item
		}
	}
	s.outbox = append(s.outbox, event)
	return nil
}

// --- channel reminders ---

func (s *Store) CreateChannelReminder(_ context.Context, reminder domain.ChannelReminder, event events.Event) error {
	if !reminder.Valid() {
		return store.InvalidArgument("channel reminder is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	creator, ok := s.users[reminder.Creator]
	if !ok || creator.WorkspaceID != reminder.WorkspaceID {
		return store.ErrNotFound
	}
	conversation, ok := s.conversations[reminder.Channel]
	if !ok || conversation.WorkspaceID != reminder.WorkspaceID {
		return store.ErrNotFound
	}
	if _, exists := s.channelReminders[reminder.ID]; exists {
		return store.ErrAlreadyExists
	}
	reminder.Reminder = storedTiming(reminder.Reminder)
	reminder.Delivery = storedDelivery(reminder.Delivery)
	reminder.CreatedAt = wholeSeconds(reminder.CreatedAt)
	reminder.UpdatedAt = wholeSeconds(reminder.UpdatedAt)
	s.channelReminders[reminder.ID] = reminder
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) GetChannelReminder(_ context.Context, workspace domain.WorkspaceID, creator domain.UserID, id domain.ChannelReminderID) (domain.ChannelReminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reminder, ok := s.channelReminders[id]
	if !ok || reminder.WorkspaceID != workspace || reminder.Creator != creator {
		return domain.ChannelReminder{}, store.ErrNotFound
	}
	return reminder, nil
}

func (s *Store) ListChannelReminders(_ context.Context, workspace domain.WorkspaceID, creator domain.UserID, request domain.PageRequest) (domain.ChannelReminderPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ChannelReminderPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ChannelReminderPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.ChannelReminder, 0, request.Limit+1)
	for _, reminder := range s.channelReminders {
		if reminder.WorkspaceID != workspace || reminder.Creator != creator || string(reminder.ID) <= after {
			continue
		}
		values = appendSorted(values, reminder, request.Limit+1, func(left, right domain.ChannelReminder) bool { return left.ID < right.ID })
	}
	page := domain.ChannelReminderPage{Items: values, HasMore: len(values) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(page.Items[len(page.Items)-1].ID))
	}
	return page, err
}

func (s *Store) DeleteChannelReminder(_ context.Context, workspace domain.WorkspaceID, creator domain.UserID, id domain.ChannelReminderID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.channelReminders[id]
	if !ok || reminder.WorkspaceID != workspace || reminder.Creator != creator || s.channelReminderQueue.leased(id, time.Now().UTC()) {
		return store.ErrNotFound
	}
	delete(s.channelReminders, id)
	s.channelReminderQueue.forget(id)
	s.outbox = append(s.outbox, event)
	return nil
}

// --- delivery ---

// memoryReminderQueue is the lease state of one kind of first-party reminder,
// the memory counterpart of the SQL profile's lease columns.
type memoryReminderQueue[K comparable] struct {
	leases map[K]memoryLease
	next   map[K]time.Time
}

func newMemoryReminderQueue[K comparable]() memoryReminderQueue[K] {
	return memoryReminderQueue[K]{leases: map[K]memoryLease{}, next: map[K]time.Time{}}
}

func (q memoryReminderQueue[K]) leased(id K, now time.Time) bool {
	active, ok := q.leases[id]
	return ok && active.Expires.After(now)
}

func (q memoryReminderQueue[K]) holds(owner string, id K, now time.Time) bool {
	active, ok := q.leases[id]
	return owner != "" && ok && active.Owner == owner && active.Expires.After(now.UTC())
}

func (q memoryReminderQueue[K]) forget(id K) {
	delete(q.leases, id)
	delete(q.next, id)
}

// waiting reports whether a pending occurrence may be claimed at now.
func (q memoryReminderQueue[K]) waiting(id K, timing domain.ReminderTiming, now time.Time) bool {
	return !timing.DueAt.After(now) && !q.next[id].After(now) && !q.leased(id, now)
}

func (q memoryReminderQueue[K]) deadline(id K, timing domain.ReminderTiming) time.Time {
	deadline := timing.DueAt
	if next := q.next[id]; next.After(deadline) {
		deadline = next
	}
	return deadline
}

func (q memoryReminderQueue[K]) renew(owner string, id K, lease time.Duration, now time.Time) error {
	if lease <= 0 || now.IsZero() || !q.holds(owner, id, now) {
		return store.ErrLeaseConflict
	}
	active := q.leases[id]
	active.Expires = time.Unix(scheduledSecondCeil(now.UTC().Add(lease)), 0).UTC()
	q.leases[id] = active
	return nil
}

func (q memoryReminderQueue[K]) release(owner string, id K, next, now time.Time) error {
	if next.IsZero() || now.IsZero() || !q.holds(owner, id, now) {
		return store.ErrLeaseConflict
	}
	delete(q.leases, id)
	q.next[id] = time.Unix(scheduledSecondCeil(next), 0).UTC()
	return nil
}

// scheduledSecondCeil rounds a lease or retry instant up to the whole second
// the SQL profiles store.
func scheduledSecondCeil(value time.Time) int64 {
	seconds := value.UTC().Unix()
	if value.UTC().Nanosecond() > 0 {
		seconds++
	}
	return seconds
}

// advance records a delivered occurrence on timing and delivery, as the SQL
// profile's markDelivered does.
func advance(timing *domain.ReminderTiming, delivery *domain.ReminderDelivery, deliveredAt, next time.Time) error {
	if timing.Recurrence == domain.ReminderOnce {
		if !next.IsZero() {
			return store.InvalidArgument("a one-time reminder cannot have a next delivery")
		}
		if timing.DueAt.After(deliveredAt) {
			return store.ErrLeaseConflict
		}
	} else {
		if next.IsZero() || !wholeSeconds(next).After(timing.DueAt) {
			return store.InvalidArgument("a recurring reminder requires a later delivery")
		}
		timing.DueAt = wholeSeconds(next)
	}
	delivery.LastDeliveredAt = wholeSeconds(deliveredAt)
	return nil
}

func claimOrder[T any](values []T, timing func(T) domain.ReminderTiming, id func(T) string) {
	sort.Slice(values, func(left, right int) bool {
		leftDue, rightDue := timing(values[left]).DueAt, timing(values[right]).DueAt
		return leftDue.Before(rightDue) || leftDue.Equal(rightDue) && id(values[left]) < id(values[right])
	})
}

func (s *Store) EarliestTodoReminder(_ context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for id, todo := range s.todos {
		if (workspace != "" && todo.WorkspaceID != workspace) || todo.Done() || !todo.Delivery.Pending(todo.Reminder) {
			continue
		}
		if deadline := s.todoQueue.deadline(id, todo.Reminder); earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, nil
}

func (s *Store) ClaimDueTodoReminders(_ context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]domain.Todo, error) {
	if owner == "" || limit <= 0 || lease <= 0 || now.IsZero() {
		return nil, store.InvalidArgument("reminder claim requires owner, positive limit, lease, and current time")
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]domain.Todo, 0)
	for id, todo := range s.todos {
		if (workspace != "" && todo.WorkspaceID != workspace) || todo.Done() || !todo.Delivery.Pending(todo.Reminder) || !s.todoQueue.waiting(id, todo.Reminder, now) {
			continue
		}
		values = append(values, todo)
	}
	claimOrder(values, func(todo domain.Todo) domain.ReminderTiming { return todo.Reminder }, func(todo domain.Todo) string { return string(todo.ID) })
	if len(values) > limit {
		values = values[:limit]
	}
	expires := time.Unix(scheduledSecondCeil(now.Add(lease)), 0).UTC()
	for _, todo := range values {
		s.todoQueue.leases[todo.ID] = memoryLease{Owner: owner, Expires: expires}
	}
	return values, nil
}

func (s *Store) RenewTodoReminder(_ context.Context, owner string, id domain.TodoID, lease time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.todoQueue.renew(owner, id, lease, now)
}

func (s *Store) ReleaseTodoReminder(_ context.Context, owner string, id domain.TodoID, next, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.todoQueue.release(owner, id, next, now)
}

func (s *Store) MarkTodoReminderFailed(_ context.Context, owner string, id domain.TodoID, failureCode string, failedAt time.Time, event events.Event) error {
	if owner == "" || failureCode == "" || failedAt.IsZero() {
		return store.InvalidArgument("reminder failure requires owner, code, and failure time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	todo, ok := s.todos[id]
	if !ok || todo.Done() || !todo.Delivery.Pending(todo.Reminder) || !s.todoQueue.holds(owner, id, failedAt) {
		return store.ErrLeaseConflict
	}
	todo.Delivery.FailedAt = wholeSeconds(failedAt)
	todo.Delivery.FailureCode = failureCode
	todo.UpdatedAt = wholeSeconds(failedAt)
	s.todos[id] = todo
	s.todoQueue.forget(id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) MarkTodoReminderDelivered(_ context.Context, owner string, id domain.TodoID, deliveredAt, next time.Time, event events.Event) error {
	if owner == "" || deliveredAt.IsZero() {
		return store.InvalidArgument("reminder delivery requires owner and delivery time")
	}
	deliveredAt = deliveredAt.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	todo, ok := s.todos[id]
	if !ok || todo.Done() || !todo.Delivery.Pending(todo.Reminder) || !s.todoQueue.holds(owner, id, deliveredAt) {
		return store.ErrLeaseConflict
	}
	if err := advance(&todo.Reminder, &todo.Delivery, deliveredAt, next); err != nil {
		return err
	}
	todo.UpdatedAt = wholeSeconds(deliveredAt)
	s.todos[id] = todo
	preferences := domain.DefaultWorkspaceNotificationPreferences(todo.WorkspaceID, todo.UserID)
	if stored, ok := s.workspaceNotificationPrefs[workspaceNotificationKey(todo.WorkspaceID, todo.UserID)]; ok {
		preferences = stored
	}
	if preferences.ActivityReminders {
		activityID := domain.ActivityIDFor(todo.UserID, "reminder:"+string(id)+":"+string(domain.NewStoredTime(deliveredAt)))
		if _, exists := s.activityItems[activityID]; !exists {
			s.activityItems[activityID] = domain.ActivityItem{
				ID: activityID, WorkspaceID: todo.WorkspaceID, UserID: todo.UserID,
				Kinds: []domain.ActivityKind{domain.ActivityReminder}, TodoID: id,
				Conversation: todo.Source.Conversation, MessageID: todo.Source.MessageID,
				OccurredAt: deliveredAt,
			}
		}
	}
	s.todoQueue.forget(id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) EarliestChannelReminder(_ context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var earliest time.Time
	for id, reminder := range s.channelReminders {
		if (workspace != "" && reminder.WorkspaceID != workspace) || !reminder.Delivery.Pending(reminder.Reminder) {
			continue
		}
		if deadline := s.channelReminderQueue.deadline(id, reminder.Reminder); earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest, nil
}

func (s *Store) ClaimDueChannelReminders(_ context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]domain.ChannelReminder, error) {
	if owner == "" || limit <= 0 || lease <= 0 || now.IsZero() {
		return nil, store.InvalidArgument("reminder claim requires owner, positive limit, lease, and current time")
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]domain.ChannelReminder, 0)
	for id, reminder := range s.channelReminders {
		if (workspace != "" && reminder.WorkspaceID != workspace) || !reminder.Delivery.Pending(reminder.Reminder) || !s.channelReminderQueue.waiting(id, reminder.Reminder, now) {
			continue
		}
		values = append(values, reminder)
	}
	claimOrder(values, func(reminder domain.ChannelReminder) domain.ReminderTiming { return reminder.Reminder }, func(reminder domain.ChannelReminder) string { return string(reminder.ID) })
	if len(values) > limit {
		values = values[:limit]
	}
	expires := time.Unix(scheduledSecondCeil(now.Add(lease)), 0).UTC()
	for _, reminder := range values {
		s.channelReminderQueue.leases[reminder.ID] = memoryLease{Owner: owner, Expires: expires}
	}
	return values, nil
}

func (s *Store) RenewChannelReminder(_ context.Context, owner string, id domain.ChannelReminderID, lease time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.channelReminderQueue.renew(owner, id, lease, now)
}

func (s *Store) ReleaseChannelReminder(_ context.Context, owner string, id domain.ChannelReminderID, next, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.channelReminderQueue.release(owner, id, next, now)
}

func (s *Store) MarkChannelReminderFailed(_ context.Context, owner string, id domain.ChannelReminderID, failureCode string, failedAt time.Time, event events.Event) error {
	if owner == "" || failureCode == "" || failedAt.IsZero() {
		return store.InvalidArgument("reminder failure requires owner, code, and failure time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.channelReminders[id]
	if !ok || !reminder.Delivery.Pending(reminder.Reminder) || !s.channelReminderQueue.holds(owner, id, failedAt) {
		return store.ErrLeaseConflict
	}
	reminder.Delivery.FailedAt = wholeSeconds(failedAt)
	reminder.Delivery.FailureCode = failureCode
	reminder.UpdatedAt = wholeSeconds(failedAt)
	s.channelReminders[id] = reminder
	s.channelReminderQueue.forget(id)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) MarkChannelReminderDelivered(_ context.Context, owner string, id domain.ChannelReminderID, deliveredAt, next time.Time, event events.Event) error {
	if owner == "" || deliveredAt.IsZero() {
		return store.InvalidArgument("reminder delivery requires owner and delivery time")
	}
	deliveredAt = deliveredAt.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	reminder, ok := s.channelReminders[id]
	if !ok || !reminder.Delivery.Pending(reminder.Reminder) || !s.channelReminderQueue.holds(owner, id, deliveredAt) {
		return store.ErrLeaseConflict
	}
	if err := advance(&reminder.Reminder, &reminder.Delivery, deliveredAt, next); err != nil {
		return err
	}
	reminder.UpdatedAt = wholeSeconds(deliveredAt)
	s.channelReminders[id] = reminder
	s.channelReminderQueue.forget(id)
	s.outbox = append(s.outbox, event)
	return nil
}
