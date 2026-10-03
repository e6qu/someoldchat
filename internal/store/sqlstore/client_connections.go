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

// clientConnectionSchema is part of the base schema, which creates it on a
// fresh database, and is schema step 209, which creates it on an existing one.
// Each row is one open client's lease; users.connected_until is the latest of a
// member's leases, so every reader of a member sees whether they are connected
// without a second query.
const clientConnectionSchema = `CREATE TABLE IF NOT EXISTS client_connections (
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), user_id TEXT NOT NULL REFERENCES users(id),
 expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS client_connections_user ON client_connections(user_id, expires_at);
`

func (s *Store) OpenClientConnection(ctx context.Context, value domain.ClientConnection, now time.Time, online events.Event) error {
	if value.ID == "" || value.ExpiresAt.IsZero() {
		return store.InvalidArgument("a client connection requires an ID and a lease")
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var connectedUntil int64
	if err := tx.QueryRowContext(ctx, `SELECT connected_until FROM users WHERE id = ? AND workspace_id = ?`, value.UserID, value.WorkspaceID).Scan(&connectedUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	// Whether the member may connect is the service's to decide; the store
	// keeps the lease to a member of the workspace it names.
	//
	// A lease whose server vanished without closing it is removed when its
	// member next connects, so the table holds at most one lapsed lease per
	// member between connections.
	if _, err := tx.ExecContext(ctx, `DELETE FROM client_connections WHERE user_id = ? AND expires_at <= ?`, value.UserID, now.UnixNano()); err != nil {
		return err
	}
	expires := value.ExpiresAt.UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO client_connections(id, workspace_id, user_id, expires_at) VALUES (?, ?, ?, ?)`, value.ID, value.WorkspaceID, value.UserID, expires); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET connected_until = ? WHERE id = ? AND connected_until < ?`, expires, value.UserID, expires); err != nil {
		return err
	}
	if online.ID != "" && connectedUntil <= now.UnixNano() {
		if err := insertOutbox(ctx, tx, online); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RenewClientConnection(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ClientConnectionID, expiresAt time.Time) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	expires := expiresAt.UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE client_connections SET expires_at = ? WHERE id = ? AND workspace_id = ? AND user_id = ? AND expires_at < ?`, expires, id, workspace, user, expires)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM client_connections WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspace, user).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET connected_until = ? WHERE id = ? AND connected_until < ?`, expires, user, expires); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CloseClientConnection(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, id domain.ClientConnectionID, now time.Time, offline events.Event) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var connectedUntil int64
	if err := tx.QueryRowContext(ctx, `SELECT connected_until FROM users WHERE id = ? AND workspace_id = ?`, user, workspace).Scan(&connectedUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM client_connections WHERE id = ? AND workspace_id = ? AND user_id = ?`, id, workspace, user)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return store.ErrNotFound
	}
	// The member stays connected until the latest lease that remains; with
	// none, they are disconnected now.
	var remaining sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(expires_at) FROM client_connections WHERE user_id = ? AND expires_at > ?`, user, now.UnixNano()).Scan(&remaining); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET connected_until = ? WHERE id = ?`, remaining.Int64, user); err != nil {
		return err
	}
	if offline.ID != "" && connectedUntil > now.UnixNano() && !remaining.Valid {
		if err := insertOutbox(ctx, tx, offline); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CountClientConnections(ctx context.Context, workspace domain.WorkspaceID, user domain.UserID, now time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM client_connections WHERE workspace_id = ? AND user_id = ? AND expires_at > ?`, workspace, user, now.UnixNano()).Scan(&count)
	return count, err
}
