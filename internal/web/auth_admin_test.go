package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
	"github.com/sameoldchat/sameoldchat/internal/service"
	store2 "github.com/sameoldchat/sameoldchat/internal/store"
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
	if strings.Contains(after, "AR1") {
		t.Fatalf("a decided app request is still queued: %s", after)
	}
	// The approved invitation leaves the pending queue and appears in the one
	// waiting to be accepted, with the link to send.
	if !strings.Contains(after, "Approved, waiting to be accepted") || !strings.Contains(after, "/app/invite/"+string(invites.Requests[0].ID)) {
		t.Fatalf("the approved invitation carries no link to send: %s", after)
	}
	if strings.Contains(after, `aria-label="Approve the invitation for guest@example.test"`) {
		t.Fatalf("an approved invitation is still offered for approval: %s", after)
	}
	if !strings.Contains(after, `aria-label="Withdraw the invitation for guest@example.test"`) {
		t.Fatalf("an approved invitation cannot be withdrawn: %s", after)
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

// AUTH-05: approving an invitation used to flip a status and do nothing else.
// The invited person still had no account, no membership and none of the
// channels the invitation recorded, so the whole promise was inert.
func TestAcceptingAnInvitationCreatesTheMemberItPromised(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	_ = handler
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "design"})
	store.SeedConversationMember("C1", "U1")
	store.SeedConversationMember("C2", "U1")
	messages := service.Messages{Store: store}
	if err := messages.AdminInviteUser(ctx, "T1", "U1", "guest@example.test", []domain.ConversationID{"C1", "C2"}, "", "Guest", false, true, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Requests) != 1 {
		t.Fatalf("invites=%+v err=%v", page.Requests, err)
	}
	invitation := page.Requests[0]

	// Nothing can be accepted before it is approved: recording an invitation
	// and issuing it are deliberately distinct transitions.
	if _, err := messages.AcceptInvitationForEmail(ctx, "T1", "guest@example.test", "Guest Person"); !errors.Is(err, store2.ErrNotFound) {
		t.Fatalf("an unapproved invitation was accepted: %v", err)
	}
	if err := messages.AdminApproveInviteRequest(ctx, "T1", "U1", invitation.ID); err != nil {
		t.Fatal(err)
	}

	user, err := messages.AcceptInvitationForEmail(ctx, "T1", "GUEST@example.test", "Guest Person")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if user.Email != "guest@example.test" || user.RealName != "Guest Person" {
		t.Fatalf("user=%+v", user)
	}
	membership, err := store.GetWorkspaceMembership(ctx, "T1", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !membership.Active || !membership.Restricted || membership.UltraRestricted || membership.Role != domain.WorkspaceRoleMember {
		t.Fatalf("membership=%+v, want an active multi-channel guest", membership)
	}
	for _, channelID := range []domain.ConversationID{"C1", "C2"} {
		members, err := store.ListConversationMembers(ctx, channelID, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		joined := false
		for _, member := range members.Users {
			if member.ID == user.ID {
				joined = true
			}
		}
		if !joined {
			t.Fatalf("the accepted member did not join %s, which the invitation recorded", channelID)
		}
	}
	// Single use: the transition to accepted is the same transaction that
	// created the member, so a second acceptance finds nothing approved.
	if _, err := messages.AcceptInvitationForEmail(ctx, "T1", "guest@example.test", "Guest Person"); !errors.Is(err, store2.ErrNotFound) {
		t.Fatalf("an invitation was accepted twice: %v", err)
	}
	stored, err := store.GetInviteRequest(ctx, "T1", invitation.ID)
	if err != nil || stored.Status != domain.InviteRequestAccepted || stored.AcceptedBy != user.ID || stored.AcceptedAt.IsZero() {
		t.Fatalf("invitation=%+v err=%v", stored, err)
	}
}

// An invitation is valid for a bounded time from when it was recorded, and an
// expired one has to say so: the remedy is a new invitation, not a retry.
func TestAnExpiredInvitationIsRefusedDistinctly(t *testing.T) {
	_, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	stale := domain.InviteRequest{
		ID: "IR_stale", WorkspaceID: "T1", Email: "late@example.test", RequestedBy: "U1",
		ChannelIDs: []domain.ConversationID{"C1"}, RealName: "Late", Status: domain.InviteRequestPending,
		CreatedAt: time.Now().UTC().Add(-30 * 24 * time.Hour), ExpiresAt: time.Now().UTC().Add(-16 * 24 * time.Hour),
	}
	if err := store.CreateInviteRequest(ctx, stale, events.Event{ID: "Estale", WorkspaceID: "T1", Topic: "invite_request.created", Payload: `{"type":"invite_request.created"}`, CreatedAt: stale.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: store}
	if err := messages.AdminApproveInviteRequest(ctx, "T1", "U1", stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AcceptInvitationForEmail(ctx, "T1", "late@example.test", "Late"); !errors.Is(err, service.ErrInvitationExpired) {
		t.Fatalf("an expired invitation was not refused as expired: %v", err)
	}
}

// The invitation page is reached signed-out, on purpose, and every terminal
// state says something different: the reader has to be able to tell whether to
// wait, to sign in, or to ask for a new invitation.
func TestTheInvitationPageAnswersEveryOutcomeSignedOut(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	messages := service.Messages{Store: store}
	if err := messages.AdminInviteUser(ctx, "T1", "U1", "guest@example.test", []domain.ConversationID{"C1"}, "", "Guest", false, false, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Requests) != 1 {
		t.Fatalf("invites=%+v err=%v", page.Requests, err)
	}
	invitation := page.Requests[0]
	visit := func(id domain.InviteRequestID) (int, string) {
		t.Helper()
		response := httptest.NewRecorder()
		// No session cookie: this is what an invited person actually has.
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app/invite/"+string(id), nil))
		return response.Code, response.Body.String()
	}

	status, body := visit(invitation.ID)
	if status != http.StatusOK || !strings.Contains(body, "waiting for approval") {
		t.Fatalf("pending invitation status=%d body=%s", status, body)
	}
	// The address is masked: an invitation identifier is workspace-visible.
	if strings.Contains(body, "guest@example.test") {
		t.Fatalf("the invited address was handed to whoever holds the link: %s", body)
	}
	if !strings.Contains(body, "g•••@example.test") {
		t.Fatalf("the invited person cannot recognise their own address: %s", body)
	}

	if err := messages.AdminApproveInviteRequest(ctx, "T1", "U1", invitation.ID); err != nil {
		t.Fatal(err)
	}
	status, body = visit(invitation.ID)
	if status != http.StatusOK || !strings.Contains(body, "Sign in to accept") || !strings.Contains(body, "Guest in one channel") || !strings.Contains(body, "#general") {
		t.Fatalf("approved invitation status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "/login?return_to=") {
		t.Fatalf("the invitation page does not lead anywhere: %s", body)
	}

	if _, err := messages.AcceptInvitationForEmail(ctx, "T1", "guest@example.test", "Guest"); err != nil {
		t.Fatal(err)
	}
	status, body = visit(invitation.ID)
	if status != http.StatusGone || !strings.Contains(body, "already been accepted") {
		t.Fatalf("accepted invitation status=%d body=%s", status, body)
	}

	status, body = visit("IR_missing")
	if status != http.StatusNotFound || !strings.Contains(body, "does not exist") {
		t.Fatalf("unknown invitation status=%d body=%s", status, body)
	}
}

// An approved invitation nobody accepted can be withdrawn, and withdrawing is
// recorded as its own status: denied answers a request, revoked withdraws an
// answer already given, and an administrator reading the record needs both.
func TestWithdrawingAnApprovedInvitationIsItsOwnOutcome(t *testing.T) {
	_, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	messages := service.Messages{Store: store}
	if err := messages.AdminInviteUser(ctx, "T1", "U1", "gone@example.test", []domain.ConversationID{"C1"}, "", "Gone", false, false, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	page, _ := store.ListInviteRequests(ctx, "T1", domain.InviteRequestPending, domain.PageRequest{Limit: 10})
	invitation := page.Requests[0]
	if err := messages.AdminApproveInviteRequest(ctx, "T1", "U1", invitation.ID); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminDenyInviteRequest(ctx, "T1", "U1", invitation.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetInviteRequest(ctx, "T1", invitation.ID)
	if err != nil || stored.Status != domain.InviteRequestRevoked {
		t.Fatalf("invitation=%+v err=%v, want it revoked rather than denied", stored, err)
	}
	if _, err := messages.AcceptInvitationForEmail(ctx, "T1", "gone@example.test", "Gone"); !errors.Is(err, store2.ErrNotFound) {
		t.Fatalf("a withdrawn invitation was accepted: %v", err)
	}
}

// ADMIN-03: the durable record and the access log both existed with no way to
// look at either, and RecordAccess was wired only into the Slack API
// authenticator — so an audit of a workspace whose people use the browser was
// empty. The page and its JSON export come from one query, because two code
// paths are how an export stops agreeing with the page it exports.
func TestTheAuditPageShowsWhatWasDoneAndWhoSignedIn(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	messages := service.Messages{Store: store}
	if _, err := messages.Post(ctx, "T1", "U1", "C1", "audited", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := messages.RecordAccess(ctx, "T1", "U1", "203.0.113.7", "Mozilla/5.0 (test)"); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/app/admin/audit", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{"message.created", "Workspace Admin", "203.0.113.7", "Mozilla/5.0 (test)", "channel C1"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("the audit page is missing %q: %s", expected, body)
		}
	}
	// The message text is not in the record and must not appear on the page:
	// payloads carry identifiers, and the delivery snapshot is never rendered.
	if strings.Contains(body, "audited") {
		t.Fatalf("message content reached the audit page: %s", body)
	}

	exportRequest := httptest.NewRequest(http.MethodGet, "/app/admin/audit", nil)
	exportRequest.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	exportRequest.Header.Set("Accept", "application/json")
	export := httptest.NewRecorder()
	handler.ServeHTTP(export, exportRequest)
	if export.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", export.Code, export.Body.String())
	}
	var decoded struct {
		OK    bool `json:"ok"`
		Audit struct {
			Entries []struct {
				Action string `json:"action"`
				Actor  string `json:"actor"`
				Target string `json:"target"`
			} `json:"entries"`
			Access []struct {
				IP string `json:"ip"`
			} `json:"access"`
		} `json:"audit"`
	}
	if err := json.Unmarshal(export.Body.Bytes(), &decoded); err != nil || !decoded.OK {
		t.Fatalf("export body=%s err=%v", export.Body.String(), err)
	}
	// Export and page agree: every entry the page rendered is in the export.
	if len(decoded.Audit.Entries) == 0 || len(decoded.Audit.Access) != 1 {
		t.Fatalf("export=%+v", decoded.Audit)
	}
	found := false
	for _, entry := range decoded.Audit.Entries {
		if entry.Action == "message.created" && entry.Target == "channel C1" && entry.Actor == "Workspace Admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the export does not carry what the page showed: %+v", decoded.Audit.Entries)
	}
	if decoded.Audit.Access[0].IP != "203.0.113.7" {
		t.Fatalf("access export=%+v", decoded.Audit.Access)
	}
}

// A member without an administrative scope must not read the audit record.
func TestTheAuditPageRefusesAnUnprivilegedReader(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeChannelsHistory})
	request := httptest.NewRequest(http.MethodGet, "/app/admin/audit", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// ADMIN-02: the settings page carries exactly what has a durable backend, and
// each control writes through to workspace state. A page that drew a control
// for a policy nothing enforces would be worse than no page.
func TestWorkspaceSettingsWriteThroughAndNameWhatIsAbsent(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "design"})
	store.SeedConversationMember("C1", "U1")
	store.SeedConversationMember("C2", "U1")

	page := func() string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app/admin/settings", nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	// Retention is implemented now, so this asserts the opposite of what it
	// used to: the control exists, and it still tells the truth about what it
	// does. Deletion under it is permanent and applied on a schedule, and both
	// have to be on the page before someone sets a limit.
	before := page()
	for _, expected := range []string{
		"Message and file retention",
		`name="message_days"`,
		`name="file_days"`,
		"permanent and cannot be undone",
		"runs on a schedule",
		"Nothing is deleted by policy",
	} {
		if !strings.Contains(before, expected) {
			t.Fatalf("the retention control is missing %q: %s", expected, before)
		}
	}
	// What genuinely has no implementation still says so.
	if !strings.Contains(before, "Audio and video") {
		t.Fatalf("the settings page stopped naming what is absent: %s", before)
	}

	identity := adminMutationRequest(http.MethodPost, "/app/admin/settings/identity", "name=Renamed&description=A+described+workspace&icon_url=https%3A%2F%2Ficons.example.test%2Ft1.png")
	identityResponse := httptest.NewRecorder()
	handler.ServeHTTP(identityResponse, identity)
	if identityResponse.Code != http.StatusOK {
		t.Fatalf("identity status=%d body=%s", identityResponse.Code, identityResponse.Body.String())
	}
	discoverability := adminMutationRequest(http.MethodPost, "/app/admin/settings/discoverability", "discoverability=unlisted")
	discoverabilityResponse := httptest.NewRecorder()
	handler.ServeHTTP(discoverabilityResponse, discoverability)
	if discoverabilityResponse.Code != http.StatusOK {
		t.Fatalf("discoverability status=%d body=%s", discoverabilityResponse.Code, discoverabilityResponse.Body.String())
	}
	defaults := adminMutationRequest(http.MethodPost, "/app/admin/settings/default-channels", "channel_ids=C1&channel_ids=C2")
	defaultsResponse := httptest.NewRecorder()
	handler.ServeHTTP(defaultsResponse, defaults)
	if defaultsResponse.Code != http.StatusOK {
		t.Fatalf("default channels status=%d body=%s", defaultsResponse.Code, defaultsResponse.Body.String())
	}

	workspace, err := store.GetWorkspace(ctx, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Name != "Renamed" || workspace.Description != "A described workspace" || workspace.IconURL != "https://icons.example.test/t1.png" {
		t.Fatalf("workspace=%+v", workspace)
	}
	if workspace.Discoverability != domain.WorkspaceDiscoverabilityUnlisted {
		t.Fatalf("discoverability=%q", workspace.Discoverability)
	}
	if len(workspace.DefaultChannelIDs) != 2 {
		t.Fatalf("default channels=%v", workspace.DefaultChannelIDs)
	}
	after := page()
	if !strings.Contains(after, `value="Renamed"`) || !strings.Contains(after, `value="unlisted" checked`) {
		t.Fatalf("the settings page does not show what was saved: %s", after)
	}
}

// A value the workspace does not accept is a caller mistake, not an outage:
// reporting it as "temporarily unavailable" tells an administrator to retry
// something that can never succeed.
func TestWorkspaceSettingsRefuseAnUnknownDiscoverability(t *testing.T) {
	handler, _ := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/app/admin/settings/discoverability", "discoverability=whenever"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

// ADMIN-02: the analytics dashboard counts the durable rows. Nothing here is
// sampled or delayed, so the page and the store must agree exactly.
func TestAnalyticsCountsWhatTheWorkspaceHolds(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "design", IsPrivate: true})
	store.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "old", Archived: true})
	store.SeedConversationMember("C1", "U1")
	store.SeedConversationMember("C2", "U1")
	messages := service.Messages{Store: store}
	for index := 0; index < 3; index++ {
		if _, err := messages.Post(ctx, "T1", "U1", "C1", "counted "+strconv.Itoa(index), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := messages.Post(ctx, "T1", "U1", "C2", "private", "", ""); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/app/admin/analytics?days=30", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{"Workspace analytics", "Busiest channels", "#general", "last 30 days"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("the analytics page is missing %q: %s", expected, body)
		}
	}
	analytics, err := messages.WorkspaceAnalytics(ctx, "T1", "U1", time.Now().UTC().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if analytics.PublicChannels != 1 || analytics.PrivateChannels != 1 || analytics.ArchivedChannels != 1 {
		t.Fatalf("channel counts=%+v", analytics)
	}
	if analytics.Messages != 4 || analytics.RecentMessages != 4 {
		t.Fatalf("message counts=%+v", analytics)
	}
	if len(analytics.BusiestChannels) == 0 || analytics.BusiestChannels[0].ConversationID != "C1" || analytics.BusiestChannels[0].Messages != 3 {
		t.Fatalf("busiest=%+v", analytics.BusiestChannels)
	}
	// The window is a closed set: an arbitrary one is a caller mistake.
	bad := httptest.NewRequest(http.MethodGet, "/app/admin/analytics?days=4000", nil)
	bad.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("window status=%d", badResponse.Code)
	}
}

// AUTH-02: switching workspaces must land the reader in an isolated session
// context, with nothing about the target in browser history. The switch mints
// a new session for the target workspace rather than rewriting the current
// one — the same person is a different user row in each workspace, so a
// rewritten session would name a user that does not belong to it.
func TestSwitchingWorkspacesMintsAnIsolatedSession(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	// The same person in a second workspace: a different user row, the same
	// address, and a plain member rather than an administrator.
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "Second"})
	store.SeedUser(domain.User{ID: "U1-second", WorkspaceID: "T2", Email: "admin@example.test", Name: "Admin", RealName: "Workspace Admin"})
	if err := store.SetWorkspaceRole(ctx, "T2", "U1-second", domain.WorkspaceRoleMember, events.Event{}); err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRequest(http.MethodGet, "/app?channel=C1", nil)
	page.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	rendered := httptest.NewRecorder()
	handler.ServeHTTP(rendered, page)
	body := rendered.Body.String()
	if !strings.Contains(body, `value="T2"`) || !strings.Contains(body, "you are here") {
		t.Fatalf("the switcher does not offer the second workspace: %s", body)
	}

	switched := adminMutationRequest(http.MethodPost, "/app/workspace/switch", "workspace_id=T2")
	switched.Header.Del("Accept")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, switched)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("switch=%d body=%s", response.Code, response.Body.String())
	}
	// Nothing about the target may reach history: the redirect goes to the
	// workspace page, not to a URL naming the workspace.
	if location := response.Header().Get("Location"); strings.Contains(location, "T2") {
		t.Fatalf("the switch leaked the target workspace into history: %q", location)
	}
	var issued *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			issued = cookie
		}
	}
	if issued == nil || issued.Value == "" || issued.Value == "session" {
		t.Fatalf("the switch did not mint a new session: %+v", issued)
	}
	record, err := store.LookupSession(ctx, issued.Value)
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkspaceID != "T2" || record.UserID != "U1-second" {
		t.Fatalf("session=%+v, want the identity held in the target workspace", record)
	}
	// The scopes follow the role held there, not the one left behind.
	for _, scope := range record.Scopes {
		if scope == string(auth.ScopeAdminUsersWrite) {
			t.Fatalf("the switched session carries administrative scope the target role does not justify: %v", record.Scopes)
		}
	}
	// The session left behind is revoked rather than left as a live credential
	// nothing references.
	previous, err := store.LookupSession(ctx, "session")
	if err == nil && !previous.Revoked {
		t.Fatal("the previous session is still usable after switching away from it")
	}
}

// A workspace the reader is not a member of is not switchable into, and the
// refusal declines to confirm it exists.
func TestSwitchingRefusesAWorkspaceYouAreNotIn(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T3", Name: "Somebody else's"})
	store.SeedUser(domain.User{ID: "U9", WorkspaceID: "T3", Email: "stranger@example.test", Name: "stranger"})
	if err := store.SetWorkspaceRole(ctx, "T3", "U9", domain.WorkspaceRoleMember, events.Event{}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/app/workspace/switch", "workspace_id=T3"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	kept, err := store.LookupSession(ctx, "session")
	if err != nil || kept.Revoked {
		t.Fatalf("a refused switch revoked the session it refused to leave: %+v err=%v", kept, err)
	}
}

// One workspace is not a choice: a menu with a single entry implies there is
// somewhere else to go.
func TestASingleWorkspaceDrawsNoSwitcher(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	request := httptest.NewRequest(http.MethodGet, "/app?channel=C1", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), "/app/workspace/switch") {
		t.Fatalf("a lone workspace drew a switcher: %s", response.Body.String())
	}
}

// CONNECT-01..03 in the browser: the details panel is where "who else is in
// this channel" is already asked, so an external organization belongs beside a
// person. It must distinguish an outstanding invitation from a connection —
// an invitation is not a place in the channel — and say what accepting it
// means before it is sent.
func TestTheConnectPanelSeparatesInvitationsFromConnections(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "Second"})
	store.SeedUser(domain.User{ID: "U1-second", WorkspaceID: "T2", Email: "admin@example.test", Name: "Admin"})
	if err := store.SetWorkspaceRole(ctx, "T2", "U1-second", domain.WorkspaceRoleAdmin, events.Event{}); err != nil {
		t.Fatal(err)
	}

	details := func() string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app?channel=C1&details=1", nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	before := details()
	for _, expected := range []string{"Shared with other organizations", "Only this workspace is in this channel", "read this channel"} {
		if !strings.Contains(before, expected) {
			t.Fatalf("the Connect panel is missing %q: %s", expected, before)
		}
	}

	sent := adminMutationRequest(http.MethodPost, "/app/connect/invite?channel=C1", "target=T2")
	sentResponse := httptest.NewRecorder()
	handler.ServeHTTP(sentResponse, sent)
	if sentResponse.Code != http.StatusSeeOther {
		t.Fatalf("invite=%d body=%s", sentResponse.Code, sentResponse.Body.String())
	}
	pending := details()
	// An outstanding invitation is not a connection: the panel must still say
	// only this workspace is in the channel.
	if !strings.Contains(pending, "Only this workspace is in this channel") || !strings.Contains(pending, "Second") {
		t.Fatalf("an invitation was rendered as a connection: %s", pending)
	}

	page, err := store.ListSharedInvites(ctx, "T1", domain.SharedInvitePending, domain.PageRequest{Limit: 10})
	if err != nil || len(page.Invites) != 1 {
		t.Fatalf("invites=%+v err=%v", page.Invites, err)
	}
	invite := page.Invites[0]
	approve := adminMutationRequest(http.MethodPost, "/app/connect/approve?channel=C1", "invite_id="+string(invite.ID))
	approveResponse := httptest.NewRecorder()
	handler.ServeHTTP(approveResponse, approve)
	if approveResponse.Code != http.StatusSeeOther {
		t.Fatalf("approve=%d body=%s", approveResponse.Code, approveResponse.Body.String())
	}
	// Still not a connection until the other organization accepts.
	if !strings.Contains(details(), "Only this workspace is in this channel") {
		t.Fatalf("an approved invitation was rendered as a connection: %s", details())
	}

	if _, err := (service.Messages{Store: store}).AcceptSharedInvite(ctx, "T2", "U1-second", invite.ID); err != nil {
		t.Fatal(err)
	}
	connected := details()
	// The organization renders as its identifier: a host administrator is not a
	// member of the workspace it invited, so its name is not readable from
	// here. See Handler.workspaceName.
	if strings.Contains(connected, "Only this workspace is in this channel") || !strings.Contains(connected, "In this channel: T2") {
		t.Fatalf("an accepted invitation is not rendered as a connection: %s", connected)
	}
}

// One control withdraws an invitation whichever side of the state machine it
// is on: two buttons that mean the same thing would make an administrator
// guess which applies.
func TestWithdrawingWorksOnPendingAndApprovedInvitations(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "Second"})
	messages := service.Messages{Store: store}

	pending, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T2", "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/app/connect/deny?channel=C1", "invite_id="+string(pending.ID)))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("deny pending=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := store.GetSharedInvite(ctx, pending.ID)
	if err != nil || stored.Status != domain.SharedInviteRevoked {
		t.Fatalf("pending invitation=%+v err=%v", stored, err)
	}

	approved, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", approved.ID); err != nil {
		t.Fatal(err)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, adminMutationRequest(http.MethodPost, "/app/connect/deny?channel=C1", "invite_id="+string(approved.ID)))
	if second.Code != http.StatusSeeOther {
		t.Fatalf("deny approved=%d body=%s", second.Code, second.Body.String())
	}
	settled, err := store.GetSharedInvite(ctx, approved.ID)
	if err != nil || settled.Status != domain.SharedInviteRevoked {
		t.Fatalf("approved invitation=%+v err=%v", settled, err)
	}
}

// The retention control has to write through and read back, and the page has
// to report whether the sweep is actually running — a policy that silently
// stopped being applied is the failure a scheduled deletion hides.
func TestRetentionControlWritesThroughAndReportsTheSweep(t *testing.T) {
	handler, store := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U1")

	page := func() string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/app/admin/settings", nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	if !strings.Contains(page(), "Nothing has been swept yet") {
		t.Fatalf("a workspace that has never swept does not say so: %s", page())
	}

	saved := adminMutationRequest(http.MethodPost, "/app/admin/settings/retention", "message_days=90&file_days=0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, saved)
	if response.Code != http.StatusOK {
		t.Fatalf("save=%d body=%s", response.Code, response.Body.String())
	}
	policy, err := store.GetRetentionPolicy(ctx, "T1")
	if err != nil || policy.MessageDays != 90 || policy.FileDays != 0 {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	// The summary says in words what two numbers in adjacent boxes do not.
	rendered := page()
	if !strings.Contains(rendered, `value="90"`) || !strings.Contains(rendered, "Files are kept forever") {
		t.Fatalf("the page does not reflect what was saved: %s", rendered)
	}

	// Once a sweep has run, the page says when — so a stalled worker shows.
	if _, err := (service.Messages{Store: store}).SetWorkspaceRetention(ctx, "T1", "U1", domain.RetentionPolicy{MessageDays: 90}); err != nil {
		t.Fatal(err)
	}
	worker, err := scheduler.NewRetentionWorker(store, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(ctx, "T1"); err != nil {
		t.Fatal(err)
	}
	swept := page()
	if strings.Contains(swept, "Nothing has been swept yet") || !strings.Contains(swept, "Last swept") {
		t.Fatalf("the page does not report the sweep that ran: %s", swept)
	}
}

// A limit outside Slack's range is a caller mistake, not an outage.
func TestRetentionControlRefusesAnImpossibleLimit(t *testing.T) {
	handler, _ := newAuthAdminTestHandlerWithRole(t, allAdminScopes(), domain.WorkspaceRoleAdmin)
	for _, body := range []string{"message_days=40000&file_days=0", "message_days=forever&file_days=0"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, adminMutationRequest(http.MethodPost, "/app/admin/settings/retention", body))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
}
