package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// todoSchema is part of the base schema, which creates the tables on a fresh
// database, and schema step 218 moves Later's rows into them on an existing
// one (migrateLaterToTodosAndSaved).
//
// To-dos and channel reminders were one later_reminders table told apart by a
// target column, so every reader had to remember which half it was reading,
// a to-do could not exist without a due date, and delivering a one-time
// reminder marked it completed. They are two tables now: a to-do's due_at is
// zero when it has no reminder, a channel reminder's never is, and delivery
// records last_delivered_at without touching completed_at. A reminder
// occurrence is pending while last_delivered_at < due_at.
const todoSchema = `CREATE TABLE IF NOT EXISTS todos (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 title TEXT NOT NULL, details TEXT NOT NULL DEFAULT '',
 source_message_id TEXT NOT NULL DEFAULT '', source_conversation_id TEXT NOT NULL DEFAULT '', source_timestamp TEXT NOT NULL DEFAULT '',
 due_at INTEGER NOT NULL DEFAULT 0, timezone TEXT NOT NULL DEFAULT '', recurrence TEXT NOT NULL DEFAULT '', recurrence_anchor INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, completed_at INTEGER NOT NULL DEFAULT 0,
 last_delivered_at INTEGER NOT NULL DEFAULT 0, acknowledged_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0,
 failure_code TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS todos_owner ON todos(workspace_id, user_id, completed_at, due_at, id);
CREATE INDEX IF NOT EXISTS todos_owner_created ON todos(workspace_id, user_id, completed_at, created_at, id);
CREATE INDEX IF NOT EXISTS todos_delivery ON todos(workspace_id, completed_at, failed_at, due_at, id);
CREATE TABLE IF NOT EXISTS channel_reminders (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), creator_id TEXT NOT NULL REFERENCES users(id),
 channel_id TEXT NOT NULL, text TEXT NOT NULL,
 due_at INTEGER NOT NULL, timezone TEXT NOT NULL, recurrence TEXT NOT NULL DEFAULT '', recurrence_anchor INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 last_delivered_at INTEGER NOT NULL DEFAULT 0, failed_at INTEGER NOT NULL DEFAULT 0,
 failure_code TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS channel_reminders_creator ON channel_reminders(workspace_id, creator_id, id);
CREATE INDEX IF NOT EXISTS channel_reminders_delivery ON channel_reminders(workspace_id, failed_at, due_at, id);
`

// migrateLaterToTodosAndSaved is schema step 218: Slack's Later became To-dos
// and the Saved section of Home. It maps Later's records as follows, and is
// written to be re-run safely if it is interrupted:
//
//   - a personal Later reminder becomes a to-do with the reminder's text as
//     its title, keeping its source message, due date, recurrence, delivery
//     state and completion (a one-time reminder Later showed as completed
//     after it fired stays a completed to-do);
//   - a channel reminder becomes a channel reminder, which no longer has a
//     completed state: a delivered one-time reminder simply has nothing
//     pending;
//   - a saved item Later showed as Completed becomes a completed to-do linked
//     to its message, unless the member already has a to-do for it;
//   - an In progress or Archived saved item with an open to-do for the same
//     message leaves Saved, because the to-do is where it now lives, and
//     every other In progress or Archived item stays in Saved (Archived has no
//     place in Slack's new model; keeping the bookmark loses nothing).
func (s *Store) migrateLaterToTodosAndSaved(ctx context.Context, db queryExecutor) error {
	later, err := s.tableColumns(ctx, db, "later_reminders")
	if err != nil {
		return err
	}
	if len(later) > 0 {
		if _, err := db.ExecContext(ctx, `INSERT INTO todos(
			id, workspace_id, user_id, title, details, source_message_id, source_conversation_id, source_timestamp,
			due_at, timezone, recurrence, recurrence_anchor, created_at, updated_at, completed_at,
			last_delivered_at, acknowledged_at, failed_at, failure_code, next_attempt_at)
			SELECT id, workspace_id, user_id, text, '', source_message_id, source_conversation_id, source_timestamp,
			due_at, timezone, recurrence, recurrence_anchor, created_at, updated_at, completed_at,
			last_delivered_at, acknowledged_at, failed_at, failure_code, next_attempt_at
			FROM later_reminders WHERE target = 'personal' AND user_id <> ''
			ON CONFLICT(id) DO NOTHING`); err != nil {
			return fmt.Errorf("migrate Later reminders to to-dos: %w", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO channel_reminders(
			id, workspace_id, creator_id, channel_id, text, due_at, timezone, recurrence, recurrence_anchor,
			created_at, updated_at, last_delivered_at, failed_at, failure_code, next_attempt_at)
			SELECT id, workspace_id, creator_id, channel_id, text, due_at, timezone, recurrence, recurrence_anchor,
			created_at, updated_at, last_delivered_at, failed_at, failure_code, next_attempt_at
			FROM later_reminders WHERE target = 'channel' AND channel_id <> ''
			ON CONFLICT(id) DO NOTHING`); err != nil {
			return fmt.Errorf("migrate channel reminders: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DROP TABLE later_reminders`); err != nil {
			return fmt.Errorf("drop Later reminders: %w", err)
		}
	}
	saved, err := s.tableColumns(ctx, db, "saved_items")
	if err != nil {
		return err
	}
	if !saved["state"] {
		return nil
	}
	if err := migrateCompletedSavedItems(ctx, db); err != nil {
		return err
	}
	for _, statement := range []string{
		`DROP TABLE IF EXISTS saved_items_next`,
		`CREATE TABLE saved_items_next (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 message_id TEXT NOT NULL REFERENCES messages(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 created_at TEXT NOT NULL,
 UNIQUE (workspace_id, user_id, message_id)
)`,
		`INSERT INTO saved_items_next(id, workspace_id, user_id, message_id, conversation_id, created_at)
			SELECT si.id, si.workspace_id, si.user_id, si.message_id, si.conversation_id, si.created_at FROM saved_items si
			WHERE si.state <> 'completed' AND NOT EXISTS (
				SELECT 1 FROM todos t WHERE t.workspace_id = si.workspace_id AND t.user_id = si.user_id
				  AND t.source_message_id = si.message_id AND t.completed_at = 0)`,
		`DROP TABLE saved_items`,
		`ALTER TABLE saved_items_next RENAME TO saved_items`,
		`CREATE INDEX IF NOT EXISTS saved_items_user_created ON saved_items(workspace_id, user_id, created_at, id)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate saved items out of Later: %w", err)
		}
	}
	return nil
}

// migrateCompletedSavedItems turns each saved item Later showed as Completed
// into a completed to-do. It runs in Go rather than one INSERT … SELECT
// because the to-do keeps the message's Slack timestamp and an integer
// instant, which the portable dialect cannot derive from the stored text
// encodings.
func migrateCompletedSavedItems(ctx context.Context, db queryExecutor) error {
	rows, err := db.QueryContext(ctx, `SELECT si.id, si.workspace_id, si.user_id, si.message_id, si.conversation_id, si.created_at, si.updated_at, m.text, m.created_at
		FROM saved_items si JOIN messages m ON m.id = si.message_id
		WHERE si.state = 'completed' AND NOT EXISTS (
			SELECT 1 FROM todos t WHERE t.workspace_id = si.workspace_id AND t.user_id = si.user_id AND t.source_message_id = si.message_id)
		ORDER BY si.id`)
	if err != nil {
		return fmt.Errorf("read completed saved items: %w", err)
	}
	type completed struct {
		todo domain.Todo
	}
	var pending []completed
	for rows.Next() {
		var id, workspace, user, message, conversation, created, updated, text, messageCreated string
		if err := rows.Scan(&id, &workspace, &user, &message, &conversation, &created, &updated, &text, &messageCreated); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read completed saved item: %w", err)
		}
		createdAt, createdErr := domain.ParseStoredTime(created)
		updatedAt, updatedErr := domain.ParseStoredTime(updated)
		sentAt, sentErr := domain.ParseStoredTime(messageCreated)
		if createdErr != nil || updatedErr != nil || sentErr != nil {
			_ = rows.Close()
			return fmt.Errorf("read completed saved item %s: %w", id, errors.Join(createdErr, updatedErr, sentErr))
		}
		pending = append(pending, completed{todo: domain.Todo{
			ID: domain.TodoID("todo_" + id), WorkspaceID: domain.WorkspaceID(workspace), UserID: domain.UserID(user),
			Title: domain.TodoTitleFromMessage(text),
			Source: domain.TodoSource{
				MessageID: domain.MessageID(message), Conversation: domain.ConversationID(conversation),
				Timestamp: domain.NewMessageTimestamp(sentAt),
			},
			CreatedAt: createdAt, UpdatedAt: updatedAt, CompletedAt: updatedAt,
		}})
	}
	if err := closeRows(rows); err != nil {
		return fmt.Errorf("read completed saved items: %w", err)
	}
	for _, value := range pending {
		todo := value.todo
		if _, err := db.ExecContext(ctx, `INSERT INTO todos(id, workspace_id, user_id, title, details, source_message_id, source_conversation_id, source_timestamp,
			created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			todo.ID, todo.WorkspaceID, todo.UserID, todo.Title, todo.Source.MessageID, todo.Source.Conversation, todo.Source.Timestamp,
			todo.CreatedAt.Unix(), todo.UpdatedAt.Unix(), max(todo.CompletedAt.Unix(), 1)); err != nil {
			return fmt.Errorf("migrate completed saved item %s: %w", todo.ID, err)
		}
	}
	return nil
}

// --- saved items ---

const savedItemColumns = `id, workspace_id, user_id, message_id, conversation_id, created_at`

func scanSavedItem(row rowScanner) (domain.SavedItem, error) {
	var item domain.SavedItem
	var createdAt string
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.UserID, &item.MessageID, &item.Conversation, &createdAt); err != nil {
		return domain.SavedItem{}, err
	}
	var err error
	item.CreatedAt, err = domain.ParseStoredTime(createdAt)
	if err != nil {
		return domain.SavedItem{}, err
	}
	return item, nil
}

func (s *Store) CreateSavedItem(ctx context.Context, item domain.SavedItem, event events.Event) (domain.SavedItem, bool, error) {
	if item.ID == "" || item.MessageID == "" || item.Conversation == "" {
		return domain.SavedItem{}, false, store.InvalidArgument("saved item is incomplete")
	}
	item.Message = domain.Message{}
	item.SourceAvailable = false
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.SavedItem{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO saved_items(id, workspace_id, user_id, message_id, conversation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, message_id) DO NOTHING`,
		item.ID, item.WorkspaceID, item.UserID, item.MessageID, item.Conversation, domain.NewStoredTime(item.CreatedAt))
	if err != nil {
		return domain.SavedItem{}, false, classify(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.SavedItem{}, false, err
	}
	if count == 0 {
		existing, err := scanSavedItem(tx.QueryRowContext(ctx, `SELECT `+savedItemColumns+` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND message_id = ?`, item.WorkspaceID, item.UserID, item.MessageID))
		if errors.Is(err, sql.ErrNoRows) {
			// The conflict was on the identifier, not the message: another
			// member's item already holds it.
			return domain.SavedItem{}, false, store.ErrAlreadyExists
		}
		return existing, false, err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.SavedItem{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SavedItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) GetSavedItem(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID) (domain.SavedItem, error) {
	item, err := scanSavedItem(s.db.QueryRowContext(ctx, `SELECT `+savedItemColumns+` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SavedItem{}, store.ErrNotFound
	}
	return item, err
}

func (s *Store) GetSavedItemByMessage(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, message domain.MessageID) (domain.SavedItem, error) {
	item, err := scanSavedItem(s.db.QueryRowContext(ctx, `SELECT `+savedItemColumns+` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND message_id = ?`, workspace, user, message))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SavedItem{}, store.ErrNotFound
	}
	return item, err
}

func (s *Store) ListSavedItemsForMessages(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, messages []domain.MessageID) ([]domain.SavedItem, error) {
	if len(messages) == 0 {
		return []domain.SavedItem{}, nil
	}
	query := `SELECT ` + savedItemColumns + ` FROM saved_items WHERE workspace_id = ? AND user_id = ? AND message_id IN (` + placeholders(len(messages)) + `) ORDER BY id`
	args := make([]any, 0, len(messages)+2)
	args = append(args, workspace, user)
	for _, message := range messages {
		args = append(args, message)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SavedItem, 0, len(messages))
	for rows.Next() {
		item, err := scanSavedItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ListSavedItems(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, request domain.PageRequest) (domain.SavedItemPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.SavedItemPage{}, err
	}
	createdAt, id, err := decodeSavedItemCursor(request.Cursor)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	query := `SELECT ` + savedItemColumns + ` FROM saved_items WHERE workspace_id = ? AND user_id = ?`
	args := []any{workspace, user}
	if id != "" {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, createdAt, createdAt, id)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, request.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.SavedItemPage{}, err
	}
	defer rows.Close()
	items := make([]domain.SavedItem, 0, request.Limit+1)
	for rows.Next() {
		item, err := scanSavedItem(rows)
		if err != nil {
			return domain.SavedItemPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.SavedItemPage{}, err
	}
	return savedItemPage(items, request.Limit)
}

// savedItemPage trims a newest-first read of limit+1 rows to a page whose
// cursor names its last item; the memory profile shares it.
func savedItemPage(items []domain.SavedItem, limit int) (domain.SavedItemPage, error) {
	page := domain.SavedItemPage{Items: items, HasMore: len(items) > limit}
	if !page.HasMore {
		return page, nil
	}
	page.Items = items[:limit]
	last := page.Items[limit-1]
	var err error
	page.NextCursor, err = domain.NewListCursor(string(domain.NewStoredTime(last.CreatedAt)) + "\x00" + string(last.ID))
	return page, err
}

func decodeSavedItemCursor(cursor domain.Cursor) (domain.StoredTime, domain.SavedItemID, error) {
	raw, err := domain.DecodeListCursor(cursor)
	if err != nil || raw == "" {
		return "", "", err
	}
	createdAt, id, ok := strings.Cut(raw, "\x00")
	if !ok || createdAt == "" || id == "" {
		return "", "", domain.ErrInvalidCursor
	}
	if _, err := domain.ParseStoredTime(createdAt); err != nil {
		return "", "", domain.ErrInvalidCursor
	}
	return domain.StoredTime(createdAt), domain.SavedItemID(id), nil
}

func (s *Store) DeleteSavedItem(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM saved_items WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClearSavedItems(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, event events.Event) (int, error) {
	if workspace == "" || user == "" {
		return 0, store.InvalidArgument("clearing saved items needs a member")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM saved_items WHERE workspace_id = ? AND user_id = ?`, workspace, user)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return 0, err
	}
	return int(count), tx.Commit()
}

func (s *Store) MoveSavedItemToTodo(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.SavedItemID, todo domain.Todo, event events.Event) error {
	if todo.WorkspaceID != workspace || todo.UserID != user || !todo.Valid() {
		return store.InvalidArgument("the to-do a saved item moves to is invalid")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var message domain.MessageID
	if err := tx.QueryRowContext(ctx, `SELECT message_id FROM saved_items WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id).Scan(&message); err != nil {
		return translateNotFound(err)
	}
	if message != todo.Source.MessageID {
		return store.InvalidArgument("the to-do must link the saved message")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM saved_items WHERE workspace_id = ? AND user_id = ? AND id = ?`, workspace, user, id); err != nil {
		return err
	}
	if err := insertTodo(ctx, tx, todo); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// --- to-dos ---

const todoColumns = `id, workspace_id, user_id, title, details, source_message_id, source_conversation_id, source_timestamp,
	due_at, timezone, recurrence, recurrence_anchor, created_at, updated_at, completed_at,
	last_delivered_at, acknowledged_at, failed_at, failure_code`

func scanTodo(scanner rowScanner) (domain.Todo, error) {
	var value domain.Todo
	var due, anchor, created, updated, completed, delivered, acknowledged, failed int64
	if err := scanner.Scan(
		&value.ID, &value.WorkspaceID, &value.UserID, &value.Title, &value.Details,
		&value.Source.MessageID, &value.Source.Conversation, &value.Source.Timestamp,
		&due, &value.Reminder.TimeZone, &value.Reminder.Recurrence, &anchor, &created, &updated, &completed,
		&delivered, &acknowledged, &failed, &value.Delivery.FailureCode,
	); err != nil {
		return domain.Todo{}, err
	}
	value.Reminder.DueAt = fromUnixSeconds(due)
	value.Reminder.RecurrenceAnchor = fromUnixSeconds(anchor)
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	value.CompletedAt = fromUnixSeconds(completed)
	value.Delivery.LastDeliveredAt = fromUnixSeconds(delivered)
	value.Delivery.AcknowledgedAt = fromUnixSeconds(acknowledged)
	value.Delivery.FailedAt = fromUnixSeconds(failed)
	if !value.Reminder.Recurrence.Valid() {
		return domain.Todo{}, errors.New("stored to-do has an invalid recurrence")
	}
	return value, nil
}

func insertTodo(ctx context.Context, tx txRunner, todo domain.Todo) error {
	if todo.Source.Set() {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ? AND workspace_id = ? AND conversation = ?`,
			todo.Source.MessageID, todo.WorkspaceID, todo.Source.Conversation).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			return store.ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO todos(`+todoColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		todo.ID, todo.WorkspaceID, todo.UserID, todo.Title, todo.Details,
		todo.Source.MessageID, todo.Source.Conversation, todo.Source.Timestamp,
		unixSeconds(todo.Reminder.DueAt), todo.Reminder.TimeZone, todo.Reminder.Recurrence, unixSeconds(todo.Reminder.RecurrenceAnchor),
		todo.CreatedAt.Unix(), todo.UpdatedAt.Unix(), unixSeconds(todo.CompletedAt),
		unixSeconds(todo.Delivery.LastDeliveredAt), unixSeconds(todo.Delivery.AcknowledgedAt), unixSeconds(todo.Delivery.FailedAt), todo.Delivery.FailureCode,
	); err != nil {
		return classify(err)
	}
	return nil
}

func (s *Store) CreateTodo(ctx context.Context, todo domain.Todo, event events.Event) error {
	if !todo.Valid() {
		return store.InvalidArgument("to-do is invalid")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertTodo(ctx, tx, todo); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetTodo(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.TodoID) (domain.Todo, error) {
	value, err := scanTodo(s.db.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspace, user))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Todo{}, store.ErrNotFound
	}
	return value, err
}

func (s *Store) ListTodos(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, query domain.TodoQuery) (domain.TodoPage, error) {
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
	now := query.Now.UTC().Unix()
	statement := `SELECT ` + todoColumns + ` FROM todos WHERE workspace_id = ? AND user_id = ?`
	args := []any{workspace, user}
	if query.Done {
		statement += ` AND completed_at <> 0`
	} else {
		statement += ` AND completed_at = 0`
	}
	if len(query.Reminders) > 0 {
		groups := make([]string, 0, len(query.Reminders))
		for _, status := range query.Reminders {
			switch status {
			case domain.TodoOverdue:
				groups = append(groups, `(due_at <> 0 AND due_at <= ?)`)
				args = append(args, now)
			case domain.TodoUpcoming:
				groups = append(groups, `due_at > ?`)
				args = append(args, now)
			case domain.TodoNoReminder:
				groups = append(groups, `due_at = 0`)
			}
		}
		statement += ` AND (` + strings.Join(groups, ` OR `) + `)`
	}
	switch query.Sort {
	case domain.TodoSortDueDate:
		// No reminder sorts after every date: due_at = 0 compares as the
		// largest key through the leading flag.
		const undated = `(CASE WHEN due_at = 0 THEN 1 ELSE 0 END)`
		if resume {
			flag := 0
			if after.DueAt == 0 {
				flag = 1
			}
			statement += ` AND (` + undated + ` > ? OR (` + undated + ` = ? AND (due_at > ? OR (due_at = ? AND id > ?))))`
			args = append(args, flag, flag, after.DueAt, after.DueAt, after.ID)
		}
		statement += ` ORDER BY ` + undated + `, due_at, id`
	case domain.TodoSortLatestFirst:
		if resume {
			statement += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
			args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
		}
		statement += ` ORDER BY created_at DESC, id DESC`
	default:
		if resume {
			statement += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
			args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
		}
		statement += ` ORDER BY created_at, id`
	}
	statement += ` LIMIT ?`
	args = append(args, query.Page.Limit+1)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return domain.TodoPage{}, err
	}
	defer rows.Close()
	items := make([]domain.Todo, 0, query.Page.Limit+1)
	for rows.Next() {
		value, err := scanTodo(rows)
		if err != nil {
			return domain.TodoPage{}, err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return domain.TodoPage{}, err
	}
	return store.TodoPageOf(items, query)
}

func (s *Store) UpdateTodo(ctx context.Context, todo domain.Todo, event events.Event) (domain.Todo, error) {
	if !todo.Valid() {
		return domain.Todo{}, store.InvalidArgument("to-do is invalid")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.Todo{}, err
	}
	defer tx.Rollback()
	current, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todos
		WHERE id = ? AND workspace_id = ? AND user_id = ? AND (lease_until = 0 OR lease_until <= ?)`,
		todo.ID, todo.WorkspaceID, todo.UserID, s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Todo{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Todo{}, err
	}
	if current.Source != todo.Source {
		return domain.Todo{}, store.InvalidArgument("a to-do's source cannot change")
	}
	if store.SameReminderTiming(current.Reminder, todo.Reminder) {
		_, err = tx.ExecContext(ctx, `UPDATE todos SET title = ?, details = ?, updated_at = ? WHERE id = ?`,
			todo.Title, todo.Details, todo.UpdatedAt.Unix(), todo.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE todos
			SET title = ?, details = ?, due_at = ?, timezone = ?, recurrence = ?, recurrence_anchor = ?, updated_at = ?,
			    last_delivered_at = 0, acknowledged_at = 0, failed_at = 0, failure_code = '', lease_owner = '', lease_until = 0, next_attempt_at = 0
			WHERE id = ?`,
			todo.Title, todo.Details, unixSeconds(todo.Reminder.DueAt), todo.Reminder.TimeZone, todo.Reminder.Recurrence,
			unixSeconds(todo.Reminder.RecurrenceAnchor), todo.UpdatedAt.Unix(), todo.ID)
	}
	if err != nil {
		return domain.Todo{}, err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.Todo{}, err
	}
	updated, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = ?`, todo.ID))
	if err != nil {
		return domain.Todo{}, err
	}
	return updated, tx.Commit()
}

func (s *Store) SetTodoCompletion(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.TodoID, completed time.Time, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT completed_at FROM todos WHERE id = ? AND workspace_id = ? AND user_id = ? AND (lease_until = 0 OR lease_until <= ?)`,
		id, workspace, user, s.now().Unix()).Scan(&current); err != nil {
		return translateNotFound(err)
	}
	if (current != 0) == !completed.IsZero() {
		return nil
	}
	updated := completed
	if updated.IsZero() {
		updated = s.now()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE todos SET completed_at = ?, updated_at = ? WHERE id = ?`,
		unixSeconds(completed), updated.Unix(), id); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteTodo(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.TodoID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM todos WHERE id = ? AND workspace_id = ? AND user_id = ? AND (lease_until = 0 OR lease_until <= ?)`,
		id, workspace, user, s.now().Unix())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_items WHERE workspace_id = ? AND user_id = ? AND reminder_id = ?`, workspace, user, id); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeTodoReminders(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, acknowledged time.Time, event events.Event) error {
	if workspace == "" || user == "" || acknowledged.IsZero() {
		return store.InvalidArgument("to-do reminder acknowledgement is incomplete")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE todos SET acknowledged_at = last_delivered_at
		WHERE workspace_id = ? AND user_id = ? AND last_delivered_at > acknowledged_at`, workspace, user)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE activity_items SET read_at = ? WHERE workspace_id = ? AND user_id = ? AND reminder_id <> '' AND read_at = 0`,
		acknowledged.UTC().UnixNano(), workspace, user); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// --- channel reminders ---

const channelReminderColumns = `id, workspace_id, creator_id, channel_id, text, due_at, timezone, recurrence, recurrence_anchor,
	created_at, updated_at, last_delivered_at, failed_at, failure_code`

func scanChannelReminder(scanner rowScanner) (domain.ChannelReminder, error) {
	var value domain.ChannelReminder
	var due, anchor, created, updated, delivered, failed int64
	if err := scanner.Scan(
		&value.ID, &value.WorkspaceID, &value.Creator, &value.Channel, &value.Text,
		&due, &value.Reminder.TimeZone, &value.Reminder.Recurrence, &anchor, &created, &updated,
		&delivered, &failed, &value.Delivery.FailureCode,
	); err != nil {
		return domain.ChannelReminder{}, err
	}
	value.Reminder.DueAt = fromUnixSeconds(due)
	value.Reminder.RecurrenceAnchor = fromUnixSeconds(anchor)
	value.CreatedAt = time.Unix(created, 0).UTC()
	value.UpdatedAt = time.Unix(updated, 0).UTC()
	value.Delivery.LastDeliveredAt = fromUnixSeconds(delivered)
	value.Delivery.FailedAt = fromUnixSeconds(failed)
	if !value.Reminder.Recurrence.Valid() {
		return domain.ChannelReminder{}, errors.New("stored channel reminder has an invalid recurrence")
	}
	return value, nil
}

func (s *Store) CreateChannelReminder(ctx context.Context, reminder domain.ChannelReminder, event events.Event) error {
	if !reminder.Valid() {
		return store.InvalidArgument("channel reminder is invalid")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id = ? AND workspace_id = ?`, reminder.Channel, reminder.WorkspaceID).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO channel_reminders(`+channelReminderColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reminder.ID, reminder.WorkspaceID, reminder.Creator, reminder.Channel, reminder.Text,
		reminder.Reminder.DueAt.Unix(), reminder.Reminder.TimeZone, reminder.Reminder.Recurrence, unixSeconds(reminder.Reminder.RecurrenceAnchor),
		reminder.CreatedAt.Unix(), reminder.UpdatedAt.Unix(),
		unixSeconds(reminder.Delivery.LastDeliveredAt), unixSeconds(reminder.Delivery.FailedAt), reminder.Delivery.FailureCode,
	); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetChannelReminder(ctx context.Context, workspace domain.WorkspaceID, creator domain.UserID, id domain.ChannelReminderID) (domain.ChannelReminder, error) {
	value, err := scanChannelReminder(s.db.QueryRowContext(ctx, `SELECT `+channelReminderColumns+` FROM channel_reminders WHERE id = ? AND workspace_id = ? AND creator_id = ?`, id, workspace, creator))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ChannelReminder{}, store.ErrNotFound
	}
	return value, err
}

func (s *Store) ListChannelReminders(ctx context.Context, workspace domain.WorkspaceID, creator domain.UserID, request domain.PageRequest) (domain.ChannelReminderPage, error) {
	if err := store.CheckAscendingPage(request); err != nil {
		return domain.ChannelReminderPage{}, err
	}
	after, err := domain.DecodeListCursor(request.Cursor)
	if err != nil {
		return domain.ChannelReminderPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+channelReminderColumns+` FROM channel_reminders
		WHERE workspace_id = ? AND creator_id = ? AND id > ? ORDER BY id LIMIT ?`, workspace, creator, after, request.Limit+1)
	if err != nil {
		return domain.ChannelReminderPage{}, err
	}
	defer rows.Close()
	items := make([]domain.ChannelReminder, 0, request.Limit+1)
	for rows.Next() {
		value, err := scanChannelReminder(rows)
		if err != nil {
			return domain.ChannelReminderPage{}, err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelReminderPage{}, err
	}
	page := domain.ChannelReminderPage{Items: items, HasMore: len(items) > request.Limit}
	if page.HasMore {
		page.Items = page.Items[:request.Limit]
		page.NextCursor, err = domain.NewListCursor(string(page.Items[len(page.Items)-1].ID))
	}
	return page, err
}

func (s *Store) DeleteChannelReminder(ctx context.Context, workspace domain.WorkspaceID, creator domain.UserID, id domain.ChannelReminderID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM channel_reminders WHERE id = ? AND workspace_id = ? AND creator_id = ? AND (lease_until = 0 OR lease_until <= ?)`,
		id, workspace, creator, s.now().Unix())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// --- delivery ---

// reminderQueue is one table of first-party reminders the delivery worker
// drains. To-dos and channel reminders keep the same delivery columns, so the
// lease protocol is written once; open narrows the due set to rows still
// waiting (a done to-do's reminder does not fire).
type reminderQueue struct {
	table string
	open  string
}

var (
	todoQueue            = reminderQueue{table: "todos", open: "completed_at = 0"}
	channelReminderQueue = reminderQueue{table: "channel_reminders", open: "1 = 1"}
)

// pending is the predicate for a reminder occurrence still to deliver.
func (q reminderQueue) pending() string {
	return q.open + ` AND failed_at = 0 AND due_at <> 0 AND last_delivered_at < due_at`
}

func (q reminderQueue) earliest(ctx context.Context, db *sql.DB, workspace domain.WorkspaceID) (time.Time, error) {
	var dueAt sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MIN(CASE WHEN next_attempt_at > due_at THEN next_attempt_at ELSE due_at END)
		FROM `+q.table+` WHERE (? = '' OR workspace_id = ?) AND `+q.pending(), workspace, workspace).Scan(&dueAt); err != nil {
		return time.Time{}, err
	}
	if !dueAt.Valid || dueAt.Int64 == 0 {
		return time.Time{}, nil
	}
	return time.Unix(dueAt.Int64, 0).UTC(), nil
}

// claim leases up to limit due rows and returns them through scan.
func claimDue[T any](ctx context.Context, s *Store, q reminderQueue, columns string, scan func(rowScanner) (T, error), id func(T) string, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]T, error) {
	if owner == "" || limit <= 0 || lease <= 0 || now.IsZero() {
		return nil, store.InvalidArgument("reminder claim requires owner, positive limit, lease, and current time")
	}
	now = now.UTC()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM `+q.table+`
		WHERE (? = '' OR workspace_id = ?) AND `+q.pending()+`
		  AND due_at <= ? AND (lease_until = 0 OR lease_until <= ?)
		  AND (next_attempt_at = 0 OR next_attempt_at <= ?)
		ORDER BY due_at, id LIMIT ?`, workspace, workspace, now.Unix(), now.Unix(), now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0, limit)
	for rows.Next() {
		value, scanErr := scan(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	expires := scheduledUnixSecondCeil(now.Add(lease))
	for _, value := range values {
		result, updateErr := tx.ExecContext(ctx, `UPDATE `+q.table+` SET lease_owner = ?, lease_until = ?
			WHERE id = ? AND `+q.pending()+` AND (lease_until = 0 OR lease_until <= ?)`,
			owner, expires, id(value), now.Unix())
		if updateErr != nil {
			return nil, updateErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, rowsErr
		}
		if changed != 1 {
			return nil, store.ErrLeaseConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

func (q reminderQueue) renew(ctx context.Context, db *sql.DB, owner, id string, lease time.Duration, now time.Time) error {
	if owner == "" || lease <= 0 || now.IsZero() {
		return store.InvalidArgument("reminder renewal requires owner, lease, and current time")
	}
	now = now.UTC()
	result, err := db.ExecContext(ctx, `UPDATE `+q.table+` SET lease_until = ?
		WHERE id = ? AND lease_owner = ? AND `+q.pending()+` AND lease_until > ?`,
		scheduledUnixSecondCeil(now.Add(lease)), id, owner, now.Unix())
	return oneLeasedRow(result, err)
}

func (q reminderQueue) release(ctx context.Context, db *sql.DB, owner, id string, next, now time.Time) error {
	if owner == "" || next.IsZero() || now.IsZero() {
		return store.InvalidArgument("reminder release requires owner, retry time, and current time")
	}
	result, err := db.ExecContext(ctx, `UPDATE `+q.table+` SET lease_owner = '', lease_until = 0, next_attempt_at = ?
		WHERE id = ? AND lease_owner = ? AND `+q.pending()+` AND lease_until > ?`,
		scheduledUnixSecondCeil(next), id, owner, now.UTC().Unix())
	return oneLeasedRow(result, err)
}

func (q reminderQueue) markFailed(ctx context.Context, tx *writeTx, owner, id, failureCode string, failedAt time.Time) error {
	if owner == "" || failureCode == "" || failedAt.IsZero() {
		return store.InvalidArgument("reminder failure requires owner, code, and failure time")
	}
	failedAt = failedAt.UTC()
	result, err := tx.ExecContext(ctx, `UPDATE `+q.table+`
		SET failed_at = ?, failure_code = ?, updated_at = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0
		WHERE id = ? AND lease_owner = ? AND `+q.pending()+` AND lease_until > ?`,
		failedAt.Unix(), failureCode, failedAt.Unix(), id, owner, failedAt.Unix())
	return oneLeasedRow(result, err)
}

// markDelivered records one occurrence. A one-time reminder keeps its due
// date, which last_delivered_at now reaches; a recurring one moves to next.
func (q reminderQueue) markDelivered(ctx context.Context, tx *writeTx, owner, id string, deliveredAt, next time.Time) error {
	if owner == "" || deliveredAt.IsZero() {
		return store.InvalidArgument("reminder delivery requires owner and delivery time")
	}
	deliveredAt = deliveredAt.UTC()
	var result sql.Result
	var err error
	if next.IsZero() {
		result, err = tx.ExecContext(ctx, `UPDATE `+q.table+`
			SET last_delivered_at = ?, updated_at = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0
			WHERE id = ? AND lease_owner = ? AND recurrence = '' AND due_at <= ? AND `+q.pending()+` AND lease_until > ?`,
			deliveredAt.Unix(), deliveredAt.Unix(), id, owner, deliveredAt.Unix(), deliveredAt.Unix())
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE `+q.table+`
			SET due_at = ?, last_delivered_at = ?, updated_at = ?, lease_owner = '', lease_until = 0, next_attempt_at = 0
			WHERE id = ? AND lease_owner = ? AND recurrence <> '' AND due_at < ? AND `+q.pending()+` AND lease_until > ?`,
			next.UTC().Unix(), deliveredAt.Unix(), deliveredAt.Unix(), id, owner, next.UTC().Unix(), deliveredAt.Unix())
	}
	return oneLeasedRow(result, err)
}

func oneLeasedRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrLeaseConflict
	}
	return nil
}

func (s *Store) EarliestTodoReminder(ctx context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	return todoQueue.earliest(ctx, s.db, workspace)
}

func (s *Store) ClaimDueTodoReminders(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]domain.Todo, error) {
	return claimDue(ctx, s, todoQueue, todoColumns, scanTodo, func(todo domain.Todo) string { return string(todo.ID) }, workspace, owner, limit, lease, now)
}

func (s *Store) RenewTodoReminder(ctx context.Context, owner string, id domain.TodoID, lease time.Duration, now time.Time) error {
	return todoQueue.renew(ctx, s.db, owner, string(id), lease, now)
}

func (s *Store) ReleaseTodoReminder(ctx context.Context, owner string, id domain.TodoID, next, now time.Time) error {
	return todoQueue.release(ctx, s.db, owner, string(id), next, now)
}

func (s *Store) MarkTodoReminderFailed(ctx context.Context, owner string, id domain.TodoID, failureCode string, failedAt time.Time, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := todoQueue.markFailed(ctx, tx, owner, string(id), failureCode, failedAt); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkTodoReminderDelivered(ctx context.Context, owner string, id domain.TodoID, deliveredAt, next time.Time, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := todoQueue.markDelivered(ctx, tx, owner, string(id), deliveredAt, next); err != nil {
		return err
	}
	var todo domain.Todo
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id, user_id, source_message_id, source_conversation_id FROM todos WHERE id = ?`, id).
		Scan(&todo.WorkspaceID, &todo.UserID, &todo.Source.MessageID, &todo.Source.Conversation); err != nil {
		return err
	}
	activityReminders := 1
	if err := tx.QueryRowContext(ctx, `SELECT activity_reminders FROM notification_preferences WHERE workspace_id = ? AND user_id = ?`, todo.WorkspaceID, todo.UserID).Scan(&activityReminders); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if activityReminders != 0 {
		deliveredAt = deliveredAt.UTC()
		activityID := domain.ActivityIDFor(todo.UserID, "reminder:"+string(id)+":"+string(domain.NewStoredTime(deliveredAt)))
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_items(id, workspace_id, user_id, conversation_id, message_id, reminder_id, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			activityID, todo.WorkspaceID, todo.UserID, todo.Source.Conversation, todo.Source.MessageID, id, deliveredAt.UnixNano()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_item_kinds(activity_id, kind) VALUES (?, ?) ON CONFLICT(activity_id, kind) DO NOTHING`, activityID, domain.ActivityReminder); err != nil {
			return err
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EarliestChannelReminder(ctx context.Context, workspace domain.WorkspaceID) (time.Time, error) {
	return channelReminderQueue.earliest(ctx, s.db, workspace)
}

func (s *Store) ClaimDueChannelReminders(ctx context.Context, workspace domain.WorkspaceID, owner string, limit int, lease time.Duration, now time.Time) ([]domain.ChannelReminder, error) {
	return claimDue(ctx, s, channelReminderQueue, channelReminderColumns, scanChannelReminder, func(reminder domain.ChannelReminder) string { return string(reminder.ID) }, workspace, owner, limit, lease, now)
}

func (s *Store) RenewChannelReminder(ctx context.Context, owner string, id domain.ChannelReminderID, lease time.Duration, now time.Time) error {
	return channelReminderQueue.renew(ctx, s.db, owner, string(id), lease, now)
}

func (s *Store) ReleaseChannelReminder(ctx context.Context, owner string, id domain.ChannelReminderID, next, now time.Time) error {
	return channelReminderQueue.release(ctx, s.db, owner, string(id), next, now)
}

func (s *Store) MarkChannelReminderFailed(ctx context.Context, owner string, id domain.ChannelReminderID, failureCode string, failedAt time.Time, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := channelReminderQueue.markFailed(ctx, tx, owner, string(id), failureCode, failedAt); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkChannelReminderDelivered(ctx context.Context, owner string, id domain.ChannelReminderID, deliveredAt, next time.Time, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := channelReminderQueue.markDelivered(ctx, tx, owner, string(id), deliveredAt, next); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
