package sqlstore

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ChangeUserGroupUsers applies a membership delta in one transaction. A user
// is added only while an active member of the group's workspace, the rule
// SetUserGroupUsers applies to the whole list.
func (s *Store) ChangeUserGroupUsers(ctx context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, add, remove []domain.UserID, actor domain.UserID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockUserGroup(ctx, tx, workspace, id, actor); err != nil {
		return err
	}
	for _, userID := range add {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_group_users(group_id, user_id)
			SELECT ?, id FROM users WHERE id = ? AND workspace_id = ? AND deleted = 0
			ON CONFLICT(group_id, user_id) DO NOTHING`, id, userID, workspace); err != nil {
			return classify(err)
		}
	}
	for _, userID := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_users WHERE group_id = ? AND user_id = ?`, id, userID); err != nil {
			return classify(err)
		}
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// ChangeUserGroupTeams applies a workspace-assignment delta in one
// transaction.
func (s *Store) ChangeUserGroupTeams(ctx context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, add, remove []domain.WorkspaceID, actor domain.UserID, event events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockUserGroup(ctx, tx, workspace, id, actor); err != nil {
		return err
	}
	if err := changeUserGroupTeams(ctx, tx, id, add, remove); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// lockUserGroup records the actor as the group's last updater, which is also
// how the transaction proves the group is this workspace's and takes its row
// before changing what hangs off it.
func lockUserGroup(ctx context.Context, tx txRunner, workspace domain.WorkspaceID, id domain.UserGroupID, actor domain.UserID) error {
	result, err := tx.ExecContext(ctx, `UPDATE user_groups SET updated_by = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, actor, time.Now().UTC().Unix(), id, workspace)
	if err != nil {
		return classify(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	return nil
}

// changeUserGroupTeams adds and removes a group's workspace assignments,
// refusing a workspace that does not exist.
func changeUserGroupTeams(ctx context.Context, tx txRunner, id domain.UserGroupID, add, remove []domain.WorkspaceID) error {
	var exists int
	for _, team := range add {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?`, team).Scan(&exists); err != nil {
			return translateNotFound(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_group_teams(group_id, team_id) VALUES (?, ?)
			ON CONFLICT(group_id, team_id) DO NOTHING`, id, team); err != nil {
			return classify(err)
		}
	}
	for _, team := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_teams WHERE group_id = ? AND team_id = ?`, id, team); err != nil {
			return classify(err)
		}
	}
	return nil
}

func (s *Store) userGroupTeams(ctx context.Context, id domain.UserGroupID) ([]domain.WorkspaceID, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT team_id FROM user_group_teams WHERE group_id = ? ORDER BY team_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []domain.WorkspaceID
	for rows.Next() {
		var team domain.WorkspaceID
		if err := rows.Scan(&team); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}
