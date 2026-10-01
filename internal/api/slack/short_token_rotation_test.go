package slack

import (
	"bytes"
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

type shortTokenFixture struct {
	t        *testing.T
	store    *memory.Store
	mux      *http.ServeMux
	clientID string
	secret   string
	appID    domain.AppID
	// issued is the user token oauth.v2.access issued for the app's install.
	issued string
}

// newShortTokenFixture installs an app with a user scope through
// oauth.v2.access, so the token rotated is one this deployment issued, and
// authenticates every request against the store, as production does.
func newShortTokenFixture(t *testing.T) shortTokenFixture {
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
	manifest := `{"display_information":{"name":"Rotator"},"oauth_config":{"redirect_urls":["https://app.example/oauth"],"scopes":{"user":["search:read"]}}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewStored(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	f := shortTokenFixture{t: t, store: repository, mux: mux, clientID: credentials.ClientID, secret: credentials.ClientSecret, appID: app.ID}
	authorization, err := messages.AuthorizeOAuth(ctx, domain.OAuthAuthorizationRequest{ClientID: f.clientID, WorkspaceID: "T1", UserID: "U1", UserScopes: []string{"search:read"}})
	if err != nil {
		t.Fatal(err)
	}
	installed := f.form("oauth.v2.access", "", url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}, "code": {authorization.Code}})
	authedUser, _ := installed["authed_user"].(map[string]any)
	f.issued, _ = authedUser["access_token"].(string)
	if !strings.HasPrefix(f.issued, "xoxp-") {
		t.Fatalf("install issued no user token: %v", installed)
	}
	return f
}

func (f shortTokenFixture) send(request *http.Request) map[string]any {
	f.t.Helper()
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		f.t.Fatalf("%s status=%d body=%s", request.URL.Path, response.Code, response.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		f.t.Fatal(err)
	}
	return body
}

// form posts form-encoded arguments, with the bearer token in the
// Authorization header when one is given.
func (f shortTokenFixture) form(method, bearer string, values url.Values) map[string]any {
	f.t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	return f.send(request)
}

func (f shortTokenFixture) json(method, bearer string, arguments map[string]string) map[string]any {
	f.t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		f.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Authorization", "Bearer "+bearer)
	return f.send(request)
}

func (f shortTokenFixture) credentials() url.Values {
	return url.Values{"client_id": {f.clientID}, "client_secret": {f.secret}}
}

func wantError(t *testing.T, body map[string]any, code string) {
	t.Helper()
	if body["ok"] != false || body["error"] != code {
		t.Fatalf("response=%v, want error %s", body, code)
	}
}

// TestShortTokenRotationReplacesTheSecretOnlyWhenCompleted walks the
// reference's flow end to end: begin answers new_token, which does not
// authenticate until complete answers it as token, after which the original
// is invalid and the replacement is the same credential with a 32-character
// secret. Begin is form-encoded with the token in the header, complete is a
// JSON body, and both answer the declared codes for every refusal.
func TestShortTokenRotationReplacesTheSecretOnlyWhenCompleted(t *testing.T) {
	f := newShortTokenFixture(t)
	original := f.issued

	began := f.form("oauth.v2.beginShortTokenRotation", original, f.credentials())
	replacement, _ := began["new_token"].(string)
	kept, secret, _ := domain.TokenSecret(replacement)
	originalKept, _, _ := domain.TokenSecret(original)
	if began["ok"] != true || kept != originalKept || len(secret) != 32 || len(began) != 2 {
		t.Fatalf("begin=%v", began)
	}
	// "This change does not take effect until you complete the flow."
	wantError(t, f.form("auth.test", replacement, nil), "invalid_auth")
	if tested := f.form("auth.test", original, nil); tested["ok"] != true {
		t.Fatalf("original stopped working before completion: %v", tested)
	}
	wantError(t, f.json("oauth.v2.completeShortTokenRotation", original, map[string]string{
		"client_id": f.clientID, "client_secret": f.secret, "new_token": originalKept + strings.Repeat("0", 32),
	}), "invalid_token")
	wantError(t, f.json("oauth.v2.completeShortTokenRotation", original, map[string]string{"client_id": f.clientID, "client_secret": f.secret}), "invalid_arguments")

	completed := f.json("oauth.v2.completeShortTokenRotation", original, map[string]string{
		"client_id": f.clientID, "client_secret": f.secret, "new_token": replacement,
	})
	if completed["ok"] != true || completed["token"] != replacement || len(completed) != 2 {
		t.Fatalf("complete=%v", completed)
	}
	wantError(t, f.form("auth.test", original, nil), "invalid_auth")
	tested := f.form("auth.test", replacement, nil)
	if tested["ok"] != true || tested["user_id"] != "U1" || tested["team_id"] != "T1" {
		t.Fatalf("auth.test with the replacement=%v", tested)
	}
	record, err := f.store.LookupToken(context.Background(), replacement)
	if err != nil || record.AppID != f.appID || len(record.Scopes) != 1 || record.Scopes[0] != "search:read" {
		t.Fatalf("replacement record=%+v err=%v", record, err)
	}
	// A one-time rotation: the replacement's secret is already long.
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", replacement, f.credentials()), "token_too_long")
	// The original is gone, so it no longer authenticates the completion.
	wantError(t, f.json("oauth.v2.completeShortTokenRotation", original, map[string]string{
		"client_id": f.clientID, "client_secret": f.secret, "new_token": replacement,
	}), "invalid_auth")
}

func TestShortTokenRotationAnswersEachDeclaredRefusal(t *testing.T) {
	f := newShortTokenFixture(t)
	ctx := context.Background()
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", "", f.credentials()), "not_authed")
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", "xoxp-unknown-token", f.credentials()), "invalid_auth")
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", f.issued, url.Values{"client_id": {"nobody"}, "client_secret": {f.secret}}), "invalid_client_id")
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", f.issued, url.Values{"client_secret": {f.secret}}), "invalid_client_id")
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", f.issued, url.Values{"client_id": {f.clientID}, "client_secret": {"wrong"}}), "bad_client_secret")
	// The token argument authenticates as well as the header does.
	values := f.credentials()
	values.Set("token", f.issued)
	if began := f.form("oauth.v2.beginShortTokenRotation", "", values); began["ok"] != true {
		t.Fatalf("begin with the token argument=%v", began)
	}
	// A completion with nothing begun for this token.
	if err := f.store.SeedToken(ctx, "xoxp-111-222-333-d6bc76", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: f.appID, TokenType: domain.TokenUser, Scopes: []string{"search:read"}}); err != nil {
		t.Fatal(err)
	}
	completion := f.credentials()
	completion.Set("new_token", "xoxp-111-222-333-"+strings.Repeat("0", 32))
	wantError(t, f.form("oauth.v2.completeShortTokenRotation", "xoxp-111-222-333-d6bc76", completion), "rotation_not_found")
	// Another app's token: client_id "is not the app the token being rotated
	// was issued to".
	if err := f.store.SeedToken(ctx, "xoxp-444-555-666-abcdef", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A-other", TokenType: domain.TokenUser, Scopes: []string{"search:read"}}); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", "xoxp-444-555-666-abcdef", f.credentials()), "invalid_client_id")
	// Only an xoxp user token is rotated.
	if err := f.store.SeedToken(ctx, "xoxb-444-555-abcdef", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: f.appID, BotID: "B1", TokenType: domain.TokenBot, Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", "xoxb-444-555-abcdef", f.credentials()), "not_allowed_token_type")
	// A revoked token is answered by authentication.
	if err := f.store.RevokeToken(ctx, "xoxp-111-222-333-d6bc76"); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.form("oauth.v2.beginShortTokenRotation", "xoxp-111-222-333-d6bc76", f.credentials()), "token_revoked")
	// GET is served as POST is.
	request := httptest.NewRequest(http.MethodGet, "/api/oauth.v2.beginShortTokenRotation?"+f.credentials().Encode(), nil)
	request.Header.Set("Authorization", "Bearer "+f.issued)
	if began := f.send(request); began["ok"] != true {
		t.Fatalf("GET begin=%v", began)
	}
}
