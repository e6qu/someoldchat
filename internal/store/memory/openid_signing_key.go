package memory

import (
	"context"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) EnsureOpenIDSigningKey(_ context.Context, candidate domain.OpenIDSigningKey) (domain.OpenIDSigningKey, error) {
	if strings.TrimSpace(candidate.KeyID) == "" || strings.TrimSpace(candidate.PrivateKeyCiphertext) == "" || candidate.CreatedAt.IsZero() {
		return domain.OpenIDSigningKey{}, store.InvalidArgument("an OpenID signing key requires an id, a sealed private key and a creation time")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openIDSigningKey == nil {
		candidate.CreatedAt = candidate.CreatedAt.UTC()
		s.openIDSigningKey = &candidate
	}
	return *s.openIDSigningKey, nil
}

func (s *Store) OpenIDSigningKey(context.Context) (domain.OpenIDSigningKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.openIDSigningKey == nil {
		return domain.OpenIDSigningKey{}, store.ErrNotFound
	}
	return *s.openIDSigningKey, nil
}
