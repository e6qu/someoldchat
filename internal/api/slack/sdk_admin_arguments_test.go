package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// The tests in this file hold arguments the published Web API clients send
// (slack_sdk 3.45.0, slack-api-client 1.52.0, @slack/web-api 8.2.0) that these
// handlers used to ignore or read under another name. An ignored argument
// answers ok and does something other than what was asked, which no
// response-shape check can see, so each test asserts the effect.

func callForm(t *testing.T, handler http.Handler, endpoint string, values url.Values) map[string]any {
	t.Helper()
	return adminCall(t, handler, http.MethodPost, endpoint, values.Encode())
}

func TestAdminUsersListHonoursIsActive(t *testing.T) {
	handler, _ := testUserHandlerWithStore()
	if removed := callForm(t, handler, "admin.users.remove", url.Values{"team_id": {"T1"}, "user_id": {"U2"}}); removed["ok"] != true {
		t.Fatalf("remove=%v", removed)
	}
	ids := func(payload map[string]any) []string {
		t.Helper()
		users, ok := payload["users"].([]any)
		if !ok {
			t.Fatalf("list=%v", payload)
		}
		listed := make([]string, 0, len(users))
		for _, user := range users {
			listed = append(listed, user.(map[string]any)["id"].(string))
		}
		return listed
	}
	// is_active defaults to true.
	for _, values := range []url.Values{{}, {"is_active": {"true"}}} {
		if got := strings.Join(ids(callForm(t, handler, "admin.users.list", values)), ","); got != "U1" {
			t.Fatalf("values=%v listed %s, want only the active member", values, got)
		}
	}
	if got := strings.Join(ids(callForm(t, handler, "admin.users.list", url.Values{"is_active": {"false"}})), ","); got != "U2" {
		t.Fatalf("is_active=false listed %s, want only the deactivated member", got)
	}
	for _, values := range []url.Values{{"is_active": {"maybe"}}, {"include_deactivated_user_workspaces": {"maybe"}}} {
		if refused := callForm(t, handler, "admin.users.list", values); refused["error"] != "invalid_arg_name" {
			t.Fatalf("values=%v refused=%v", values, refused)
		}
	}
	if accepted := callForm(t, handler, "admin.users.list", url.Values{"include_deactivated_user_workspaces": {"true"}}); accepted["ok"] != true {
		t.Fatalf("include_deactivated_user_workspaces=%v", accepted)
	}
}

func TestTeamInfoAnswersOnlyTheTeamItNames(t *testing.T) {
	handler, _ := testUserHandlerWithStore()
	own := callForm(t, handler, "team.info", url.Values{})
	team, _ := own["team"].(map[string]any)
	if team == nil || team["id"] != "T1" {
		t.Fatalf("team.info=%v", own)
	}
	domainName, _ := team["domain"].(string)
	for _, values := range []url.Values{{"team": {"T1"}}, {"domain": {domainName}}, {"team": {"T1"}, "domain": {strings.ToUpper(domainName)}}} {
		if got := callForm(t, handler, "team.info", values); got["team"].(map[string]any)["id"] != "T1" {
			t.Fatalf("values=%v answered %v", values, got)
		}
	}
	// Naming another workspace used to answer the caller's own.
	for _, values := range []url.Values{{"team": {"T-elsewhere"}}, {"domain": {"elsewhere"}}} {
		if got := callForm(t, handler, "team.info", values); got["error"] != "team_not_found" {
			t.Fatalf("values=%v answered %v", values, got)
		}
	}
}

func TestAdminUsersAssignAddsTheGuestTierItNames(t *testing.T) {
	handler, store := testUserHandlerWithStore()
	tier := func(t *testing.T, user domain.UserID) (bool, bool) {
		t.Helper()
		membership, err := store.GetWorkspaceMembership(context.Background(), "T1", user)
		if err != nil {
			t.Fatal(err)
		}
		return membership.Restricted, membership.UltraRestricted
	}
	assign := func(t *testing.T, extra url.Values) map[string]any {
		t.Helper()
		values := url.Values{"team_id": {"T1"}, "user_id": {"U2"}}
		for key, value := range extra {
			values[key] = value
		}
		return callForm(t, handler, "admin.users.assign", values)
	}
	if got := assign(t, url.Values{"is_restricted": {"true"}}); got["ok"] != true {
		t.Fatalf("assign as guest=%v", got)
	}
	if restricted, ultra := tier(t, "U2"); !restricted || ultra {
		t.Fatalf("is_restricted=true left restricted=%v ultra=%v", restricted, ultra)
	}
	// Naming neither flag keeps the tier: reactivating a guest is not a
	// promotion.
	if got := assign(t, url.Values{}); got["ok"] != true {
		t.Fatalf("assign=%v", got)
	}
	if restricted, _ := tier(t, "U2"); !restricted {
		t.Fatal("an assign naming no tier promoted a guest")
	}
	if got := assign(t, url.Values{"is_ultra_restricted": {"true"}}); got["ok"] != true {
		t.Fatalf("assign as single-channel guest=%v", got)
	}
	if restricted, ultra := tier(t, "U2"); restricted || !ultra {
		t.Fatalf("is_ultra_restricted=true left restricted=%v ultra=%v", restricted, ultra)
	}
	if got := assign(t, url.Values{"is_restricted": {"false"}, "is_ultra_restricted": {"false"}}); got["ok"] != true {
		t.Fatalf("assign as member=%v", got)
	}
	if restricted, ultra := tier(t, "U2"); restricted || ultra {
		t.Fatalf("both flags false left restricted=%v ultra=%v", restricted, ultra)
	}
	for _, extra := range []url.Values{{"is_restricted": {"true"}, "is_ultra_restricted": {"true"}}, {"is_restricted": {"maybe"}}} {
		if refused := assign(t, extra); refused["error"] != "invalid_arg_name" {
			t.Fatalf("extra=%v refused=%v", extra, refused)
		}
	}
	// A guest cannot hold an administrator's role, so an administrator is not
	// made one.
	if refused := callForm(t, handler, "admin.users.assign", url.Values{"team_id": {"T1"}, "user_id": {"U1"}, "is_restricted": {"true"}}); refused["error"] != "invalid_arg_name" {
		t.Fatalf("an administrator was made a guest: %v", refused)
	}
	if restricted, ultra := tier(t, "U1"); restricted || ultra {
		t.Fatal("the refused assign changed the administrator's membership")
	}
}

func TestAdminUsersSessionResetHonoursMobileAndWebOnly(t *testing.T) {
	handler, store := testUserHandlerWithStore()
	ctx := context.Background()
	seed := func(t *testing.T, prefix string) {
		t.Helper()
		for _, token := range []string{prefix + "-a", prefix + "-b"} {
			if err := store.SeedSession(ctx, token, domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	live := func(t *testing.T) int {
		t.Helper()
		sessions, err := store.ListUserSessions(ctx, "T1", "U2")
		if err != nil {
			t.Fatal(err)
		}
		return len(sessions)
	}
	for _, endpoint := range []struct{ name, target string }{{"admin.users.session.reset", "user_id"}, {"admin.users.session.resetBulk", "user_ids"}} {
		seed(t, endpoint.name)
		// Every session this deployment issues is a web session; there is no
		// mobile client, so a mobile-only reset has nothing to end.
		if got := callForm(t, handler, endpoint.name, url.Values{endpoint.target: {"U2"}, "mobile_only": {"true"}}); got["ok"] != true {
			t.Fatalf("%s mobile_only=%v", endpoint.name, got)
		}
		if count := live(t); count != 2 {
			t.Fatalf("%s mobile_only ended web sessions: %d left", endpoint.name, count)
		}
		if refused := callForm(t, handler, endpoint.name, url.Values{endpoint.target: {"U2"}, "mobile_only": {"true"}, "web_only": {"true"}}); refused["error"] != "invalid_arg_name" {
			t.Fatalf("%s both=%v", endpoint.name, refused)
		}
		if count := live(t); count != 2 {
			t.Fatalf("%s a refused reset ended sessions: %d left", endpoint.name, count)
		}
		if got := callForm(t, handler, endpoint.name, url.Values{endpoint.target: {"U2"}, "web_only": {"true"}}); got["ok"] != true {
			t.Fatalf("%s web_only=%v", endpoint.name, got)
		}
		if count := live(t); count != 0 {
			t.Fatalf("%s web_only left %d sessions", endpoint.name, count)
		}
	}
}

func TestUsersProfileSetEditsTheMemberItNames(t *testing.T) {
	handler, store := testUserHandlerWithStore()
	ctx := context.Background()
	displayName := func(t *testing.T, user domain.UserID) string {
		t.Helper()
		value, err := store.GetUser(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return value.Profile.DisplayName
	}
	// An administrator editing a member changes that member, not themselves.
	got := callForm(t, handler, "users.profile.set", url.Values{"user": {"U2"}, "name": {"display_name"}, "value": {"bobby"}})
	if got["ok"] != true || got["profile"].(map[string]any)["display_name"] != "bobby" {
		t.Fatalf("admin edit=%v", got)
	}
	if displayName(t, "U2") != "bobby" || displayName(t, "U1") != "alice" {
		t.Fatalf("U2=%q U1=%q after editing U2", displayName(t, "U2"), displayName(t, "U1"))
	}
	// Only the primary owner edits an administrator's profile.
	if err := store.SeedWorkspaceRole("T1", "U2", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if refused := callForm(t, handler, "users.profile.set", url.Values{"user": {"U2"}, "name": {"display_name"}, "value": {"robert"}}); refused["error"] != "cannot_update_admin_user" {
		t.Fatalf("admin editing an admin=%v", refused)
	}
	// A member naming someone else is refused with the declared code.
	if err := store.SeedWorkspaceRole("T1", "U2", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	if refused := callForm(t, handler, "users.profile.set", url.Values{"user": {"U2"}, "name": {"display_name"}, "value": {"mallory"}}); refused["error"] != "not_admin" {
		t.Fatalf("member editing another=%v", refused)
	}
	if displayName(t, "U2") != "bobby" {
		t.Fatal("a refused edit changed the profile")
	}
	// Naming oneself is the ordinary edit.
	if own := callForm(t, handler, "users.profile.set", url.Values{"user": {"U1"}, "name": {"display_name"}, "value": {"al"}}); own["ok"] != true || displayName(t, "U1") != "al" {
		t.Fatalf("own edit=%v", own)
	}
}

func TestAdminConversationsSearchHonoursQueryTypesAndSort(t *testing.T) {
	handler, store := testUserHandlerWithStore()
	// A direct message is never a channel result, whatever its name.
	if err := store.SeedConversation(domain.Conversation{ID: "D1", WorkspaceID: "T1", Name: "general", Kind: domain.ConversationTypeIM}); err != nil {
		t.Fatal(err)
	}
	names := func(t *testing.T, values url.Values) (string, string) {
		t.Helper()
		got := callForm(t, handler, "admin.conversations.search", values)
		conversations, ok := got["conversations"].([]any)
		if !ok {
			t.Fatalf("values=%v search=%v", values, got)
		}
		listed := make([]string, 0, len(conversations))
		for _, conversation := range conversations {
			listed = append(listed, conversation.(map[string]any)["name"].(string))
		}
		next, _ := got["response_metadata"].(map[string]any)["next_cursor"].(string)
		return strings.Join(listed, ","), next
	}
	// No query is every channel. The default sort is member_count: archived
	// has one member and general two.
	if got, _ := names(t, url.Values{}); got != "archived,general" {
		t.Fatalf("no query=%s", got)
	}
	cases := []struct {
		values url.Values
		want   string
	}{
		{url.Values{"query": {"gen"}}, "general"},
		{url.Values{"sort": {"name"}, "sort_dir": {"desc"}}, "general,archived"},
		{url.Values{"sort": {"member_count"}, "sort_dir": {"desc"}}, "general,archived"},
		{url.Values{"sort": {"relevant"}, "query": {"general"}}, "general"},
		{url.Values{"search_channel_types": {"exclude_archived"}}, "general"},
		{url.Values{"search_channel_types": {"archived,private_exclude"}}, "archived"},
		{url.Values{"search_channel_types": {"private"}}, ""},
		// The fixture channels carry no creation time, so the id breaks the tie.
		{url.Values{"team_ids": {"T1"}, "sort": {"created"}}, "general,archived"},
	}
	for _, test := range cases {
		if got, _ := names(t, test.values); got != test.want {
			t.Fatalf("values=%v got %q want %q", test.values, got, test.want)
		}
	}
	// A page's cursor reaches the next page under the same sort.
	first, next := names(t, url.Values{"sort": {"name"}, "limit": {"1"}})
	if first != "archived" || next == "" {
		t.Fatalf("first page=%q next=%q", first, next)
	}
	if second, after := names(t, url.Values{"sort": {"name"}, "limit": {"1"}, "cursor": {next}}); second != "general" || after != "" {
		t.Fatalf("second page=%q next=%q", second, after)
	}
	refusals := map[string]url.Values{
		"invalid_sort":                {"sort": {"popularity"}},
		"invalid_sort_dir":            {"sort_dir": {"sideways"}},
		"invalid_search_channel_type": {"search_channel_types": {"private,private_exclude"}},
		"invalid_cursor":              {"sort": {"created"}, "cursor": {next}},
		"team_not_found":              {"team_ids": {"T-elsewhere"}},
	}
	for code, values := range refusals {
		if got := callForm(t, handler, "admin.conversations.search", values); got["error"] != code {
			t.Fatalf("values=%v got %v want %s", values, got, code)
		}
	}
	if got := callForm(t, handler, "admin.conversations.search", url.Values{"search_channel_types": {"multi_workspace"}}); got["error"] != "invalid_search_channel_type" {
		t.Fatalf("an unmodelled channel type was not refused: %v", got)
	}
}

func TestAdminConversationsCreateHonoursDescriptionAndWorkspace(t *testing.T) {
	handler := testUserHandler()
	created := callForm(t, handler, "admin.conversations.create", url.Values{"name": {"Launch Room"}, "is_private": {"false"}, "team_id": {"T1"}, "description": {"Where the launch is run"}})
	id, _ := created["channel_id"].(string)
	if created["ok"] != true || id == "" {
		t.Fatalf("create=%v", created)
	}
	channel := callForm(t, handler, "conversations.info", url.Values{"channel": {id}})["channel"].(map[string]any)
	if channel["name"] != "launch-room" || channel["purpose"].(map[string]any)["value"] != "Where the launch is run" {
		t.Fatalf("the channel does not carry its description: %v", channel)
	}
	if orgWide := callForm(t, handler, "admin.conversations.create", url.Values{"name": {"everyone-here"}, "is_private": {"false"}, "org_wide": {"true"}}); orgWide["ok"] != true {
		t.Fatalf("org_wide without team_id=%v", orgWide)
	}
	refusals := map[string]url.Values{
		"team_id_or_org_required": {"name": {"nowhere"}, "is_private": {"false"}},
		"team_not_found":          {"name": {"elsewhere"}, "is_private": {"false"}, "team_id": {"T-elsewhere"}},
		"invalid_arg_name":        {"name": {"too-long"}, "is_private": {"false"}, "team_id": {"T1"}, "description": {strings.Repeat("x", domain.MaxConversationTextLength+1)}},
	}
	for code, values := range refusals {
		if got := callForm(t, handler, "admin.conversations.create", values); got["error"] != code {
			t.Fatalf("values=%v got %v want %s", values, got, code)
		}
	}
	for _, channel := range callForm(t, handler, "conversations.list", url.Values{"limit": {"100"}})["channels"].([]any) {
		if channel.(map[string]any)["name"] == "too-long" {
			t.Fatal("a refused description left a channel behind")
		}
	}
}

func TestAdminConversationsRenameNormalizesTheName(t *testing.T) {
	handler := testUserHandler()
	if renamed := callForm(t, handler, "admin.conversations.rename", url.Values{"channel_id": {"C1"}, "name": {"Q3  Plans"}}); renamed["ok"] != true {
		t.Fatalf("rename=%v", renamed)
	}
	if name := callForm(t, handler, "conversations.info", url.Values{"channel": {"C1"}})["channel"].(map[string]any)["name"]; name != "q3-plans" {
		t.Fatalf("an administrator stored the channel name %q", name)
	}
}

func TestAdminWorkflowsSearchHonoursItsFilters(t *testing.T) {
	handler, repository := testUserHandlerWithStore()
	messages := service.Messages{Store: repository}
	code, err := messages.CreateWorkflow(context.Background(), "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", CallbackID: "nightly", Title: "Nightly triage", InputSchema: `{}`,
		Steps: `[{"function_id":"triage","title":"Triage"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	builder, err := messages.CreateWorkflow(context.Background(), "T1", "U1", domain.WorkflowDefinition{Title: "Welcome wagon"})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(t *testing.T, values url.Values) string {
		t.Helper()
		got := callForm(t, handler, "admin.workflows.search", values)
		workflows, ok := got["workflows"].([]any)
		if !ok {
			t.Fatalf("values=%v search=%v", values, got)
		}
		listed := make([]string, 0, len(workflows))
		for _, workflow := range workflows {
			listed = append(listed, workflow.(map[string]any)["id"].(string))
		}
		return strings.Join(listed, ",")
	}
	cases := []struct {
		values url.Values
		want   string
	}{
		{url.Values{"source": {"code"}}, string(code.ID)},
		{url.Values{"source": {"workflow_builder"}}, string(builder.ID)},
		{url.Values{"app_id": {"A1"}}, string(code.ID)},
		{url.Values{"app_id": {"A-elsewhere"}}, ""},
		{url.Values{"collaborator_ids": {"U-nobody"}}, ""},
		{url.Values{"sort": {"premium_runs"}, "sort_dir": {"desc"}, "query": {"welcome"}}, string(builder.ID)},
	}
	for _, test := range cases {
		if got := ids(t, test.values); got != test.want {
			t.Fatalf("values=%v got %q want %q", test.values, got, test.want)
		}
	}
	for _, values := range []url.Values{
		{"source": {"spreadsheet"}},
		{"sort": {"popularity"}},
		{"sort_dir": {"sideways"}},
		{"no_collaborators": {"true"}, "collaborator_ids": {"U1"}},
	} {
		if refused := callForm(t, handler, "admin.workflows.search", values); refused["error"] != "invalid_arg_name" {
			t.Fatalf("values=%v refused=%v", values, refused)
		}
	}
}

func TestTeamAccessLogsPagesByLimitAndCursor(t *testing.T) {
	handler, _ := testUserHandlerWithStore()
	get := func(path, agent string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("User-Agent", agent)
		request.RemoteAddr = "192.0.2.20:40000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: %s", path, response.Body)
		}
		return payload
	}
	// One row per member, address and agent: three agents are three rows.
	for index := range 3 {
		get("/api/users.info?user=U1", "agent-"+strconv.Itoa(index))
	}
	// limit alone used to be ignored: the caller got 100 rows and no cursor,
	// so it never learned there were more.
	first := get("/api/team.accessLogs?limit=2", "agent-0")
	cursor, _ := first["response_metadata"].(map[string]any)["next_cursor"].(string)
	if logins, ok := first["logins"].([]any); !ok || len(logins) != 2 || cursor == "" {
		t.Fatalf("first page=%v", first)
	}
	second := get("/api/team.accessLogs?cursor="+url.QueryEscape(cursor), "agent-0")
	paging, _ := second["paging"].(map[string]any)
	if logins, ok := second["logins"].([]any); !ok || len(logins) == 0 || paging["page"] != float64(2) || paging["count"] != float64(2) {
		t.Fatalf("the cursor did not name page two of two-row pages: %v", second)
	}
	if refused := get("/api/team.accessLogs?cursor=not-a-cursor", "reader"); refused["error"] != "invalid_arg_name" {
		t.Fatalf("a malformed cursor=%v", refused)
	}
}
