package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// codeChannelViewSchema is part of the base schema, which creates it on a
// fresh database, and is schema step 206, which creates it on an existing one.
// A view is unique by its channel and the agent's key for it; its CSP is read
// and written whole, so it is JSON. A conversation's deletion removes its
// views (see the conversation footprint in sqlstore.go).
const codeChannelViewSchema = `CREATE TABLE IF NOT EXISTS code_channel_views (
 view_id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 file_id TEXT NOT NULL, view_key TEXT NOT NULL, view_type TEXT NOT NULL, label TEXT NOT NULL DEFAULT '',
 app_id TEXT NOT NULL DEFAULT '', bot_user_id TEXT NOT NULL DEFAULT '',
 content TEXT NOT NULL DEFAULT '', blocks TEXT NOT NULL DEFAULT '', canvas_id TEXT NOT NULL DEFAULT '', access_level TEXT NOT NULL DEFAULT '',
 agent_content_hash TEXT NOT NULL DEFAULT '', pr_url TEXT NOT NULL DEFAULT '', base_branch TEXT NOT NULL DEFAULT '', head_branch TEXT NOT NULL DEFAULT '',
 csp TEXT NOT NULL DEFAULT '{}', version INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS code_channel_views_key ON code_channel_views(workspace_id, conversation_id, view_key);
`

const codeChannelViewColumns = `view_id, workspace_id, conversation_id, file_id, view_key, view_type, label, app_id, bot_user_id, content, blocks, canvas_id, access_level, agent_content_hash, pr_url, base_branch, head_branch, csp, version, created_at, updated_at`

func scanCodeChannelView(row rowScanner) (domain.CodeChannelView, error) {
	var value domain.CodeChannelView
	var csp string
	var created, updated int64
	if err := row.Scan(&value.ID, &value.WorkspaceID, &value.Conversation, &value.FileID, &value.Key, &value.Type, &value.Label, &value.AppID, &value.BotUserID,
		&value.Content, &value.Blocks, &value.CanvasID, &value.AccessLevel, &value.AgentContentHash, &value.PRURL, &value.BaseBranch, &value.HeadBranch,
		&csp, &value.Version, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.CodeChannelView{}, store.ErrNotFound
		}
		return domain.CodeChannelView{}, err
	}
	var err error
	if value.CSP, err = domain.DecodeCodeChannelViewCSP(csp); err != nil {
		return domain.CodeChannelView{}, err
	}
	value.CreatedAt = time.Unix(0, created).UTC()
	value.UpdatedAt = time.Unix(0, updated).UTC()
	return value, nil
}

func (s *Store) SetCodeChannelView(ctx context.Context, view domain.CodeChannelView, event events.Event) (domain.CodeChannelView, error) {
	csp, err := domain.EncodeCodeChannelViewCSP(view.CSP)
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.CodeChannelView{}, err
	}
	defer tx.Rollback()
	existing, err := scanCodeChannelView(tx.QueryRowContext(ctx, `SELECT `+codeChannelViewColumns+` FROM code_channel_views WHERE workspace_id = ? AND conversation_id = ? AND view_key = ?`,
		view.WorkspaceID, view.Conversation, view.Key))
	switch {
	case err == nil:
		view.ID, view.FileID, view.CreatedAt, view.Version = existing.ID, existing.FileID, existing.CreatedAt, existing.Version+1
		if _, err := tx.ExecContext(ctx, `UPDATE code_channel_views SET view_type = ?, label = ?, app_id = ?, bot_user_id = ?, content = ?, blocks = ?, canvas_id = ?, access_level = ?,
			agent_content_hash = ?, pr_url = ?, base_branch = ?, head_branch = ?, csp = ?, version = ?, updated_at = ? WHERE view_id = ?`,
			view.Type, view.Label, view.AppID, view.BotUserID, view.Content, view.Blocks, view.CanvasID, view.AccessLevel, view.AgentContentHash,
			view.PRURL, view.BaseBranch, view.HeadBranch, csp, view.Version, view.UpdatedAt.UnixNano(), view.ID); err != nil {
			return domain.CodeChannelView{}, classify(err)
		}
	case errors.Is(err, store.ErrNotFound):
		view.Version = 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO code_channel_views(`+codeChannelViewColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			view.ID, view.WorkspaceID, view.Conversation, view.FileID, view.Key, view.Type, view.Label, view.AppID, view.BotUserID,
			view.Content, view.Blocks, view.CanvasID, view.AccessLevel, view.AgentContentHash, view.PRURL, view.BaseBranch, view.HeadBranch,
			csp, view.Version, view.CreatedAt.UnixNano(), view.UpdatedAt.UnixNano()); err != nil {
			return domain.CodeChannelView{}, classify(err)
		}
	default:
		return domain.CodeChannelView{}, err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.CodeChannelView{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CodeChannelView{}, err
	}
	return view, nil
}

func (s *Store) ListCodeChannelViews(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) ([]domain.CodeChannelView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+codeChannelViewColumns+` FROM code_channel_views WHERE workspace_id = ? AND conversation_id = ? ORDER BY created_at, view_id`, workspace, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := []domain.CodeChannelView{}
	for rows.Next() {
		view, err := scanCodeChannelView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

func (s *Store) RemoveCodeChannelView(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, id domain.CodeChannelViewID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM code_channel_views WHERE workspace_id = ? AND conversation_id = ? AND view_id = ?`, workspace, conversation, id)
	if err != nil {
		return err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed == 0 {
		return store.ErrNotFound
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
