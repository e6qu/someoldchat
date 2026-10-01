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

// agentSessionSchema is part of the base schema, which creates it on a fresh
// database, and is schema step 195, which creates it on an existing one.
//
// A session is keyed by its thread; each app that writes a status to it owns
// one agent row. A conversation's deletion removes both (see the conversation
// footprint in sqlstore.go).
const agentSessionSchema = `CREATE TABLE IF NOT EXISTS agent_sessions (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), conversation_id TEXT NOT NULL REFERENCES conversations(id),
 thread_ts TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', initiator_user_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY (workspace_id, conversation_id, thread_ts)
);
CREATE TABLE IF NOT EXISTS agent_session_agents (
 workspace_id TEXT NOT NULL, conversation_id TEXT NOT NULL, thread_ts TEXT NOT NULL, app_id TEXT NOT NULL,
 status TEXT NOT NULL, username TEXT NOT NULL DEFAULT '', icon_emoji TEXT NOT NULL DEFAULT '', icon_url TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL,
 PRIMARY KEY (workspace_id, conversation_id, thread_ts, app_id),
 FOREIGN KEY (workspace_id, conversation_id, thread_ts) REFERENCES agent_sessions(workspace_id, conversation_id, thread_ts)
);
`

func (s *Store) GetAgentSession(ctx context.Context, workspace domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSession, error) {
	return readAgentSession(ctx, s.db, workspace, conversation, thread)
}

func readAgentSession(ctx context.Context, db txRunner, workspace domain.WorkspaceID, conversation domain.ConversationID, thread domain.MessageTimestamp) (domain.AgentSession, error) {
	session := domain.AgentSession{WorkspaceID: workspace, Conversation: conversation, ThreadTimestamp: thread}
	var initiator string
	var created, updated int64
	err := db.QueryRowContext(ctx, `SELECT title, initiator_user_id, created_at, updated_at FROM agent_sessions WHERE workspace_id = ? AND conversation_id = ? AND thread_ts = ?`,
		workspace, conversation, string(thread)).Scan(&session.Title, &initiator, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSession{}, store.ErrNotFound
	}
	if err != nil {
		return domain.AgentSession{}, err
	}
	session.InitiatorUserID = domain.UserID(initiator)
	session.CreatedAt = time.Unix(0, created).UTC()
	session.UpdatedAt = time.Unix(0, updated).UTC()
	rows, err := db.QueryContext(ctx, `SELECT app_id, status, username, icon_emoji, icon_url, updated_at FROM agent_session_agents
		WHERE workspace_id = ? AND conversation_id = ? AND thread_ts = ? ORDER BY app_id`,
		workspace, conversation, string(thread))
	if err != nil {
		return domain.AgentSession{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent domain.AgentSessionAgent
		var app, status string
		var at int64
		if err := rows.Scan(&app, &status, &agent.Identity.Username, &agent.Identity.IconEmoji, &agent.Identity.IconURL, &at); err != nil {
			return domain.AgentSession{}, err
		}
		agent.AppID = domain.AppID(app)
		agent.Status = domain.AgentSessionStatus(status)
		agent.UpdatedAt = time.Unix(0, at).UTC()
		session.Agents = append(session.Agents, agent)
	}
	if err := rows.Err(); err != nil {
		return domain.AgentSession{}, err
	}
	// ORDER BY app_id compares bytes on SQLite and by collation on
	// PostgreSQL; sorting here keeps the promised order identical.
	domain.SortAgentSessionAgents(session.Agents)
	return session, nil
}

func (s *Store) SetAgentSessionStatus(ctx context.Context, write domain.AgentSessionStatusWrite, event events.Event) (domain.AgentSession, error) {
	if !write.Valid() {
		return domain.AgentSession{}, store.InvalidArgument("an agent session status write requires a workspace, conversation, thread, app, valid status and time")
	}
	at := write.At.UTC().UnixNano()
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.AgentSession{}, err
	}
	defer tx.Rollback()
	// The title and initiator belong to the session's creation: an existing
	// row keeps its own, which is the reference's "ignored if the session
	// already exists".
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions(workspace_id, conversation_id, thread_ts, title, initiator_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, conversation_id, thread_ts) DO UPDATE SET updated_at = excluded.updated_at`,
		write.WorkspaceID, write.Conversation, string(write.ThreadTimestamp), write.Title, string(write.InitiatorUserID), at, at); err != nil {
		return domain.AgentSession{}, classify(err)
	}
	identity := ""
	if write.ReplaceIdentity {
		identity = ", username = excluded.username, icon_emoji = excluded.icon_emoji, icon_url = excluded.icon_url"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_session_agents(workspace_id, conversation_id, thread_ts, app_id, status, username, icon_emoji, icon_url, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, conversation_id, thread_ts, app_id) DO UPDATE SET status = excluded.status`+identity+`, updated_at = excluded.updated_at`,
		write.WorkspaceID, write.Conversation, string(write.ThreadTimestamp), string(write.AppID), string(write.Status),
		write.Identity.Username, write.Identity.IconEmoji, write.Identity.IconURL, at); err != nil {
		return domain.AgentSession{}, classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return domain.AgentSession{}, err
	}
	session, err := readAgentSession(ctx, tx, write.WorkspaceID, write.Conversation, write.ThreadTimestamp)
	if err != nil {
		return domain.AgentSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentSession{}, err
	}
	return session, nil
}

func (s *Store) RenameAgentSession(ctx context.Context, rename domain.AgentSessionRename, produced []events.Event) (domain.AgentSession, error) {
	if !rename.Valid() {
		return domain.AgentSession{}, store.InvalidArgument("an agent session rename requires a workspace, conversation, thread, title and time")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return domain.AgentSession{}, err
	}
	defer tx.Rollback()
	// The title is compared in the UPDATE itself, so the check and the write
	// are one statement on every profile rather than a read another writer
	// can slip between.
	result, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET title = ?, updated_at = ? WHERE workspace_id = ? AND conversation_id = ? AND thread_ts = ? AND title = ?`,
		rename.Title, rename.At.UTC().UnixNano(), rename.WorkspaceID, rename.Conversation, string(rename.ThreadTimestamp), rename.ExpectedTitle)
	if err != nil {
		return domain.AgentSession{}, classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.AgentSession{}, err
	}
	if changed != 1 {
		if _, err := readAgentSession(ctx, tx, rename.WorkspaceID, rename.Conversation, rename.ThreadTimestamp); err != nil {
			return domain.AgentSession{}, err
		}
		return domain.AgentSession{}, store.ErrConflict
	}
	for _, event := range produced {
		if err := insertOutbox(ctx, tx, event); err != nil {
			return domain.AgentSession{}, err
		}
	}
	session, err := readAgentSession(ctx, tx, rename.WorkspaceID, rename.Conversation, rename.ThreadTimestamp)
	if err != nil {
		return domain.AgentSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentSession{}, err
	}
	return session, nil
}

func (s *Store) StopAgentSession(ctx context.Context, stop domain.AgentSessionStop, produced []events.Event) error {
	if !stop.Valid() {
		return store.InvalidArgument("an agent session stop requires a workspace, conversation, thread and at least one agent")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Each precondition is a conditional write that must touch exactly one
	// row: the agent is still processing, the stream still holds the state the
	// stop was computed from. A row that moved underneath answers ErrConflict
	// and the transaction is discarded whole.
	for _, app := range stop.Agents {
		result, err := tx.ExecContext(ctx, `UPDATE agent_session_agents SET status = status
			WHERE workspace_id = ? AND conversation_id = ? AND thread_ts = ? AND app_id = ? AND status = ?`,
			stop.WorkspaceID, stop.Conversation, string(stop.ThreadTimestamp), string(app), string(domain.AgentSessionProcessing))
		if err := exactlyOneRow(result, err); err != nil {
			return err
		}
	}
	for _, stream := range stop.Streams {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET stream_state = ? WHERE id = ? AND workspace_id = ? AND conversation = ? AND stream_state = ?`,
			stream.Message.StreamState, stream.Message.ID, stop.WorkspaceID, stop.Conversation, stream.PreviousStreamState)
		if err := exactlyOneRow(result, err); err != nil {
			return err
		}
	}
	for _, event := range produced {
		if err := insertOutbox(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// exactlyOneRow turns a conditional write that matched nothing into
// ErrConflict.
func exactlyOneRow(result sql.Result, err error) error {
	if err != nil {
		return classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrConflict
	}
	return nil
}

func (s *Store) ListActiveMessageStreams(ctx context.Context, conversation domain.ConversationID, thread domain.MessageTimestamp, app domain.AppID) ([]domain.MessageTimestamp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT created_at FROM messages
		WHERE conversation = ? AND thread_timestamp = ? AND app_id = ? AND deleted = 0 AND stream_state LIKE ?
		ORDER BY created_at, id`,
		conversation, string(thread), string(app), domain.ActiveMessageStreamPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []domain.MessageTimestamp
	for rows.Next() {
		var created string
		if err := rows.Scan(&created); err != nil {
			return nil, err
		}
		at, err := domain.ParseStoredTime(created)
		if err != nil {
			return nil, err
		}
		found = append(found, domain.NewMessageTimestamp(at))
	}
	return found, rows.Err()
}
