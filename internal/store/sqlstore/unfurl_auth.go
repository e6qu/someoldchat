package sqlstore

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// unfurlAuthDeclinesTable holds each member's "Never ask me again" on an app's
// unfurl authentication prompt. Schema 199 creates it.
const unfurlAuthDeclinesTable = `CREATE TABLE IF NOT EXISTS unfurl_auth_declines (workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, app_id TEXT NOT NULL, declined_at INTEGER NOT NULL, PRIMARY KEY (workspace_id, user_id, app_id))`

func (s *Store) DeclineUnfurlAuth(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, at time.Time) error {
	if workspaceID == "" || userID == "" || appID == "" {
		return store.InvalidArgument("an unfurl authentication decline names a workspace, a member and an app")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO unfurl_auth_declines(workspace_id, user_id, app_id, declined_at) VALUES (?, ?, ?, ?) ON CONFLICT(workspace_id, user_id, app_id) DO NOTHING`,
		workspaceID, userID, appID, at.UTC().UnixNano())
	return classify(err)
}

func (s *Store) UnfurlAuthDeclined(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM unfurl_auth_declines WHERE workspace_id = ? AND user_id = ? AND app_id = ?`, workspaceID, userID, appID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
