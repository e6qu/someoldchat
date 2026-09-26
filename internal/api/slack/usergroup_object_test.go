package slack

import (
	"net/url"
	"slices"
	"testing"
)

func TestUserGroupObjectReportsDefaultChannelsAndWhoDisabledIt(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := apiCaller(t, handler)
	channelsOf := func(group map[string]any) []string {
		t.Helper()
		prefs, ok := group["prefs"].(map[string]any)
		if !ok {
			t.Fatalf("usergroup lacks prefs: %v", group)
		}
		channels := []string{}
		for _, value := range prefs["channels"].([]any) {
			channels = append(channels, value.(string))
		}
		return channels
	}

	created := call("usergroups.create", url.Values{"name": {"Oncall"}, "channels": {"C1"}})
	group := created["usergroup"].(map[string]any)
	requirePinnedFields(t, "usergroups.create", group, pinnedDefinition(t, "objs_subteam", 0))
	if channels := channelsOf(group); !slices.Equal(channels, []string{"C1"}) || group["auto_type"] != nil || group["deleted_by"] != nil || group["channel_count"] != float64(1) {
		t.Fatalf("created group=%v", group)
	}
	id := group["id"].(string)

	updated := call("usergroups.update", url.Values{"usergroup": {id}, "channels": {"C2,C1"}})["usergroup"].(map[string]any)
	if channels := channelsOf(updated); !slices.Equal(channels, []string{"C1", "C2"}) {
		t.Fatalf("updated channels=%v", channels)
	}
	// Updating something else leaves the defaults alone.
	renamed := call("usergroups.update", url.Values{"usergroup": {id}, "description": {"pages"}})["usergroup"].(map[string]any)
	if channels := channelsOf(renamed); !slices.Equal(channels, []string{"C1", "C2"}) {
		t.Fatalf("channels after an unrelated update=%v", channels)
	}
	listed := call("usergroups.list", url.Values{})["usergroups"].([]any)
	if len(listed) != 1 || !slices.Equal(channelsOf(listed[0].(map[string]any)), []string{"C1", "C2"}) {
		t.Fatalf("usergroups.list=%v", listed)
	}
	if unknown := call("usergroups.update", url.Values{"usergroup": {id}, "channels": {"C404"}}); unknown["ok"] != false {
		t.Fatalf("an unknown default channel was accepted: %v", unknown)
	}

	disabled := call("usergroups.disable", url.Values{"usergroup": {id}})["usergroup"].(map[string]any)
	if disabled["deleted_by"] != "U1" || disabled["date_delete"].(float64) <= 0 {
		t.Fatalf("disabled group=%v", disabled)
	}
}
