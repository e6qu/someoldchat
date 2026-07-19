package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func newAuthAdminTestHandler(t *testing.T, scopes []auth.Scope) http.Handler {
	t.Helper()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
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
	return mux
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
	request.AddCookie(&http.Cookie{Name: auth.CSRFTokenCookieName, Value: auth.CSRFToken("session")})
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
	request.AddCookie(&http.Cookie{Name: auth.CSRFTokenCookieName, Value: auth.CSRFToken("session")})
	return request
}

func TestAuthAdminListsMembershipState(t *testing.T) {
	handler := newAuthAdminTestHandler(t, []auth.Scope{auth.ScopeAdminUsersRead})
	request := httptest.NewRequest(http.MethodGet, "/api/admin.auth.users.list?limit=10", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"role":"member"`) || !strings.Contains(body, `"active":true`) {
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
