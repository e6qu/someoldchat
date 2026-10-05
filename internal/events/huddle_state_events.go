package events

import (
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// UserHuddleChangedTopic is the durable record of a member's huddle_state
// changing: joining a huddle, leaving the one they were in, or having it end
// around them. It translates to Slack's user_huddle_changed and, beside it,
// user_change, because the change is a change to the member's profile.
const UserHuddleChangedTopic = "user.huddle_changed"

// UserHuddleChangedEvent builds the record for one huddle_state change.
//
// The repositories call it, inside the mutation that moves the member, for the
// reason GuestStatusChangedEvent is minted there: whether a join added the
// member or found them already in, and whom an ending huddle took with it, is
// what the transaction reads, not what a caller saw before it. A record minted
// outside could announce a join that did not happen or miss the members an
// ended huddle released.
//
// user is the member as the change leaves them: its HuddleCallID is the new
// state. cache_ts is the instant of the change in whole seconds, as on
// user_guest_status_changed; the official SDKs model the field on
// user_huddle_changed (Node @slack/types UserHuddleChangedEvent, Java
// UserHuddleChangedEvent.cacheTs).
func UserHuddleChangedEvent(user domain.User, actorID domain.UserID, at time.Time) (Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return Event{}, err
	}
	payload, err := UserChangePayload(UserHuddleChangedTopic, user, user.Deleted, false, at, Int("cache_ts", at.UTC().Unix()))
	if err != nil {
		return Event{}, err
	}
	return New(id, user.WorkspaceID, actorID, payload, at)
}

// userHuddleChanged renders one huddle_state change as the two events Slack
// sends for it: user_huddle_changed, with the user object and cache_ts, and
// user_change with the same user object. The subscription filter narrows the
// pair to the names each app subscribed to.
func userHuddleChanged(delivered Delivered, _ Surface) ([]Inner, error) {
	huddle, err := userInnerWithCacheTS("user_huddle_changed", delivered)
	if err != nil {
		return nil, err
	}
	change, err := userInner("user_change", delivered)
	if err != nil {
		return nil, err
	}
	return []Inner{huddle, change}, nil
}
