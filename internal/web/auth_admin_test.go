package web

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if !strings.Contains(body, "/api/admin.auth.users.invite") {
		t.Fatal("user administration section is missing")
	}
	if strings.Contains(body, "/api/admin.auth.methods.set") {
		t.Fatal("authorization-method section was exposed without its scope")
	}
}
