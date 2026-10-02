package memory

import (
	"context"
	"sort"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ChangeUserGroupUsers applies a membership delta under the store lock. As in
// the SQL profiles, only an active user of the group's workspace is added.
func (s *Store) ChangeUserGroupUsers(_ context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, add, remove []domain.UserID, actor domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.userGroups[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	members := make(map[domain.UserID]struct{}, len(value.Users)+len(add))
	for _, user := range value.Users {
		members[user] = struct{}{}
	}
	for _, user := range add {
		if record, exists := s.users[user]; exists && record.WorkspaceID == workspace && !record.Deleted {
			members[user] = struct{}{}
		}
	}
	for _, user := range remove {
		delete(members, user)
	}
	value.Users = make([]domain.UserID, 0, len(members))
	for user := range members {
		value.Users = append(value.Users, user)
	}
	sort.Slice(value.Users, func(left, right int) bool { return value.Users[left] < value.Users[right] })
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	s.userGroups[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}

// ChangeUserGroupTeams applies a workspace-assignment delta under the store
// lock. A workspace that does not exist is refused, as the SQL profiles'
// foreign key refuses it.
func (s *Store) ChangeUserGroupTeams(_ context.Context, workspace domain.WorkspaceID, id domain.UserGroupID, add, remove []domain.WorkspaceID, actor domain.UserID, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.userGroups[id]
	if !ok || value.WorkspaceID != workspace {
		return store.ErrNotFound
	}
	teams := make(map[domain.WorkspaceID]struct{}, len(value.Teams)+len(add))
	for _, team := range value.Teams {
		teams[team] = struct{}{}
	}
	for _, team := range add {
		if _, exists := s.workspaces[team]; !exists {
			return store.ErrNotFound
		}
		teams[team] = struct{}{}
	}
	for _, team := range remove {
		delete(teams, team)
	}
	value.Teams = make([]domain.WorkspaceID, 0, len(teams))
	for team := range teams {
		value.Teams = append(value.Teams, team)
	}
	sort.Slice(value.Teams, func(left, right int) bool { return value.Teams[left] < value.Teams[right] })
	value.UpdatedBy = actor
	value.UpdatedAt = time.Now().UTC()
	s.userGroups[id] = value
	s.outbox = append(s.outbox, event)
	return nil
}
