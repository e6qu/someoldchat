package slack

import (
	"context"
	"encoding/json"
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

// installFixture is an app created from a manifest in workspace "Test", with
// the Web API mounted over the same service an install consents through.
type installFixture struct {
	t        *testing.T
	messages service.Messages
	store    *memory.Store
	mux      *http.ServeMux
	clientID string
	secret   string
	appID    domain.AppID
}

func newInstallFixture(t *testing.T, rotation bool) installFixture {
	t.Helper()
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
	rotating := "false"
	if rotation {
		rotating = "true"
	}
	manifest := `{"display_information":{"name":"Installer"},"oauth_config":{"redirect_urls":["https://app.example/oauth"],"scopes":{"bot":["chat:write"],"user":["search:read"]}},"settings":{"token_rotation_enabled":` + rotating + `}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStatic("unused", auth.Principal{WorkspaceID: "T1", UserID: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	return installFixture{t: t, messages: messages, store: repository, mux: mux, clientID: credentials.ClientID, secret: credentials.ClientSecret, appID: app.ID}
}

func (f installFixture) authorize(redirect string, botScopes, userScopes []string) domain.OAuthAuthorization {
	f.t.Helper()
	authorization, err := f.messages.AuthorizeOAuth(context.Background(), domain.OAuthAuthorizationRequest{
		ClientID: f.clientID, WorkspaceID: "T1", UserID: "U1", RedirectURI: redirect, BotScopes: botScopes, UserScopes: userScopes,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return authorization
}

func (f installFixture) call(method string, values url.Values) map[string]any {
	f.t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		f.t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		f.t.Fatal(err)
	}
	return body
}

// redemption is the form an app posts to redeem code, naming redirect only
// when it is not empty.
func (f installFixture) redemption(code, redirect string) url.Values {
	values := url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "code": {code}}
	if redirect != "" {
		values.Set("redirect_uri", redirect)
	}
	return values
}

// An install that let the app's single redirect URL stand in redeems without
// naming one, reports its team by name, and a reinstall keeps the bot the app
// already has, as Slack does.
func TestOAuthV2AccessInstallsAndReinstallsTheSameBot(t *testing.T) {
	f := newInstallFixture(t, false)
	first := f.call("oauth.v2.access", f.redemption(f.authorize("", []string{"chat:write"}, nil).Code, ""))
	team, _ := first["team"].(map[string]any)
	authedUser, _ := first["authed_user"].(map[string]any)
	if first["ok"] != true || first["token_type"] != "bot" || first["bot_user_id"] == "" || team["id"] != "T1" || team["name"] != "Test" || authedUser["id"] != "U1" {
		t.Fatalf("first install=%v", first)
	}
	second := f.call("oauth.v2.access", f.redemption(f.authorize("https://app.example/oauth", []string{"chat:write"}, []string{"search:read"}).Code, "https://app.example/oauth"))
	if second["ok"] != true || second["bot_user_id"] != first["bot_user_id"] {
		t.Fatalf("reinstall bot_user_id=%v, want %v: %v", second["bot_user_id"], first["bot_user_id"], second)
	}
	if bot, err := f.store.GetBotByApp(context.Background(), "T1", f.appID); err != nil || string(bot.UserID) != first["bot_user_id"] {
		t.Fatalf("app bot=%+v err=%v", bot, err)
	}
	legacy := f.call("oauth.access", url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "code": {f.authorize("", nil, []string{"search:read"}).Code}})
	if legacy["ok"] != true || legacy["team_name"] != "Test" || legacy["team_id"] != "T1" {
		t.Fatalf("oauth.access=%v", legacy)
	}
}

// A user-scope-only install is redeemable through oauth.v2.access: the
// installer's token under authed_user and no bot fields.
func TestOAuthV2AccessRedeemsAUserScopeOnlyInstall(t *testing.T) {
	f := newInstallFixture(t, false)
	body := f.call("oauth.v2.access", f.redemption(f.authorize("", nil, []string{"search:read"}).Code, ""))
	authedUser, _ := body["authed_user"].(map[string]any)
	token, _ := authedUser["access_token"].(string)
	if body["ok"] != true || authedUser["id"] != "U1" || authedUser["token_type"] != "user" || authedUser["scope"] != "search:read" || !strings.HasPrefix(token, "xoxp-") {
		t.Fatalf("user-only install=%v", body)
	}
	for _, field := range []string{"access_token", "bot_user_id", "token_type", "scope"} {
		if _, present := body[field]; present {
			t.Fatalf("user-only install carried top-level %s: %v", field, body)
		}
	}
	if team, _ := body["team"].(map[string]any); team["name"] != "Test" {
		t.Fatalf("user-only install team=%v", body["team"])
	}
	record, err := f.store.LookupToken(context.Background(), token)
	if err != nil || record.TokenType != domain.TokenUser || record.UserID != "U1" {
		t.Fatalf("issued token=%+v err=%v", record, err)
	}
}

// Every failure of oauth.v2.access names its own cause.
func TestOAuthV2AccessNamesEachFailure(t *testing.T) {
	f := newInstallFixture(t, false)
	directed := f.authorize("https://app.example/oauth", []string{"chat:write"}, nil).Code
	for _, testCase := range []struct {
		name   string
		values url.Values
		want   string
	}{
		{"wrong secret", url.Values{"client_id": {f.clientID}, "client_secret": {"wrong"}, "code": {directed}, "redirect_uri": {"https://app.example/oauth"}}, "bad_client_secret"},
		{"unknown client", url.Values{"client_id": {"nobody"}, "client_secret": {f.secret}, "code": {directed}}, "invalid_client_id"},
		{"omitted redirect", url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "code": {directed}}, "bad_redirect_uri"},
		{"different redirect", url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "code": {directed}, "redirect_uri": {"https://app.example/other"}}, "bad_redirect_uri"},
		{"unknown code", url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "code": {"code-nope"}}, "invalid_code"},
	} {
		if body := f.call("oauth.v2.access", testCase.values); body["ok"] != false || body["error"] != testCase.want {
			t.Fatalf("%s: %v, want %s", testCase.name, body, testCase.want)
		}
	}
	// None of those refusals spent the code.
	if body := f.call("oauth.v2.access", f.redemption(directed, "https://app.example/oauth")); body["ok"] != true {
		t.Fatalf("redemption after refusals=%v", body)
	}
}

// oauth.v2.exchange converts a legacy bot token and reports the member who
// installed the app as authed_user, not the bot user.
func TestOAuthV2ExchangeReportsTheInstaller(t *testing.T) {
	f := newInstallFixture(t, true)
	installed := f.call("oauth.v2.access", f.redemption(f.authorize("", []string{"chat:write"}, nil).Code, ""))
	botUser, _ := installed["bot_user_id"].(string)
	bot, err := f.store.GetBotByApp(context.Background(), "T1", f.appID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SeedToken(context.Background(), "xoxb-legacy", domain.TokenRecord{WorkspaceID: "T1", UserID: domain.UserID(botUser), AppID: f.appID, BotID: bot.ID, TokenType: domain.TokenBot, Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	body := f.call("oauth.v2.exchange", url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "token": {"xoxb-legacy"}})
	authedUser, _ := body["authed_user"].(map[string]any)
	if body["ok"] != true || body["bot_user_id"] != botUser || authedUser["id"] != "U1" {
		t.Fatalf("oauth.v2.exchange=%v", body)
	}
	if refused := f.call("oauth.v2.exchange", url.Values{"client_id": {f.clientID}, "client_secret": {"wrong"}, "token": {"xoxb-legacy"}}); refused["error"] != "bad_client_secret" {
		t.Fatalf("wrong secret=%v", refused)
	}
}
