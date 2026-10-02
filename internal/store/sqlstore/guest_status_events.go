package sqlstore

import (
	"context"
	"errors"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// guestDeactivationEvent is the user.guest_status_changed record a
// deactivation owes when the account it deactivates is a guest's: a guest
// becoming a deactivated guest is one of the changes Slack reports. It reads
// the membership inside the deactivating transaction, so the record describes
// the account the transaction actually changed.
func guestDeactivationEvent(ctx context.Context, tx txRunner, workspaceID domain.WorkspaceID, userID domain.UserID, deactivation events.Event) ([]events.Event, error) {
	var role domain.WorkspaceRole
	var restricted, ultraRestricted int
	err := tx.QueryRowContext(ctx, `SELECT role, restricted, ultra_restricted FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role, &restricted, &ultraRestricted)
	if errors.Is(translateNotFound(err), store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if restricted == 0 && ultraRestricted == 0 {
		return nil, nil
	}
	user, err := scanUserRow(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, userID))
	if err != nil {
		return nil, translateNotFound(err)
	}
	user.Deleted = true
	user.Role, user.Restricted, user.UltraRestricted = role, restricted != 0, ultraRestricted != 0
	event, err := events.GuestStatusChangedEvent(user, deactivation.ActorID, deactivation.CreatedAt)
	if err != nil {
		return nil, err
	}
	return []events.Event{event}, nil
}

func (s *Store) AssignWorkspaceRole(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole, event events.Event) error {
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return store.InvalidArgument("invalid workspace role")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var restricted, ultraRestricted int
	if err := tx.QueryRowContext(ctx, `SELECT restricted, ultra_restricted FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&restricted, &ultraRestricted); err != nil {
		return translateNotFound(err)
	}
	if role != domain.WorkspaceRoleOwner {
		if err := refusePrimaryOwnerChange(ctx, tx, workspaceID, userID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspace_members SET role = ?, active = 1, restricted = 0, ultra_restricted = 0 WHERE workspace_id = ? AND user_id = ?`, role, workspaceID, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return store.ErrNotFound
	}
	if err := claimPrimaryOwnership(ctx, tx, workspaceID, userID); err != nil {
		return err
	}
	if err := insertOutbox(ctx, tx, event); err != nil {
		return err
	}
	if restricted != 0 || ultraRestricted != 0 {
		// Ending a guest tier changes the member's record, so the user row's
		// updated instant moves with it, as the event's user object says.
		if _, err := tx.ExecContext(ctx, `UPDATE users SET updated_at = ? WHERE id = ? AND workspace_id = ?`, unixSeconds(event.CreatedAt), userID, workspaceID); err != nil {
			return err
		}
		user, err := scanUserRow(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ? AND workspace_id = ?`, userID, workspaceID))
		if err != nil {
			return translateNotFound(err)
		}
		user.Role = role
		changedEvent, err := events.GuestStatusChangedEvent(user, event.ActorID, event.CreatedAt)
		if err != nil {
			return err
		}
		if err := insertOutbox(ctx, tx, changedEvent); err != nil {
			return err
		}
	}
	return tx.Commit()
}
