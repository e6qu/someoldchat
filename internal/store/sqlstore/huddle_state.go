package sqlstore

import (
	"context"
	"errors"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// refreshHuddleStateTx settles a member's huddle_call_id after a huddle
// mutation has moved them, inside that mutation's transaction, and journals
// user.huddle_changed when it changed. The running huddles the member is in
// are read back from call_participants rather than assumed, so a member who
// leaves one of two huddles stays in the other and a member an ending huddle
// released is out of it; domain.CurrentHuddle makes the choice, as it does
// for the in-memory repository.
func refreshHuddleStateTx(ctx context.Context, tx txRunner, workspace domain.WorkspaceID, userID domain.UserID, preferred domain.CallID, actor domain.UserID, at time.Time) error {
	user, err := scanUserRow(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ? AND workspace_id = ?`, userID, workspace))
	if err != nil {
		if errors.Is(translateNotFound(err), store.ErrNotFound) {
			return nil
		}
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.started_at FROM call_participants cp JOIN calls c ON c.id = cp.call_id WHERE cp.user_id = ? AND c.workspace_id = ? AND c.kind = ? AND c.ended_at = 0`, userID, workspace, domain.CallKindHuddle)
	if err != nil {
		return err
	}
	var in []domain.Call
	for rows.Next() {
		var call domain.Call
		var started int64
		if err := rows.Scan(&call.ID, &started); err != nil {
			rows.Close()
			return err
		}
		call.StartedAt = time.Unix(started, 0).UTC()
		in = append(in, call)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	next := domain.CurrentHuddle(in, preferred, user.HuddleCallID)
	if next == user.HuddleCallID {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET huddle_call_id = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, next, unixSeconds(at), userID, workspace); err != nil {
		return err
	}
	user.HuddleCallID = next
	user.Updated = time.Unix(unixSeconds(at), 0).UTC()
	var role domain.WorkspaceRole
	var restricted, ultraRestricted, primaryOwner int
	err = tx.QueryRowContext(ctx, `SELECT role, restricted, ultra_restricted, primary_owner FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspace, userID).Scan(&role, &restricted, &ultraRestricted, &primaryOwner)
	switch {
	case err == nil:
		user.Role, user.Restricted, user.UltraRestricted, user.PrimaryOwner = role, restricted != 0, ultraRestricted != 0, primaryOwner != 0
	case !errors.Is(translateNotFound(err), store.ErrNotFound):
		return err
	}
	event, err := events.UserHuddleChangedEvent(user, actor, at)
	if err != nil {
		return err
	}
	return insertOutbox(ctx, tx, event)
}
