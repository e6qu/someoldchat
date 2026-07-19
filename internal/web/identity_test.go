package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestNewLoginHandlerAcceptsSupportedAuthorizationProviders(t *testing.T) {
	service := service.Messages{Store: memory.New()}
	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{
		{Name: "Google", ClientID: "google-id", ClientSecret: "google-secret", AuthorizeURL: "https://accounts.google.com/authorize", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "email"}},
		{Name: "github", ClientID: "github-id", ClientSecret: "github-secret", AuthorizeURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", UserInfoURL: "https://api.github.com/user", EmailURL: "https://api.github.com/user/emails", Scopes: []string{"user:email"}},
		{Name: "entra", ClientID: "entra-id", ClientSecret: "entra-secret", AuthorizeURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize", TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", UserInfoURL: "https://graph.microsoft.com/oidc/userinfo", Scopes: []string{"openid", "email"}},
		{Name: "oidc", ClientID: "oidc-id", ClientSecret: "oidc-secret", AuthorizeURL: "https://id.example.test/authorize", TokenURL: "https://id.example.test/token", UserInfoURL: "https://id.example.test/userinfo", Scopes: []string{"openid", "profile", "email"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.providers) != 4 {
		t.Fatalf("providers=%d, want 4", len(handler.providers))
	}
	if got := handler.providers["google"].Scopes; len(got) != 2 || got[0] != "openid" || got[1] != "email" {
		t.Fatalf("normalized Google scopes=%v", got)
	}
}

func TestDiscoverOpenIDConnectProvider(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(OpenIDConfiguration{
			Issuer:                server.URL,
			AuthorizationEndpoint: server.URL + "/authorize",
			TokenEndpoint:         server.URL + "/token",
			UserInfoEndpoint:      server.URL + "/userinfo",
		}); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	provider, err := DiscoverOpenIDConnectProvider(context.Background(), server.Client(), server.URL, "client", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name != "oidc" || provider.AuthorizeURL != server.URL+"/authorize" || provider.TokenURL != server.URL+"/token" || provider.UserInfoURL != server.URL+"/userinfo" {
		t.Fatalf("provider=%+v", provider)
	}
	if got := strings.Join(provider.Scopes, " "); got != "openid profile email" {
		t.Fatalf("scopes=%q", got)
	}
}

func TestNewLoginHandlerRejectsUnsupportedOrIncompleteProviders(t *testing.T) {
	service := service.Messages{Store: memory.New()}
	base := func(provider ProviderConfig) error {
		_, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{provider})
		return err
	}

	unsupported := ProviderConfig{Name: "custom", ClientID: "id", ClientSecret: "secret", AuthorizeURL: "https://example.test/authorize", TokenURL: "https://example.test/token", UserInfoURL: "https://example.test/user", Scopes: []string{"openid"}}
	if err := base(unsupported); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported provider error=%v", err)
	}
	githubWithoutEmail := unsupported
	githubWithoutEmail.Name = "github"
	if err := base(githubWithoutEmail); err == nil || !strings.Contains(err.Error(), "email endpoint") {
		t.Fatalf("incomplete GitHub error=%v", err)
	}
	withEmptyScope := unsupported
	withEmptyScope.Name = "google"
	withEmptyScope.Scopes = []string{"openid", " "}
	if err := base(withEmptyScope); err == nil || !strings.Contains(err.Error(), "scope entries") {
		t.Fatalf("empty scope error=%v", err)
	}
}

func TestGoogleAuthorizationLinksVerifiedMemberAndCreatesSession(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	service := service.Messages{Store: store}
	providerClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/token":
			return providerResponse(r, `{"access_token":"provider-token"}`), nil
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				return providerResponse(r, "missing token"), nil
			}
			return providerResponse(r, `{"sub":"google-subject","email":"alice@example.com","name":"Alice"}`), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}

	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "google", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://accounts.google.com/authorize", TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", Scopes: []string{"openid", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = providerClient
	mux := http.NewServeMux()
	handler.Register(mux)

	begin := httptest.NewRecorder()
	mux.ServeHTTP(begin, httptest.NewRequest(http.MethodGet, "/auth/google", nil))
	if begin.Code != http.StatusFound {
		t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	location, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	if state == "" || location.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization location=%s", location)
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=one-time-code&state="+url.QueryEscape(state), nil)
	for _, cookie := range begin.Result().Cookies() {
		callbackRequest.AddCookie(cookie)
	}
	callback := httptest.NewRecorder()
	mux.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/app" {
		t.Fatalf("callback status=%d location=%q body=%s", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}

	var sessionCookie *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("callback did not create a browser session cookie")
	}
	if sessionCookie.Domain != "example.test" {
		t.Fatalf("session cookie domain = %q", sessionCookie.Domain)
	}
	session, err := store.LookupSession(context.Background(), sessionCookie.Value)
	if err != nil || session.UserID != "U1" || session.WorkspaceID != "T1" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	identity, err := store.GetExternalIdentity(context.Background(), "T1", "google", "google-subject")
	if err != nil || identity.UserID != "U1" {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestShauthAuthorizationProvisionsAuthorizedIdentityAndCreatesSession(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"})
	service := service.Messages{Store: store}
	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/oauth2/token":
			return providerResponse(r, `{"access_token":"shauth-token"}`), nil
		case "/userinfo":
			return providerResponse(r, `{"sub":"shauth-subject","email":"alice@example.com","preferred_username":"alice","role":"admin"}`), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}
	mux := http.NewServeMux()
	handler.Register(mux)

	callback := completeAuthorization(t, mux, "oidc")
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/app" {
		t.Fatalf("callback status=%d location=%q body=%s", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}

	user, err := store.FindUserByEmail(context.Background(), "T1", "alice@example.com")
	if err != nil {
		t.Fatalf("provisioned user: %v", err)
	}
	membership, err := store.GetWorkspaceMembership(context.Background(), "T1", user.ID)
	if err != nil || membership.Role != domain.WorkspaceRoleAdmin || !membership.Active {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}
	identity, err := store.GetExternalIdentity(context.Background(), "T1", "oidc", "shauth-subject")
	if err != nil || identity.UserID != user.ID {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	if session := findSessionCookie(callback.Result().Cookies()); session == nil || session.Value == "" {
		t.Fatal("callback did not create a browser session cookie")
	}
}

func TestShauthAuthorizationRejectsIdentityWithoutSupportedRole(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"})
	handler, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/oauth2/token":
			return providerResponse(r, `{"access_token":"shauth-token"}`), nil
		case "/userinfo":
			return providerResponse(r, `{"sub":"shauth-subject","email":"alice@example.com","preferred_username":"alice"}`), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}
	mux := http.NewServeMux()
	handler.Register(mux)

	callback := completeAuthorization(t, mux, "oidc")
	if callback.Code != http.StatusForbidden {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	if _, err := store.FindUserByEmail(context.Background(), "T1", "alice@example.com"); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("untrusted identity provisioned a user: %v", err)
	}
}

func TestShauthAuthorizationSynchronizesLinkedWorkspaceRole(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	service := service.Messages{Store: store}
	if err := service.CreateExternalIdentity(context.Background(), domain.ExternalIdentity{WorkspaceID: "T1", Provider: "oidc", Subject: "shauth-subject", UserID: "U2"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetUserRole(context.Background(), "T1", "U1", "U2", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := handler.resolveIdentityUser(context.Background(), "oidc", externalIdentity{Subject: "shauth-subject", Email: "alice@example.com", Role: "developer"})
	if err != nil || user.ID != "U2" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	membership, err := store.GetWorkspaceMembership(context.Background(), "T1", "U2")
	if err != nil || membership.Role != domain.WorkspaceRoleMember {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}
}

func completeAuthorization(t *testing.T, handler http.Handler, provider string) *httptest.ResponseRecorder {
	t.Helper()
	begin := httptest.NewRecorder()
	handler.ServeHTTP(begin, httptest.NewRequest(http.MethodGet, "/auth/"+provider, nil))
	if begin.Code != http.StatusFound {
		t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	location, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	if state == "" || location.Query().Get("code_challenge") == "" {
		t.Fatalf("authorization location=%s", location)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/"+provider+"/callback?code=one-time-code&state="+url.QueryEscape(state), nil)
	for _, cookie := range begin.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func findSessionCookie(cookies []*http.Cookie) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == auth.SessionCookieName {
			return cookie
		}
	}
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func providerResponse(request *http.Request, body string) *http.Response {
	status := http.StatusOK
	statusText := "200 OK"
	if body == "missing token" || body == "not found" {
		status = http.StatusUnauthorized
		statusText = "401 Unauthorized"
	}
	return &http.Response{StatusCode: status, Status: statusText, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}
