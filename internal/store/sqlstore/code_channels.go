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

// codeChannelSchema is part of the base schema, which creates it on a fresh
// database, and is schema step 205, which creates it on an existing one.
//
// A code channel's record sits beside its conversation. An agent's session
// key names at most one channel, so creating with it again finds that one.
// The context bar and the agent resource are each read and written whole, so
// they are JSON. A conversation's deletion removes the record (see the
// conversation footprint in sqlstore.go).
const codeChannelSchema = `CREATE TABLE IF NOT EXISTS code_channels (
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 app_id TEXT NOT NULL, bot_user_id TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '',
 origin_channel_id TEXT NOT NULL DEFAULT '', origin_ts TEXT NOT NULL DEFAULT '',
 context_bar TEXT NOT NULL DEFAULT '[]', summary_message_ts TEXT NOT NULL DEFAULT '', summary_thread_ts TEXT NOT NULL DEFAULT '',
 agent_resource TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS code_channels_session ON code_channels(workspace_id, app_id, session_id) WHERE session_id <> '';
`

// storedContextItem and storedAgentResource are the JSON forms of the record's
// two structured columns.
type storedContextItem struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Icon      string `json:"icon,omitempty"`
	URL       string `json:"url,omitempty"`
	ItemType  string `json:"item_type"`
	BotUserID string `json:"bot_user_id"`
}

type storedAgentResource struct {
	URL          string `json:"url,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	Title        string `json:"title,omitempty"`
	Provider     string `json:"provider,omitempty"`
}

func encodeCodeChannelColumns(value domain.CodeChannel) (string, string, error) {
	items := make([]storedContextItem, 0, len(value.ContextBar))
	for _, item := range value.ContextBar {
		items = append(items, storedContextItem{Key: item.Key, Label: item.Label, Icon: item.Icon, URL: item.URL, ItemType: item.ItemType, BotUserID: string(item.BotUserID)})
	}
	bar, err := json.Marshal(items)
	if err != nil {
		return "", "", err
	}
	resource, err := json.Marshal(storedAgentResource(value.AgentResource))
	if err != nil {
		return "", "", err
	}
	return string(bar), string(resource), nil
}

func (s *Store) CreateCodeChannel(ctx context.Context, conversation domain.Conversation, members []domain.UserID, value domain.CodeChannel, emitted []events.Event) error {
	bar, resource, err := encodeCodeChannelColumns(value)
	if err != nil {
		return err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	private := 0
	if conversation.PrivateFlag() {
		private = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name, is_private, name_folded, created_at, creator_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		conversation.ID, conversation.WorkspaceID, conversation.Name, private, domain.FoldSearchText(conversation.Name), unixSeconds(conversation.Created), conversation.CreatorID); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_teams(conversation_id, team_id, org_channel) VALUES (?, ?, 0)`, conversation.ID, conversation.WorkspaceID); err != nil {
		return classify(err)
	}
	for _, member := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_members(conversation_id, user_id) VALUES (?, ?) ON CONFLICT(conversation_id, user_id) DO NOTHING`, conversation.ID, member); err != nil {
			return classify(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO code_channels(conversation_id, workspace_id, app_id, bot_user_id, session_id, origin_channel_id, origin_ts, context_bar, summary_message_ts, summary_thread_ts, agent_resource, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		value.Conversation, value.WorkspaceID, value.AppID, value.BotUserID, value.SessionID, value.Origin.Channel, string(value.Origin.Timestamp),
		bar, string(value.Summary.MessageTimestamp), string(value.Summary.ThreadTimestamp), resource, value.CreatedAt.UnixNano(), value.UpdatedAt.UnixNano()); err != nil {
		return classify(err)
	}
	for _, event := range emitted {
		if err := insertOutbox(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const codeChannelColumns = `conversation_id, workspace_id, app_id, bot_user_id, session_id, origin_channel_id, origin_ts, context_bar, summary_message_ts, summary_thread_ts, agent_resource, created_at, updated_at`

func scanCodeChannel(row rowScanner) (domain.CodeChannel, error) {
	var value domain.CodeChannel
	var originTS, summaryTS, summaryThread, bar, resource string
	var created, updated int64
	if err := row.Scan(&value.Conversation, &value.WorkspaceID, &value.AppID, &value.BotUserID, &value.SessionID, &value.Origin.Channel, &originTS,
		&bar, &summaryTS, &summaryThread, &resource, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.CodeChannel{}, store.ErrNotFound
		}
		return domain.CodeChannel{}, err
	}
	value.Origin.Timestamp = domain.MessageTimestamp(originTS)
	value.Summary = domain.CodeChannelSummary{MessageTimestamp: domain.MessageTimestamp(summaryTS), ThreadTimestamp: domain.MessageTimestamp(summaryThread)}
	var items []storedContextItem
	if err := json.Unmarshal([]byte(bar), &items); err != nil {
		return domain.CodeChannel{}, err
	}
	value.ContextBar = make([]domain.CodeChannelContextItem, 0, len(items))
	for _, item := range items {
		value.ContextBar = append(value.ContextBar, domain.CodeChannelContextItem{Key: item.Key, Label: item.Label, Icon: item.Icon, URL: item.URL, ItemType: item.ItemType, BotUserID: domain.UserID(item.BotUserID)})
	}
	var stored storedAgentResource
	if err := json.Unmarshal([]byte(resource), &stored); err != nil {
		return domain.CodeChannel{}, err
	}
	value.AgentResource = domain.AgentResource(stored)
	value.CreatedAt = time.Unix(0, created).UTC()
	value.UpdatedAt = time.Unix(0, updated).UTC()
	return value, nil
}

func (s *Store) GetCodeChannel(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID) (domain.CodeChannel, error) {
	return scanCodeChannel(s.db.QueryRowContext(ctx, `SELECT `+codeChannelColumns+` FROM code_channels WHERE workspace_id = ? AND conversation_id = ?`, workspace, conversation))
}

func (s *Store) FindCodeChannelBySession(ctx context.Context, workspace domain.WorkspaceID, app domain.AppID, session string) (domain.CodeChannel, error) {
	if session == "" {
		return domain.CodeChannel{}, store.ErrNotFound
	}
	return scanCodeChannel(s.db.QueryRowContext(ctx, `SELECT `+codeChannelColumns+` FROM code_channels WHERE workspace_id = ? AND app_id = ? AND session_id = ?`, workspace, app, session))
}

func (s *Store) UpdateCodeChannel(ctx context.Context, value domain.CodeChannel, expected time.Time, event events.Event) error {
	bar, resource, err := encodeCodeChannelColumns(value)
	if err != nil {
		return err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE code_channels SET context_bar = ?, summary_message_ts = ?, summary_thread_ts = ?, agent_resource = ?, updated_at = ?
		WHERE workspace_id = ? AND conversation_id = ? AND updated_at = ?`,
		bar, string(value.Summary.MessageTimestamp), string(value.Summary.ThreadTimestamp), resource, value.UpdatedAt.UnixNano(),
		value.WorkspaceID, value.Conversation, expected.UnixNano())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		if _, err := scanCodeChannel(tx.QueryRowContext(ctx, `SELECT `+codeChannelColumns+` FROM code_channels WHERE workspace_id = ? AND conversation_id = ?`, value.WorkspaceID, value.Conversation)); err != nil {
			return err
		}
		return store.ErrConflict
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
