package memory

import (
	"context"
	"slices"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func (s *Store) GetAppPermission(_ context.Context, workspace domain.WorkspaceID, app domain.AppID) (domain.AppPermission, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.appPermissions[appConfigKey(workspace, app)]
	if !exists {
		return domain.AppPermission{}, store.ErrNotFound
	}
	return cloneAppPermission(value), nil
}

func (s *Store) SetAppPermission(_ context.Context, value domain.AppPermission, event events.Event) error {
	if err := value.Check(); err != nil {
		return store.InvalidArgument("invalid app permission")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[value.AppID]; !exists {
		return store.ErrNotFound
	}
	if _, exists := s.workspaces[value.WorkspaceID]; !exists {
		return store.ErrNotFound
	}
	s.appPermissions[appConfigKey(value.WorkspaceID, value.AppID)] = cloneAppPermission(value)
	s.outbox = append(s.outbox, event)
	return nil
}

func (s *Store) ListMCPServerPermissions(_ context.Context, workspace domain.WorkspaceID, app domain.AppID) ([]domain.MCPServerPermission, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.MCPServerPermission, 0)
	for _, value := range s.mcpServerPermissions {
		if value.WorkspaceID == workspace && value.AppID == app {
			values = append(values, cloneMCPServerPermission(value))
		}
	}
	slices.SortFunc(values, func(left, right domain.MCPServerPermission) int {
		switch {
		case left.ServerID < right.ServerID:
			return -1
		case left.ServerID > right.ServerID:
			return 1
		}
		return 0
	})
	return values, nil
}

func (s *Store) SetMCPServerPermission(_ context.Context, value domain.MCPServerPermission, event events.Event) error {
	if value.WorkspaceID == "" || value.AppID == "" || value.ServerID == "" || !value.PermissionType.Valid() {
		return store.InvalidArgument("invalid MCP server permission")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[value.AppID]; !exists {
		return store.ErrNotFound
	}
	if _, exists := s.workspaces[value.WorkspaceID]; !exists {
		return store.ErrNotFound
	}
	s.mcpServerPermissions[appConfigKey(value.WorkspaceID, value.AppID)+"\x00"+string(value.ServerID)] = cloneMCPServerPermission(value)
	s.outbox = append(s.outbox, event)
	return nil
}

// The lists are copied in and out so a caller holding a returned value cannot
// change what the store holds, and an empty list reads back as empty rather
// than nil, as it does from the SQL profiles.
func cloneAppPermission(value domain.AppPermission) domain.AppPermission {
	value.UserIDs = append([]domain.UserID{}, value.UserIDs...)
	value.UserGroupIDs = append([]domain.UserGroupID{}, value.UserGroupIDs...)
	value.ChannelIDs = append([]domain.ConversationID{}, value.ChannelIDs...)
	return value
}

func cloneMCPServerPermission(value domain.MCPServerPermission) domain.MCPServerPermission {
	value.UserIDs = append([]domain.UserID{}, value.UserIDs...)
	value.UserGroupIDs = append([]domain.UserGroupID{}, value.UserGroupIDs...)
	return value
}
