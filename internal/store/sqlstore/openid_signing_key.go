package sqlstore

import (
	"context"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// openIDSigningKeysTable holds the deployment's one Sign in with Slack signing
// key. The singleton column admits a single row, so replicas racing to create
// the first key cannot store two. Schema 197 creates it.
const openIDSigningKeysTable = `CREATE TABLE IF NOT EXISTS openid_signing_keys (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), key_id TEXT NOT NULL, private_key_ciphertext TEXT NOT NULL, created_at INTEGER NOT NULL)`

func (s *Store) EnsureOpenIDSigningKey(ctx context.Context, candidate domain.OpenIDSigningKey) (domain.OpenIDSigningKey, error) {
	if strings.TrimSpace(candidate.KeyID) == "" || strings.TrimSpace(candidate.PrivateKeyCiphertext) == "" || candidate.CreatedAt.IsZero() {
		return domain.OpenIDSigningKey{}, store.InvalidArgument("an OpenID signing key requires an id, a sealed private key and a creation time")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO openid_signing_keys(singleton, key_id, private_key_ciphertext, created_at) VALUES (1, ?, ?, ?) ON CONFLICT(singleton) DO NOTHING`,
		candidate.KeyID, candidate.PrivateKeyCiphertext, candidate.CreatedAt.UTC().UnixNano()); err != nil {
		return domain.OpenIDSigningKey{}, classify(err)
	}
	return s.OpenIDSigningKey(ctx)
}

func (s *Store) OpenIDSigningKey(ctx context.Context) (domain.OpenIDSigningKey, error) {
	var stored domain.OpenIDSigningKey
	var createdAt int64
	if err := s.db.QueryRowContext(ctx, `SELECT key_id, private_key_ciphertext, created_at FROM openid_signing_keys WHERE singleton = 1`).Scan(&stored.KeyID, &stored.PrivateKeyCiphertext, &createdAt); err != nil {
		return domain.OpenIDSigningKey{}, translateNotFound(err)
	}
	stored.CreatedAt = timeFromUnixNanoOrZero(createdAt)
	return stored, nil
}
