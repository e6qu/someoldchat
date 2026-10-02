package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// orgUserGroupHandler is the user-token fixture, whose U1 is an administrator,
// with a multi-channel guest UG.
func orgUserGroupHandler(t *testing.T) (http.Handler, *memory.Store) {
	t.Helper()
	handler, repository := testUserHandlerWithStore()
	guest := domain.User{ID: "UG", WorkspaceID: "T1", Name: "guest", Email: "guest@example.com"}
	if err := repository.CreateUser(context.Background(), guest, domain.WorkspaceMembership{
		WorkspaceID: "T1", UserID: "UG", Role: domain.WorkspaceRoleMember, Active: true, Restricted: true,
	}, events.Event{ID: "E-org-guest", WorkspaceID: "T1", Topic: "user.created", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return handler, repository
}

func orgUserGroupCall(t *testing.T, handler http.Handler, method string, form url.Values) map[string]any {
	t.Helper()
	response := postForm(handler, "/api/"+method, form.Encode())
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return body
}

func createOrgUserGroup(t *testing.T, handler http.Handler, form url.Values) string {
	t.Helper()
	created := orgUserGroupCall(t, handler, "admin.usergroups.create", form)
	subteam, _ := created["subteam"].(map[string]any)
	id, _ := subteam["id"].(string)
	if created["ok"] != true || id == "" {
		t.Fatalf("create %v", created)
	}
	return id
}

func TestAdminUserGroupsCreateFetchAndUpdateReturnTheSubteam(t *testing.T) {
	handler, _ := orgUserGroupHandler(t)
	created := orgUserGroupCall(t, handler, "admin.usergroups.create", url.Values{"name": {"Marketing Team"}, "purpose": {"Marketing gurus"}, "is_visible": {"false"}})
	subteam, _ := created["subteam"].(map[string]any)
	want := map[string]any{
		"team_id": "T1", "enterprise_id": "T1", "is_subteam": true, "is_usergroup": true, "name": "Marketing Team",
		"description": "Marketing gurus", "handle": "marketing-team", "is_external": false, "date_delete": float64(0),
		"auto_type": nil, "auto_provision": false, "enterprise_subteam_id": "", "created_by": "U1", "updated_by": "U1",
		"deleted_by": nil, "is_section": false, "is_editing_restricted": true, "is_membership_locked": true,
		"is_idp_group": false, "is_visible": false, "is_org_level": true, "user_count": float64(0), "channel_count": float64(0),
	}
	for key, value := range want {
		if got, present := subteam[key]; !present || !reflect.DeepEqual(got, value) {
			t.Errorf("create subteam[%s] = %#v (present %v), want %#v", key, got, present, value)
		}
	}
	for _, key := range []string{"id", "date_create", "date_update"} {
		if _, present := subteam[key]; !present {
			t.Errorf("create subteam lacks %s", key)
		}
	}
	if _, present := subteam["teams"]; present {
		t.Errorf("create subteam carries teams, which its reference does not: %v", subteam)
	}
	if _, present := subteam["prefs"]; present {
		t.Errorf("create subteam carries prefs, which its reference does not: %v", subteam)
	}
	id, _ := subteam["id"].(string)

	duplicate := orgUserGroupCall(t, handler, "admin.usergroups.create", url.Values{"name": {"marketing team"}})
	if duplicate["error"] != "name_already_exists" {
		t.Fatalf("duplicate name %v", duplicate)
	}
	duplicateHandle := orgUserGroupCall(t, handler, "admin.usergroups.create", url.Values{"name": {"Other"}, "handle": {"marketing-team"}})
	if duplicateHandle["error"] != "handle_already_exists" {
		t.Fatalf("duplicate handle %v", duplicateHandle)
	}
	for _, form := range []url.Values{{}, {"name": {" "}}, {"name": {"Bad"}, "is_visible": {"maybe"}}} {
		if refused := orgUserGroupCall(t, handler, "admin.usergroups.create", form); refused["error"] != "invalid_arguments" {
			t.Fatalf("create %v: %v", form, refused)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/admin.usergroups.fetch?id="+id, nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var fetched struct {
		OK      bool           `json:"ok"`
		Subteam map[string]any `json:"subteam"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &fetched); err != nil || !fetched.OK || fetched.Subteam["id"] != id ||
		fetched.Subteam["is_org_level"] != true || !reflect.DeepEqual(fetched.Subteam["teams"], []any{}) {
		t.Fatalf("GET fetch %s err=%v", response.Body, err)
	}
	if missing := orgUserGroupCall(t, handler, "admin.usergroups.fetch", url.Values{"id": {"S-nobody"}}); missing["error"] != "invalid_usergroup" {
		t.Fatalf("fetch missing %v", missing)
	}
	if missing := orgUserGroupCall(t, handler, "admin.usergroups.fetch", url.Values{}); missing["error"] != "invalid_arguments" {
		t.Fatalf("fetch without id %v", missing)
	}

	renamed := orgUserGroupCall(t, handler, "admin.usergroups.update", url.Values{"id": {id}, "name": {"Marketing"}})
	renamedSubteam, _ := renamed["subteam"].(map[string]any)
	if renamed["ok"] != true || renamedSubteam["name"] != "Marketing" || renamedSubteam["description"] != "Marketing gurus" ||
		renamedSubteam["handle"] != "marketing-team" || renamedSubteam["is_visible"] != false || renamedSubteam["teams"] == nil {
		t.Fatalf("update name only %v", renamed)
	}
	shown := orgUserGroupCall(t, handler, "admin.usergroups.update", url.Values{"id": {id}, "is_visible": {"true"}, "description": {""}})
	shownSubteam, _ := shown["subteam"].(map[string]any)
	if shown["ok"] != true || shownSubteam["is_visible"] != true || shownSubteam["description"] != "" {
		t.Fatalf("update visibility %v", shown)
	}
	if refused := orgUserGroupCall(t, handler, "admin.usergroups.update", url.Values{"id": {id}, "handle": {""}}); refused["error"] != "visible_group_needs_handle" {
		t.Fatalf("remove a visible handle %v", refused)
	}
	if refused := orgUserGroupCall(t, handler, "admin.usergroups.update", url.Values{"id": {"S-nobody"}, "name": {"x"}}); refused["error"] != "invalid_usergroup" {
		t.Fatalf("update missing %v", refused)
	}
	other := createOrgUserGroup(t, handler, url.Values{"name": {"Sales"}})
	if refused := orgUserGroupCall(t, handler, "admin.usergroups.update", url.Values{"id": {other}, "name": {"Marketing"}}); refused["error"] != "name_already_exists" {
		t.Fatalf("update to a taken name %v", refused)
	}
	if refused := orgUserGroupCall(t, handler, "admin.usergroups.update", url.Values{"id": {other}, "handle": {"marketing-team"}}); refused["error"] != "handle_already_exists" {
		t.Fatalf("update to a taken handle %v", refused)
	}
}

func TestAdminUserGroupsAddRemoveAndUploadUsers(t *testing.T) {
	handler, _ := orgUserGroupHandler(t)
	id := createOrgUserGroup(t, handler, url.Values{"name": {"Org"}})
	members := func() []any {
		fetched := orgUserGroupCall(t, handler, "admin.usergroups.fetch", url.Values{"id": {id}})
		subteam, _ := fetched["subteam"].(map[string]any)
		count, _ := subteam["user_count"].(float64)
		listed := orgUserGroupCall(t, handler, "usergroups.users.list", url.Values{"usergroup": {id}})
		users, _ := listed["users"].([]any)
		if int(count) != len(users) {
			t.Fatalf("user_count %v disagrees with %v", count, users)
		}
		return users
	}

	added := orgUserGroupCall(t, handler, "admin.usergroups.addUsers", url.Values{"id": {id}, "users": {"U2,UG"}})
	if added["ok"] != true || !reflect.DeepEqual(added["invalid_users"], []any{map[string]any{"user_id": "UG", "reason": "guest_user"}}) {
		t.Fatalf("add with a guest %v", added)
	}
	if got := members(); !reflect.DeepEqual(got, []any{"U2"}) {
		t.Fatalf("members %v", got)
	}
	everyone := orgUserGroupCall(t, handler, "admin.usergroups.addUsers", url.Values{"id": {id}, "users": {`["U1","U2"]`}})
	if _, present := everyone["invalid_users"]; everyone["ok"] != true || present {
		t.Fatalf("add with a JSON array %v", everyone)
	}
	for _, testCase := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"id": {id}, "users": {"U2,U-nobody"}}, "user_not_found"},
		{url.Values{"id": {id}, "users": {"UG"}}, "invalid_users"},
		{url.Values{"id": {"S-nobody"}, "users": {"U2"}}, "invalid_usergroup"},
		{url.Values{"id": {id}}, "invalid_arguments"},
		{url.Values{"users": {"U2"}}, "invalid_arguments"},
		{url.Values{"id": {id}, "users": {strings.Repeat("U2,", domain.MaxAdminUserGroupUsers+1)}}, "invalid_arguments"},
	} {
		if refused := orgUserGroupCall(t, handler, "admin.usergroups.addUsers", testCase.form); refused["error"] != testCase.want {
			t.Fatalf("addUsers %v: %v, want %s", testCase.form, refused, testCase.want)
		}
	}
	// A JSON body may carry users as an array.
	request := httptest.NewRequest(http.MethodPost, "/api/admin.usergroups.removeUsers", strings.NewReader(`{"id":"`+id+`","users":["U1"]}`))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if code := errorCode(t, response); code != "" {
		t.Fatalf("removeUsers with a JSON body: %s", response.Body)
	}
	if got := members(); !reflect.DeepEqual(got, []any{"U2"}) {
		t.Fatalf("members after removal %v", got)
	}
	if again := orgUserGroupCall(t, handler, "admin.usergroups.removeUsers", url.Values{"id": {id}, "users": {"U1"}}); again["ok"] != true {
		t.Fatalf("removing a non-member %v", again)
	}
	for _, testCase := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"id": {id}, "users": {"U-nobody"}}, "user_not_found"},
		{url.Values{"id": {"S-nobody"}, "users": {"U2"}}, "invalid_usergroup"},
		{url.Values{"id": {id}}, "invalid_arguments"},
	} {
		if refused := orgUserGroupCall(t, handler, "admin.usergroups.removeUsers", testCase.form); refused["error"] != testCase.want {
			t.Fatalf("removeUsers %v: %v, want %s", testCase.form, refused, testCase.want)
		}
	}

	uploaded := orgUserGroupCall(t, handler, "admin.usergroups.uploadUsers", url.Values{"id": {id}, "file": {"member id,email\n,alice@example.com\nUG,\nU-nobody,\n"}})
	if uploaded["ok"] != true || uploaded["successful_user_count"] != float64(1) || !reflect.DeepEqual(uploaded["invalid_users"], []any{
		map[string]any{"user_id": "UG", "reason": "guest_user"}, map[string]any{"user_id": "U-nobody", "reason": "user_not_found"},
	}) {
		t.Fatalf("upload %v", uploaded)
	}
	if got := members(); !reflect.DeepEqual(got, []any{"U1", "U2"}) {
		t.Fatalf("members after upload %v", got)
	}
	// The file may also arrive as a multipart file part.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("id", id)
	part, _ := writer.CreateFormFile("file", "users.csv")
	_, _ = part.Write([]byte("U2\n"))
	_ = writer.Close()
	request = httptest.NewRequest(http.MethodPost, "/api/admin.usergroups.uploadUsers", &body)
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), `"successful_user_count":1`) || strings.Contains(response.Body.String(), "invalid_users") {
		t.Fatalf("multipart upload %s", response.Body)
	}
	for _, testCase := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"id": {id}, "file": {"U1,a,b"}}, "unable_to_parse_csv"},
		{url.Values{"id": {id}, "file": {"UG\nU-nobody"}}, "no_valid_users"},
		{url.Values{"id": {id}}, "no_valid_users"},
		{url.Values{"id": {"S-nobody"}, "file": {"U1"}}, "invalid_usergroup"},
		{url.Values{"file": {"U1"}}, "invalid_arguments"},
	} {
		if refused := orgUserGroupCall(t, handler, "admin.usergroups.uploadUsers", testCase.form); refused["error"] != testCase.want {
			t.Fatalf("uploadUsers %v: %v, want %s", testCase.form, refused, testCase.want)
		}
	}
}

func TestAdminUserGroupsRemoveTeams(t *testing.T) {
	handler, _ := orgUserGroupHandler(t)
	id := createOrgUserGroup(t, handler, url.Values{"name": {"Org"}})
	teams := func() any {
		fetched := orgUserGroupCall(t, handler, "admin.usergroups.fetch", url.Values{"id": {id}})
		subteam, _ := fetched["subteam"].(map[string]any)
		return subteam["teams"]
	}
	if added := orgUserGroupCall(t, handler, "admin.usergroups.addTeams", url.Values{"usergroup_id": {id}, "team_ids": {"T1"}}); added["ok"] != true {
		t.Fatalf("addTeams %v", added)
	}
	if got := teams(); !reflect.DeepEqual(got, []any{"T1"}) {
		t.Fatalf("teams after addTeams %v", got)
	}
	if removed := orgUserGroupCall(t, handler, "admin.usergroups.removeTeams", url.Values{"usergroup_id": {id}, "team_ids": {"T1"}}); !reflect.DeepEqual(removed, map[string]any{"ok": true}) {
		t.Fatalf("removeTeams %v", removed)
	}
	if got := teams(); !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("teams after removeTeams %v", got)
	}
	foreign := orgUserGroupCall(t, handler, "admin.usergroups.removeTeams", url.Values{"usergroup_id": {id}, "team_ids": {"T1,T2"}})
	if foreign["error"] != "invalid_team_ids" || !reflect.DeepEqual(foreign["errors"], []any{"T2"}) {
		t.Fatalf("foreign team %v", foreign)
	}
	for _, testCase := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"usergroup_id": {id}}, "no_team_ids_given"},
		{url.Values{"usergroup_id": {id}, "team_ids": {""}}, "no_team_ids_given"},
		{url.Values{"usergroup_id": {"S-nobody"}, "team_ids": {"T1"}}, "usergroup_not_found"},
		{url.Values{"team_ids": {"T1"}}, "invalid_arguments"},
		{url.Values{"usergroup_id": {id}, "team_ids": {strings.Repeat("T1,", domain.MaxAdminUserGroupTeams+1)}}, "invalid_arguments"},
	} {
		if refused := orgUserGroupCall(t, handler, "admin.usergroups.removeTeams", testCase.form); refused["error"] != testCase.want {
			t.Fatalf("removeTeams %v: %v, want %s", testCase.form, refused, testCase.want)
		}
	}
}

// Every organization method serves user tokens only, and refuses a member who
// holds the scopes with restricted_action, the code each reference declares
// for a caller who may not act on the group.
func TestAdminUserGroupsRefuseBotTokensAndMembers(t *testing.T) {
	calls := []struct {
		method string
		form   url.Values
	}{
		{"admin.usergroups.create", url.Values{"name": {"Seized"}}},
		{"admin.usergroups.fetch", url.Values{"id": {"GROUP"}}},
		{"admin.usergroups.update", url.Values{"id": {"GROUP"}, "name": {"Seized"}}},
		{"admin.usergroups.addUsers", url.Values{"id": {"GROUP"}, "users": {"U2"}}},
		{"admin.usergroups.removeUsers", url.Values{"id": {"GROUP"}, "users": {"U2"}}},
		{"admin.usergroups.uploadUsers", url.Values{"id": {"GROUP"}, "file": {"U2"}}},
		{"admin.usergroups.removeTeams", url.Values{"usergroup_id": {"GROUP"}, "team_ids": {"T1"}}},
	}
	bot, _ := testHandlerWithStore()
	for _, call := range calls {
		if code := errorCode(t, postForm(bot, "/api/"+call.method, call.form.Encode())); code != "not_allowed_token_type" {
			t.Errorf("%s with a bot token: %q", call.method, code)
		}
	}
	handler, repository := orgUserGroupHandler(t)
	id := createOrgUserGroup(t, handler, url.Values{"name": {"Org"}})
	if err := repository.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleMember); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if values := call.form["id"]; len(values) > 0 {
			call.form.Set("id", id)
		}
		if values := call.form["usergroup_id"]; len(values) > 0 {
			call.form.Set("usergroup_id", id)
		}
		if code := errorCode(t, postForm(handler, "/api/"+call.method, call.form.Encode())); code != "restricted_action" {
			t.Errorf("%s as a member: %q", call.method, code)
		}
	}
}
