package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The inner event is the shape the official SDKs model as
// UserHuddleChangedEvent (Node @slack/types 3.2.0 events/user.d.ts, Java
// slack-api-model 1.49.0): {type, user, cache_ts, event_ts}, the user object's
// profile carrying huddle_state, huddle_state_expiration_ts and
// huddle_state_call_id. Slack sends user_change beside it with the same user
// object, and both reach every surface user_change does.
func TestUserHuddleChangedTranslatesToTheSDKShape(t *testing.T) {
	at := time.Unix(1_758_000_000, 100_000).UTC()
	for _, test := range []struct {
		name   string
		callID domain.CallID
		state  string
	}{
		{"joining", "R0123", "in_a_huddle"},
		{"leaving", "", "default_unset"},
	} {
		event, err := UserHuddleChangedEvent(domain.User{
			ID: "U1234567", WorkspaceID: "T1234567", Name: "sandraslacksalot", RealName: "Sandra Slacksalot",
			Role: domain.WorkspaceRoleAdmin, HuddleCallID: test.callID,
		}, "U1234567", at)
		if err != nil {
			t.Fatal(err)
		}
		if event.Topic != UserHuddleChangedTopic || event.ActorID != "U1234567" || event.WorkspaceID != "T1234567" {
			t.Fatalf("%s: event = %+v", test.name, event)
		}
		delivered, err := Broadcastable(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, surface := range []Surface{SurfaceEventsAPI, SurfaceSocketMode, SurfaceRTM} {
			inners, err := SlackInner(event.Topic, delivered, surface)
			if err != nil || len(inners) != 2 || inners[0].Type() != "user_huddle_changed" || inners[1].Type() != "user_change" {
				t.Fatalf("%s surface %d: inners=%v err=%v", test.name, surface, inners, err)
			}
			encoded, err := inners[0].Encode()
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"type", "user", "cache_ts", "event_ts"} {
				if _, ok := raw[key]; !ok {
					t.Fatalf("%s: user_huddle_changed lacks %s: %s", test.name, key, encoded)
				}
			}
			var inner struct {
				Type    string `json:"type"`
				EventTS string `json:"event_ts"`
				CacheTS int64  `json:"cache_ts"`
				User    struct {
					ID      string `json:"id"`
					TeamID  string `json:"team_id"`
					IsAdmin bool   `json:"is_admin"`
					Updated int64  `json:"updated"`
					Profile struct {
						HuddleState             *string `json:"huddle_state"`
						HuddleStateExpirationTS *int64  `json:"huddle_state_expiration_ts"`
						HuddleStateCallID       *string `json:"huddle_state_call_id"`
					} `json:"profile"`
				} `json:"user"`
			}
			if err := json.Unmarshal([]byte(encoded), &inner); err != nil {
				t.Fatal(err)
			}
			profile := inner.User.Profile
			if inner.Type != "user_huddle_changed" || inner.CacheTS != 1_758_000_000 || inner.EventTS != "1758000000.000100" ||
				inner.User.ID != "U1234567" || inner.User.TeamID != "T1234567" || !inner.User.IsAdmin || inner.User.Updated != 1_758_000_000 ||
				profile.HuddleState == nil || *profile.HuddleState != test.state ||
				profile.HuddleStateExpirationTS == nil || *profile.HuddleStateExpirationTS != 0 {
				t.Fatalf("%s: inner = %s", test.name, encoded)
			}
			// The call is named while there is one, and the optional field is
			// absent rather than empty when there is none.
			if (profile.HuddleStateCallID != nil) != (test.callID != "") || (profile.HuddleStateCallID != nil && *profile.HuddleStateCallID != string(test.callID)) {
				t.Fatalf("%s: huddle_state_call_id in %s", test.name, encoded)
			}
			change, err := inners[1].Encode()
			if err != nil {
				t.Fatal(err)
			}
			var changed struct {
				Type string          `json:"type"`
				User json.RawMessage `json:"user"`
			}
			if err := json.Unmarshal([]byte(change), &changed); err != nil {
				t.Fatal(err)
			}
			if changed.Type != "user_change" || string(changed.User) != string(raw["user"]) {
				t.Fatalf("%s: user_change = %s, want the same user as %s", test.name, change, encoded)
			}
		}
	}
}

// A record without its cache_ts or user object is incomplete, not rendered
// with the field missing.
func TestUserHuddleChangedRefusesAnIncompletePayload(t *testing.T) {
	for _, payload := range []Payload{
		NewPayload(UserHuddleChangedTopic, String("user_id", "U1"), Int("cache_ts", 1)),
		NewPayload(UserHuddleChangedTopic, String("user_id", "U1"), JSON("user", `{"id":"U1"}`)),
	} {
		event, err := New("Ev1", "T1", "U1", payload, time.Unix(1_758_000_000, 0))
		if err != nil {
			t.Fatal(err)
		}
		delivered, err := Broadcastable(event)
		if err != nil {
			t.Fatal(err)
		}
		if inners, err := SlackInner(event.Topic, delivered, SurfaceEventsAPI); err == nil {
			t.Fatalf("payload %+v rendered %v", payload, inners)
		}
	}
}
