package memory

import (
	"context"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func memberPreferenceKey(workspace domain.WorkspaceID, user domain.UserID) string {
	return string(workspace) + "\x00" + string(user)
}

func (s *Store) MemberPreferences(_ context.Context, workspace domain.WorkspaceID, user domain.UserID) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := map[string]string{}
	for name, value := range s.memberPreferences[memberPreferenceKey(workspace, user)] {
		values[name] = value
	}
	return values, nil
}

// SetMemberPreference mirrors the SQL profile, including the limit on how
// many a member keeps.
func (s *Store) SetMemberPreference(_ context.Context, workspace domain.WorkspaceID, user domain.UserID, name, value string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if member, ok := s.users[user]; !ok || member.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	key := memberPreferenceKey(workspace, user)
	values := s.memberPreferences[key]
	if value == "" {
		delete(values, name)
		return nil
	}
	if values == nil {
		values = map[string]string{}
		s.memberPreferences[key] = values
	}
	if _, exists := values[name]; !exists && len(values) >= domain.MemberPreferenceLimit {
		return domain.ErrInvalidMemberPreference
	}
	values[name] = value
	return nil
}
