package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type ProviderConfig struct {
	Name          string
	Issuer        string
	ClientID      string
	ClientSecret  string
	AuthorizeURL  string
	TokenURL      string
	UserInfoURL   string
	EmailURL      string
	EndSessionURL string
	Scopes        []string
	verifier      *oidc.IDTokenVerifier
}

type OpenIDConfiguration struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

type LoginHandler struct {
	service      chatapi.Service
	workspace    domain.WorkspaceID
	lookupUser   domain.UserID
	publicURL    string
	cookieDomain string
	stateKey     []byte
	providers    map[string]ProviderConfig
	client       *http.Client
}

var supportedAuthorizationProviders = map[string]struct{}{
	"entra":  {},
	"github": {},
	"google": {},
	"oidc":   {},
}

const (
	maxAuthorizationDiscoveryResponse = 1 << 20
	maxAuthorizationTokenResponse     = 64 << 10
	maxAuthorizationUserInfoResponse  = 256 << 10
	maxAuthorizationEmailResponse     = 1 << 20
	maxBackchannelLogoutRequest       = 64 << 10
)

func decodeAuthorizationJSON(body io.Reader, limit int64, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > limit {
		return fmt.Errorf("authorization response exceeds %d bytes", limit)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return err
	}
	return nil
}

func DiscoverOpenIDConnectProvider(ctx context.Context, client *http.Client, issuer, clientID, clientSecret string) (ProviderConfig, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	parsed, err := url.Parse(issuer)
	if err != nil || !validAuthorizationURL(parsed) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProviderConfig{}, errors.New("OpenID Connect issuer must be an absolute HTTPS URL, except for an explicit loopback development coordinate")
	}
	if clientID == "" || clientSecret == "" {
		return ProviderConfig{}, errors.New("OpenID Connect client ID and secret are required")
	}
	if client == nil {
		return ProviderConfig{}, errors.New("OpenID Connect discovery requires an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return ProviderConfig{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("discover OpenID Connect provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ProviderConfig{}, fmt.Errorf("OpenID Connect discovery returned %s", response.Status)
	}
	var document OpenIDConfiguration
	if err := decodeAuthorizationJSON(response.Body, maxAuthorizationDiscoveryResponse, &document); err != nil {
		return ProviderConfig{}, fmt.Errorf("decode OpenID Connect discovery: %w", err)
	}
	if strings.TrimRight(document.Issuer, "/") != issuer {
		return ProviderConfig{}, errors.New("OpenID Connect discovery issuer does not match configured issuer")
	}
	for label, endpoint := range map[string]string{"authorization": document.AuthorizationEndpoint, "token": document.TokenEndpoint, "userinfo": document.UserInfoEndpoint, "JSON Web Key Set": document.JWKSURI} {
		parsedEndpoint, parseErr := url.Parse(strings.TrimSpace(endpoint))
		if parseErr != nil || !validAuthorizationURL(parsedEndpoint) {
			return ProviderConfig{}, fmt.Errorf("OpenID Connect %s endpoint must be an absolute HTTPS URL, except for an explicit loopback development coordinate", label)
		}
	}
	if document.EndSessionEndpoint != "" {
		parsedEndpoint, parseErr := url.Parse(strings.TrimSpace(document.EndSessionEndpoint))
		if parseErr != nil || !validAuthorizationURL(parsedEndpoint) {
			return ProviderConfig{}, errors.New("OpenID Connect end-session endpoint must be an absolute HTTPS URL, except for an explicit loopback development coordinate")
		}
	}
	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, client), document.JWKSURI)
	verifier := oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: clientID})
	return ProviderConfig{Name: "oidc", Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret, AuthorizeURL: document.AuthorizationEndpoint, TokenURL: document.TokenEndpoint, UserInfoURL: document.UserInfoEndpoint, EndSessionURL: document.EndSessionEndpoint, Scopes: []string{"openid", "profile", "email"}, verifier: verifier}, nil
}

func NewLoginHandler(service chatapi.Service, workspace domain.WorkspaceID, lookupUser domain.UserID, publicURL, cookieDomain string, stateKey []byte, providers []ProviderConfig) (LoginHandler, error) {
	if service == nil || workspace == "" || lookupUser == "" || strings.TrimSpace(publicURL) == "" || len(stateKey) < 32 {
		return LoginHandler{}, errors.New("login requires service, workspace, lookup user, public URL, and a 32-byte state key")
	}
	base, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || !validAuthorizationURL(base) || base.RawQuery != "" || base.Fragment != "" {
		return LoginHandler{}, errors.New("login public URL must be an absolute HTTPS URL, except for an explicit loopback development coordinate")
	}
	cookieDomain = strings.TrimSpace(cookieDomain)
	if err := auth.ValidateSessionCookieDomain(cookieDomain); err != nil {
		return LoginHandler{}, err
	}
	if cookieDomain != "" && base.Hostname() != cookieDomain && !strings.HasSuffix(base.Hostname(), "."+cookieDomain) {
		return LoginHandler{}, errors.New("session cookie domain must contain the authorization callback host")
	}
	configured := make(map[string]ProviderConfig, len(providers))
	for _, provider := range providers {
		provider.Name = strings.ToLower(strings.TrimSpace(provider.Name))
		provider.Issuer = strings.TrimRight(strings.TrimSpace(provider.Issuer), "/")
		provider.ClientID = strings.TrimSpace(provider.ClientID)
		provider.ClientSecret = strings.TrimSpace(provider.ClientSecret)
		provider.AuthorizeURL = strings.TrimSpace(provider.AuthorizeURL)
		provider.TokenURL = strings.TrimSpace(provider.TokenURL)
		provider.UserInfoURL = strings.TrimSpace(provider.UserInfoURL)
		provider.EmailURL = strings.TrimSpace(provider.EmailURL)
		provider.EndSessionURL = strings.TrimSpace(provider.EndSessionURL)
		if provider.Name == "" || provider.ClientID == "" || provider.ClientSecret == "" || provider.AuthorizeURL == "" || provider.TokenURL == "" || provider.UserInfoURL == "" {
			return LoginHandler{}, fmt.Errorf("provider %q is incomplete", provider.Name)
		}
		if _, supported := supportedAuthorizationProviders[provider.Name]; !supported {
			return LoginHandler{}, fmt.Errorf("provider %q is unsupported", provider.Name)
		}
		if provider.Name == "github" && provider.EmailURL == "" {
			return LoginHandler{}, errors.New("github provider requires an email endpoint")
		}
		if provider.EndSessionURL != "" {
			endpoint, parseErr := url.Parse(provider.EndSessionURL)
			if parseErr != nil || !validAuthorizationURL(endpoint) {
				return LoginHandler{}, fmt.Errorf("provider %q end-session endpoint must be an absolute HTTPS URL, except for an explicit loopback development coordinate", provider.Name)
			}
		}
		normalizedScopes, err := normalizeScopes(provider.Scopes)
		if err != nil {
			return LoginHandler{}, fmt.Errorf("provider %q scopes: %w", provider.Name, err)
		}
		provider.Scopes = normalizedScopes
		if _, exists := configured[provider.Name]; exists {
			return LoginHandler{}, fmt.Errorf("provider %q is duplicated", provider.Name)
		}
		configured[provider.Name] = provider
	}
	if len(configured) == 0 {
		return LoginHandler{}, errors.New("at least one authorization provider is required")
	}
	return LoginHandler{service: service, workspace: workspace, lookupUser: lookupUser, publicURL: strings.TrimRight(publicURL, "/"), cookieDomain: cookieDomain, stateKey: append([]byte(nil), stateKey...), providers: configured, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func normalizeScopes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one scope is required")
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("scope entries must not be empty")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func (h LoginHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", h.login)
	for name := range h.providers {
		provider := name
		mux.HandleFunc("GET /auth/"+provider, func(w http.ResponseWriter, r *http.Request) { h.begin(w, r, provider) })
		mux.HandleFunc("GET /auth/"+provider+"/callback", func(w http.ResponseWriter, r *http.Request) { h.callback(w, r, provider) })
	}
	if provider, ok := h.providers["oidc"]; ok && provider.Issuer != "" {
		mux.HandleFunc("POST /auth/oidc/backchannel-logout", h.backchannelLogout)
		mux.HandleFunc("GET /auth/shauth/logout/complete", h.providerLogoutComplete)
	}
}

func validAuthorizationURL(value *url.URL) bool {
	if value == nil || value.Host == "" || value.User != nil || value.Fragment != "" {
		return false
	}
	if value.Scheme == "https" {
		return true
	}
	host := strings.Trim(strings.ToLower(value.Hostname()), "[]")
	address := net.ParseIP(host)
	return value.Scheme == "http" && (host == "localhost" || strings.HasSuffix(host, ".localhost") || address != nil && address.IsLoopback())
}

func (h LoginHandler) hasOpenIDConnectProvider() bool {
	provider, ok := h.providers["oidc"]
	return ok && provider.Issuer != ""
}

func (h LoginHandler) providerLogoutComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	provider, ok := h.providers["oidc"]
	if !ok || provider.Issuer == "" {
		http.Error(w, "Shauth logout completion is unavailable", http.StatusNotFound)
		return
	}
	issuer, err := url.Parse(provider.Issuer)
	if err != nil {
		http.Error(w, "Shauth logout completion is unavailable", http.StatusServiceUnavailable)
		return
	}
	target := issuer.ResolveReference(&url.URL{Path: "/oauth/logout/complete"})
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (h Handler) signedOut(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w, entryContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	message := "Your SameOldChat and organization sign-in sessions have ended."
	if r.URL.Query().Get("global") == "failed" {
		w.WriteHeader(http.StatusServiceUnavailable)
		message = "Your SameOldChat session ended, but the organization identity service could not complete global sign-out."
	}
	signIn := ""
	switch {
	case h.Login == nil:
		signIn = `<p>Ask a workspace administrator how to sign in again.</p>`
	case h.Login.hasOpenIDConnectProvider():
		signIn = `<a href="/auth/oidc">Sign in with Shauth</a>`
	default:
		signIn = `<a href="/login">Choose a sign-in method</a>`
	}
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light dark"><title>Signed out · SameOldChat</title><style>:root{color-scheme:light dark;--bg:#f8f8fa;--panel:#fff;--text:#1d1c1d;--muted:#5e5e65;--line:#d5d5da;--accent:#611f69;--focus:#1264a3}@media(prefers-color-scheme:dark){:root{--bg:#1a1d21;--panel:#222529;--text:#f4f4f5;--muted:#c7c7cc;--line:#4a4e55;--accent:#b869c2;--focus:#5bb8ff}}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--text);font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(480px,100%);padding:32px;background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:0 14px 42px #0002}h1{margin:0 0 12px;font-size:2rem}p{margin:0 0 24px;color:var(--muted)}a{display:inline-block;padding:11px 16px;border-radius:7px;background:var(--accent);color:#fff;font-weight:700;text-decoration:none}a:focus-visible{outline:3px solid var(--focus);outline-offset:3px}</style></head><body><main class="card"><h1>You’re signed out</h1><p role="status">`+template.HTMLEscapeString(message)+`</p>`+signIn+`</main></body></html>`)
}

const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

type backchannelLogoutClaims struct {
	Events   map[string]json.RawMessage `json:"events"`
	IssuedAt int64                      `json:"iat"`
	Expires  int64                      `json:"exp"`
	JWTID    string                     `json:"jti"`
	Nonce    string                     `json:"nonce"`
	Subject  string                     `json:"sub"`
	SID      string                     `json:"sid"`
}

func (h LoginHandler) verifyBackchannelLogout(ctx context.Context, raw string) (backchannelLogoutClaims, error) {
	providerConfig := h.providers["oidc"]
	verifier := providerConfig.verifier
	if verifier == nil {
		// The verifier built by DiscoverOpenIDConnectProvider passes no
		// SupportedSigningAlgs, which pins RS256. Rebuilding one here from the
		// issuer's own advertised algorithm list would widen the accepted set to
		// whatever the issuer claims to support, so an unconfigured verifier fails
		// closed instead.
		return backchannelLogoutClaims{}, errors.New("OpenID Connect logout token verifier is unavailable")
	}
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return backchannelLogoutClaims{}, fmt.Errorf("verify logout token: %w", err)
	}
	var claims backchannelLogoutClaims
	if err := token.Claims(&claims); err != nil {
		return backchannelLogoutClaims{}, fmt.Errorf("decode logout token: %w", err)
	}
	claims.Subject = token.Subject
	event, eventPresent := claims.Events[backchannelLogoutEvent]
	var eventValue map[string]any
	if eventPresent {
		if err := json.Unmarshal(event, &eventValue); err != nil || eventValue == nil || len(eventValue) != 0 {
			eventPresent = false
		}
	}
	now := time.Now().UTC().Unix()
	if (claims.Subject == "" && claims.SID == "") || claims.IssuedAt == 0 || claims.Expires == 0 || claims.JWTID == "" || claims.Nonce != "" || !eventPresent || claims.IssuedAt > now+60 || claims.IssuedAt < now-600 {
		return backchannelLogoutClaims{}, errors.New("logout token claims are invalid")
	}
	return claims, nil
}

func (h LoginHandler) backchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(contentType, "application/x-www-form-urlencoded") {
		http.Error(w, "logout token media type is unsupported", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBackchannelLogoutRequest)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "logout token is invalid", http.StatusBadRequest)
		return
	}
	if len(r.URL.Query()["logout_token"]) != 0 {
		http.Error(w, "logout token is invalid", http.StatusBadRequest)
		return
	}
	values := r.PostForm["logout_token"]
	if len(values) != 1 {
		http.Error(w, "logout token is invalid", http.StatusBadRequest)
		return
	}
	claims, err := h.verifyBackchannelLogout(r.Context(), strings.TrimSpace(values[0]))
	if err != nil {
		http.Error(w, "logout token is invalid", http.StatusBadRequest)
		return
	}
	if err := h.service.RevokeOIDCSessions(r.Context(), h.workspace, "oidc", claims.Subject, claims.SID, claims.JWTID, time.Unix(claims.Expires, 0)); err != nil {
		http.Error(w, "logout token could not be applied", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h LoginHandler) login(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w, entryContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	returnTo := safeLocalReturn(r.URL.Query().Get("return_to"))
	links, count, err := h.providerLinks(r.Context(), returnTo)
	message := "Choose your organization’s authorization source."
	status := http.StatusOK
	if err != nil {
		status = http.StatusServiceUnavailable
		message = "Sign-in methods are temporarily unavailable. Try again later."
		links = ""
	} else if count == 0 {
		status = http.StatusServiceUnavailable
		message = "No sign-in methods are enabled for this workspace. Contact a workspace administrator."
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light dark"><title>Sign in · SameOldChat</title><style>:root{color-scheme:light dark;--bg:#f8f8fa;--panel:#fff;--text:#1d1c1d;--muted:#5e5e65;--line:#d5d5da;--accent:#611f69;--focus:#1264a3}@media(prefers-color-scheme:dark){:root{--bg:#1a1d21;--panel:#222529;--text:#f4f4f5;--muted:#c7c7cc;--line:#4a4e55;--accent:#7c2d86;--focus:#5bb8ff}}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--text);font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(440px,100%);padding:32px;background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:0 14px 42px #0002}h1{margin:0 0 10px;font-size:2rem}p{margin:0 0 22px;color:var(--muted)}.provider{display:block;margin:12px 0;padding:12px 16px;border-radius:7px;background:var(--accent);color:#fff;text-align:center;text-decoration:none;font-weight:800}.provider:focus-visible{outline:3px solid var(--focus);outline-offset:3px}</style></head><body><main class="card"><h1>Sign in to SameOldChat</h1><p role="status">`+template.HTMLEscapeString(message)+`</p>`+links+`</main></body></html>`)
}

func (h LoginHandler) providerLinks(ctx context.Context, returnTo string) (string, int, error) {
	var result strings.Builder
	names := make([]string, 0, len(h.providers))
	for name := range h.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	count := 0
	for _, name := range names {
		method, err := h.service.GetAuthMethod(ctx, h.workspace, name)
		if err != nil {
			return "", 0, err
		}
		if !method.Enabled {
			continue
		}
		result.WriteString(`<a class="provider" href="/auth/`)
		result.WriteString(name)
		if returnTo != "" {
			result.WriteString(`?return_to=`)
			result.WriteString(url.QueryEscape(returnTo))
		}
		result.WriteString(`">Continue with `)
		result.WriteString(providerLabel(name))
		result.WriteString(`</a>`)
		count++
	}
	return result.String(), count, nil
}

func providerLabel(name string) string {
	switch name {
	case "google":
		return "Google"
	case "github":
		return "GitHub"
	case "entra":
		return "Microsoft Entra ID"
	case "oidc":
		return "Shauth"
	default:
		return name
	}
}

func (h LoginHandler) begin(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := h.providers[name]
	if !ok {
		http.Error(w, "authorization method is disabled", http.StatusNotFound)
		return
	}
	method, err := h.service.GetAuthMethod(r.Context(), h.workspace, name)
	if err != nil || !method.Enabled {
		http.Error(w, "authorization method is disabled", http.StatusNotFound)
		return
	}
	state, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "authorization state unavailable", http.StatusServiceUnavailable)
		return
	}
	verifier, err := randomURLValue(48)
	if err != nil {
		http.Error(w, "authorization verifier unavailable", http.StatusServiceUnavailable)
		return
	}
	nonce, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "authorization nonce unavailable", http.StatusServiceUnavailable)
		return
	}
	returnTo := safeLocalReturn(r.URL.Query().Get("return_to"))
	payload := name + "\x00" + state + "\x00" + verifier + "\x00" + nonce + "\x00" + returnTo
	signature := signState(h.stateKey, payload)
	http.SetCookie(w, &http.Cookie{Name: "sameoldchat_oauth_state", Value: base64.RawURLEncoding.EncodeToString([]byte(payload + "\x00" + signature)), Path: "/auth/", MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	query := url.Values{"client_id": {provider.ClientID}, "redirect_uri": {h.callbackURL(name)}, "response_type": {"code"}, "scope": {strings.Join(provider.Scopes, " ")}, "state": {state}, "code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"}}
	if provider.Issuer != "" {
		query.Set("nonce", nonce)
	}
	http.Redirect(w, r, provider.AuthorizeURL+"?"+query.Encode(), http.StatusFound)
}

func (h LoginHandler) callback(w http.ResponseWriter, r *http.Request, name string) {
	if provider, ok := h.providers[name]; !ok || provider.Name != name {
		http.Error(w, "authorization method is disabled", http.StatusNotFound)
		return
	}
	method, err := h.service.GetAuthMethod(r.Context(), h.workspace, name)
	if err != nil || !method.Enabled {
		http.Error(w, "authorization method is disabled", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("error")) != "" {
		http.Error(w, "authorization was denied", http.StatusBadRequest)
		return
	}
	stateCookie, err := r.Cookie("sameoldchat_oauth_state")
	if err != nil {
		http.Error(w, "authorization state is missing", http.StatusBadRequest)
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(stateCookie.Value)
	if err != nil {
		http.Error(w, "authorization state is invalid", http.StatusBadRequest)
		return
	}
	parts := strings.Split(string(decoded), "\x00")
	// The state value is the per-request secret an authorization-code injection
	// attacker has to guess, so it is compared with the same constant-time
	// primitive as the signature beside it.
	if len(parts) != 6 || parts[0] != name || !hmac.Equal([]byte(parts[5]), []byte(signState(h.stateKey, strings.Join(parts[:5], "\x00")))) || !hmac.Equal([]byte(parts[1]), []byte(strings.TrimSpace(r.URL.Query().Get("state")))) {
		http.Error(w, "authorization state is invalid", http.StatusBadRequest)
		return
	}
	tokens, err := h.exchangeCode(r.Context(), h.providers[name], r.URL.Query().Get("code"), parts[2], name)
	if err != nil {
		http.Error(w, "authorization token exchange failed", http.StatusBadGateway)
		return
	}
	provider := h.providers[name]
	oidcSubject, oidcSID := "", ""
	sessionExpiresAt := time.Now().UTC().Add(24 * time.Hour)
	if provider.Issuer != "" {
		var providerExpiresAt time.Time
		oidcSubject, oidcSID, providerExpiresAt, err = h.verifyOIDCLoginToken(r.Context(), provider, tokens.IDToken, parts[3])
		if err != nil {
			http.Error(w, "authorization identity is unavailable", http.StatusBadGateway)
			return
		}
		if providerExpiresAt.Before(sessionExpiresAt) {
			sessionExpiresAt = providerExpiresAt
		}
	}
	identity, err := h.userInfo(r.Context(), provider, tokens.AccessToken, name)
	if err != nil || strings.TrimSpace(identity.Email) == "" {
		http.Error(w, "authorization identity is unavailable", http.StatusBadGateway)
		return
	}
	if oidcSubject != "" && oidcSubject != identity.Subject {
		http.Error(w, "authorization identity is unavailable", http.StatusBadGateway)
		return
	}
	user, role, err := h.resolveIdentityUser(r.Context(), name, identity)
	if errors.Is(err, ErrUnverifiedProviderEmail) {
		http.Error(w, "authorization provider did not verify this email address", http.StatusForbidden)
		return
	}
	if err != nil || user.Deleted {
		http.Error(w, "authorization identity is not provisioned", http.StatusForbidden)
		return
	}
	// A browser session carries only the authority its user's durable workspace
	// role justifies. An unrecognized role produces no scopes at all rather than
	// a default set, so the failure direction is closed.
	scopes, err := auth.ScopesForWorkspaceRole(role)
	if err != nil {
		http.Error(w, "authorization identity has no supported access role", http.StatusForbidden)
		return
	}
	sessionToken, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	record := domain.SessionRecord{WorkspaceID: user.WorkspaceID, UserID: user.ID, Scopes: scopes.Values(), ExpiresAt: sessionExpiresAt}
	if provider.Issuer != "" {
		record.OIDCProvider = name
		record.OIDCIDToken = tokens.IDToken
		record.OIDCSubject = oidcSubject
		record.OIDCSID = oidcSID
	}
	cookieMaxAge := int(time.Until(sessionExpiresAt).Seconds())
	if cookieMaxAge < 1 {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.service.CreateSession(r.Context(), sessionToken, record); err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, auth.SessionCookie(sessionToken, cookieMaxAge, h.cookieDomain))
	http.SetCookie(w, &http.Cookie{Name: "sameoldchat_oauth_state", Value: "", Path: "/auth/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	target := safeLocalReturn(parts[4])
	if target == "" {
		target = "/app"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func safeLocalReturn(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	target, err := url.Parse(raw)
	if err != nil || target.IsAbs() || target.Host != "" || target.User != nil {
		return ""
	}
	return target.RequestURI()
}

// ErrUnverifiedProviderEmail rejects an account link derived from an email
// address the identity provider does not assert as verified.
//
// Linking a provider subject to a local user by email address is only sound if
// the provider proved the user controls that address. A provider the operator
// does not own — any tenant of a multi-tenant issuer, for example — can set a
// directory attribute to a victim's workspace address without owning the domain,
// which is the nOAuth account-takeover pattern. The check therefore fails closed
// when the assertion is absent, not only when it is explicitly false.
var ErrUnverifiedProviderEmail = errors.New("authorization provider did not assert a verified email address")

func (h LoginHandler) resolveIdentityUser(ctx context.Context, provider string, identity externalIdentity) (domain.User, domain.WorkspaceRole, error) {
	link, err := h.service.GetExternalIdentity(ctx, h.workspace, provider, identity.Subject)
	if err == nil {
		// The subject is already bound to a local user, so no email is trusted
		// here and no new link is created.
		user, lookupErr := h.service.UserInfo(ctx, h.workspace, h.lookupUser, link.UserID)
		if lookupErr != nil {
			return domain.User{}, "", lookupErr
		}
		return h.resolveExternalUser(ctx, provider, identity, user)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.User{}, "", err
	}

	// Everything past this point establishes a new binding from the provider's
	// email address, so the address must be verified by the provider.
	if !identity.EmailVerified {
		return domain.User{}, "", ErrUnverifiedProviderEmail
	}

	user, err := h.service.UserByEmail(ctx, h.workspace, h.lookupUser, identity.Email)
	if errors.Is(err, store.ErrNotFound) && provider == "oidc" {
		role, roleErr := oidcWorkspaceRole(identity.Role)
		if roleErr != nil {
			return domain.User{}, "", roleErr
		}
		displayName := identity.Name
		if displayName == "" {
			displayName = identity.PreferredUsername
		}
		if displayName == "" {
			displayName = identity.Email
		}
		// First-login provisioning has no administrator behind it and no
		// session for the user it creates, so it runs as the system operation
		// whose authority is the provider assertion verified above — not as the
		// configured lookup identity, whose own workspace role is deliberately
		// that of a plain member.
		user, err = h.service.ProvisionExternalUser(ctx, h.workspace, identity.Email, displayName, role)
		if errors.Is(err, store.ErrAlreadyExists) {
			user, err = h.service.UserByEmail(ctx, h.workspace, h.lookupUser, identity.Email)
		}
	}
	if err != nil {
		return domain.User{}, "", err
	}
	if user.Deleted {
		return domain.User{}, "", store.ErrNotFound
	}

	err = h.service.CreateExternalIdentity(ctx, domain.ExternalIdentity{WorkspaceID: h.workspace, Provider: provider, Subject: identity.Subject, UserID: user.ID})
	if errors.Is(err, store.ErrAlreadyExists) {
		link, err = h.service.GetExternalIdentity(ctx, h.workspace, provider, identity.Subject)
		if err == nil {
			user, err = h.service.UserInfo(ctx, h.workspace, h.lookupUser, link.UserID)
		}
	}
	if err != nil {
		return domain.User{}, "", err
	}
	return h.resolveExternalUser(ctx, provider, identity, user)
}

// resolveExternalUser applies the provider assertions that remain
// authoritative after an external subject has been linked. OpenID Connect
// already writes its role claim through; its preferred_username must receive
// the same treatment so linking a pre-existing local bootstrap account by
// verified email cannot keep presenting that account's seed display name as
// the Shauth identity.
func (h LoginHandler) resolveExternalUser(ctx context.Context, provider string, identity externalIdentity, user domain.User) (domain.User, domain.WorkspaceRole, error) {
	user, role, err := h.resolveWorkspaceRole(ctx, provider, identity.Role, user)
	if err != nil || provider != "oidc" {
		return user, role, err
	}
	username := strings.TrimSpace(identity.PreferredUsername)
	if username == "" {
		username = strings.TrimSpace(identity.Name)
	}
	if username == "" {
		username = strings.TrimSpace(identity.Email)
	}
	if displayName(user) == username {
		return user, role, nil
	}
	profile := user.Profile
	profile.DisplayName = username
	user, err = h.service.SetUserProfile(ctx, h.workspace, user.ID, profile)
	return user, role, err
}

// resolveWorkspaceRole returns the durable workspace role that a session's
// authority is derived from. For an OpenID Connect identity the provider's role
// claim is authoritative and is written through first; every other provider
// carries no role assertion, so the stored membership decides.
func (h LoginHandler) resolveWorkspaceRole(ctx context.Context, provider, role string, user domain.User) (domain.User, domain.WorkspaceRole, error) {
	if provider != "oidc" {
		durable, err := h.workspaceRole(ctx, user.ID)
		if err != nil {
			return domain.User{}, "", err
		}
		return user, durable, nil
	}
	workspaceRole, err := oidcWorkspaceRole(role)
	if err != nil {
		return domain.User{}, "", err
	}
	// Writing the provider's role claim through is a role change, which no member
	// may perform and which the signing-in user certainly may not perform on
	// themselves. It runs as the system operation whose authority is the ID token
	// verified earlier in the callback.
	if err := h.service.SynchronizeExternalUserRole(ctx, h.workspace, user.ID, workspaceRole); err != nil {
		return domain.User{}, "", err
	}
	return user, workspaceRole, nil
}

// workspaceRole reads a user's own durable workspace membership role.
//
// It reads exactly the one membership it needs, as the user it is about. The
// previous implementation paged the whole workspace through AdminListUsers as the
// configured lookup identity, which made an administrative read a dependency of
// every sign-in — so gating that read on a real administrator would have locked
// every member out — and cost O(workspace) work for an O(1) question.
func (h LoginHandler) workspaceRole(ctx context.Context, userID domain.UserID) (domain.WorkspaceRole, error) {
	membership, err := h.service.WorkspaceMembership(ctx, h.workspace, userID, userID)
	if err != nil {
		return "", err
	}
	return membership.Role, nil
}

func oidcWorkspaceRole(role string) (domain.WorkspaceRole, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "developer":
		return domain.WorkspaceRoleMember, nil
	case "admin":
		return domain.WorkspaceRoleAdmin, nil
	default:
		return "", errors.New("OpenID Connect identity has no supported access role")
	}
}

type externalIdentity struct {
	Subject           string
	Email             string
	EmailVerified     bool
	Name              string
	PreferredUsername string
	Role              string
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
}

func (h LoginHandler) exchangeCode(ctx context.Context, provider ProviderConfig, code, verifier, name string) (tokenResponse, error) {
	if strings.TrimSpace(code) == "" {
		return tokenResponse{}, errors.New("authorization code is required")
	}
	form := url.Values{"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "code": {code}, "code_verifier": {verifier}, "grant_type": {"authorization_code"}, "redirect_uri": {h.callbackURL(name)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		return tokenResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token endpoint returned %s", response.Status)
	}
	var value tokenResponse
	if err := decodeAuthorizationJSON(response.Body, maxAuthorizationTokenResponse, &value); err != nil || strings.TrimSpace(value.AccessToken) == "" {
		return tokenResponse{}, errors.New("token response did not contain an access token")
	}
	value.AccessToken = strings.TrimSpace(value.AccessToken)
	value.IDToken = strings.TrimSpace(value.IDToken)
	if provider.Issuer != "" && value.IDToken == "" {
		return tokenResponse{}, errors.New("OpenID Connect token response did not contain an ID token")
	}
	return value, nil
}

func (h LoginHandler) verifyOIDCLoginToken(ctx context.Context, provider ProviderConfig, raw, expectedNonce string) (string, string, time.Time, error) {
	if provider.verifier == nil || strings.TrimSpace(raw) == "" {
		return "", "", time.Time{}, errors.New("OpenID Connect ID token verifier is unavailable")
	}
	token, err := provider.verifier.Verify(ctx, raw)
	if err != nil {
		return "", "", time.Time{}, err
	}
	var claims struct {
		Nonce string `json:"nonce"`
		SID   string `json:"sid"`
	}
	if err := token.Claims(&claims); err != nil {
		return "", "", time.Time{}, err
	}
	if expectedNonce == "" || !hmac.Equal([]byte(claims.Nonce), []byte(expectedNonce)) {
		return "", "", time.Time{}, errors.New("OpenID Connect ID token nonce is invalid")
	}
	return token.Subject, strings.TrimSpace(claims.SID), token.Expiry.UTC(), nil
}

func (h LoginHandler) logoutRedirectURL(ctx context.Context, sessionToken string) (string, error) {
	record, err := h.service.LookupSession(ctx, sessionToken)
	if err != nil {
		return "", err
	}
	if record.OIDCProvider == "" {
		return "/signed-out", nil
	}
	provider, ok := h.providers[record.OIDCProvider]
	if !ok {
		return "", fmt.Errorf("OpenID Connect provider %q is not configured", record.OIDCProvider)
	}
	if record.OIDCIDToken == "" {
		return "", errors.New("OpenID Connect session has no ID token")
	}
	if provider.EndSessionURL == "" {
		return "", fmt.Errorf("OpenID Connect provider %q has no end-session endpoint", record.OIDCProvider)
	}
	endpoint, err := url.Parse(provider.EndSessionURL)
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("id_token_hint", record.OIDCIDToken)
	query.Set("client_id", provider.ClientID)
	query.Set("post_logout_redirect_uri", h.publicURL+"/auth/shauth/logout/complete")
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func (h LoginHandler) userInfo(ctx context.Context, provider ProviderConfig, token, name string) (externalIdentity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.UserInfoURL, nil)
	if err != nil {
		return externalIdentity{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		return externalIdentity{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return externalIdentity{}, fmt.Errorf("userinfo endpoint returned %s", response.Status)
	}
	var value struct {
		Subject           string          `json:"sub"`
		ID                any             `json:"id"`
		Email             string          `json:"email"`
		EmailVerified     json.RawMessage `json:"email_verified"`
		Login             string          `json:"login"`
		Name              string          `json:"name"`
		PreferredUsername string          `json:"preferred_username"`
		Role              string          `json:"role"`
	}
	if err := decodeAuthorizationJSON(response.Body, maxAuthorizationUserInfoResponse, &value); err != nil {
		return externalIdentity{}, err
	}
	identity := externalIdentity{
		Subject:           value.Subject,
		Email:             strings.ToLower(strings.TrimSpace(value.Email)),
		EmailVerified:     assertedEmailVerified(value.EmailVerified),
		Name:              strings.TrimSpace(value.Name),
		PreferredUsername: strings.TrimSpace(value.PreferredUsername),
		Role:              strings.ToLower(strings.TrimSpace(value.Role)),
	}
	if identity.Subject == "" && value.ID != nil {
		identity.Subject = fmt.Sprint(value.ID)
	}
	// Whether an address is verified is provider-specific knowledge. Requiring
	// every provider to assert `email_verified` refused GitHub for any account
	// with a public profile address and refused Entra for everyone, because
	// neither userinfo endpoint emits that claim at all. Each provider states
	// address ownership in its own way, so each is read in its own way.
	if name == "entra" {
		if identity.Email == "" {
			identity.Email = strings.ToLower(strings.TrimSpace(value.PreferredUsername))
		}
		// Entra emits no email_verified. What stands in for it is the tenant: a
		// deployment pinned to one tenant is reading an address out of a
		// directory that deployment's organization controls. A multi-tenant
		// endpoint is exactly the nOAuth position — an attacker brings their own
		// tenant and asserts any address — so an address from one is never
		// treated as verified.
		identity.EmailVerified = entraTenantIsPinned(provider)
	}
	if name == "github" {
		if provider.EmailURL == "" {
			return externalIdentity{}, errors.New("github email endpoint is required")
		}
		// Always resolved through the email endpoint, which reports `verified`
		// per address, rather than trusting /user. The profile address is public
		// and unverified, and preferring it meant the one call that proves
		// ownership was skipped for every account that had one.
		identity.Email, err = h.githubEmail(ctx, provider.EmailURL, token)
		if err != nil {
			return externalIdentity{}, err
		}
		// githubEmail accepts only a primary, verified address.
		identity.EmailVerified = true
	}
	if identity.Subject == "" || identity.Email == "" {
		return externalIdentity{}, errors.New("userinfo identity is incomplete")
	}
	return identity, nil
}

// assertedEmailVerified reads an `email_verified` claim. Providers emit it as a
// JSON boolean or, less correctly, as the string "true"; anything else — including
// an absent claim — counts as unverified so the decision fails closed.
func assertedEmailVerified(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		return boolean
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.EqualFold(strings.TrimSpace(text), "true")
	}
	return false
}

// entraTenantIsPinned reports whether an Entra provider is configured against a
// single named tenant rather than one of Microsoft's multi-tenant endpoints.
//
// `common`, `organizations` and `consumers` accept sign-ins from any tenant,
// including one the attacker created, so an address asserted through them proves
// nothing. A named tenant is a directory the deploying organization administers.
func entraTenantIsPinned(provider ProviderConfig) bool {
	for _, endpoint := range []string{provider.AuthorizeURL, provider.TokenURL, provider.Issuer} {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return false
		}
		for _, segment := range strings.Split(parsed.Path, "/") {
			switch strings.ToLower(segment) {
			case "common", "organizations", "consumers":
				return false
			}
		}
	}
	return true
}

func (h LoginHandler) githubEmail(ctx context.Context, endpoint, token string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := h.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("github email endpoint returned %s", response.Status)
	}
	var values []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := decodeAuthorizationJSON(response.Body, maxAuthorizationEmailResponse, &values); err != nil {
		return "", err
	}
	for _, value := range values {
		if value.Primary && value.Verified && strings.TrimSpace(value.Email) != "" {
			return strings.ToLower(strings.TrimSpace(value.Email)), nil
		}
	}
	return "", errors.New("github has no verified primary email")
}

func (h LoginHandler) callbackURL(name string) string {
	return h.publicURL + "/auth/" + name + "/callback"
}

func randomURLValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func signState(key []byte, value string) string {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}
