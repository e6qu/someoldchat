package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// apiCaller posts a form to the fixture handler and decodes the answer.
func apiCaller(t *testing.T, handler http.Handler) func(method string, form url.Values) map[string]any {
	return func(method string, form url.Values) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(form.Encode()))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s body=%s: %v", method, response.Body, err)
		}
		return payload
	}
}

// pinnedDefinition reads one object definition from the pinned Slack OpenAPI
// document. A definition that is a list of variants (objs_conversation is one
// for channels and one for IMs) is indexed by variant.
func pinnedDefinition(t *testing.T, name string, variant int) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "specs", "upstream", "slack-api-specs", "web-api", "slack_web_openapi_v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Definitions map[string]map[string]any `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	definition, ok := document.Definitions[name]
	if !ok {
		t.Fatalf("pinned spec has no %s", name)
	}
	if variants, ok := definition["items"].([]any); ok {
		return variants[variant].(map[string]any)
	}
	return definition
}

// requirePinnedFields fails when an object lacks a property the pinned
// definition requires. Typed SDK models (slack-api-client) are generated from
// the same objects, so a missing required field reads as a zero value there.
func requirePinnedFields(t *testing.T, label string, object map[string]any, definition map[string]any) {
	t.Helper()
	required, _ := definition["required"].([]any)
	for _, field := range required {
		if _, present := object[field.(string)]; !present {
			t.Errorf("%s lacks required field %q: %v", label, field, object)
		}
	}
}

func TestUsersConversationsListsOnlyTheMembersConversations(t *testing.T) {
	handler, s := testHandlerWithStore()
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})
	s.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "elsewhere"})
	s.SeedConversationMember("C3", "U3")
	call := apiCaller(t, handler)

	ids := func(payload map[string]any) map[string]map[string]any {
		t.Helper()
		if payload["ok"] != true {
			t.Fatalf("payload=%v", payload)
		}
		channels := map[string]map[string]any{}
		for _, value := range payload["channels"].([]any) {
			channel := value.(map[string]any)
			channels[channel["id"].(string)] = channel
		}
		return channels
	}
	mine := ids(call("users.conversations", url.Values{"types": {"public_channel"}}))
	if _, listed := mine["C3"]; listed || mine["C1"] == nil {
		t.Fatalf("users.conversations for the caller = %v, want C1 and not the unjoined C3", mine)
	}
	if _, present := mine["C1"]["is_member"]; present {
		t.Fatalf("users.conversations objects carry is_member, which the pinned schema says they omit: %v", mine["C1"])
	}
	theirs := ids(call("users.conversations", url.Values{"user": {"U3"}}))
	if len(theirs) != 1 || theirs["C3"] == nil {
		t.Fatalf("users.conversations for U3 = %v, want only C3", theirs)
	}

	all := ids(call("conversations.list", url.Values{"types": {"public_channel"}}))
	if all["C1"]["is_member"] != true || all["C3"]["is_member"] != false {
		t.Fatalf("is_member must be the reader's own membership: C1=%v C3=%v", all["C1"], all["C3"])
	}
	if all["C1"]["num_members"] != float64(2) || all["C3"]["num_members"] != float64(1) {
		t.Fatalf("num_members C1=%v C3=%v", all["C1"]["num_members"], all["C3"]["num_members"])
	}
	requirePinnedFields(t, "conversations.list channel", all["C1"], pinnedDefinition(t, "objs_conversation", 0))
}

func TestConversationObjectReportsProvenanceAndTheGeneralChannel(t *testing.T) {
	handler, s := testHandlerWithStore()
	s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test", DefaultChannelIDs: []domain.ConversationID{"C1"}})
	call := apiCaller(t, handler)

	created := call("conversations.create", url.Values{"name": {"Launch Plans"}})
	channel := created["channel"].(map[string]any)
	requirePinnedFields(t, "conversations.create channel", channel, pinnedDefinition(t, "objs_conversation", 0))
	if channel["creator"] != "U1" || channel["created"].(float64) <= 0 || channel["name_normalized"] != "launch-plans" ||
		channel["is_member"] != true || channel["is_general"] != false || channel["is_group"] != false || channel["is_org_shared"] != false {
		t.Fatalf("created channel=%v", channel)
	}
	topic := channel["topic"].(map[string]any)
	if topic["value"] != "" || topic["creator"] != "" || topic["last_set"] != float64(0) {
		t.Fatalf("unset topic=%v", topic)
	}
	id := channel["id"].(string)

	set := call("conversations.setTopic", url.Values{"channel": {id}, "topic": {"ship it"}})
	topic = set["channel"].(map[string]any)["topic"].(map[string]any)
	if topic["value"] != "ship it" || topic["creator"] != "U1" || topic["last_set"].(float64) <= 0 {
		t.Fatalf("set topic=%v", topic)
	}
	info := call("conversations.info", url.Values{"channel": {id}, "include_num_members": {"true"}, "include_locale": {"true"}})
	described := info["channel"].(map[string]any)
	if described["num_members"] != float64(1) || described["locale"] != "en-US" || described["topic"].(map[string]any)["creator"] != "U1" {
		t.Fatalf("conversations.info=%v", described)
	}
	if _, present := call("conversations.info", url.Values{"channel": {id}})["channel"].(map[string]any)["num_members"]; present {
		t.Fatal("conversations.info reported num_members without include_num_members")
	}

	general := call("conversations.info", url.Values{"channel": {"C1"}})["channel"].(map[string]any)
	if general["is_general"] != true {
		t.Fatalf("required channel is not general: %v", general)
	}
	if archived := call("conversations.archive", url.Values{"channel": {"C1"}}); archived["error"] != "cant_archive_general" {
		t.Fatalf("archive general=%v", archived)
	}
	if left := call("conversations.leave", url.Values{"channel": {"C1"}}); left["error"] != "cant_leave_general" {
		t.Fatalf("leave general=%v", left)
	}
	if kicked := call("conversations.kick", url.Values{"channel": {"C1"}, "user": {"U2"}}); kicked["error"] != "cant_kick_from_general" {
		t.Fatalf("kick from general=%v", kicked)
	}
}

func TestSetTopicAndPurposeFollowTheirPinnedErrors(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := apiCaller(t, handler)
	for _, method := range []string{"conversations.setTopic", "conversations.setPurpose"} {
		field := "topic"
		if method == "conversations.setPurpose" {
			field = "purpose"
		}
		// The limit is 250 characters, not bytes: 250 two-byte characters fit.
		if fits := call(method, url.Values{"channel": {"C1"}, field: {strings.Repeat("é", 250)}}); fits["ok"] != true {
			t.Fatalf("%s with 250 characters=%v", method, fits)
		}
		if long := call(method, url.Values{"channel": {"C1"}, field: {strings.Repeat("é", 251)}}); long["error"] != "too_long" {
			t.Fatalf("%s with 251 characters=%v", method, long)
		}
		if archived := call(method, url.Values{"channel": {"C2"}, field: {"late"}}); archived["error"] != "is_archived" {
			t.Fatalf("%s on an archived channel=%v", method, archived)
		}
	}
}

func TestConversationsKickFollowsItsPinnedErrors(t *testing.T) {
	handler, s := testHandlerWithStore()
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})
	call := apiCaller(t, handler)
	for _, testCase := range []struct {
		user, want string
	}{
		{"U1", "cant_kick_self"},
		{"U3", "not_in_channel"},
		{"U404", "user_not_found"},
	} {
		if kicked := call("conversations.kick", url.Values{"channel": {"C1"}, "user": {testCase.user}}); kicked["error"] != testCase.want {
			t.Fatalf("kick %s=%v, want %s", testCase.user, kicked, testCase.want)
		}
	}
	if member, err := s.IsConversationMember(t.Context(), "C1", "U1"); err != nil || !member {
		t.Fatalf("a self-kick removed the caller: member=%v err=%v", member, err)
	}
	if kicked := call("conversations.kick", url.Values{"channel": {"C1"}, "user": {"U2"}}); kicked["ok"] != true || len(kicked) != 1 {
		t.Fatalf("kick U2=%v", kicked)
	}
	if member, err := s.IsConversationMember(t.Context(), "C1", "U2"); err != nil || member {
		t.Fatalf("kick left U2 in the channel: member=%v err=%v", member, err)
	}
}

func TestConversationsOpenNamesUsersAndSelfAndGroupDMs(t *testing.T) {
	handler, s := testHandlerWithStore()
	s.SeedUser(domain.User{ID: "U3", WorkspaceID: "T1", Name: "carol"})
	call := apiCaller(t, handler)

	if missing := call("conversations.open", url.Values{"users": {"U404"}}); missing["error"] != "user_not_found" {
		t.Fatalf("open with an unknown user=%v", missing)
	}
	self := call("conversations.open", url.Values{"users": {"U1"}, "return_im": {"true"}})
	im := self["channel"].(map[string]any)
	requirePinnedFields(t, "self-DM", im, pinnedDefinition(t, "objs_conversation", 2))
	if !strings.HasPrefix(im["id"].(string), "D") || im["user"] != "U1" || im["is_im"] != true || im["is_user_deleted"] != false {
		t.Fatalf("self-DM=%v", im)
	}
	if again := call("conversations.open", url.Values{"users": {"U1"}}); again["already_open"] != true || again["channel"].(map[string]any)["id"] != im["id"] {
		t.Fatalf("reopened self-DM=%v", again)
	}

	group := call("conversations.open", url.Values{"users": {"U2,U3"}})["channel"].(map[string]any)
	if group["name"] != "mpdm-alice--bob--carol-1" || group["is_mpim"] != true || !strings.HasPrefix(group["id"].(string), "C") {
		t.Fatalf("group DM=%v", group)
	}
	requirePinnedFields(t, "group DM", group, pinnedDefinition(t, "objs_conversation", 0))

	// "Resume a conversation by supplying an im or mpim's ID": channel stands
	// for the users the conversation already has.
	if resumed := call("conversations.open", url.Values{"channel": {im["id"].(string)}}); resumed["ok"] != true || resumed["already_open"] != true || resumed["channel"].(map[string]any)["id"] != im["id"] {
		t.Fatalf("resumed self-DM=%v", resumed)
	}
	if closed := call("conversations.close", url.Values{"channel": {group["id"].(string)}}); closed["ok"] != true {
		t.Fatalf("close group DM=%v", closed)
	}
	resumedGroup := call("conversations.open", url.Values{"channel": {group["id"].(string)}})
	if resumedGroup["ok"] != true || resumedGroup["already_open"] != nil || resumedGroup["channel"].(map[string]any)["id"] != group["id"] {
		t.Fatalf("a closed group DM resumed by ID=%v", resumedGroup)
	}
	if channel := call("conversations.open", url.Values{"channel": {"C1"}}); channel["error"] != "method_not_supported_for_channel_type" {
		t.Fatalf("open of a channel=%v", channel)
	}
	if missing := call("conversations.open", url.Values{"channel": {"D404"}}); missing["error"] != "channel_not_found" {
		t.Fatalf("open of an unknown conversation=%v", missing)
	}
	if neither := call("conversations.open", url.Values{}); neither["error"] != "users_list_not_supplied" {
		t.Fatalf("open with neither=%v", neither)
	}
}

func TestChannelNoticesCarryTheirSlackFieldsInHistory(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := apiCaller(t, handler)
	if renamed := call("conversations.rename", url.Values{"channel": {"C1"}, "name": {"announcements"}}); renamed["ok"] != true {
		t.Fatalf("rename=%v", renamed)
	}
	if set := call("conversations.setTopic", url.Values{"channel": {"C1"}, "topic": {"launch"}}); set["ok"] != true {
		t.Fatalf("setTopic=%v", set)
	}
	if set := call("conversations.setPurpose", url.Values{"channel": {"C1"}, "purpose": {"news"}}); set["ok"] != true {
		t.Fatalf("setPurpose=%v", set)
	}
	history := call("conversations.history", url.Values{"channel": {"C1"}})
	bySubtype := map[string]map[string]any{}
	for _, value := range history["messages"].([]any) {
		message := value.(map[string]any)
		if subtype, ok := message["subtype"].(string); ok {
			bySubtype[subtype] = message
		}
	}
	if renamed := bySubtype["channel_name"]; renamed["name"] != "announcements" || renamed["old_name"] != "general" {
		t.Fatalf("channel_name message=%v", renamed)
	}
	if topic := bySubtype["channel_topic"]; topic["topic"] != "launch" {
		t.Fatalf("channel_topic message=%v", topic)
	}
	if purpose := bySubtype["channel_purpose"]; purpose["purpose"] != "news" {
		t.Fatalf("channel_purpose message=%v", purpose)
	}
}
