package web

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A relying party that only replaced Slack's URLs with this deployment's walks
// Sign in with Slack the way it walks Slack's: it discovers the endpoints, sends
// the member to /openid/connect/authorize with a nonce, redeems the code at
// openid.connect.token, and verifies the ID token against the published keys.
func TestSignInWithSlackIsDiscoverableAndVerifiable(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	messages := service.Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32)), PublicURL: "https://chat.example"}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Sign-in App"},"oauth_config":{"redirect_urls":["https://client.example/callback"],"scopes":{"user":["openid","email","profile"]}},"settings":{"socket_mode_enabled":true}}`
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
	if err := handler.SetPublicURL("https://chat.example"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}

	response := get("https://chat.example/.well-known/openid-configuration")
	var discovery map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &discovery); err != nil || response.Code != http.StatusOK {
		t.Fatalf("discovery status=%d body=%s", response.Code, response.Body)
	}
	for name, want := range map[string]string{
		"issuer":                 "https://chat.example",
		"authorization_endpoint": "https://chat.example/openid/connect/authorize",
		"token_endpoint":         "https://chat.example/api/openid.connect.token",
		"userinfo_endpoint":      "https://chat.example/api/openid.connect.userInfo",
		"jwks_uri":               "https://chat.example/openid/connect/keys",
	} {
		if discovery[name] != want {
			t.Fatalf("discovery %s=%v, want %s", name, discovery[name], want)
		}
	}

	authorize := url.Values{"response_type": {"code"}, "scope": {"openid email profile"}, "client_id": {credentials.ClientID}, "redirect_uri": {"https://client.example/callback"}, "state": {"opaque-state"}, "nonce": {"nonce-123"}, "response_mode": {"form_post"}}
	response = get("https://chat.example/openid/connect/authorize?" + authorize.Encode())
	if body := response.Body.String(); response.Code != http.StatusOK || !strings.Contains(body, `name="nonce" value="nonce-123"`) || !strings.Contains(body, `name="response_mode" value="form_post"`) {
		t.Fatalf("consent status=%d body=%s", response.Code, body)
	}
	for name, refused := range map[string]url.Values{
		"no openid scope":        {"response_type": {"code"}, "scope": {"email"}, "client_id": {credentials.ClientID}},
		"no response_type":       {"scope": {"openid"}, "client_id": {credentials.ClientID}},
		"unsupported mode":       {"response_type": {"code"}, "scope": {"openid"}, "client_id": {credentials.ClientID}, "response_mode": {"fragment"}},
		"implicit response type": {"response_type": {"id_token"}, "scope": {"openid"}, "client_id": {credentials.ClientID}},
	} {
		if response := get("https://chat.example/openid/connect/authorize?" + refused.Encode()); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d, want 400", name, response.Code)
		}
	}

	form := url.Values{"_csrf": {auth.CSRFToken(session)}, "decision": {"approve"}, "client_id": {credentials.ClientID}, "redirect_uri": {"https://client.example/callback"}, "scope": {""}, "user_scope": {"openid,email,profile"}, "state": {"opaque-state"}, "nonce": {"nonce-123"}, "response_mode": {"form_post"}}
	request := httptest.NewRequest(http.MethodPost, "https://chat.example/openid/connect/authorize", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `action="https://client.example/callback"`) || !strings.Contains(body, `name="state" value="opaque-state"`) {
		t.Fatalf("form_post status=%d body=%s", response.Code, body)
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "form-action https://client.example;") {
		t.Fatalf("form_post policy=%q, want form-action limited to the relying party", policy)
	}
	match := regexp.MustCompile(`name="code" value="([^"]+)"`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("form_post carries no code: %s", body)
	}

	token, err := messages.OpenIDConnectToken(ctx, credentials.ClientID, credentials.ClientSecret, match[1], "https://client.example/callback", "authorization_code", "", "")
	if err != nil {
		t.Fatal(err)
	}
	response = get("https://chat.example/openid/connect/keys")
	var keySet struct {
		Keys []struct{ Kty, Use, Alg, Kid, N, E string } `json:"keys"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &keySet); err != nil || response.Code != http.StatusOK || len(keySet.Keys) != 1 {
		t.Fatalf("keys status=%d body=%s", response.Code, response.Body)
	}
	published := keySet.Keys[0]
	if published.Kty != "RSA" || published.Alg != "RS256" || published.Use != "sig" {
		t.Fatalf("published key=%+v", published)
	}
	modulus, _ := base64.RawURLEncoding.DecodeString(published.N)
	exponent, _ := base64.RawURLEncoding.DecodeString(published.E)
	publicKey := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(new(big.Int).SetBytes(exponent).Int64())}

	parts := strings.Split(token.IDToken, ".")
	if len(parts) != 3 {
		t.Fatalf("id_token=%q", token.IDToken)
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("the ID token does not verify against the published key: %v", err)
	}
	var header, claims map[string]any
	headerJSON, _ := base64.RawURLEncoding.DecodeString(parts[0])
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(headerJSON, &header) != nil || json.Unmarshal(claimsJSON, &claims) != nil {
		t.Fatalf("header=%s claims=%s", headerJSON, claimsJSON)
	}
	if header["alg"] != "RS256" || header["kid"] != published.Kid {
		t.Fatalf("header=%v, want RS256 naming key %s", header, published.Kid)
	}
	accessDigest := sha256.Sum256([]byte(token.AccessToken))
	for name, want := range map[string]any{
		"iss": "https://chat.example", "aud": credentials.ClientID, "sub": "U1", "nonce": "nonce-123",
		"at_hash": base64.RawURLEncoding.EncodeToString(accessDigest[:16]), "https://slack.com/team_id": "T1", "email": "alice@example.com",
	} {
		if claims[name] != want {
			t.Fatalf("claim %s=%v, want %v (claims %v)", name, claims[name], want, claims)
		}
	}
	if _, present := claims["auth_time"]; !present {
		t.Fatalf("claims=%v carry no auth_time", claims)
	}
}

// Without a public URL there is no issuer a relying party could compare ID
// tokens against, and a Host header must not choose one.
func TestOpenIDDiscoveryNeedsAPublicURL(t *testing.T) {
	repository := memory.New()
	browser, err := auth.NewBrowser(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service.Messages{Store: repository}, browser, repository, "C1", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://attacker.example/.well-known/openid-configuration", nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "attacker.example") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}
