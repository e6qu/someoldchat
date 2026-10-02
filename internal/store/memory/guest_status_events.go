package memory

import (
	"context"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// guestDeactivationEventLocked is the user.guest_status_changed record a
// deactivation owes when the account it deactivates is a guest's: a guest
// becoming a deactivated guest is one of the changes Slack reports. It is
// built before anything is written, so a record that cannot be built leaves
// the account as it was. The caller holds the lock and has already decided
// the account is active.
func (s *Store) guestDeactivationEventLocked(user domain.User, deactivation events.Event) ([]events.Event, error) {
	membership, exists := s.members[string(user.WorkspaceID)+"\x00"+string(user.ID)]
	if !exists || !membership.Guest() {
		return nil, nil
	}
	snapshot := user
	snapshot.Deleted = true
	snapshot.Role, snapshot.Restricted, snapshot.UltraRestricted = membership.Role, membership.Restricted, membership.UltraRestricted
	event, err := events.GuestStatusChangedEvent(snapshot, deactivation.ActorID, deactivation.CreatedAt)
	if err != nil {
		return nil, err
	}
	return []events.Event{event}, nil
}

func (s *Store) AssignWorkspaceRole(_ context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, role domain.WorkspaceRole, event events.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin && role != domain.WorkspaceRoleOwner {
		return store.InvalidArgument("invalid workspace role")
	}
	key := string(workspaceID) + "\x00" + string(userID)
	membership, ok := s.members[key]
	if !ok {
		return store.ErrNotFound
	}
	user, ok := s.users[userID]
	if !ok || user.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	if role != domain.WorkspaceRoleOwner {
		if err := s.refusePrimaryOwnerChangeLocked(workspaceID, userID); err != nil {
			return err
		}
	}
	wasGuest := membership.Guest()
	membership.Role, membership.Active = role, true
	membership.Restricted, membership.UltraRestricted = false, false
	var guestEvent []events.Event
	if wasGuest {
		user.Updated = secondsInstant(event.CreatedAt)
		snapshot := user
		snapshot.Role = role
		changed, err := events.GuestStatusChangedEvent(snapshot, event.ActorID, event.CreatedAt)
		if err != nil {
			return err
		}
		guestEvent = append(guestEvent, changed)
		s.users[userID] = user
	}
	s.members[key] = membership
	s.claimPrimaryOwnershipLocked(workspaceID, userID)
	s.outbox = append(s.outbox, event)
	s.outbox = append(s.outbox, guestEvent...)
	return nil
}
