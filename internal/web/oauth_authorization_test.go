package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestOAuthAuthorizationConsentCreatesRedeemableBotGrant(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"OAuth App"},"oauth_config":{"redirect_urls":["https://client.example/callback"],"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	session := "browser-session"
	if err := repository.SeedSession(ctx, session, domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	browser, err := auth.NewBrowser(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, browser, repository, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	query := url.Values{"client_id": {credentials.ClientID}, "redirect_uri": {"https://client.example/callback"}, "scope": {"chat:write"}, "state": {"opaque-state"}, "response_type": {"code"}}
	request := httptest.NewRequest(http.MethodGet, "https://chat.example/oauth/v2/authorize?"+query.Encode(), nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Authorize OAuth App") || !strings.Contains(response.Body.String(), "chat:write") {
		t.Fatalf("consent status=%d body=%s", response.Code, response.Body.String())
	}
	// The consent screen explains the scope, not just names it: the person
	// reads "Send messages" beside the raw chat:write token they are granting.
	if body := response.Body.String(); !strings.Contains(body, "Send messages") || !strings.Contains(body, `<code class="scope-token">chat:write</code>`) {
		t.Fatalf("consent screen did not explain the requested scope: %s", body)
	}

	form := url.Values{
		"_csrf":        {auth.CSRFToken(session)},
		"decision":     {"approve"},
		"client_id":    {credentials.ClientID},
		"redirect_uri": {"https://client.example/callback"},
		"scope":        {"chat:write"},
		"state":        {"opaque-state"},
	}
	request = httptest.NewRequest(http.MethodPost, "https://chat.example/oauth/v2/authorize", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("approval status=%d body=%s", response.Code, response.Body.String())
	}
	redirect, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := redirect.Query().Get("code")
	if redirect.Scheme != "https" || redirect.Host != "client.example" || code == "" || redirect.Query().Get("state") != "opaque-state" {
		t.Fatalf("approval redirect=%s", redirect)
	}
	token, err := messages.OAuthV2Exchange(ctx, credentials.ClientID, credentials.ClientSecret, code, "https://client.example/callback", false)
	if err != nil {
		t.Fatal(err)
	}
	if token.AppID != app.ID || token.BotID == "" || token.TokenType != "bot" || token.InstallerID != "U1" {
		t.Fatalf("oauth token=%+v", token)
	}
}

func TestOAuthAuthorizationRejectsUnregisteredRedirectWithoutRedirecting(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	_ = repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	_ = repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	messages := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	_, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, `{"display_information":{"name":"OAuth App"},"oauth_config":{"redirect_urls":["https://client.example/callback"],"scopes":{"bot":["chat:write"]}},"settings":{"socket_mode_enabled":true}}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedSession(ctx, "session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	browser, _ := auth.NewBrowser(repository)
	handler, _ := NewHandler(messages, browser, repository, "C1", "")
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "https://chat.example/oauth/v2/authorize?client_id="+url.QueryEscape(credentials.ClientID)+"&redirect_uri=https%3A%2F%2Fattacker.example%2Fcallback&scope=chat%3Awrite", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Location") != "" {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}
