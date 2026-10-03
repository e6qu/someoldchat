package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// slackbotResponseSchema is part of the base schema, which creates it on a
// fresh database, and is schema step 208, which creates it on an existing one.
//
// A custom response's phrases and replies are read and written whole, so they
// are JSON. The cursor is how far Slackbot has read the event journal for
// messages to answer, per workspace ("" for a reader of every workspace).
const slackbotResponseSchema = `CREATE TABLE IF NOT EXISTS slackbot_responses (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 triggers TEXT NOT NULL, replies TEXT NOT NULL, created_by TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS slackbot_responses_workspace ON slackbot_responses(workspace_id, created_at, id);
CREATE TABLE IF NOT EXISTS slackbot_response_cursor (
 workspace_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL
);
`

func (s *Store) CreateSlackbotResponse(ctx context.Context, value domain.SlackbotResponse, event events.Event) error {
	triggers, err := json.Marshal(value.Triggers)
	if err != nil {
		return err
	}
	replies, err := json.Marshal(value.Replies)
	if err != nil {
		return err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO slackbot_responses(id, workspace_id, triggers, replies, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		value.ID, value.WorkspaceID, string(triggers), string(replies), value.CreatedBy, value.CreatedAt.UnixNano()); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListSlackbotResponses(ctx context.Context, workspace domain.WorkspaceID) ([]domain.SlackbotResponse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, triggers, replies, created_by, created_at FROM slackbot_responses
		WHERE workspace_id = ? ORDER BY created_at, id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []domain.SlackbotResponse{}
	for rows.Next() {
		var value domain.SlackbotResponse
		var triggers, replies string
		var created int64
		if err := rows.Scan(&value.ID, &value.WorkspaceID, &triggers, &replies, &value.CreatedBy, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(triggers), &value.Triggers); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(replies), &value.Replies); err != nil {
			return nil, err
		}
		value.CreatedAt = time.Unix(0, created).UTC()
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) DeleteSlackbotResponse(ctx context.Context, workspace domain.WorkspaceID, id domain.SlackbotResponseID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM slackbot_responses WHERE workspace_id = ? AND id = ?`, workspace, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// SlackbotResponseCursor reads the cursor, starting it at the head of the
// journal the first time: Slackbot answers messages from then on, never the
// history that came before it was reading.
func (s *Store) SlackbotResponseCursor(ctx context.Context, workspace domain.WorkspaceID) (uint64, error) {
	var sequence uint64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM slackbot_response_cursor WHERE workspace_id = ?`, workspace).Scan(&sequence)
	if err == nil {
		return sequence, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	head := `SELECT COALESCE(MAX(sequence), 0) FROM outbox`
	args := []any{workspace}
	if workspace != "" {
		head += ` WHERE workspace_id = ?`
		args = append(args, workspace)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO slackbot_response_cursor(workspace_id, sequence) SELECT ?, (`+head+`)
		ON CONFLICT(workspace_id) DO NOTHING`, args...); err != nil {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT sequence FROM slackbot_response_cursor WHERE workspace_id = ?`, workspace).Scan(&sequence)
	return sequence, err
}

// AdvanceSlackbotResponseCursor moves the cursor forward only.
func (s *Store) AdvanceSlackbotResponseCursor(ctx context.Context, workspace domain.WorkspaceID, sequence uint64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO slackbot_response_cursor(workspace_id, sequence) VALUES (?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET sequence = excluded.sequence
		WHERE slackbot_response_cursor.sequence < excluded.sequence`, workspace, sequence)
	return err
}
