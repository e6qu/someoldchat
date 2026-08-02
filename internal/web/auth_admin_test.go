package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func newAuthAdminTestHandler(t *testing.T, scopes []auth.Scope) http.Handler {
	t.Helper()
	handler, _ := newAuthAdminTestHandlerWithRole(t, scopes, domain.WorkspaceRoleAdmin)
	return handler
}

// newAuthAdminTestHandlerWithRole builds the administration surface for a caller
// holding an explicit scope set and an explicit durable workspace role. Both are
// required to reach the control plane, so both are parameters: a fixture cannot
// grant authority by listing scopes alone.
func newAuthAdminTestHandlerWithRole(t *testing.T, scopes []auth.Scope, role domain.WorkspaceRole) (http.Handler, *memory.Store) {
	t.Helper()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "Admin", RealName: "Workspace Admin", Email: "admin@example.test"})
	if err := store.SetWorkspaceRole(context.Background(), "T1", "U1", role, events.Event{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSession(context.Background(), "session", domain.SessionRecord{
		WorkspaceID: "T1",
		UserID:      "U1",
		Scopes:      authScopeStrings(scopes),
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name:         "google",
		ClientID:     "client",
		ClientSecret: "secret",
		AuthorizeURL: "https://accounts.google.com/authorize",
		TokenURL:     "https://oauth2.googleapis.com/token",
		UserInfoURL:  "https://openidconnect.googleapis.com/v1/userinfo",
		Scopes:       []string{"openid", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	browser, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: store}, browser, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux, store
}

// allAdminScopes is every scope this package knows, as the typed values the
// fixture takes. auth.AllScopes reports strings.
func allAdminScopes() []auth.Scope {
	names := auth.AllScopes()
	scopes := make([]auth.Scope, 0, len(names))
	for _, name := range names {
		scopes = append(scopes, auth.Scope(name))
	}
	return scopes
}

func authScopeStrings(scopes []auth.Scope) []string {
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		values = append(values, string(scope))
	}
	return values
}

func adminPageRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/app/admin/auth", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	return request
}

func TestAuthAdminPageRejectsAuthenticatedUserWithoutAdminScope(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeChannelsHistory})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminPageRequest())
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthAdminPageShowsOnlyAuthorizedSections(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminPageRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "/api/admin.auth.users.create") {
		t.Fatal("user administration section is missing")
	}
	if strings.Contains(body, "/api/admin.auth.methods.set") {
		t.Fatal("authorization-method section was exposed without its scope")
	}
	for _, expected := range []string{
		`<title>Workspace administration · SameOldChat</title>`,
		`href="/app">Back to chat</a>`,
		`aria-label="Disable Workspace Admin"`,
		`aria-label="Save role for Workspace Admin"`,
		// The page renders through the shared layout, so it honours the theme
		// the administrator chose in the workspace instead of only the one the
		// operating system reports.
		`<html lang="en" data-theme="light">`,
		`id="theme-toggle"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("administration page is missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `id="authorization-heading"`) {
		t.Fatal("user-only administrator was shown an empty authorization-method section")
	}
}

func TestAuthAdminPageOffersNextUserPage(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	for index := 0; index < 51; index++ {
		request := adminMutationRequest(http.MethodPost, "/api/admin.auth.users.create", "email=user-"+strconv.Itoa(index)+"%40example.com&real_name=User-"+strconv.Itoa(index)+"&role=member")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create user %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	request := adminPageRequest()
	request.URL.RawQuery = "limit=10"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Next page") || !strings.Contains(response.Body.String(), "limit=10&amp;cursor=") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthAdminCreatesManualUserWithCSRFAndRole(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	body := "email=Alice%40Example.COM&real_name=Alice+Example&role=admin&_csrf=" + auth.CSRFToken("session")
	request := httptest.NewRequest(http.MethodPost, "/api/admin.auth.users.create", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"ok":true`) || !strings.Contains(response.Body.String(), "alice@example.com") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthAdminReadScopeCanInspectProvidersWithoutMutationControl(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminAppsRead})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminPageRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Google") || !strings.Contains(body, "enabled") || strings.Contains(body, "/api/admin.auth.methods.set") {
		t.Fatalf("read-only provider page exposed the wrong controls: %s", body)
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/admin.auth.methods.list", nil)
	listRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"ok":true`) {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	setRequest := httptest.NewRequest(http.MethodPost, "/api/admin.auth.methods.set", strings.NewReader("provider=google&enabled=false"))
	setRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	setResponse := httptest.NewRecorder()
	handler.ServeHTTP(setResponse, setRequest)
	if setResponse.Code != http.StatusForbidden {
		t.Fatalf("read-only mutation status=%d body=%s", setResponse.Code, setResponse.Body.String())
	}
}

func TestAuthAdminCreateUserRejectsMissingCSRF(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	request := httptest.NewRequest(http.MethodPost, "/api/admin.auth.users.create", strings.NewReader("email=a%40example.com&real_name=Alice&role=member"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func adminMutationRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body+"&_csrf="+auth.CSRFToken("session")))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	return request
}

func TestAuthAdminListsMembershipState(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdminUsersRead}, domain.WorkspaceRoleAdmin)
	// The administrator doing the reading and an ordinary member must both be
	// reported with their own durable role, not with a single hard-coded one.
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "member@example.com", Name: "member"})
	request := httptest.NewRequest(http.MethodGet, "/api/admin.auth.users.list?limit=10", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"role":"member"`) || !strings.Contains(body, `"role":"admin"`) || !strings.Contains(body, `"active":true`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestAuthAdminUpdatesUserLifecycle(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	create := adminMutationRequest(http.MethodPost, "/api/admin.auth.users.create", "email=target%40example.com&real_name=Target&role=member")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, create)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		User domain.User `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id="+string(created.User.ID)+"&action=disable"))
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id="+string(created.User.ID)+"&action=enable"))
	if response.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id="+string(created.User.ID)+"&action=role&role=admin"))
	if response.Code != http.StatusOK {
		t.Fatalf("role status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestAuthAdminRoleEditorTellsTheTruthAboutOwnership covers the defect where
// the page could not represent the owner role at all. Both booleans behind the
// two-option select were false for an owner row, so every browser
// default-selected the first option: the page showed a workspace owner with a
// demotion already loaded into the form, one click away, and then refused
// role=owner so nothing could put it back.
func TestAuthAdminRoleEditorTellsTheTruthAboutOwnership(t *testing.T) {
	t.Run("an administrator cannot demote an owner from the page", func(t *testing.T) {
		handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite}, domain.WorkspaceRoleAdmin)
		store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "owner@example.com", Name: "owner"})
		if err := store.SetWorkspaceRole(context.Background(), "T1", "U2", domain.WorkspaceRoleOwner, events.Event{}); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, adminPageRequest())
		body := response.Body.String()
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, body)
		}
		// The owner row carries no role form at all, because this actor may not
		// write it — rather than a form pre-loaded with the demotion.
		if strings.Contains(body, `<option value="owner"`) {
			t.Fatalf("an administrator is offered the owner role: %s", body)
		}
		rows := strings.Split(body, "<tr>")
		for _, row := range rows {
			if !strings.Contains(row, "owner@example.com") {
				continue
			}
			if strings.Contains(row, `name="action" value="role"`) {
				t.Fatalf("an administrator is offered a role editor for the owner: %s", row)
			}
		}
		// And the write is refused with an authorization answer, not an outage.
		refused := httptest.NewRecorder()
		handler.ServeHTTP(refused, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id=U2&action=role&role=member"))
		if refused.Code != http.StatusForbidden {
			t.Fatalf("demotion status=%d body=%s", refused.Code, refused.Body.String())
		}
		membership, err := store.GetWorkspaceMembership(context.Background(), "T1", "U2")
		if err != nil || membership.Role != domain.WorkspaceRoleOwner {
			t.Fatalf("membership=%+v err=%v", membership, err)
		}
	})

	t.Run("an owner sees every role with the current one selected", func(t *testing.T) {
		handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite}, domain.WorkspaceRoleOwner)
		store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "member@example.com", Name: "member"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, adminPageRequest())
		body := response.Body.String()
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, body)
		}
		for _, expected := range []string{
			`<option value="member" selected>Member</option>`,
			`<option value="admin">Administrator</option>`,
			`<option value="owner">Owner</option>`,
			`<option value="owner" selected>Owner</option>`,
		} {
			if !strings.Contains(body, expected) {
				t.Fatalf("the role editor is missing %q: %s", expected, body)
			}
		}
		// An owner can appoint another owner, so ownership is recoverable from
		// the page that can lose it.
		promote := httptest.NewRecorder()
		handler.ServeHTTP(promote, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id=U2&action=role&role=owner"))
		if promote.Code != http.StatusOK {
			t.Fatalf("promotion status=%d body=%s", promote.Code, promote.Body.String())
		}
		membership, err := store.GetWorkspaceMembership(context.Background(), "T1", "U2")
		if err != nil || membership.Role != domain.WorkspaceRoleOwner {
			t.Fatalf("membership=%+v err=%v", membership, err)
		}
	})
}

func TestAuthAdminRejectsUnknownUserMutation(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id=U1&action=unknown"))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_action") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthAdminReportsMissingUserAsNotFound(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.set", "user_id=missing&action=disable"))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "user_not_found") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestAuthAdminRefusesMemberRoleHoldingControlPlaneScopes is the transport half of
// the privilege-escalation defect: before this change the administration surface
// authorized on scope presence alone, so any session that carried admin.users:write
// — every browser session did — could promote its own user. The durable workspace
// role is now asserted as well, so a member is refused even while holding the scope.
func TestAuthAdminRefusesMemberRoleHoldingControlPlaneScopes(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdmin, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite, auth.ScopeAdminAppsRead, auth.ScopeAdminAppsWrite}, domain.WorkspaceRoleMember)
	for _, attempt := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "self promotion", method: http.MethodPost, target: "/api/admin.auth.users.set", body: "user_id=U1&action=role&role=admin"},
		{name: "deactivate", method: http.MethodPost, target: "/api/admin.auth.users.set", body: "user_id=U1&action=disable"},
		{name: "disable the login provider", method: http.MethodPost, target: "/api/admin.auth.methods.set", body: "provider=google&enabled=false"},
		{name: "mint an administrator", method: http.MethodPost, target: "/api/admin.auth.users.create", body: "email=mine%40example.com&real_name=Mine&role=admin"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, adminMutationRequest(attempt.method, attempt.target, attempt.body))
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "not_authorized") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	membership, err := store.GetWorkspaceMembership(context.Background(), "T1", "U1")
	if err != nil || membership.Role != domain.WorkspaceRoleMember || !membership.Active {
		t.Fatalf("membership=%+v err=%v, want an unchanged active member", membership, err)
	}
	method, err := store.GetAuthMethod(context.Background(), "T1", "google")
	if err != nil || !method.Enabled {
		t.Fatalf("authorization method=%+v err=%v, want it still enabled", method, err)
	}
	for _, target := range []string{"/app/admin/auth", "/api/admin.auth.users.list", "/api/admin.auth.methods.list"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s disclosed the workspace to a member: status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

// TestAuthAdminRejectsRejectedMutationWithAStatusAndABody covers the silent-success
// defect: a decodeFormFields failure used to `return` with nothing written, so the
// caller received HTTP 200 with an empty body and could not tell a refused
// administrative mutation from an applied one.
func TestAuthAdminRejectsRejectedMutationWithAStatusAndABody(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite, auth.ScopeAdminAppsWrite})
	for _, target := range []string{"/api/admin.auth.users.set", "/api/admin.auth.methods.set", "/api/admin.auth.users.create", "/api/admin.auth.users.invite"} {
		t.Run(target, func(t *testing.T) {
			// A repeated field is rejected by decodeFormFields, which is the path that
			// previously wrote no response at all.
			request := adminMutationRequest(http.MethodPost, target, "user_id=U1&user_id=U1&action=disable&provider=google&provider=google&enabled=false")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400; body=%s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "invalid_form") {
				t.Fatalf("rejection body=%q, want a machine-readable reason", response.Body.String())
			}
		})
	}
}

// TestAuthAdminRendersFailuresAsHypertextForBrowsers covers the second half of the
// same defect: every failure path used to emit a raw JSON envelope into the browser.
func TestAuthAdminRendersFailuresAsHypertextForBrowsers(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	request := httptest.NewRequest(http.MethodPost, "/api/admin.auth.users.set", strings.NewReader("user_id=missing&action=disable&_csrf="+auth.CSRFToken("session")))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content type=%q, want rendered hypertext", got)
	}
	if strings.Contains(body, `"ok":false`) || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, "does not exist") {
		t.Fatalf("browser failure body=%s", body)
	}
}

// TestAuthAdminPageIsNotFrameableAndCarriesNoScript covers the UI-redress defect:
// the page renders a valid CSRF token into pre-filled mutation forms, so framing it
// turns an administrator's click into a role change.
func TestAuthAdminPageIsNotFrameableAndCarriesNoScript(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite, auth.ScopeAdminAppsWrite})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminPageRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "form-action 'self'") {
		t.Fatalf("content security policy=%q", policy)
	}
	for name, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	} {
		if got := response.Header().Get(name); got != want {
			t.Fatalf("%s=%q, want %q", name, got, want)
		}
	}
	// The rejection path must be protected identically: it also renders on the
	// authenticated origin.
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/app/admin/auth", nil))
	if rejected.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("rejection headers=%v", rejected.Header())
	}
}

// TestAuthAdminPageEscapesUserControlledValuesInEveryContext replaces the
// hand-concatenated HTML with contextual escaping. html.EscapeString does not
// neutralize an attribute or URL payload; html/template does.
func TestAuthAdminPageEscapesUserControlledValuesInEveryContext(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdminUsersWrite}, domain.WorkspaceRoleAdmin)
	store.SeedUser(domain.User{ID: `U"><script>alert(1)</script>`, WorkspaceID: "T1", RealName: `"><img src=x onerror=alert(1)>`, Email: `evil"@example.com`})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminPageRequest())
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, payload := range []string{"<script>alert(1)</script>", "<img src=x onerror=alert(1)>", `value="U"`} {
		if strings.Contains(body, payload) {
			t.Fatalf("unescaped payload %q in page: %s", payload, body)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;") && !strings.Contains(body, "&#43;") && !strings.Contains(body, "&#34;") {
		t.Fatalf("page did not escape the hostile values at all: %s", body)
	}
}

// The invitation form is the only way a person reaches admin.auth.users.invite,
// and the guest tiers are the point of it: the handler used to hardcode
// resend, restricted, ultra_restricted and the expiry to their zero values, so
// every invitation it could produce was a full permanent member no matter what
// the service was willing to record.
func TestAdminInvitationCarriesTheGuestTierAndExpiry(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdminUsersWrite, auth.ScopeAdminUsersRead}, domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "design"})
	store.SeedConversationMember("C1", "U1")
	store.SeedConversationMember("C2", "U1")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.invite",
		"email=guest%40example.test&real_name=Guest&tier=multi_channel_guest&guest_expires_on=2026-09-01&resend=true&channel_ids=C1&channel_ids=C2"))
	if response.Code != http.StatusOK {
		t.Fatalf("invite status=%d body=%s", response.Code, response.Body.String())
	}
	page, err := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Requests) != 1 {
		t.Fatalf("requests=%+v err=%v", page.Requests, err)
	}
	recorded := page.Requests[0]
	if !recorded.Restricted || recorded.UltraRestricted {
		t.Fatalf("tier recorded as restricted=%v ultra=%v, want a multi-channel guest", recorded.Restricted, recorded.UltraRestricted)
	}
	if !recorded.Resend {
		t.Fatal("resend was not recorded")
	}
	if len(recorded.ChannelIDs) != 2 {
		t.Fatalf("channels=%v, want both checked channels", recorded.ChannelIDs)
	}
	// The expiry is a date, and the guest keeps the whole of the day chosen.
	if recorded.GuestExpirationAt.UTC().Format("2006-01-02 15:04") != "2026-09-01 23:59" {
		t.Fatalf("expiry=%s, want the end of the chosen day", recorded.GuestExpirationAt.UTC())
	}
}

// A full member cannot expire, and the tier field cannot express the state the
// service always refuses, so both wrong shapes are rejected as caller mistakes
// rather than reported as an outage.
func TestAdminInvitationRefusesAnExpiryOnAFullMember(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, []auth.Scope{auth.ScopeAdminUsersWrite}, domain.WorkspaceRoleAdmin)
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/api/admin.auth.users.invite",
		"email=member%40example.test&real_name=Member&tier=member&guest_expires_on=2026-09-01&channel_ids=C1"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || decoded["error"] != "invalid_expiration" {
		t.Fatalf("body=%s err=%v", response.Body.String(), err)
	}
}

// ADMIN-02: the pending queues are the surface an administrator acts on. Both
// were unreachable from any page, so an invitation or an app request could be
// recorded and never decided.
func TestAdminPageDecidesPendingInvitationsAndAppRequests(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	messages := service.Messages{Store: store}
	if err := messages.AdminInviteUser(ctx, "T1", "U1", "guest@example.test", []domain.ConversationID{"C1"}, "", "Guest", false, true, false, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppApproval(ctx, "T1", "A1", "AR1", domain.AppApprovalRequested, time.Now().UTC(), events.Event{ID: "Eapp", WorkspaceID: "T1", Topic: "app.requested", Payload: `{"type":"app.requested","app_id":"A1"}`, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	body := func() string {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, adminPageRequest())
		if response.Code != http.StatusOK {
			t.Fatalf("page status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	listed := body()
	for _, expected := range []string{"guest@example.test", "Guest, several channels", "#general", "AR1", "/app/admin/invites/approve", "/app/admin/apps/restrict"} {
		if !strings.Contains(listed, expected) {
			t.Fatalf("the pending queues are missing %q: %s", expected, listed)
		}
	}

	invites, err := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(invites.Requests) != 1 {
		t.Fatalf("invites=%+v err=%v", invites.Requests, err)
	}
	approve := httptest.NewRecorder()
	handler.ServeHTTP(approve, adminMutationRequest(http.MethodPost, "/app/admin/invites/approve", "invite_request_id="+string(invites.Requests[0].ID)))
	if approve.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", approve.Code, approve.Body.String())
	}
	restrict := httptest.NewRecorder()
	handler.ServeHTTP(restrict, adminMutationRequest(http.MethodPost, "/app/admin/apps/restrict", "app_id=A1&request_id=AR1"))
	if restrict.Code != http.StatusOK {
		t.Fatalf("restrict status=%d body=%s", restrict.Code, restrict.Body.String())
	}
	remaining, err := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(remaining.Requests) != 0 {
		t.Fatalf("pending invitations after approval=%+v err=%v", remaining.Requests, err)
	}
	restricted, err := store.ListAppApprovals(ctx, "T1", domain.AppApprovalRestricted, domain.PageRequest{Limit: 10})
	if err != nil || len(restricted.Apps) != 1 {
		t.Fatalf("restricted apps=%+v err=%v", restricted.Apps, err)
	}
	after := body()
	if strings.Contains(after, "guest@example.test") || strings.Contains(after, "AR1") {
		t.Fatalf("a decided request is still queued: %s", after)
	}
}

// A decision that empties a row with nothing else on screen leaves the
// administrator unsure which button landed.
func TestAdminDecisionRedirectsWithANotice(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	if err := (service.Messages{Store: store}).AdminInviteUser(ctx, "T1", "U1", "denied@example.test", []domain.ConversationID{"C1"}, "", "Denied", false, false, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	invites, err := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(invites.Requests) != 1 {
		t.Fatalf("invites=%+v err=%v", invites.Requests, err)
	}
	request := adminMutationRequest(http.MethodPost, "/app/admin/invites/deny", "invite_request_id="+string(invites.Requests[0].ID))
	request.Header.Del("Accept")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); !strings.Contains(location, "notice=Invitation+denied") {
		t.Fatalf("location=%q, want the decision reported back", location)
	}
}

// The administration page carries inline script through the shared layout, so
// it needs the same document/policy agreement the workspace pages have: a hash
// the policy omits disables the script in a browser and in nothing else.
func TestAuthAdminDocumentAndItsPolicyAgree(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersWrite})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminPageRequest())
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "form-action 'self'") || !strings.Contains(policy, "frame-ancestors 'none'") {
		t.Fatalf("policy=%q, want the administration page to keep its framing and form restrictions", policy)
	}
	bodies := inlineScriptBodies(response.Body.String())
	if len(bodies) == 0 {
		t.Fatal("the administration page renders no inline script")
	}
	for _, body := range bodies {
		digest := sha256.Sum256([]byte(body))
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
		if !strings.Contains(policy, hash) {
			t.Fatalf("the administration page serves an inline script its policy blocks: %s\npolicy=%s", hash, policy)
		}
	}
	if strings.Contains(policy, "script-src 'unsafe-inline'") {
		t.Fatalf("the administration page allows any inline script: %s", policy)
	}
}
