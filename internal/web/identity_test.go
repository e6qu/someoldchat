package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	storepkg "github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestDecodeAuthorizationJSONBoundsExternalResponses(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		limit int64
		want  string
	}{
		{name: "within limit", body: `{"access_token":"token"}`, limit: 64, want: ""},
		{name: "over limit", body: `{"access_token":"token"}`, limit: 10, want: "exceeds"},
		{name: "invalid JSON", body: `{`, limit: 64, want: "unexpected end of JSON input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value tokenResponse
			err := decodeAuthorizationJSON(strings.NewReader(test.body), test.limit, &value)
			if test.want == "" {
				if err != nil {
					t.Fatalf("decode error=%v", err)
				}
				if value.AccessToken != "token" {
					t.Fatalf("decoded value=%+v", value)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestOpenIDConnectBackchannelLogoutVerifiesTokenAndRevokesSessions(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "logout-key"
	var issuer *httptest.Server
	issuer = newIPv4TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/oauth2/auth", "token_endpoint": issuer.URL + "/oauth2/token", "userinfo_endpoint": issuer.URL + "/userinfo", "jwks_uri": issuer.URL + "/.well-known/jwks.json"})
		case "/.well-known/jwks.json":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()

	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "person@example.com", Name: "person"})
	service := service.Messages{Store: store}
	for _, token := range []string{"browser-session", "second-browser-session"} {
		if err := service.CreateSession(context.Background(), token, domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", Scopes: memberSessionScopes(t), ExpiresAt: time.Now().Add(time.Hour), OIDCProvider: "oidc", OIDCSubject: "oidc-subject", OIDCSID: "oidc-session"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.CreateSession(context.Background(), "other-provider-session", domain.SessionRecord{WorkspaceID: "T1", UserID: "U2", Scopes: memberSessionScopes(t), ExpiresAt: time.Now().Add(time.Hour), OIDCProvider: "oidc", OIDCSubject: "oidc-subject", OIDCSID: "other-session"}); err != nil {
		t.Fatal(err)
	}
	// The provider is assembled through discovery, exactly as cmd/server does, so
	// the logout-token verifier is the RS256-pinned one built there. A hand-built
	// ProviderConfig has no verifier and now fails closed by design.
	discovered, err := DiscoverOpenIDConnectProvider(context.Background(), issuer.Client(), issuer.URL, "sameoldchat", "secret")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{discovered})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = issuer.Client()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("logout+jwt").WithHeader("kid", keyID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": issuer.URL, "aud": "sameoldchat", "sid": "oidc-session", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": "logout-id",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}, "https://example.test/other-event": map[string]any{}},
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"logout_token": {raw}, "unrecognized_parameter": {"ignored"}}
	request := httptest.NewRequest(http.MethodPost, "/auth/oidc/backchannel-logout", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.backchannelLogout(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, sessionToken := range []string{"browser-session", "second-browser-session"} {
		record, err := store.LookupSession(context.Background(), sessionToken)
		if err != nil || !record.Revoked {
			t.Fatalf("session %q=%+v err=%v", sessionToken, record, err)
		}
	}
	other, err := store.LookupSession(context.Background(), "other-provider-session")
	if err != nil || other.Revoked {
		t.Fatalf("unrelated provider session=%+v err=%v", other, err)
	}
	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/auth/oidc/backchannel-logout", strings.NewReader(form.Encode()))
	replayRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.backchannelLogout(replay, replayRequest)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed logout token status=%d body=%s", replay.Code, replay.Body.String())
	}
	subjectOnly, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": issuer.URL, "aud": "sameoldchat", "sub": "oidc-subject", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": "subject-logout-id",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	subjectRequest := httptest.NewRequest(http.MethodPost, "/auth/oidc/backchannel-logout", strings.NewReader(url.Values{"logout_token": {subjectOnly}}.Encode()))
	subjectRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	subjectResponse := httptest.NewRecorder()
	handler.backchannelLogout(subjectResponse, subjectRequest)
	if subjectResponse.Code != http.StatusOK {
		t.Fatalf("subject-only logout status=%d body=%s", subjectResponse.Code, subjectResponse.Body.String())
	}
	other, err = store.LookupSession(context.Background(), "other-provider-session")
	if err != nil || !other.Revoked {
		t.Fatalf("subject-matched provider session=%+v err=%v", other, err)
	}

	invalid, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": issuer.URL, "aud": "sameoldchat", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": "invalid-logout-id",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.verifyBackchannelLogout(context.Background(), invalid); err == nil {
		t.Fatal("logout token without a subject or provider session ID was accepted")
	}
	invalidEvent, err := jwt.Signed(signer).Claims(map[string]any{
		"iss": issuer.URL, "aud": "sameoldchat", "sub": "oidc-subject", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": "invalid-event-id",
		"events": map[string]any{backchannelLogoutEvent: "not-an-object"},
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.verifyBackchannelLogout(context.Background(), invalidEvent); err == nil {
		t.Fatal("logout token with a non-object event was accepted")
	}
}

func TestOpenIDConnectBackchannelLogoutRejectsNonCanonicalTokenDelivery(t *testing.T) {
	handler, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		target      string
		contentType string
		body        string
		want        int
	}{
		{name: "JSON body", target: "/auth/oidc/backchannel-logout", contentType: "application/json", body: `{"logout_token":"token"}`, want: http.StatusUnsupportedMediaType},
		{name: "query token", target: "/auth/oidc/backchannel-logout?logout_token=token", contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "query and body token", target: "/auth/oidc/backchannel-logout?logout_token=query", contentType: "application/x-www-form-urlencoded", body: "logout_token=body", want: http.StatusBadRequest},
		{name: "duplicate body token", target: "/auth/oidc/backchannel-logout", contentType: "application/x-www-form-urlencoded", body: "logout_token=one&logout_token=two", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.backchannelLogout(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

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
	server = newIPv4TLSServer(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
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
			JWKSURI:               server.URL + "/jwks",
			EndSessionEndpoint:    server.URL + "/logout",
		}); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	provider, err := DiscoverOpenIDConnectProvider(context.Background(), server.Client(), server.URL, "client", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name != "oidc" || provider.AuthorizeURL != server.URL+"/authorize" || provider.TokenURL != server.URL+"/token" || provider.UserInfoURL != server.URL+"/userinfo" || provider.EndSessionURL != server.URL+"/logout" {
		t.Fatalf("provider=%+v", provider)
	}
	if got := strings.Join(provider.Scopes, " "); got != "openid profile email" {
		t.Fatalf("scopes=%q", got)
	}
}

func TestOpenIDConnectCallbackBindsNonceAndProviderSessionLifetime(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "login-key"
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", keyID))
	if err != nil {
		t.Fatal(err)
	}
	var expectedNonce atomic.Value
	providerExpiry := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	var issuer *httptest.Server
	issuer = newIPv4TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(OpenIDConfiguration{Issuer: issuer.URL, AuthorizationEndpoint: issuer.URL + "/authorize", TokenEndpoint: issuer.URL + "/token", UserInfoEndpoint: issuer.URL + "/userinfo", JWKSURI: issuer.URL + "/jwks", EndSessionEndpoint: issuer.URL + "/logout"})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig"}}})
		case "/token":
			nonceValue, ok := expectedNonce.Load().(string)
			if !ok || nonceValue == "" {
				http.Error(w, "authorization nonce was not captured", http.StatusBadRequest)
				return
			}
			now := time.Now().UTC()
			value, signErr := jwt.Signed(signer).Claims(map[string]any{"iss": issuer.URL, "aud": "sameoldchat", "sub": "sha-auth-subject", "sid": "sha-auth-session", "nonce": nonceValue, "iat": now.Unix(), "exp": providerExpiry.Unix()}).Serialize()
			if signErr != nil {
				http.Error(w, "ID token signing failed", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "access-token", "id_token": value})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": "sha-auth-subject", "email": "developer@example.test", "email_verified": "true", "name": "Developer", "role": "developer"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	provider, err := DiscoverOpenIDConnectProvider(context.Background(), issuer.Client(), issuer.URL, "sameoldchat", "secret")
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.test", Name: "admin"})
	handler, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{provider})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = issuer.Client()
	mux := http.NewServeMux()
	handler.Register(mux)
	begin := httptest.NewRecorder()
	mux.ServeHTTP(begin, httptest.NewRequest(http.MethodGet, "/auth/oidc", nil))
	location, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	nonceValue := location.Query().Get("nonce")
	expectedNonce.Store(nonceValue)
	state := location.Query().Get("state")
	if begin.Code != http.StatusFound || nonceValue == "" || state == "" {
		t.Fatalf("authorization response=%d location=%s", begin.Code, location)
	}
	callbackRequest := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=code&state="+url.QueryEscape(state), nil)
	for _, cookie := range begin.Result().Cookies() {
		callbackRequest.AddCookie(cookie)
	}
	callback := httptest.NewRecorder()
	mux.ServeHTTP(callback, callbackRequest)
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/app" {
		t.Fatalf("callback status=%d location=%q body=%s", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}
	sessionCookie := findSessionCookie(callback.Result().Cookies())
	if sessionCookie == nil || sessionCookie.MaxAge < 240 || sessionCookie.MaxAge > 300 {
		t.Fatalf("session cookie=%+v, want provider-bounded lifetime", sessionCookie)
	}
	record, err := store.LookupSession(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if record.OIDCProvider != "oidc" || record.OIDCSubject != "sha-auth-subject" || record.OIDCSID != "sha-auth-session" || record.OIDCIDToken == "" || !record.ExpiresAt.Equal(providerExpiry) {
		t.Fatalf("durable session=%+v, want provider identity and expiry %s", record, providerExpiry)
	}
	if _, _, _, err := handler.verifyOIDCLoginToken(context.Background(), provider, record.OIDCIDToken, "wrong-nonce"); err == nil {
		t.Fatal("ID token with a mismatched authorization nonce was accepted")
	}
}

func TestOIDCLogoutRedirectUsesDurableSessionMetadata(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	if err := store.CreateSession(context.Background(), "session", domain.SessionRecord{
		WorkspaceID: "T1", UserID: "U1", Scopes: memberSessionScopes(t), ExpiresAt: time.Now().UTC().Add(time.Hour),
		OIDCProvider: "oidc", OIDCIDToken: "signed.id.token", OIDCSubject: "subject", OIDCSID: "provider-session",
	}); err != nil {
		t.Fatal(err)
	}
	service := service.Messages{Store: store}
	login, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", EndSessionURL: "https://auth.example.test/oauth2/sessions/logout", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Scheme != "https" || location.Host != "auth.example.test" || location.Path != "/oauth2/sessions/logout" || location.Query().Get("id_token_hint") != "signed.id.token" || location.Query().Get("client_id") != "sameoldchat" || location.Query().Get("post_logout_redirect_uri") != "https://chat.example.test/auth/shauth/logout/complete" {
		t.Fatalf("logout redirect=%s", location)
	}
	record, err := store.LookupSession(context.Background(), "session")
	if err != nil || !record.Revoked {
		t.Fatalf("session=%+v err=%v", record, err)
	}
}

func TestShauthLogoutCompletionBridgeUsesOnlyTheIssuerCompletionEndpoint(t *testing.T) {
	login, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", EndSessionURL: "https://auth.example.test/oauth2/sessions/logout", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	login.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "https://chat.example.test/auth/shauth/logout/complete?next=https%3A%2F%2Fattacker.invalid%2F&redirect_uri=https%3A%2F%2Fattacker.invalid%2F", nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "https://auth.example.test/oauth/logout/complete" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("completion status=%d location=%q headers=%v", response.Code, response.Header().Get("Location"), response.Header())
	}
}

func TestDiscoverOpenIDConnectProviderAllowsOnlyLoopbackHTTPDevelopmentCoordinates(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(OpenIDConfiguration{Issuer: server.URL, AuthorizationEndpoint: server.URL + "/authorize", TokenEndpoint: server.URL + "/token", UserInfoEndpoint: server.URL + "/userinfo", JWKSURI: server.URL + "/jwks", EndSessionEndpoint: server.URL + "/logout"})
	}))
	defer server.Close()
	provider, err := DiscoverOpenIDConnectProvider(context.Background(), server.Client(), server.URL, "client", "secret")
	if err != nil || provider.Issuer != server.URL {
		t.Fatalf("loopback provider=%+v err=%v", provider, err)
	}
	if _, err := DiscoverOpenIDConnectProvider(context.Background(), server.Client(), "http://identity.example.test", "client", "secret"); err == nil {
		t.Fatal("non-loopback HTTP issuer was accepted")
	}
}

func TestSignedOutPageStaysOnApplicationOriginAndDoesNotRestartSSO(t *testing.T) {
	handler := Handler{Login: &LoginHandler{providers: map[string]ProviderConfig{
		"oidc": {Name: "oidc", Issuer: "https://auth.example.test"},
	}}}
	request := httptest.NewRequest(http.MethodGet, "https://chat.example.test/signed-out", nil)
	response := httptest.NewRecorder()
	handler.signedOut(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Location") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d location=%q headers=%v", response.Code, response.Header().Get("Location"), response.Header())
	}
	body := response.Body.String()
	if !strings.Contains(body, "You’re signed out") || !strings.Contains(body, `href="/auth/oidc">Sign in with Shauth</a>`) || strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatalf("signed-out page=%s", body)
	}
	failure := httptest.NewRecorder()
	handler.signedOut(failure, httptest.NewRequest(http.MethodGet, "https://chat.example.test/signed-out?global=failed", nil))
	if failure.Code != http.StatusServiceUnavailable || !strings.Contains(failure.Body.String(), "could not complete global sign-out") {
		t.Fatalf("global logout failure status=%d body=%s", failure.Code, failure.Body.String())
	}

	nonShauth := Handler{Login: &LoginHandler{providers: map[string]ProviderConfig{
		"github": {Name: "github"},
	}}}
	other := httptest.NewRecorder()
	nonShauth.signedOut(other, request)
	if other.Code != http.StatusOK || !strings.Contains(other.Body.String(), `href="/login">Choose a sign-in method</a>`) || strings.Contains(other.Body.String(), `/auth/oidc`) {
		t.Fatalf("non-Shauth signed-out page=%s", other.Body.String())
	}
}

func TestLoginPageListsOnlyEnabledProviders(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1"})
	login, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{
		{Name: "google", ClientID: "google-client", ClientSecret: "secret", AuthorizeURL: "https://accounts.google.com/authorize", TokenURL: "https://accounts.google.com/token", UserInfoURL: "https://accounts.google.com/userinfo", Scopes: []string{"openid", "email"}},
		{Name: "github", ClientID: "github-client", ClientSecret: "secret", AuthorizeURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", UserInfoURL: "https://api.github.com/user", EmailURL: "https://api.github.com/user/emails", Scopes: []string{"user:email"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuthMethod(context.Background(), domain.AuthMethod{WorkspaceID: "T1", Provider: "google", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	login.login(response, httptest.NewRequest(http.MethodGet, "https://chat.example.test/login", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `href="/auth/github">Continue with GitHub</a>`) || strings.Contains(body, `/auth/google`) {
		t.Fatalf("partially enabled login page status=%d body=%s", response.Code, body)
	}
	if !strings.Contains(body, `<meta name="color-scheme" content="light dark">`) {
		t.Fatalf("login page does not support the reader's color scheme: %s", body)
	}

	if err := store.SetAuthMethod(context.Background(), domain.AuthMethod{WorkspaceID: "T1", Provider: "github", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRecorder()
	login.login(disabled, httptest.NewRequest(http.MethodGet, "https://chat.example.test/login", nil))
	if disabled.Code != http.StatusServiceUnavailable || !strings.Contains(disabled.Body.String(), "No sign-in methods are enabled") || strings.Contains(disabled.Body.String(), `class="provider"`) {
		t.Fatalf("fully disabled login page status=%d body=%s", disabled.Code, disabled.Body.String())
	}
}

func TestOIDCLogoutRedirectFailsClosedForIncompleteProviderMetadata(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	if err := store.CreateSession(context.Background(), "session", domain.SessionRecord{
		WorkspaceID: "T1", UserID: "U1", Scopes: memberSessionScopes(t), ExpiresAt: time.Now().UTC().Add(time.Hour),
		OIDCProvider: "oidc", OIDCIDToken: "signed.id.token",
	}); err != nil {
		t.Fatal(err)
	}
	service := service.Messages{Store: store}
	login, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := login.logoutRedirectURL(context.Background(), "session"); err == nil || !strings.Contains(err.Error(), "end-session endpoint") {
		t.Fatalf("logout redirect error=%v", err)
	}
	authenticator, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, authenticator, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	addBrowserCookies(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/signed-out?global=failed" {
		t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	record, err := store.LookupSession(context.Background(), "session")
	if err != nil || !record.Revoked {
		t.Fatalf("session=%+v err=%v", record, err)
	}
}

// memberSessionScopes is the scope set a signed-in member is entitled to. Tests
// that only need an authenticated browser use it instead of auth.AllScopes(): a
// browser session is never minted with the control plane, so a fixture that grants
// it is testing a session the product cannot issue.
func memberSessionScopes(t *testing.T) []string {
	t.Helper()
	scopes, err := auth.ScopesForWorkspaceRole(domain.WorkspaceRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	return scopes.Values()
}

func newIPv4TLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
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
			return providerResponse(r, `{"sub":"google-subject","email":"alice@example.com","email_verified":true,"name":"Alice"}`), nil
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

func TestOIDCAuthorizationProvisionsAuthorizedIdentityAndCreatesSession(t *testing.T) {
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
			return providerResponse(r, `{"access_token":"oidc-token"}`), nil
		case "/userinfo":
			return providerResponse(r, `{"sub":"oidc-subject","email":"alice@example.com","email_verified":true,"preferred_username":"alice","role":"admin"}`), nil
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
	identity, err := store.GetExternalIdentity(context.Background(), "T1", "oidc", "oidc-subject")
	if err != nil || identity.UserID != user.ID {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	if session := findSessionCookie(callback.Result().Cookies()); session == nil || session.Value == "" {
		t.Fatal("callback did not create a browser session cookie")
	}
}

func TestOIDCAuthorizationRejectsIdentityWithoutSupportedRole(t *testing.T) {
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
			return providerResponse(r, `{"access_token":"oidc-token"}`), nil
		case "/userinfo":
			return providerResponse(r, `{"sub":"oidc-subject","email":"alice@example.com","email_verified":true,"preferred_username":"alice"}`), nil
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

func TestOIDCAuthorizationSynchronizesLinkedWorkspaceRole(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.com", Name: "admin"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	service := service.Messages{Store: store}
	if err := service.CreateExternalIdentity(context.Background(), domain.ExternalIdentity{WorkspaceID: "T1", Provider: "oidc", Subject: "oidc-subject", UserID: "U2"}); err != nil {
		t.Fatal(err)
	}
	// Establish the starting role through the system operation rather than
	// through SetUserRole with U1 as actor: U1 is a plain member, and an
	// administrative mutation must refuse a member even in a test fixture.
	// Granting fake authority here would have hidden the very defect the
	// authorization change closes.
	if err := service.SynchronizeExternalUserRole(context.Background(), "T1", "U2", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	handler, err := NewLoginHandler(service, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// EmailVerified is deliberately false: the provider subject is already bound to
	// a local user, so this path never links by email and must not demand a
	// verification claim it has no use for.
	user, role, err := handler.resolveIdentityUser(context.Background(), "oidc", externalIdentity{Subject: "oidc-subject", Email: "alice@example.com", PreferredUsername: "alice-from-shauth", Role: "developer"})
	if err != nil || user.ID != "U2" || user.Profile.DisplayName != "alice-from-shauth" || role != domain.WorkspaceRoleMember {
		t.Fatalf("user=%+v role=%q err=%v", user, role, err)
	}
	membership, err := store.GetWorkspaceMembership(context.Background(), "T1", "U2")
	if err != nil || membership.Role != domain.WorkspaceRoleMember {
		t.Fatalf("membership=%+v err=%v", membership, err)
	}
	persisted, err := store.FindUserByEmail(context.Background(), "T1", "alice@example.com")
	if err != nil || persisted.Profile.DisplayName != "alice-from-shauth" {
		t.Fatalf("persisted user=%+v err=%v", persisted, err)
	}
}

func TestOIDCAuthorizationSynchronizesVerifiedEmailLinkedBootstrapIdentity(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{
		ID:          "Ubootstrap",
		WorkspaceID: "T1",
		Email:       "bootstrap-admin@example.com",
		Name:        "sameoldchat",
		RealName:    "SameOldChat",
	})
	handler, err := NewLoginHandler(
		service.Messages{Store: store},
		"T1",
		"Ubootstrap",
		"https://chat.example.test",
		"",
		[]byte(strings.Repeat("k", 32)),
		[]ProviderConfig{{
			Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret",
			AuthorizeURL: "https://auth.example.test/oauth2/auth",
			TokenURL:     "https://auth.example.test/oauth2/token",
			UserInfoURL:  "https://auth.example.test/userinfo",
			Scopes:       []string{"openid", "profile", "email"},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	user, role, err := handler.resolveIdentityUser(context.Background(), "oidc", externalIdentity{
		Subject:           "shauth-bootstrap-subject",
		Email:             "bootstrap-admin@example.com",
		EmailVerified:     true,
		Name:              "Bootstrap Administrator",
		PreferredUsername: "bootstrap-admin",
		Role:              "admin",
	})
	if err != nil || user.ID != "Ubootstrap" || user.Profile.DisplayName != "bootstrap-admin" || role != domain.WorkspaceRoleAdmin {
		t.Fatalf("user=%+v role=%q err=%v", user, role, err)
	}
	link, err := store.GetExternalIdentity(context.Background(), "T1", "oidc", "shauth-bootstrap-subject")
	if err != nil || link.UserID != "Ubootstrap" {
		t.Fatalf("external identity=%+v err=%v", link, err)
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

// newIdentityWorkspace assembles the login surface and the application surface over
// one store, so a session minted by the callback can be replayed against the
// administration endpoints exactly as a browser would.
func newIdentityWorkspace(t *testing.T, userinfo string) (http.Handler, *memory.Store) {
	t.Helper()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.test", Name: "admin"})
	messages := service.Messages{Store: store}
	login, err := NewLoginHandler(messages, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid", "profile", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	login.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/oauth2/token":
			return providerResponse(r, `{"access_token":"oidc-token"}`), nil
		case "/userinfo":
			return providerResponse(r, userinfo), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}
	browser, err := auth.NewBrowser(store)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(messages, browser, store, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	handler.Login = &login
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux, store
}

// TestBrowserSessionScopesFollowTheWorkspaceRole is the mint-side half of the
// privilege-escalation defect. Every browser session used to be created with
// auth.AllScopes(), which includes admin, admin.users:read/write, admin.teams:write
// and admin.apps:write, regardless of the workspace role that had just been
// synchronized — so a `developer` identity could POST admin.auth.users.set and
// promote itself. A session now carries only what its role justifies.
func TestBrowserSessionScopesFollowTheWorkspaceRole(t *testing.T) {
	mux, store := newIdentityWorkspace(t, `{"sub":"oidc-subject","email":"developer@example.test","email_verified":true,"name":"Developer","role":"developer"}`)
	callback := completeAuthorization(t, mux, "oidc")
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	sessionCookie := findSessionCookie(callback.Result().Cookies())
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("callback did not create a browser session cookie")
	}
	record, err := store.LookupSession(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range record.Scopes {
		if auth.IsControlPlaneScope(auth.Scope(scope)) {
			t.Fatalf("member session was minted with the control-plane scope %q", scope)
		}
	}
	// The member must still hold ordinary chat authority; this is a narrowing, not
	// a lockout.
	principal := auth.Principal{Scopes: map[auth.Scope]struct{}{}}
	for _, scope := range record.Scopes {
		principal.Scopes[auth.Scope(scope)] = struct{}{}
	}
	for _, required := range []auth.Scope{auth.ScopeChatWrite, auth.ScopeChannelsHistory, auth.ScopeUsersRead, auth.ScopeSearchRead} {
		if !principal.HasScope(required) {
			t.Fatalf("member session lost the ordinary scope %q", required)
		}
	}

	provisioned, err := store.FindUserByEmail(context.Background(), "T1", "developer@example.test")
	if err != nil {
		t.Fatal(err)
	}
	csrf := auth.CSRFToken(sessionCookie.Value)
	for _, attempt := range []struct {
		name   string
		target string
		body   string
	}{
		{name: "self promotion", target: "/api/admin.auth.users.set", body: "user_id=" + string(provisioned.ID) + "&action=role&role=admin"},
		{name: "lock out the administrator", target: "/api/admin.auth.users.set", body: "user_id=U1&action=disable"},
		{name: "disable the only login provider", target: "/api/admin.auth.methods.set", body: "provider=oidc&enabled=false"},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, attempt.target, strings.NewReader(attempt.body+"&_csrf="+csrf))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept", "application/json")
			request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionCookie.Value})
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	membership, err := store.GetWorkspaceMembership(context.Background(), "T1", provisioned.ID)
	if err != nil || membership.Role != domain.WorkspaceRoleMember {
		t.Fatalf("membership=%+v err=%v, want an unchanged member", membership, err)
	}
	administrator, err := store.GetWorkspaceMembership(context.Background(), "T1", "U1")
	if err != nil || !administrator.Active {
		t.Fatalf("administrator membership=%+v err=%v", administrator, err)
	}
	method, err := store.GetAuthMethod(context.Background(), "T1", "oidc")
	if err != nil || !method.Enabled {
		t.Fatalf("authorization method=%+v err=%v", method, err)
	}
}

// TestAdministratorSessionKeepsTheControlPlane is the positive counterpart: role
// derivation must not lock a real administrator out.
func TestAdministratorSessionKeepsTheControlPlane(t *testing.T) {
	mux, store := newIdentityWorkspace(t, `{"sub":"oidc-subject","email":"boss@example.test","email_verified":true,"name":"Boss","role":"admin"}`)
	callback := completeAuthorization(t, mux, "oidc")
	sessionCookie := findSessionCookie(callback.Result().Cookies())
	if callback.Code != http.StatusSeeOther || sessionCookie == nil {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	record, err := store.LookupSession(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	control := 0
	for _, scope := range record.Scopes {
		if auth.IsControlPlaneScope(auth.Scope(scope)) {
			control++
		}
	}
	if control == 0 {
		t.Fatalf("administrator session carries no control-plane scope: %v", record.Scopes)
	}
	csrf := auth.CSRFToken(sessionCookie.Value)
	request := httptest.NewRequest(http.MethodPost, "/api/admin.auth.users.set", strings.NewReader("user_id=U1&action=role&role=admin&_csrf="+csrf))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionCookie.Value})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("administrator mutation status=%d body=%s", response.Code, response.Body.String())
	}
}

// TestAuthorizationRefusesUnverifiedProviderEmailForAccountLinking covers the nOAuth
// account-takeover defect. Account linking was performed on the email string alone,
// with no email_verified check anywhere in internal/web, so an attacker holding their
// own tenant of a multi-tenant issuer could set a directory attribute to a victim's
// workspace address and be issued that victim's session.
func TestAuthorizationRefusesUnverifiedProviderEmailForAccountLinking(t *testing.T) {
	for _, test := range []struct {
		name     string
		userinfo string
	}{
		{name: "claim absent", userinfo: `{"sub":"attacker-subject","email":"victim@example.test","name":"Not The Victim","role":"admin"}`},
		{name: "claim false", userinfo: `{"sub":"attacker-subject","email":"victim@example.test","email_verified":false,"name":"Not The Victim","role":"admin"}`},
		{name: "claim not a boolean", userinfo: `{"sub":"attacker-subject","email":"victim@example.test","email_verified":"yes","role":"admin"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mux, store := newIdentityWorkspace(t, test.userinfo)
			store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "victim@example.test", Name: "victim"})
			callback := completeAuthorization(t, mux, "oidc")
			if callback.Code != http.StatusForbidden {
				t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
			}
			if session := findSessionCookie(callback.Result().Cookies()); session != nil && session.Value != "" {
				t.Fatalf("unverified provider email produced a session cookie: %+v", session)
			}
			if _, err := store.GetExternalIdentity(context.Background(), "T1", "oidc", "attacker-subject"); !errors.Is(err, storepkg.ErrNotFound) {
				t.Fatalf("unverified provider email created a durable account link: %v", err)
			}
			membership, err := store.GetWorkspaceMembership(context.Background(), "T1", "U2")
			if err != nil || membership.Role != domain.WorkspaceRoleMember {
				t.Fatalf("victim membership=%+v err=%v, want it untouched", membership, err)
			}
		})
	}
}

// TestAuthorizationRefusesUnverifiedProviderEmailForProvisioning closes the same hole
// on the first-login path, where an unverified address would otherwise create a
// workspace user the victim can never reclaim.
func TestAuthorizationRefusesUnverifiedProviderEmailForProvisioning(t *testing.T) {
	mux, store := newIdentityWorkspace(t, `{"sub":"attacker-subject","email":"newcomer@example.test","role":"admin"}`)
	callback := completeAuthorization(t, mux, "oidc")
	if callback.Code != http.StatusForbidden || !strings.Contains(callback.Body.String(), "did not verify") {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	if _, err := store.FindUserByEmail(context.Background(), "T1", "newcomer@example.test"); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("unverified provider email provisioned a workspace user: %v", err)
	}
}

// TestEntraPreferredUsernameIsNeverTreatedAsVerified pins the Microsoft Entra ID
// fallback: `preferred_username` is a directory attribute, not proof of address
// ownership, and the deployment default authority is the multi-tenant `common`.
func TestEntraPreferredUsernameIsNeverTreatedAsVerified(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "admin@example.test", Name: "admin"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Email: "victim@example.test", Name: "victim"})
	messages := service.Messages{Store: store}
	login, err := NewLoginHandler(messages, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "entra", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize", TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", Scopes: []string{"openid", "email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	login.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/token":
			return providerResponse(r, `{"access_token":"provider-token"}`), nil
		case "/userinfo":
			// `mail` is empty and email_verified is asserted for an address the
			// tenant does not own; the fallback must not inherit that assertion.
			return providerResponse(r, `{"sub":"attacker-subject","email_verified":true,"preferred_username":"victim@example.test"}`), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}
	mux := http.NewServeMux()
	login.Register(mux)
	callback := completeAuthorization(t, mux, "entra")
	if callback.Code != http.StatusForbidden {
		t.Fatalf("callback status=%d body=%s", callback.Code, callback.Body.String())
	}
	if _, err := store.GetExternalIdentity(context.Background(), "T1", "entra", "attacker-subject"); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("preferred_username linked an account: %v", err)
	}
}

// TestBackchannelLogoutFailsClosedWithoutAPinnedVerifier pins the fallback removal:
// rebuilding the verifier from the issuer's advertised algorithm list would widen the
// accepted signature set beyond the RS256 pin.
func TestBackchannelLogoutFailsClosedWithoutAPinnedVerifier(t *testing.T) {
	handler, err := NewLoginHandler(service.Messages{Store: memory.New()}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "oidc", Issuer: "https://auth.example.test", ClientID: "sameoldchat", ClientSecret: "secret", AuthorizeURL: "https://auth.example.test/oauth2/auth", TokenURL: "https://auth.example.test/oauth2/token", UserInfoURL: "https://auth.example.test/userinfo", Scopes: []string{"openid"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.verifyBackchannelLogout(context.Background(), "any.logout.token"); err == nil || !strings.Contains(err.Error(), "verifier is unavailable") {
		t.Fatalf("verification error=%v, want a closed failure", err)
	}
}

func TestAssertedEmailVerifiedFailsClosed(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: ``, want: false},
		{raw: `null`, want: false},
		{raw: `true`, want: true},
		{raw: `false`, want: false},
		{raw: `"true"`, want: true},
		{raw: `"TRUE"`, want: true},
		{raw: `"yes"`, want: false},
		{raw: `1`, want: false},
		{raw: `{}`, want: false},
	} {
		if got := assertedEmailVerified([]byte(test.raw)); got != test.want {
			t.Fatalf("assertedEmailVerified(%q)=%v, want %v", test.raw, got, test.want)
		}
	}
}

// signInThroughProvider drives the full authorization round trip and reports the
// callback response, so a provider's sign-in can be asserted end to end.
func signInThroughProvider(t *testing.T, handler LoginHandler, provider string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	handler.Register(mux)
	begin := httptest.NewRecorder()
	mux.ServeHTTP(begin, httptest.NewRequest(http.MethodGet, "/auth/"+provider, nil))
	if begin.Code != http.StatusFound {
		t.Fatalf("begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	location, err := url.Parse(begin.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/"+provider+"/callback?code=one-time-code&state="+url.QueryEscape(location.Query().Get("state")), nil)
	for _, cookie := range begin.Result().Cookies() {
		request.AddCookie(cookie)
	}
	callback := httptest.NewRecorder()
	mux.ServeHTTP(callback, request)
	return callback
}

// GitHub's /user returns a profile address for any account that made one public,
// and carries no email_verified claim. Requiring that claim therefore refused
// every such account, because the one call that actually proves ownership —
// /user/emails, which reports `verified` per address — was skipped whenever
// /user had already supplied an address.
func TestGitHubSignInResolvesTheVerifiedAddressEvenWhenTheProfileHasOne(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
	emailEndpointCalled := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/token":
			return providerResponse(r, `{"access_token":"provider-token"}`), nil
		case "/userinfo":
			// A public profile address, and no email_verified anywhere.
			return providerResponse(r, `{"id":4242,"login":"alice","email":"public@example.invalid","name":"Alice"}`), nil
		case "/emails":
			emailEndpointCalled = true
			return providerResponse(r, `[{"email":"alice@example.com","primary":true,"verified":true},{"email":"public@example.invalid","primary":false,"verified":false}]`), nil
		default:
			return providerResponse(r, "not found"), nil
		}
	})}
	handler, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
		Name: "github", ClientID: "client", ClientSecret: "secret", AuthorizeURL: "https://github.com/login/oauth/authorize", TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", EmailURL: "https://provider.test/emails", Scopes: []string{"read:user", "user:email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.client = client

	callback := signInThroughProvider(t, handler, "github")
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("github sign-in was refused: status=%d body=%s", callback.Code, callback.Body.String())
	}
	if !emailEndpointCalled {
		t.Fatal("the verified-address endpoint was never called, so ownership was never proven")
	}
	// The account linked is the one holding the VERIFIED address, not the public
	// profile address.
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			session, err := store.LookupSession(context.Background(), cookie.Value)
			if err != nil || session.UserID != "U1" {
				t.Fatalf("session=%+v err=%v, want the user holding the verified address", session, err)
			}
			return
		}
	}
	t.Fatal("no session cookie was issued")
}

// Entra emits no email_verified either, so requiring it refused every first
// sign-in. A deployment pinned to one tenant is reading an address out of a
// directory it administers; a multi-tenant endpoint is the nOAuth position and
// must still be refused.
func TestEntraSignInTrustsAPinnedTenantAndRefusesAMultiTenantEndpoint(t *testing.T) {
	newHandler := func(t *testing.T, authorizeURL string) (LoginHandler, *memory.Store) {
		t.Helper()
		store := memory.New()
		store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
		store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice"})
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/token":
				return providerResponse(r, `{"access_token":"provider-token"}`), nil
			case "/userinfo":
				return providerResponse(r, `{"sub":"entra-subject","email":"alice@example.com","name":"Alice"}`), nil
			default:
				return providerResponse(r, "not found"), nil
			}
		})}
		handler, err := NewLoginHandler(service.Messages{Store: store}, "T1", "U1", "https://chat.example.test", "", []byte(strings.Repeat("k", 32)), []ProviderConfig{{
			Name: "entra", ClientID: "client", ClientSecret: "secret", AuthorizeURL: authorizeURL, TokenURL: "https://provider.test/token", UserInfoURL: "https://provider.test/userinfo", Scopes: []string{"openid", "email"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		handler.client = client
		return handler, store
	}

	pinned, _ := newHandler(t, "https://login.microsoftonline.com/8f4c1e2a-0000-0000-0000-abcdefabcdef/oauth2/v2.0/authorize")
	if callback := signInThroughProvider(t, pinned, "entra"); callback.Code != http.StatusSeeOther {
		t.Fatalf("a pinned-tenant Entra sign-in was refused: status=%d body=%s", callback.Code, callback.Body.String())
	}

	for _, tenant := range []string{"common", "organizations", "consumers"} {
		multi, store := newHandler(t, "https://login.microsoftonline.com/"+tenant+"/oauth2/v2.0/authorize")
		callback := signInThroughProvider(t, multi, "entra")
		if callback.Code == http.StatusSeeOther {
			t.Fatalf("%s: a multi-tenant endpoint linked an account on an unproven address", tenant)
		}
		for _, cookie := range callback.Result().Cookies() {
			if cookie.Name == auth.SessionCookieName && cookie.Value != "" {
				if _, err := store.LookupSession(context.Background(), cookie.Value); err == nil {
					t.Fatalf("%s: a session was created from an unproven address", tenant)
				}
			}
		}
	}
}
