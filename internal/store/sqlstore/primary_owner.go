package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The primary owner is a flag on the owner's membership. A partial unique
// index admits one flagged row per workspace, so no interleaving of writes can
// leave a workspace with two. Schema 198 adds both.
const (
	primaryOwnerColumn = `ALTER TABLE workspace_members ADD COLUMN primary_owner INTEGER NOT NULL DEFAULT 0`
	primaryOwnerIndex  = `CREATE UNIQUE INDEX IF NOT EXISTS workspace_members_primary_owner ON workspace_members(workspace_id) WHERE primary_owner = 1`
)

// refusePrimaryOwnerChange refuses demoting or deactivating the primary owner.
func refusePrimaryOwnerChange(ctx context.Context, tx *writeTx, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	var primary int
	err := tx.QueryRowContext(ctx, `SELECT primary_owner FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&primary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if primary != 0 {
		return domain.ErrPrimaryOwner
	}
	return nil
}

// claimPrimaryOwnership makes an active owner the primary owner of a
// workspace that has none, so every workspace that has an owner has one.
func claimPrimaryOwnership(ctx context.Context, tx *writeTx, workspaceID domain.WorkspaceID, userID domain.UserID) error {
	_, err := tx.ExecContext(ctx, `UPDATE workspace_members SET primary_owner = 1
		WHERE workspace_id = ? AND user_id = ? AND role = ? AND active = 1
		AND NOT EXISTS (SELECT 1 FROM workspace_members primary_member WHERE primary_member.workspace_id = ? AND primary_member.primary_owner = 1)`,
		workspaceID, userID, domain.WorkspaceRoleOwner, workspaceID)
	return err
}

func (s *Store) TransferPrimaryOwnership(ctx context.Context, workspaceID domain.WorkspaceID, fromID, toID domain.UserID, event events.Event) error {
	if fromID == toID {
		return store.InvalidArgument("primary ownership moves to another member")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var primary, active int
	if err := tx.QueryRowContext(ctx, `SELECT primary_owner, active FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, fromID).Scan(&primary, &active); err != nil {
		return translateNotFound(err)
	}
	if primary == 0 || active == 0 {
		return domain.ErrNotWorkspaceAdmin
	}
	// The new primary owner is an active, full member who is a person: a guest
	// or a bot cannot hold the workspace.
	var restricted, ultraRestricted, deleted, bots int
	if err := tx.QueryRowContext(ctx, `SELECT m.active, m.restricted, m.ultra_restricted, u.deleted, (SELECT COUNT(*) FROM bots b WHERE b.workspace_id = m.workspace_id AND b.user_id = m.user_id)
		FROM workspace_members m JOIN users u ON u.id = m.user_id AND u.workspace_id = m.workspace_id
		WHERE m.workspace_id = ? AND m.user_id = ?`, workspaceID, toID).Scan(&active, &restricted, &ultraRestricted, &deleted, &bots); err != nil {
		return translateNotFound(err)
	}
	if active == 0 || deleted != 0 || restricted != 0 || ultraRestricted != 0 || bots != 0 {
		return store.InvalidArgument("the primary owner must be an active full member")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET primary_owner = 0 WHERE workspace_id = ? AND user_id = ?`, workspaceID, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET role = ?, primary_owner = 1 WHERE workspace_id = ? AND user_id = ?`, domain.WorkspaceRoleOwner, workspaceID, toID); err != nil {
		return classify(err)
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}
