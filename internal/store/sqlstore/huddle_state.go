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

// releaseFromHuddlesTx takes a deactivated member out of every running huddle
// they are in, inside the deactivation's transaction, as leaving would: each
// records huddle.left, a huddle left empty ends and records huddle.ended, and
// their huddle_state is cleared. The memory repository's
// releaseFromHuddlesLocked is the same rule.
func releaseFromHuddlesTx(ctx context.Context, tx txRunner, workspace domain.WorkspaceID, userID domain.UserID, actor domain.UserID, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.conversation_id, c.started_at FROM call_participants cp JOIN calls c ON c.id = cp.call_id WHERE cp.user_id = ? AND c.workspace_id = ? AND c.kind = ? AND c.ended_at = 0 ORDER BY c.id`, userID, workspace, domain.CallKindHuddle)
	if err != nil {
		return err
	}
	var huddles []domain.Call
	for rows.Next() {
		var call domain.Call
		var started int64
		if err := rows.Scan(&call.ID, &call.ConversationID, &started); err != nil {
			rows.Close()
			return err
		}
		call.StartedAt = time.Unix(started, 0).UTC()
		huddles = append(huddles, call)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, call := range huddles {
		if _, err := tx.ExecContext(ctx, `DELETE FROM call_participants WHERE call_id = ? AND user_id = ?`, call.ID, userID); err != nil {
			return err
		}
		left, err := events.HuddleEvent(workspace, userID, "huddle.left", call.ID, call.ConversationID, at)
		if err != nil {
			return err
		}
		if err := insertOutbox(ctx, tx, left); err != nil {
			return err
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_participants WHERE call_id = ?`, call.ID).Scan(&remaining); err != nil {
			return err
		}
		if remaining > 0 {
			continue
		}
		endedAt := at.UTC()
		duration := max(int64(endedAt.Sub(call.StartedAt).Seconds()), 0)
		if _, err := tx.ExecContext(ctx, `UPDATE calls SET ended_at = ?, duration_seconds = ? WHERE workspace_id = ? AND id = ?`, endedAt.Unix(), duration, workspace, call.ID); err != nil {
			return err
		}
		ended, err := events.HuddleEvent(workspace, userID, "huddle.ended", call.ID, call.ConversationID, at)
		if err != nil {
			return err
		}
		if err := insertOutbox(ctx, tx, ended); err != nil {
			return err
		}
	}
	return refreshHuddleStateTx(ctx, tx, workspace, userID, "", actor, at)
}
