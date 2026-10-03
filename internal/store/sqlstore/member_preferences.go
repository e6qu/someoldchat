package sqlstore

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// memberPreferenceSchema is part of the base schema, which creates it on a
// fresh database, and is schema step 210, which creates it on an existing one.
const memberPreferenceSchema = `CREATE TABLE IF NOT EXISTS member_preferences (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, value TEXT NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY (workspace_id, user_id, name)
);
`

func (s *Store) MemberPreferences(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM member_preferences WHERE workspace_id = ? AND user_id = ?`, workspace, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, rows.Err()
}

func (s *Store) SetMemberPreference(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, name, value string, updatedAt time.Time) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var member int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND workspace_id = ?`, user, workspace).Scan(&member); err != nil {
		return err
	}
	if member == 0 {
		return store.ErrNotFound
	}
	if value == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM member_preferences WHERE workspace_id = ? AND user_id = ? AND name = ?`, workspace, user, name); err != nil {
			return err
		}
		return tx.Commit()
	}
	var others int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM member_preferences WHERE workspace_id = ? AND user_id = ? AND name <> ?`, workspace, user, name).Scan(&others); err != nil {
		return err
	}
	if others >= domain.MemberPreferenceLimit {
		return domain.ErrInvalidMemberPreference
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO member_preferences(workspace_id, user_id, name, value, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		workspace, user, name, value, updatedAt.UTC().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}
