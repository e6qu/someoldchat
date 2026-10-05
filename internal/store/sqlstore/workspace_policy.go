package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// workspacePolicySchema is part of the base schema, which creates it on a
// fresh database, and is schema step 217, which creates it on an existing one.
// A workspace without a row follows Slack's defaults, so the new table starts
// empty and every existing workspace keeps the behavior it had.
const workspacePolicySchema = `CREATE TABLE IF NOT EXISTS workspace_policies (
 workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),
 broadcast_warning_off INTEGER NOT NULL DEFAULT 0,
 private_channel_creators TEXT NOT NULL DEFAULT 'everyone'
);
`

func (s *Store) GetWorkspacePolicy(ctx context.Context, workspace domain.WorkspaceID) (domain.WorkspacePolicy, error) {
	var broadcastWarningOff sql.NullInt64
	var privateChannelCreators sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT p.broadcast_warning_off, p.private_channel_creators FROM workspaces w
		LEFT JOIN workspace_policies p ON p.workspace_id = w.id WHERE w.id = ?`, workspace).Scan(&broadcastWarningOff, &privateChannelCreators)
	if err != nil {
		return domain.WorkspacePolicy{}, translateNotFound(err)
	}
	policy := domain.DefaultWorkspacePolicy()
	if broadcastWarningOff.Valid {
		policy.BroadcastWarningOff = broadcastWarningOff.Int64 != 0
	}
	if privateChannelCreators.Valid {
		policy.PrivateChannelCreators = domain.PolicyAudience(privateChannelCreators.String)
	}
	if !policy.Valid() {
		// A row this build cannot read must not quietly widen who may act, and
		// it is a fault in the stored data rather than in anyone's request.
		return domain.WorkspacePolicy{}, fmt.Errorf("stored workspace policy for %s names an unknown audience %q", workspace, policy.PrivateChannelCreators)
	}
	return policy, nil
}

func (s *Store) SetWorkspacePolicy(ctx context.Context, workspace domain.WorkspaceID, policy domain.WorkspacePolicy, event events.Event) error {
	if !policy.Valid() {
		return store.InvalidArgument("invalid workspace policy")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?`, workspace).Scan(&present); err != nil {
		return translateNotFound(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_policies(workspace_id, broadcast_warning_off, private_channel_creators)
		VALUES (?, ?, ?) ON CONFLICT(workspace_id) DO UPDATE SET broadcast_warning_off = excluded.broadcast_warning_off,
		private_channel_creators = excluded.private_channel_creators`,
		workspace, boolInt(policy.BroadcastWarningOff), string(policy.PrivateChannelCreators)); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
