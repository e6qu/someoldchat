package memory

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func membershipKey(workspaceID domain.WorkspaceID, userID domain.UserID) string {
	return string(workspaceID) + "\x00" + string(userID)
}

// refusePrimaryOwnerChangeLocked refuses demoting or deactivating the primary
// owner. The caller holds the write lock.
func (s *Store) refusePrimaryOwnerChangeLocked(workspaceID domain.WorkspaceID, userID domain.UserID) error {
	if s.members[membershipKey(workspaceID, userID)].PrimaryOwner {
		return domain.ErrPrimaryOwner
	}
	return nil
}

// claimPrimaryOwnershipLocked makes an active owner the primary owner of a
// workspace that has none, so every workspace that has an owner has one.
func (s *Store) claimPrimaryOwnershipLocked(workspaceID domain.WorkspaceID, userID domain.UserID) {
	key := membershipKey(workspaceID, userID)
	membership, ok := s.members[key]
	if !ok || membership.Role != domain.WorkspaceRoleOwner || !membership.Active {
		return
	}
	for _, other := range s.members {
		if other.WorkspaceID == workspaceID && other.PrimaryOwner {
			return
		}
	}
	membership.PrimaryOwner = true
	s.members[key] = membership
}

func (s *Store) TransferPrimaryOwnership(_ context.Context, workspaceID domain.WorkspaceID, fromID, toID domain.UserID, event events.Event) error {
	if fromID == toID {
		return store.InvalidArgument("primary ownership moves to another member")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	from, ok := s.members[membershipKey(workspaceID, fromID)]
	if !ok {
		return store.ErrNotFound
	}
	if !from.PrimaryOwner || !from.Active {
		return domain.ErrNotWorkspaceAdmin
	}
	to, ok := s.members[membershipKey(workspaceID, toID)]
	user, userOK := s.users[toID]
	if !ok || !userOK || user.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	// The new primary owner is an active, full member who is a person: a guest
	// or a bot cannot hold the workspace.
	isBot := false
	for _, bot := range s.bots {
		if bot.WorkspaceID == workspaceID && bot.UserID == toID {
			isBot = true
		}
	}
	if !to.Active || user.Deleted || to.Guest() || isBot {
		return store.InvalidArgument("the primary owner must be an active full member")
	}
	from.PrimaryOwner = false
	to.Role, to.PrimaryOwner = domain.WorkspaceRoleOwner, true
	s.members[membershipKey(workspaceID, fromID)] = from
	s.members[membershipKey(workspaceID, toID)] = to
	s.outbox = append(s.outbox, event)
	return nil
}
