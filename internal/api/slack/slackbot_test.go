package slack

import (
	"encoding/json"
	"testing"
)

// TestSlackbotIsInEveryWorkspaceAsUSLACKBOT holds the identity clients rely on.
// Slack's Slackbot is USLACKBOT in every workspace: users.list ends with it,
// users.info describes it in the caller's workspace, it is always active, and a
// member can open a DM with it. Clients hard-code the ID, so a generated one
// would break them.
func TestSlackbotIsInEveryWorkspaceAsUSLACKBOT(t *testing.T) {
	handler, _ := testHandlerWithStore()

	var info struct {
		OK   bool `json:"ok"`
		User struct {
			ID       string `json:"id"`
			TeamID   string `json:"team_id"`
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			IsBot    bool   `json:"is_bot"`
			Profile  struct {
				AlwaysActive bool   `json:"always_active"`
				Team         string `json:"team"`
			} `json:"profile"`
		} `json:"user"`
	}
	result := getAPI(handler, "/api/users.info?user=USLACKBOT")
	if err := json.Unmarshal(result.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	user := info.User
	if !info.OK || user.ID != "USLACKBOT" || user.TeamID != "T1" || user.Name != "slackbot" || user.RealName != "Slackbot" ||
		user.IsBot || !user.Profile.AlwaysActive || user.Profile.Team != "T1" {
		t.Fatalf("users.info USLACKBOT = %s", result.Body)
	}

	var list struct {
		OK      bool `json:"ok"`
		Members []struct {
			ID     string `json:"id"`
			TeamID string `json:"team_id"`
		} `json:"members"`
	}
	result = getAPI(handler, "/api/users.list")
	if err := json.Unmarshal(result.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if !list.OK || len(list.Members) == 0 || list.Members[len(list.Members)-1].ID != "USLACKBOT" || list.Members[len(list.Members)-1].TeamID != "T1" {
		t.Fatalf("users.list does not end with Slackbot: %s", result.Body)
	}

	var presence struct {
		OK       bool   `json:"ok"`
		Presence string `json:"presence"`
	}
	result = getAPI(handler, "/api/users.getPresence?user=USLACKBOT")
	if err := json.Unmarshal(result.Body.Bytes(), &presence); err != nil {
		t.Fatal(err)
	}
	if !presence.OK || presence.Presence != "active" {
		t.Fatalf("users.getPresence USLACKBOT = %s", result.Body)
	}

	var opened struct {
		OK      bool `json:"ok"`
		Channel struct {
			ID   string `json:"id"`
			User string `json:"user"`
		} `json:"channel"`
	}
	result = getAPI(handler, "/api/conversations.open?users=USLACKBOT&return_im=true")
	if err := json.Unmarshal(result.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	if !opened.OK || opened.Channel.ID == "" || opened.Channel.User != "USLACKBOT" {
		t.Fatalf("conversations.open USLACKBOT = %s", result.Body)
	}
}
