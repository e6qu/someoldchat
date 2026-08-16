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

// TestOAuthAuthorizationOffersAndBindsAnIncomingWebhookChannel drives the
// install-time webhook flow through the browser consent screen: the app that
// asks for the incoming-webhook scope gets a channel picker, choosing one mints
// a working hook the oauth.v2.access exchange hands back, and the consent
// refuses a grant that names no channel or one the installer cannot reach.
func TestOAuthAuthorizationOffersAndBindsAnIncomingWebhookChannel(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversation(domain.Conversation{ID: "Cother", WorkspaceID: "T1", Name: "secret", Kind: domain.ConversationTypePrivate}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Hook App"},"oauth_config":{"redirect_urls":["https://client.example/callback"],"scopes":{"bot":["chat:write","incoming-webhook"]}},"settings":{"socket_mode_enabled":true}}`
	_, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
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

	// The consent screen offers a channel picker for the webhook scope.
	query := url.Values{"client_id": {credentials.ClientID}, "redirect_uri": {"https://client.example/callback"}, "scope": {"chat:write,incoming-webhook"}, "state": {"opaque-state"}, "response_type": {"code"}}
	request := httptest.NewRequest(http.MethodGet, "https://chat.example/oauth/v2/authorize?"+query.Encode(), nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if body := response.Body.String(); response.Code != http.StatusOK || !strings.Contains(body, "Where its webhook posts") || !strings.Contains(body, `name="incoming_webhook_channel"`) || !strings.Contains(body, `value="C1"`) {
		t.Fatalf("consent status=%d body=%s", response.Code, response.Body.String())
	}

	approve := func(channel string) *httptest.ResponseRecorder {
		form := url.Values{"_csrf": {auth.CSRFToken(session)}, "decision": {"approve"}, "client_id": {credentials.ClientID}, "redirect_uri": {"https://client.example/callback"}, "scope": {"chat:write,incoming-webhook"}, "state": {"opaque-state"}}
		if channel != "" {
			form.Set("incoming_webhook_channel", channel)
		}
		post := httptest.NewRequest(http.MethodPost, "https://chat.example/oauth/v2/authorize", strings.NewReader(form.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Sec-Fetch-Site", "same-origin")
		post.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, post)
		return rec
	}

	// A grant that names no channel, or one the installer is not in, is refused
	// rather than minting a hook they could not have posted to by hand.
	if rec := approve(""); rec.Code == http.StatusFound {
		t.Fatal("a webhook install with no channel was approved")
	}
	if rec := approve("Cother"); rec.Code == http.StatusFound {
		t.Fatal("a webhook install to a channel the installer cannot reach was approved")
	}

	// The real grant carries the chosen channel through to a minted hook.
	rec := approve("C1")
	if rec.Code != http.StatusFound {
		t.Fatalf("approval status=%d body=%s", rec.Code, rec.Body.String())
	}
	redirect, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := redirect.Query().Get("code")
	if code == "" {
		t.Fatalf("approval redirect carried no code: %s", redirect)
	}
	token, err := messages.OAuthV2Exchange(ctx, credentials.ClientID, credentials.ClientSecret, code, "https://client.example/callback", false)
	if err != nil {
		t.Fatal(err)
	}
	if token.IncomingWebhookURL == "" || token.IncomingWebhookChannel != "C1" || token.IncomingWebhookChannelName != "general" {
		t.Fatalf("exchanged token webhook fields = %+v", token)
	}
	if member, _ := repository.IsConversationMember(ctx, "C1", token.UserID); !member {
		t.Fatal("the app bot was not added to the webhook channel")
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
