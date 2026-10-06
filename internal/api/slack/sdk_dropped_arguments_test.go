package slack

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
)

// The tests in this file hold arguments the published SDKs send (python
// slack_sdk 3.45, Java slack-api-client 1.52, @slack/web-api 8.2) that the
// handlers used to read as absent.

// reactions.list's legacy count/page paging walks the whole listing and
// describes it in a paging object. Both arguments were ignored, so every page
// was the first and a legacy paginator looped forever.
func TestReactionsListPagesByCountAndPage(t *testing.T) {
	handler := testHandler()
	for index := range 3 {
		ts := post(t, handler, url.Values{"channel": {"C1"}, "text": {"react " + strconv.Itoa(index)}})
		requireOK(t, "reactions.add", slackCall(t, handler, "token", "reactions.add", url.Values{"channel": {"C1"}, "timestamp": {ts}, "name": {"eyes"}}))
	}
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		listed := slackCall(t, handler, "token", "reactions.list", url.Values{"count": {"1"}, "page": {strconv.Itoa(page)}})
		requireOK(t, "reactions.list", listed)
		items := listed["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("page %d items=%v", page, items)
		}
		ts := items[0].(map[string]any)["message"].(map[string]any)["ts"].(string)
		if seen[ts] {
			t.Fatalf("page %d repeated %s", page, ts)
		}
		seen[ts] = true
		paging := listed["paging"].(map[string]any)
		if paging["count"] != float64(1) || paging["page"] != float64(page) || paging["pages"] != float64(3) || paging["total"] != float64(3) {
			t.Fatalf("page %d paging=%v", page, paging)
		}
	}
	past := slackCall(t, handler, "token", "reactions.list", url.Values{"count": {"1"}, "page": {"4"}})
	if items := past["items"].([]any); len(items) != 0 {
		t.Fatalf("a page past the end=%v", past)
	}
	// Cursor paging is unchanged and carries no legacy paging object.
	if cursorPage := slackCall(t, handler, "token", "reactions.list", url.Values{"limit": {"2"}}); cursorPage["paging"] != nil || len(cursorPage["items"].([]any)) != 2 {
		t.Fatalf("cursor paging=%v", cursorPage)
	}
	requireError(t, "reactions.list", slackCall(t, handler, "token", "reactions.list", url.Values{"page": {"0"}}), "invalid_arg_name")
	requireError(t, "reactions.list", slackCall(t, handler, "token", "reactions.list", url.Values{"team_id": {"T-elsewhere"}}), "invalid_arg_name")
	requireOK(t, "reactions.list", slackCall(t, handler, "token", "reactions.list", url.Values{"team_id": {"T1"}}))
}

// A file or file comment named as the item of a reactions.* or pins.* call is
// answered for that item, with the code each method's pinned enum declares,
// rather than as no item at all.
func TestReactionsAndPinsAnswerForAFileItem(t *testing.T) {
	handler := testHandler()
	got := slackCall(t, handler, "token", "reactions.get", url.Values{"file": {"F1"}})
	requireOK(t, "reactions.get", got)
	if file := got["file"].(map[string]any); got["type"] != "file" || file["id"] != "F1" || file["reactions"] != nil {
		t.Fatalf("reactions.get file=%v", got)
	}
	for _, check := range []struct {
		method string
		values url.Values
		want   string
	}{
		{"reactions.get", url.Values{"file": {"F-nobody"}}, "file_not_found"},
		{"reactions.get", url.Values{"file_comment": {"FC1"}}, "file_comment_not_found"},
		{"reactions.remove", url.Values{"file": {"F1"}, "name": {"eyes"}}, "no_reaction"},
		{"reactions.remove", url.Values{"file": {"F-nobody"}, "name": {"eyes"}}, "file_not_found"},
		{"reactions.remove", url.Values{"file_comment": {"FC1"}, "name": {"eyes"}}, "file_comment_not_found"},
		{"reactions.add", url.Values{"file": {"F1"}, "name": {"eyes"}}, "invalid_arg_name"},
		{"pins.add", url.Values{"channel": {"C1"}, "file": {"F1"}}, "not_pinnable"},
		{"pins.add", url.Values{"channel": {"C1"}, "file_comment": {"FC1"}}, "not_pinnable"},
		{"pins.remove", url.Values{"channel": {"C1"}, "file": {"F1"}}, "not_pinned"},
		{"pins.remove", url.Values{"channel": {"C1"}, "file": {"F-nobody"}}, "file_not_found"},
		{"pins.remove", url.Values{"channel": {"C1"}, "file_comment": {"FC1"}}, "file_comment_not_found"},
	} {
		requireError(t, check.method, slackCall(t, handler, "token", check.method, check.values), check.want)
	}
	// With no item at all the methods still say so.
	requireError(t, "reactions.get", slackCall(t, handler, "token", "reactions.get", url.Values{}), "no_item_specified")
}

// users.info and users.list report a member's locale when include_locale asks:
// the language they chose in Language & region, else the source language.
// Both methods dropped the argument, so no member object carried one.
func TestUsersReportTheirLocaleWhenAsked(t *testing.T) {
	handler, repository := testHandlerWithStore()
	if err := repository.SetMemberPreference(context.Background(), "T1", "U2", domain.LanguagePreference, "en-XA", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	plain := slackCall(t, handler, "token", "users.info", url.Values{"user": {"U2"}})
	requireOK(t, "users.info", plain)
	if _, present := plain["user"].(map[string]any)["locale"]; present {
		t.Fatalf("a locale nobody asked for: %v", plain)
	}
	chosen := slackCall(t, handler, "token", "users.info", url.Values{"user": {"U2"}, "include_locale": {"true"}})
	if locale := chosen["user"].(map[string]any)["locale"]; locale != "en-XA" {
		t.Fatalf("U2's chosen locale=%v", locale)
	}
	unchosen := slackCall(t, handler, "token", "users.info", url.Values{"user": {"U1"}, "include_locale": {"1"}})
	if locale := unchosen["user"].(map[string]any)["locale"]; locale != "en-US" {
		t.Fatalf("U1's locale with no choice=%v", locale)
	}
	requireError(t, "users.info", slackCall(t, handler, "token", "users.info", url.Values{"user": {"U1"}, "include_locale": {"maybe"}}), "invalid_arg_name")
	listed := slackCall(t, handler, "token", "users.list", url.Values{"include_locale": {"true"}})
	requireOK(t, "users.list", listed)
	locales := map[string]any{}
	for _, member := range listed["members"].([]any) {
		object := member.(map[string]any)
		locales[object["id"].(string)] = object["locale"]
	}
	if locales["U1"] != "en-US" || locales["U2"] != "en-XA" {
		t.Fatalf("users.list locales=%v", locales)
	}
	if bare := slackCall(t, handler, "token", "users.list", url.Values{}); bare["members"].([]any)[0].(map[string]any)["locale"] != nil {
		t.Fatalf("users.list answered a locale nobody asked for: %v", bare)
	}
}

// slackLists.items.create's duplicated_item_id copies an item of the list, and
// slackLists.items.info's include_is_subscribed reports whether the reader
// follows the row. Both were dropped: duplicating made an empty item, and the
// field a client asked for was missing.
func TestListItemsDuplicateAndReportSubscription(t *testing.T) {
	_, mux := canvasFixture(t)
	created := canvasCall(t, mux, "/api/slackLists.create", "name=Incidents&description_blocks=%5B%5D&schema="+url.QueryEscape(`[{"key":"title","name":"Title","type":"text"},{"key":"owner","name":"Owner","type":"text"}]`))
	listID := created["list"].(map[string]any)["id"].(string)
	source := canvasCall(t, mux, "/api/slackLists.items.create", "list_id="+listID+"&initial_fields="+url.QueryEscape(`[{"column_id":"title","value":"Disk full"},{"column_id":"owner","value":"ops"}]`))
	sourceID := source["item"].(map[string]any)["id"].(string)

	copied := canvasCall(t, mux, "/api/slackLists.items.create", "list_id="+listID+"&duplicated_item_id="+sourceID)
	if fields, _ := json.Marshal(copied["item"].(map[string]any)["fields"]); string(fields) != `[{"column_id":"title","value":"Disk full"},{"column_id":"owner","value":"ops"}]` {
		t.Fatalf("a duplicate's cells=%s", fields)
	}
	if copied["item"].(map[string]any)["id"] == sourceID {
		t.Fatalf("the duplicate is the source: %v", copied)
	}
	// Initial fields replace the copied cell of their column.
	overridden := canvasCall(t, mux, "/api/slackLists.items.create", "list_id="+listID+"&duplicated_item_id="+sourceID+"&initial_fields="+url.QueryEscape(`[{"column_id":"owner","value":"db"}]`))
	if fields, _ := json.Marshal(overridden["item"].(map[string]any)["fields"]); string(fields) != `[{"column_id":"title","value":"Disk full"},{"column_id":"owner","value":"db"}]` {
		t.Fatalf("a duplicate with initial fields=%s", fields)
	}
	if missing := canvasCall(t, mux, "/api/slackLists.items.create", "list_id="+listID+"&duplicated_item_id=Rec-nobody"); missing["ok"] == true {
		t.Fatalf("a duplicate of nothing was created: %v", missing)
	}

	info := canvasCall(t, mux, "/api/slackLists.items.info", "list_id="+listID+"&id="+sourceID)
	if _, present := info["item"].(map[string]any)["is_subscribed"]; present || info["record"].(map[string]any)["id"] != sourceID {
		t.Fatalf("slackLists.items.info=%v", info)
	}
	subscribed := canvasCall(t, mux, "/api/slackLists.items.info", "list_id="+listID+"&id="+sourceID+"&include_is_subscribed=true")
	if subscribed["record"].(map[string]any)["is_subscribed"] != false {
		t.Fatalf("include_is_subscribed=%v", subscribed)
	}
}

// usergroups.users.list answers a disabled group only when include_disabled
// asks, and refuses a team_id other than the token's.
func TestUserGroupUsersListHonoursIncludeDisabled(t *testing.T) {
	handler := testHandler()
	created := slackCall(t, handler, "token", "usergroups.create", url.Values{"name": {"Engineering"}, "handle": {"engineering"}})
	requireOK(t, "usergroups.create", created)
	id := created["usergroup"].(map[string]any)["id"].(string)
	requireOK(t, "usergroups.users.update", slackCall(t, handler, "token", "usergroups.users.update", url.Values{"usergroup": {id}, "users": {"U1"}}))
	requireOK(t, "usergroups.users.list", slackCall(t, handler, "token", "usergroups.users.list", url.Values{"usergroup": {id}}))
	requireOK(t, "usergroups.disable", slackCall(t, handler, "token", "usergroups.disable", url.Values{"usergroup": {id}}))
	requireError(t, "usergroups.users.list", slackCall(t, handler, "token", "usergroups.users.list", url.Values{"usergroup": {id}}), "usergroup_not_found")
	included := slackCall(t, handler, "token", "usergroups.users.list", url.Values{"usergroup": {id}, "include_disabled": {"true"}})
	requireOK(t, "usergroups.users.list", included)
	if users := included["users"].([]any); len(users) != 1 || users[0] != "U1" {
		t.Fatalf("a disabled group's members=%v", included)
	}
	requireError(t, "usergroups.users.list", slackCall(t, handler, "token", "usergroups.users.list", url.Values{"usergroup": {id}, "include_disabled": {"sure"}}), "invalid_arg_name")
	requireError(t, "usergroups.users.list", slackCall(t, handler, "token", "usergroups.users.list", url.Values{"usergroup": {id}, "include_disabled": {"true"}, "team_id": {"T-elsewhere"}}), "invalid_arg_name")
}

// team.externalTeams.list applies its filters and its sort. Every connection
// here is connected, carries no Slack Connect preference override and is made
// through this workspace, so a filter that asks for anything else matches
// nothing; the sort is by name in either direction.
func TestExternalTeamsListFiltersAndSorts(t *testing.T) {
	scopes := make([]auth.Scope, 0)
	for _, name := range auth.AllScopes() {
		scopes = append(scopes, auth.Scope(name))
	}
	handler, repository := testHandlerWithScopes(scopes...)
	ctx := context.Background()
	for id, name := range map[domain.WorkspaceID]string{"T-a": "Zebra", "T-b": "Aardvark", "T-c": "Mongoose"} {
		repository.SeedWorkspace(domain.Workspace{ID: id, Name: name})
	}
	if err := repository.SetConversationTeams(ctx, "T1", "C1", []domain.WorkspaceID{"T1", "T-a", "T-b", "T-c"}, false, events.Event{ID: "E-teams", WorkspaceID: "T1", Topic: "conversation.connected", Payload: `{"type":"conversation.connected"}`}); err != nil {
		t.Fatal(err)
	}
	names := func(body map[string]any) []string {
		requireOK(t, "team.externalTeams.list", body)
		result := []string{}
		for _, organization := range body["organizations"].([]any) {
			result = append(result, organization.(map[string]any)["team_name"].(string))
		}
		return result
	}
	if got := names(slackCall(t, handler, "token", "team.externalTeams.list", url.Values{})); !slices.Equal(got, []string{"Aardvark", "Mongoose", "Zebra"}) {
		t.Fatalf("default order=%v", got)
	}
	if got := names(slackCall(t, handler, "token", "team.externalTeams.list", url.Values{"sort_field": {"team_name"}, "sort_direction": {"desc"}})); !slices.Equal(got, []string{"Zebra", "Mongoose", "Aardvark"}) {
		t.Fatalf("descending=%v", got)
	}
	// Paging walks the same order in either direction.
	first := slackCall(t, handler, "token", "team.externalTeams.list", url.Values{"sort_direction": {"desc"}, "limit": {"2"}})
	cursor := first["response_metadata"].(map[string]any)["next_cursor"].(string)
	if got := names(slackCall(t, handler, "token", "team.externalTeams.list", url.Values{"sort_direction": {"desc"}, "limit": {"2"}, "cursor": {cursor}})); !slices.Equal(got, []string{"Aardvark"}) {
		t.Fatalf("descending second page=%v", got)
	}
	for _, values := range []url.Values{
		{"connection_status_filter": {"CONNECTED"}},
		{"workspace_filter": {"T1"}},
	} {
		if got := names(slackCall(t, handler, "token", "team.externalTeams.list", values)); len(got) != 3 {
			t.Fatalf("values=%v listed %v, want every connection", values, got)
		}
	}
	for _, values := range []url.Values{
		{"connection_status_filter": {"BLOCKED"}},
		{"slack_connect_pref_filter": {"approved_orgs_only"}},
		{"workspace_filter": {"T-elsewhere"}},
	} {
		if got := names(slackCall(t, handler, "token", "team.externalTeams.list", values)); len(got) != 0 {
			t.Fatalf("values=%v listed %v, want none", values, got)
		}
	}
	for _, values := range []url.Values{
		{"connection_status_filter": {"SOMETIMES"}},
		{"slack_connect_pref_filter": {"favourite_colour"}},
		{"sort_field": {"vibes"}},
		{"sort_direction": {"sideways"}},
	} {
		requireError(t, "team.externalTeams.list", slackCall(t, handler, "token", "team.externalTeams.list", values), "invalid_arguments")
	}
}

// team_id selects the workspace of an organization-wide token. A token here
// belongs to one workspace, so every method the SDKs send team_id to refuses
// another workspace instead of answering about the token's own as if it were
// the one asked for, and accepts its own.
func TestTeamIDNamesOnlyTheTokensOwnWorkspace(t *testing.T) {
	handler := testHandler()
	for _, check := range []struct {
		method string
		values url.Values
	}{
		{"usergroups.list", url.Values{}},
		{"usergroups.create", url.Values{"name": {"Design"}, "handle": {"design"}}},
		{"usergroups.update", url.Values{"usergroup": {"S-nobody"}, "name": {"Renamed"}}},
		{"usergroups.enable", url.Values{"usergroup": {"S-nobody"}}},
		{"usergroups.disable", url.Values{"usergroup": {"S-nobody"}}},
		{"usergroups.users.update", url.Values{"usergroup": {"S-nobody"}, "users": {"U1"}}},
		{"users.list", url.Values{}},
		{"users.conversations", url.Values{}},
		{"conversations.list", url.Values{}},
		{"reactions.list", url.Values{}},
	} {
		foreign := url.Values{"team_id": {"T-elsewhere"}}
		for name, values := range check.values {
			foreign[name] = values
		}
		requireError(t, check.method, slackCall(t, handler, "token", check.method, foreign), "invalid_arg_name")
	}
	for _, method := range []string{"usergroups.list", "users.list", "users.conversations", "conversations.list", "reactions.list"} {
		requireOK(t, method, slackCall(t, handler, "token", method, url.Values{"team_id": {"T1"}}))
	}
}
