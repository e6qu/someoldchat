package sqlstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// shortTokenRotationsTable holds the begun, not yet completed, short-secret
// rotations: one row per token being rotated, by hash, naming the hash of the
// replacement the begin issued and when it stops being completable. Schema 196
// creates it.
const shortTokenRotationsTable = `CREATE TABLE IF NOT EXISTS short_token_rotations (token_hash TEXT PRIMARY KEY, new_token_hash TEXT NOT NULL, app_id TEXT NOT NULL, expires_at INTEGER NOT NULL)`

func (s *Store) BeginShortTokenRotation(ctx context.Context, rotation domain.ShortTokenRotation) error {
	if strings.TrimSpace(rotation.TokenHash) == "" || strings.TrimSpace(rotation.NewTokenHash) == "" || rotation.AppID == "" || rotation.ExpiresAt.IsZero() {
		return store.InvalidArgument("short token rotation requires both tokens, the app and an expiry")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO short_token_rotations(token_hash, new_token_hash, app_id, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET new_token_hash = excluded.new_token_hash, app_id = excluded.app_id, expires_at = excluded.expires_at`,
		rotation.TokenHash, rotation.NewTokenHash, rotation.AppID, rotation.ExpiresAt.UTC().UnixNano())
	return classify(err)
}

func (s *Store) CompleteShortTokenRotation(ctx context.Context, tokenHash, newTokenHash string, appID domain.AppID, now time.Time) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pendingHash string
	var pendingApp domain.AppID
	var expiresAt int64
	if err := tx.QueryRowContext(ctx, `SELECT new_token_hash, app_id, expires_at FROM short_token_rotations WHERE token_hash = ?`, tokenHash).Scan(&pendingHash, &pendingApp, &expiresAt); err != nil {
		if errors.Is(translateNotFound(err), store.ErrNotFound) {
			return domain.ErrShortTokenRotationNotFound
		}
		return err
	}
	if expiresAt <= now.UTC().UnixNano() {
		// An expired rotation is gone: removing it is the only write, and it
		// is committed so the next begin starts from nothing.
		if _, err := tx.ExecContext(ctx, `DELETE FROM short_token_rotations WHERE token_hash = ?`, tokenHash); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return domain.ErrShortTokenRotationNotFound
	}
	if pendingApp != appID {
		return domain.ErrOAuthAppMismatch
	}
	if pendingHash != newTokenHash {
		return domain.ErrShortTokenRotationMismatch
	}
	var revoked int
	if err := tx.QueryRowContext(ctx, `SELECT revoked FROM tokens WHERE token_hash = ?`, tokenHash).Scan(&revoked); err != nil {
		return translateNotFound(err)
	}
	if revoked != 0 {
		return store.ErrNotFound
	}
	// The replacement is the same credential with a longer secret, so the row
	// moves to the new hash rather than being copied: the original stops
	// authenticating in the same commit the replacement starts.
	if _, err := tx.ExecContext(ctx, `UPDATE tokens SET token_hash = ? WHERE token_hash = ?`, newTokenHash, tokenHash); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_refresh_tokens SET access_hash = ? WHERE access_hash = ?`, newTokenHash, tokenHash); err != nil {
		return classify(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM short_token_rotations WHERE token_hash = ?`, tokenHash); err != nil {
		return err
	}
	return tx.Commit()
}
