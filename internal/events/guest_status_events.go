package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// GuestStatusChangedTopic is the durable record of a member's guest status
// changing: a regular member becoming a guest or the reverse, one guest tier
// becoming the other, or a guest being deactivated. It translates to Slack's
// user_guest_status_changed. A member who joins as a guest is team_join
// (user.created), not this.
const GuestStatusChangedTopic = "user.guest_status_changed"

// GuestStatusChangedEvent builds the record for one guest status change.
//
// It lives here, and the repositories call it, because the change is decided
// inside the mutation that makes it: whether a deactivated member was a guest,
// and whether a role assignment ended a guest tier, is the state the
// transaction reads, not the state a caller saw before it. Minting the record
// in the same transaction is what keeps the journal from announcing a change
// that did not commit or missing one that did — the same reason
// TokensRevokedEvent is minted inside revocation.
//
// user is the member as the change leaves them: the guest tier, role and
// deletion it carries are the new status. cache_ts is the instant of the
// change in whole seconds, the value Slack's example shows equal to the user
// object's updated time.
func GuestStatusChangedEvent(user domain.User, actorID domain.UserID, at time.Time) (Event, error) {
	id, err := domain.NewEventID()
	if err != nil {
		return Event{}, err
	}
	payload, err := UserChangePayload(GuestStatusChangedTopic, user, user.Deleted, false, at, Int("cache_ts", at.UTC().Unix()))
	if err != nil {
		return Event{}, err
	}
	return New(id, user.WorkspaceID, actorID, payload, at)
}

// userGuestStatusChanged renders user_guest_status_changed: the user object
// and cache_ts, beside the type and event_ts every inner event carries.
func userGuestStatusChanged(delivered Delivered, _ Surface) ([]Inner, error) {
	user, exists := delivered.Object["user"]
	if !exists || len(user) == 0 {
		return nil, fmt.Errorf("%w: %s payload has no user", ErrSlackEventIncomplete, delivered.Type)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(user, &object) != nil || object == nil {
		return nil, fmt.Errorf("%w: %s payload has an invalid user", ErrSlackEventIncomplete, delivered.Type)
	}
	cacheTS, ok := delivered.Int("cache_ts")
	if !ok {
		return nil, fmt.Errorf("%w: %s payload has no cache_ts", ErrSlackEventIncomplete, delivered.Type)
	}
	inner, err := newInner("user_guest_status_changed", delivered,
		Field{name: "user", value: append(json.RawMessage(nil), user...)},
		Int("cache_ts", cacheTS))
	if err != nil {
		return nil, err
	}
	return []Inner{inner}, nil
}
