package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestNewLoginHandlerAcceptsSupportedAuthorizationProviders(t *testing.T) {
	service := service.Messages{Store: memory.New()}
	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{
		{Name: "Google", ClientID: "google-id", ClientSecret: "google-secret", AuthorizeURL: "https://accounts.google.com/authorize", TokenURL: "https://oauth2.googleapis.com/token", UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo", Scopes: []string{"openid", "email"}},
		{Name: "github", ClientID: "github-id", ClientSecret: "github-secret", AuthorizeURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", UserInfoURL: "https://api.github.com/user", EmailURL: "https://api.github.com/user/emails", Scopes: []string{"user:email"}},
		{Name: "entra", ClientID: "entra-id", ClientSecret: "entra-secret", AuthorizeURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize", TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", UserInfoURL: "https://graph.microsoft.com/oidc/userinfo", Scopes: []string{"openid", "email"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.providers) != 3 {
		t.Fatalf("providers=%d, want 3", len(handler.providers))
	}
	if got := handler.providers["google"].Scopes; len(got) != 2 || got[0] != "openid" || got[1] != "email" {
		t.Fatalf("normalized Google scopes=%v", got)
	}
}

func TestLoginPageSupportsThemeSelection(t *testing.T) {
	handler, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "google", ClientID: "id", ClientSecret: "secret", AuthorizeURL: "https://provider.test/authorize", TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", Scopes: []string{"openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))
	body := response.Body.String()
	for _, required := range []string{`data-theme="light"`, `id="theme-toggle"`, "localStorage", "data-theme=dark", "Continue with Google"} {
		if !strings.Contains(body, required) {
			t.Fatalf("login page missing %q: %s", required, body)
		}
	}
}

func TestNewLoginHandlerRejectsUnsupportedOrIncompleteProviders(t *testing.T) {
	service := service.Messages{Store: memory.New()}
	base := func(provider ProviderConfig) error {
		_, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{provider})
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

func TestNewLoginHandlerRejectsInsecureOrMalformedURLs(t *testing.T) {
	service := service.Messages{Store: memory.New()}
	provider := ProviderConfig{Name: "google", ClientID: "id", ClientSecret: "secret", AuthorizeURL: "https://provider.test/authorize", TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", Scopes: []string{"openid"}}
	if _, err := NewLoginHandler(service, "T1", "U1", "http://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{provider}); err == nil {
		t.Fatal("insecure public URL was accepted")
	}
	provider.TokenURL = "http://provider.test/token"
	if _, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{provider}); err == nil {
		t.Fatal("insecure token endpoint was accepted")
	}
	provider.TokenURL = "https://provider.test/token"
	if _, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test/login?next=/app", []byte(strings.Repeat("k", 32)), []ProviderConfig{provider}); err == nil {
		t.Fatal("public URL with a query was accepted")
	}
}

func TestGoogleAuthorizationLinksVerifiedMemberAndCreatesSession(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	service := service.Messages{Store: store}
	if err := service.SetAuthMethod(context.Background(), domain.AuthMethod{WorkspaceID: "T1", Provider: "google", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	providerClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/token":
			return providerResponse(r, `{"access_token":"provider-token"}`), nil
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				return providerResponse(r, "missing token"), nil
			}
			return providerResponse(r, `{"sub":"google-subject","email":"alice@example.com","email_verified":true,"name":"Alice"}`), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}

	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
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
	session, err := store.LookupSession(context.Background(), sessionCookie.Value)
	if err != nil || session.UserID != "U1" || session.WorkspaceID != "T1" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	identity, err := store.GetExternalIdentity(context.Background(), "T1", "google", "google-subject")
	if err != nil || identity.UserID != "U1" {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestGoogleUserInfoRejectsUnverifiedEmail(t *testing.T) {
	handler, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "google", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://provider.test/authorize", TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", Scopes: []string{"openid", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return providerResponse(r, `{"sub":"google-subject","email":"alice@example.com","email_verified":false}`), nil
	})}
	if _, err := handler.userInfo(context.Background(), handler.providers["google"], "provider-token", "google"); err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("userInfo error=%v, want unverified email error", err)
	}
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
