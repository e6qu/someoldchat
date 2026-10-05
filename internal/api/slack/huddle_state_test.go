package slack

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/service"
)

// A member's huddle participation is in their profile wherever the Web API
// reports one — users.info, users.list and users.profile.get — as the official
// SDKs model it: huddle_state and huddle_state_expiration_ts always, and
// huddle_state_call_id while there is a call to name.
func TestHuddleStateIsInTheProfileEveryUserMethodReports(t *testing.T) {
	handler, s := testHandlerWithStore()
	ctx := context.Background()
	messages := service.Messages{Store: s}
	call, err := messages.StartHuddle(ctx, "T1", "U1", "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	decode := func(method string, form url.Values) map[string]any {
		t.Helper()
		response := callSlackForm(t, handler, "/api/"+method, form.Encode())
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["ok"] != true {
			t.Fatalf("%s: %s", method, response.Body)
		}
		return payload
	}
	profiles := func(user string) map[string]map[string]any {
		t.Helper()
		found := map[string]map[string]any{
			"users.info":        decode("users.info", url.Values{"user": {user}})["user"].(map[string]any)["profile"].(map[string]any),
			"users.profile.get": decode("users.profile.get", url.Values{"user": {user}})["profile"].(map[string]any),
		}
		for _, raw := range decode("users.list", url.Values{})["members"].([]any) {
			if member := raw.(map[string]any); member["id"] == user {
				found["users.list"] = member["profile"].(map[string]any)
			}
		}
		if found["users.list"] == nil {
			t.Fatalf("users.list does not list %s", user)
		}
		return found
	}
	check := func(user, state string, callID any) {
		t.Helper()
		for method, profile := range profiles(user) {
			if profile["huddle_state"] != state || profile["huddle_state_expiration_ts"] != float64(0) || profile["huddle_state_call_id"] != callID {
				t.Fatalf("%s for %s: huddle_state=%v expiration=%v call=%v, want %s / 0 / %v", method, user,
					profile["huddle_state"], profile["huddle_state_expiration_ts"], profile["huddle_state_call_id"], state, callID)
			}
		}
	}
	check("U1", "in_a_huddle", string(call.ID))
	check("U2", "default_unset", nil)

	if _, err := messages.LeaveHuddle(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	check("U1", "default_unset", nil)
}
