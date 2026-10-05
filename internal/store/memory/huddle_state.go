package memory

import (
	"slices"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// refreshHuddleStateLocked settles a member's HuddleCallID after a huddle
// mutation has moved them, and appends the user.huddle_changed record when it
// changed. The caller holds the lock and has already written the call.
//
// The state is the huddle the member is in: preferred when they are in it (the
// huddle they just joined), else the one already recorded while they are
// still in it, else any other running huddle they are in, else none. Reading
// it back from the calls, rather than setting what the caller expects, is what
// keeps a member who leaves one of two huddles in the other, and a member an
// ending huddle released out of it.
func (s *Store) refreshHuddleStateLocked(userID domain.UserID, preferred domain.CallID, actor domain.UserID, at time.Time) error {
	user, exists := s.users[userID]
	if !exists {
		return nil
	}
	var in []domain.Call
	for _, call := range s.calls {
		if call.WorkspaceID == user.WorkspaceID && call.Kind == domain.CallKindHuddle && call.Active() && slices.Contains(call.Participants, userID) {
			in = append(in, call)
		}
	}
	next := domain.CurrentHuddle(in, preferred, user.HuddleCallID)
	if next == user.HuddleCallID {
		return nil
	}
	user.HuddleCallID = next
	user.Updated = secondsInstant(at)
	snapshot := user
	if membership, ok := s.members[string(user.WorkspaceID)+"\x00"+string(user.ID)]; ok {
		snapshot.Role, snapshot.Restricted, snapshot.UltraRestricted, snapshot.PrimaryOwner = membership.Role, membership.Restricted, membership.UltraRestricted, membership.PrimaryOwner
	}
	event, err := events.UserHuddleChangedEvent(snapshot, actor, at)
	if err != nil {
		return err
	}
	s.users[userID] = user
	s.outbox = append(s.outbox, event)
	return nil
}
