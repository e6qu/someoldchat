package memory

import (
	"context"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) BeginShortTokenRotation(_ context.Context, rotation domain.ShortTokenRotation) error {
	if strings.TrimSpace(rotation.TokenHash) == "" || strings.TrimSpace(rotation.NewTokenHash) == "" || rotation.AppID == "" || rotation.ExpiresAt.IsZero() {
		return store.InvalidArgument("short token rotation requires both tokens, the app and an expiry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rotation.ExpiresAt = rotation.ExpiresAt.UTC()
	s.shortTokenRotations[rotation.TokenHash] = rotation
	return nil
}

func (s *Store) CompleteShortTokenRotation(_ context.Context, tokenHash, newTokenHash string, appID domain.AppID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, exists := s.shortTokenRotations[tokenHash]
	if !exists {
		return domain.ErrShortTokenRotationNotFound
	}
	if !pending.ExpiresAt.After(now) {
		delete(s.shortTokenRotations, tokenHash)
		return domain.ErrShortTokenRotationNotFound
	}
	if pending.AppID != appID {
		return domain.ErrOAuthAppMismatch
	}
	if pending.NewTokenHash != newTokenHash {
		return domain.ErrShortTokenRotationMismatch
	}
	record, exists := s.tokens[tokenHash]
	if !exists || record.Revoked {
		return store.ErrNotFound
	}
	if _, taken := s.tokens[newTokenHash]; taken {
		return store.ErrAlreadyExists
	}
	// The replacement is the same credential with a longer secret, so the
	// record moves to the new hash rather than being copied: the original
	// stops authenticating in the same step the replacement starts.
	s.tokens[newTokenHash] = record
	delete(s.tokens, tokenHash)
	for key, grant := range s.oauthRefreshGrants {
		if grant.AccessTokenHash == tokenHash {
			grant.AccessTokenHash = newTokenHash
			s.oauthRefreshGrants[key] = grant
		}
	}
	delete(s.shortTokenRotations, tokenHash)
	return nil
}
