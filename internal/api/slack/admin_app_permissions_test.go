package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// appAccessManifest declares two MCP servers, which is what the
// admin.apps.mcp.servers.* methods describe.
const appAccessManifest = `{"display_information":{"name":"Agent tools"},"features":{"mcp_servers":[{"name":"search","url":"https://mcp.example.test/search"},{"name":"tickets","url":"https://mcp.example.test/tickets"}]}}`

// appAccessFixture is the shared fixture with U1's user token, plus two more
// members (U3, U4), a user group holding U3, and an approved app AM that
// declares two MCP servers.
func appAccessFixture(t *testing.T) (http.Handler, *memory.Store) {
	t.Helper()
	handler, repository := testUserHandlerWithStore()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, user := range []domain.User{{ID: "U3", WorkspaceID: "T1", Name: "carol"}, {ID: "U4", WorkspaceID: "T1", Name: "dave"}} {
		if err := repository.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.CreateUserGroup(ctx, domain.UserGroup{
		WorkspaceID: "T1", ID: "S1", Name: "Support", Handle: "support", Creator: "U1", UpdatedBy: "U1",
		CreatedAt: now, UpdatedAt: now, Enabled: true, Users: []domain.UserID{"U3"},
	}, events.Event{ID: "EvS1", WorkspaceID: "T1", Topic: "subteam.created", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: "AM", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Agent tools", ClientID: "agent-tools-client",
		SigningSecretHash: "hash", SigningSecretCiphertext: "cipher", VerificationTokenHash: "hash", VerificationTokenCiphertext: "cipher",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "AM", Version: 1, Manifest: appAccessManifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "agent-tools-client", SecretHash: "secret", AppID: "AM"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetAppApproval(ctx, "T1", "AM", "RM", domain.AppApprovalApproved, now, events.Event{ID: "EvAM", WorkspaceID: "T1", Topic: "app.approved", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "AM", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return handler, repository
}

func callAppAccess(t *testing.T, handler http.Handler, method string, form url.Values) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return payload
}

func expectAppAccessError(t *testing.T, handler http.Handler, method string, form url.Values, code string) {
	t.Helper()
	if got := callAppAccess(t, handler, method, form); got["ok"] != false || got["error"] != code {
		t.Fatalf("%s %v = %v, want error %q", method, form, got, code)
	}
}

func manyIDs(prefix string, count int) string {
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		ids = append(ids, prefix+strings.Repeat("X", 1)+string(rune('A'+index%26))+strings.Repeat("0", index/26+1))
	}
	return strings.Join(ids, ",")
}

// TestAdminAppsPermissionsFollowTheSlackReference drives admin.apps.permissions.*
// through every behaviour and error its reference pages document.
func TestAdminAppsPermissionsFollowTheSlackReference(t *testing.T) {
	handler, repository := appAccessFixture(t)
	form := func(pairs ...string) url.Values {
		values := url.Values{}
		for index := 0; index+1 < len(pairs); index += 2 {
			values.Set(pairs[index], pairs[index+1])
		}
		return values
	}

	// An app nobody has restricted answers everyone and no channel restriction.
	listed := callAppAccess(t, handler, "admin.apps.permissions.list", form("app_id", "A1"))
	if !reflect.DeepEqual(listed, map[string]any{"ok": true, "permission_type": "everyone"}) {
		t.Fatalf("default list=%v", listed)
	}
	expectAppAccessError(t, handler, "admin.apps.permissions.list", form(), "invalid_arguments")
	expectAppAccessError(t, handler, "admin.apps.permissions.list", form("app_id", "A-none"), "app_not_found")
	// add and remove act on an existing list.
	expectAppAccessError(t, handler, "admin.apps.permissions.add", form("app_id", "A1", "user_ids", "U3"), "app_acl_not_found")
	expectAppAccessError(t, handler, "admin.apps.permissions.remove", form("app_id", "A1", "user_ids", "U3"), "app_acl_not_found")

	// set: every documented refusal.
	for _, refusal := range []struct {
		form url.Values
		code string
	}{
		{form("app_id", "A1"), "invalid_arguments"},
		{form("permission_type", "everyone"), "invalid_arguments"},
		{form("app_id", "A1", "permission_type", "whoever"), "invalid_permission_type"},
		{form("app_id", "A1", "permission_type", "everyone", "user_ids", "U3"), "invalid_permission_type"},
		{form("app_id", "A1", "permission_type", "named_entities"), "named_entities_cannot_be_empty"},
		{form("app_id", "A1", "permission_type", "named_entities", "user_ids", manyIDs("U", 201)), "too_many_named_entities"},
		{form("app_id", "A1", "permission_type", "everyone", "channel_restriction_mode", "nowhere"), "invalid_channel_restriction_mode"},
		{form("app_id", "A1", "permission_type", "everyone", "channel_ids", "C1"), "invalid_channel_restriction_mode"},
		{form("app_id", "A1", "permission_type", "everyone", "channel_restriction_mode", "specific_channels"), "channel_ids_required"},
		{form("app_id", "A1", "permission_type", "everyone", "channel_restriction_mode", "all_channels_except"), "channel_ids_required"},
		{form("app_id", "A1", "permission_type", "no_one", "channel_restriction_mode", "all_channels"), "channel_restriction_requires_app_access"},
		{form("app_id", "A1", "permission_type", "named_entities", "user_ids", "U-ghost"), "no_valid_named_entities"},
		{form("app_id", "A1", "permission_type", "named_entities", "user_ids", "U3,U-ghost"), "user_not_found"},
		{form("app_id", "A1", "permission_type", "named_entities", "usergroup_ids", "S1,S-ghost"), "usergroup_not_found"},
		{form("app_id", "A1", "permission_type", "everyone", "channel_restriction_mode", "specific_channels", "channel_ids", "C1,C-ghost"), "channel_not_found"},
		{form("app_id", "A-none", "permission_type", "everyone"), "app_not_found"},
	} {
		expectAppAccessError(t, handler, "admin.apps.permissions.set", refusal.form, refusal.code)
	}

	// The documented set response: the mode and the channels it applies to.
	set := callAppAccess(t, handler, "admin.apps.permissions.set", form("app_id", "A1", "permission_type", "everyone", "channel_restriction_mode", "specific_channels", "channel_ids", `["C1","C2"]`))
	if !reflect.DeepEqual(set, map[string]any{"ok": true, "permission_type": "everyone", "channel_restriction_mode": "specific_channels", "channel_ids": []any{"C1", "C2"}}) {
		t.Fatalf("set=%v", set)
	}
	// The documented remove response reflects the updated channel list.
	removed := callAppAccess(t, handler, "admin.apps.permissions.remove", form("app_id", "A1", "channel_ids", "C2"))
	if !reflect.DeepEqual(removed, map[string]any{"ok": true, "permission_type": "everyone", "channel_restriction_mode": "specific_channels", "channel_ids": []any{"C1"}}) {
		t.Fatalf("remove=%v", removed)
	}
	// Users belong to a named_entities list.
	expectAppAccessError(t, handler, "admin.apps.permissions.add", form("app_id", "A1", "user_ids", "U3"), "invalid_permission_type")
	expectAppAccessError(t, handler, "admin.apps.permissions.remove", form("app_id", "A1", "user_ids", "U3"), "invalid_permission_type")

	// Leaving channel_restriction_mode out keeps the restriction the app has.
	named := callAppAccess(t, handler, "admin.apps.permissions.set", form("app_id", "A1", "permission_type", "named_entities", "user_ids", "U3"))
	if !reflect.DeepEqual(named, map[string]any{
		"ok": true, "permission_type": "named_entities", "user_ids": []any{"U3"}, "usergroup_ids": []any{},
		"channel_restriction_mode": "specific_channels", "channel_ids": []any{"C1"},
	}) {
		t.Fatalf("named set=%v", named)
	}
	added := callAppAccess(t, handler, "admin.apps.permissions.add", form("app_id", "A1", "usergroup_ids", "S1", "channel_ids", "C2"))
	if !reflect.DeepEqual(added["usergroup_ids"], []any{"S1"}) || !reflect.DeepEqual(added["user_ids"], []any{"U3"}) || !reflect.DeepEqual(added["channel_ids"], []any{"C1", "C2"}) {
		t.Fatalf("add=%v", added)
	}
	for _, refusal := range []struct {
		method string
		form   url.Values
		code   string
	}{
		{"admin.apps.permissions.add", form("app_id", "A1"), "invalid_arguments"},
		{"admin.apps.permissions.add", form("user_ids", "U3"), "invalid_arguments"},
		{"admin.apps.permissions.add", form("app_id", "A-none", "user_ids", "U3"), "app_not_found"},
		{"admin.apps.permissions.add", form("app_id", "A1", "user_ids", manyIDs("U", 51)), "too_many_named_entities"},
		{"admin.apps.permissions.add", form("app_id", "A1", "user_ids", "U-ghost"), "no_valid_named_entities"},
		{"admin.apps.permissions.add", form("app_id", "A1", "user_ids", "U4,U-ghost"), "user_not_found"},
		{"admin.apps.permissions.add", form("app_id", "A1", "usergroup_ids", "S1,S-ghost"), "invalid_entities"},
		{"admin.apps.permissions.add", form("app_id", "A1", "channel_ids", "C-ghost"), "channel_not_found"},
		{"admin.apps.permissions.remove", form("app_id", "A1"), "invalid_arguments"},
		{"admin.apps.permissions.remove", form("app_id", "A1", "user_ids", manyIDs("U", 51)), "too_many_named_entities"},
		{"admin.apps.permissions.remove", form("app_id", "A1", "user_ids", "U-ghost"), "user_not_found"},
		{"admin.apps.permissions.remove", form("app_id", "A1", "channel_ids", "C-ghost"), "channel_not_found"},
	} {
		expectAppAccessError(t, handler, refusal.method, refusal.form, refusal.code)
	}
	if kept := callAppAccess(t, handler, "admin.apps.permissions.remove", form("app_id", "A1", "user_ids", "U3")); !reflect.DeepEqual(kept["user_ids"], []any{}) || !reflect.DeepEqual(kept["usergroup_ids"], []any{"S1"}) {
		t.Fatalf("remove user=%v", kept)
	}
	// Removing the last named entity would leave a list that names nobody.
	expectAppAccessError(t, handler, "admin.apps.permissions.remove", form("app_id", "A1", "usergroup_ids", "S1"), "named_entities_cannot_be_empty")
	listed = callAppAccess(t, handler, "admin.apps.permissions.list", form("app_id", "A1"))
	if !reflect.DeepEqual(listed, map[string]any{
		"ok": true, "permission_type": "named_entities", "user_ids": []any{}, "usergroup_ids": []any{"S1"},
		"channel_restriction_mode": "specific_channels", "channel_ids": []any{"C1", "C2"},
	}) {
		t.Fatalf("list=%v", listed)
	}

	// no_one clears the channel restriction and reports it as no_one.
	nobody := callAppAccess(t, handler, "admin.apps.permissions.set", form("app_id", "A1", "permission_type", "no_one"))
	if !reflect.DeepEqual(nobody, map[string]any{"ok": true, "permission_type": "no_one", "channel_restriction_mode": "no_one"}) {
		t.Fatalf("no_one=%v", nobody)
	}
	expectAppAccessError(t, handler, "admin.apps.permissions.add", form("app_id", "A1", "channel_ids", "C1"), "invalid_channel_restriction_mode")
	expectAppAccessError(t, handler, "admin.apps.permissions.remove", form("app_id", "A1", "channel_ids", "C1"), "invalid_channel_restriction_mode")
	// all_channels is a configured restriction with no list.
	everywhere := callAppAccess(t, handler, "admin.apps.permissions.set", form("app_id", "A1", "permission_type", "everyone", "channel_restriction_mode", "all_channels"))
	if !reflect.DeepEqual(everywhere, map[string]any{"ok": true, "permission_type": "everyone", "channel_restriction_mode": "all_channels"}) {
		t.Fatalf("all_channels=%v", everywhere)
	}
	expectAppAccessError(t, handler, "admin.apps.permissions.add", form("app_id", "A1", "channel_ids", "C1"), "invalid_channel_restriction_mode")

	// The methods are an administrator's.
	if err := repository.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	expectAppAccessError(t, handler, "admin.apps.permissions.list", form("app_id", "A1"), "no_permission")
	expectAppAccessError(t, handler, "admin.apps.permissions.set", form("app_id", "A1", "permission_type", "everyone"), "no_permission")
}

// TestAdminAppAccessMethodsRefuseABotToken: every admin.apps access method
// documents a user token only and declares not_allowed_token_type.
func TestAdminAppAccessMethodsRefuseABotToken(t *testing.T) {
	handler, _ := testHandlerWithStore()
	for _, method := range []string{
		"admin.apps.permissions.add", "admin.apps.permissions.list", "admin.apps.permissions.remove", "admin.apps.permissions.set",
		"admin.apps.mcp.servers.list", "admin.apps.mcp.servers.permissions.list", "admin.apps.mcp.servers.permissions.set",
	} {
		expectAppAccessError(t, handler, method, url.Values{"app_id": {"A1"}}, "not_allowed_token_type")
	}
}

// TestAdminAppsMCPServersFollowTheSlackReference drives admin.apps.mcp.servers.*:
// the allowlist an approval puts an app's servers on, its pagination, and the
// server rules that may only narrow the app-level list.
func TestAdminAppsMCPServersFollowTheSlackReference(t *testing.T) {
	handler, repository := appAccessFixture(t)
	search, tickets := string(domain.NewMCPServerID("AM", "search")), string(domain.NewMCPServerID("AM", "tickets"))
	ordered := []string{search, tickets}
	if ordered[0] > ordered[1] {
		ordered[0], ordered[1] = ordered[1], ordered[0]
	}

	listed := callAppAccess(t, handler, "admin.apps.mcp.servers.list", url.Values{})
	servers, _ := listed["mcp_servers"].([]any)
	if listed["ok"] != true || len(servers) != 2 {
		t.Fatalf("list=%v", listed)
	}
	first := servers[0].(map[string]any)
	if first["id"] != ordered[0] || first["app_id"] != "AM" || !strings.HasPrefix(first["id"].(string), "Amcp") || first["url"] == "" || first["name"] == "" {
		t.Fatalf("first server=%v", first)
	}
	if metadata := listed["response_metadata"].(map[string]any); metadata["next_cursor"] != "" {
		t.Fatalf("single page metadata=%v", metadata)
	}
	// One server a page, resuming inside the app's server list.
	pageOne := callAppAccess(t, handler, "admin.apps.mcp.servers.list", url.Values{"limit": {"1"}})
	cursor := pageOne["response_metadata"].(map[string]any)["next_cursor"].(string)
	if got := pageOne["mcp_servers"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != ordered[0] || cursor == "" {
		t.Fatalf("page one=%v", pageOne)
	}
	pageTwo := callAppAccess(t, handler, "admin.apps.mcp.servers.list", url.Values{"limit": {"1"}, "cursor": {cursor}})
	if got := pageTwo["mcp_servers"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != ordered[1] || pageTwo["response_metadata"].(map[string]any)["next_cursor"] != "" {
		t.Fatalf("page two=%v", pageTwo)
	}
	expectAppAccessError(t, handler, "admin.apps.mcp.servers.list", url.Values{"cursor": {"not-a-cursor"}}, "invalid_cursor")

	// Every server an app declares, each answering everyone until restricted.
	permissions := callAppAccess(t, handler, "admin.apps.mcp.servers.permissions.list", url.Values{"app_id": {"AM"}})
	entries, _ := permissions["mcp_servers"].([]any)
	if permissions["app_id"] != "AM" || len(entries) != 2 || entries[0].(map[string]any)["permission_type"] != "everyone" {
		t.Fatalf("permissions list=%v", permissions)
	}
	expectAppAccessError(t, handler, "admin.apps.mcp.servers.permissions.list", url.Values{}, "invalid_arguments")
	expectAppAccessError(t, handler, "admin.apps.mcp.servers.permissions.list", url.Values{"app_id": {"A-none"}}, "app_not_found")

	setServer := func(values url.Values) map[string]any {
		values.Set("app_id", "AM")
		if values.Get("server_id") == "" {
			values.Set("server_id", search)
		}
		return callAppAccess(t, handler, "admin.apps.mcp.servers.permissions.set", values)
	}
	for _, refusal := range []struct {
		form url.Values
		code string
	}{
		{url.Values{"permission_type": {"whoever"}}, "invalid_permission_type"},
		{url.Values{"permission_type": {"everyone"}, "server_id": {"Amcp-none"}}, "server_not_found"},
		{url.Values{"permission_type": {"named_entities"}}, "named_entities_cannot_be_empty"},
		{url.Values{"permission_type": {"named_entities_exclude"}}, "named_entities_cannot_be_empty"},
		{url.Values{"permission_type": {"named_entities"}, "user_ids": {manyIDs("U", 201)}}, "too_many_named_entities"},
		{url.Values{"permission_type": {"named_entities"}, "usergroup_ids": {manyIDs("S", 201)}}, "too_many_named_entities"},
		{url.Values{"permission_type": {"named_entities"}, "user_ids": {"U-ghost"}}, "no_valid_named_entities"},
		{url.Values{"permission_type": {"named_entities"}, "user_ids": {"U3,U-ghost"}}, "user_not_found"},
		{url.Values{"permission_type": {"named_entities"}, "user_ids": {"U3"}, "usergroup_ids": {"S-ghost"}}, "usergroup_not_found"},
	} {
		if got := setServer(refusal.form); got["error"] != refusal.code {
			t.Fatalf("set %v = %v, want %s", refusal.form, got, refusal.code)
		}
	}
	expectAppAccessError(t, handler, "admin.apps.mcp.servers.permissions.set", url.Values{"app_id": {"AM"}, "permission_type": {"everyone"}}, "invalid_arguments")
	expectAppAccessError(t, handler, "admin.apps.mcp.servers.permissions.set", url.Values{"app_id": {"A-none"}, "server_id": {search}, "permission_type": {"everyone"}}, "app_not_found")
	if got := setServer(url.Values{"permission_type": {"named_entities_exclude"}, "user_ids": {"U4"}}); !reflect.DeepEqual(got, map[string]any{"ok": true}) {
		t.Fatalf("set exclude=%v", got)
	}
	permissions = callAppAccess(t, handler, "admin.apps.mcp.servers.permissions.list", url.Values{"app_id": {"AM"}})
	for _, entry := range permissions["mcp_servers"].([]any) {
		server := entry.(map[string]any)
		if server["id"] == search && (server["permission_type"] != "named_entities_exclude" || !reflect.DeepEqual(server["user_ids"], []any{"U4"})) {
			t.Fatalf("restricted server=%v", server)
		}
		if server["id"] == tickets && (server["permission_type"] != "everyone" || server["user_ids"] != nil) {
			t.Fatalf("unrestricted server=%v", server)
		}
	}

	// A server may only narrow the app. Under a named list of S1 (holding U3),
	// everyone and named_entities_exclude are broader, U3 is in scope by the
	// group, and U4 is not.
	if got := callAppAccess(t, handler, "admin.apps.permissions.set", url.Values{"app_id": {"AM"}, "permission_type": {"named_entities"}, "usergroup_ids": {"S1"}}); got["ok"] != true {
		t.Fatalf("app set=%v", got)
	}
	for _, refusal := range []struct {
		form url.Values
		code string
	}{
		{url.Values{"permission_type": {"everyone"}}, "server_acl_type_broader_than_app"},
		{url.Values{"permission_type": {"named_entities_exclude"}, "user_ids": {"U4"}}, "server_acl_type_broader_than_app"},
		{url.Values{"permission_type": {"named_entities"}, "user_ids": {"U4"}}, "server_acl_entities_not_in_scope"},
	} {
		if got := setServer(refusal.form); got["error"] != refusal.code {
			t.Fatalf("set %v = %v, want %s", refusal.form, got, refusal.code)
		}
	}
	for _, accepted := range []url.Values{
		{"permission_type": {"named_entities"}, "user_ids": {"U3"}, "usergroup_ids": {"S1"}},
		{"permission_type": {"no_one"}},
	} {
		if got := setServer(accepted); got["ok"] != true {
			t.Fatalf("set %v = %v", accepted, got)
		}
	}
	if got := callAppAccess(t, handler, "admin.apps.permissions.set", url.Values{"app_id": {"AM"}, "permission_type": {"no_one"}}); got["ok"] != true {
		t.Fatalf("app no_one=%v", got)
	}
	if got := setServer(url.Values{"permission_type": {"named_entities"}, "user_ids": {"U3"}}); got["error"] != "server_acl_type_broader_than_app" {
		t.Fatalf("set under no_one=%v", got)
	}

	// The allowlist reflects approval, not installation: an uninstalled app's
	// servers are still listed.
	if got := callAppAccess(t, handler, "admin.apps.uninstall", url.Values{"app_ids": {"AM"}}); got["ok"] != true {
		t.Fatalf("uninstall=%v", got)
	}
	if got := callAppAccess(t, handler, "admin.apps.mcp.servers.list", url.Values{}); len(got["mcp_servers"].([]any)) != 2 {
		t.Fatalf("uninstalled app's servers left the allowlist: %v", got)
	}
	// The allowlist is the approval: a restricted app's servers leave it.
	if got := callAppAccess(t, handler, "admin.apps.restrict", url.Values{"app_id": {"AM"}}); got["ok"] != true {
		t.Fatalf("restrict=%v", got)
	}
	if got := callAppAccess(t, handler, "admin.apps.mcp.servers.list", url.Values{}); len(got["mcp_servers"].([]any)) != 0 {
		t.Fatalf("restricted app still listed: %v", got)
	}
	if err := repository.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	expectAppAccessError(t, handler, "admin.apps.mcp.servers.list", url.Values{}, "no_permission")
}

// TestAppsManagedPermissionsSetFollowsTheSlackReference: the method takes an
// app configuration token and validates the request in full; no app here is
// managed by a manager app, so a valid request is answered app_not_managed.
func TestAppsManagedPermissionsSetFollowsTheSlackReference(t *testing.T) {
	handler, repository := testUserHandlerWithStore()
	configuration, err := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}.IssueAppConfigurationToken(context.Background(), "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string, values url.Values) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/apps.managed.permissions.set", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	for _, testCase := range []struct {
		token  string
		values url.Values
		code   string
	}{
		{"", url.Values{"app_id": {"A1"}, "permissions": {"everyone"}}, "not_authed"},
		{"xoxe.xoxp-not-issued", url.Values{"app_id": {"A1"}, "permissions": {"everyone"}}, "invalid_auth"},
		{configuration.Token, url.Values{"permissions": {"everyone"}}, "invalid_arguments"},
		{configuration.Token, url.Values{"app_id": {"A1"}}, "invalid_arguments"},
		{configuration.Token, url.Values{"app_id": {"A1"}, "permissions": {"named_entities"}}, "invalid_arguments"},
		{configuration.Token, url.Values{"app_id": {"A-none"}, "permissions": {"everyone"}}, "app_not_found"},
		{configuration.Token, url.Values{"app_id": {"A1"}, "permissions": {"everyone"}}, "app_not_managed"},
		{configuration.Token, url.Values{"app_id": {"A1"}, "permissions": {"app_owner"}}, "app_not_managed"},
	} {
		if got := call(testCase.token, testCase.values); got["ok"] != false || got["error"] != testCase.code {
			t.Fatalf("token=%q values=%v got=%v, want %s", testCase.token, testCase.values, got, testCase.code)
		}
	}
	// The token travels in the body as well as the header, as apps.manifest.* allows.
	if got := call("", url.Values{"token": {configuration.Token}, "app_id": {"A1"}, "permissions": {"everyone"}}); got["error"] != "app_not_managed" {
		t.Fatalf("body token=%v", got)
	}
}
