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

func signedOut(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	message := "Your SameOldChat and organization sign-in sessions have ended."
	if r.URL.Query().Get("global") == "failed" {
		w.WriteHeader(http.StatusServiceUnavailable)
		message = "Your SameOldChat session ended, but the organization identity service could not complete global sign-out."
	}
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light dark"><title>Signed out · SameOldChat</title><style>:root{color-scheme:light dark;--bg:#f8f8fa;--panel:#fff;--text:#1d1c1d;--muted:#5e5e65;--line:#d5d5da;--accent:#611f69;--focus:#1264a3}@media(prefers-color-scheme:dark){:root{--bg:#1a1d21;--panel:#222529;--text:#f4f4f5;--muted:#c7c7cc;--line:#4a4e55;--accent:#b869c2;--focus:#5bb8ff}}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--text);font:16px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(480px,100%);padding:32px;background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:0 14px 42px #0002}h1{margin:0 0 12px;font-size:2rem}p{margin:0 0 24px;color:var(--muted)}a{display:inline-block;padding:11px 16px;border-radius:7px;background:var(--accent);color:#fff;font-weight:700;text-decoration:none}a:focus-visible{outline:3px solid var(--focus);outline-offset:3px}</style></head><body><main class="card"><h1>You’re signed out</h1><p role="status">`+template.HTMLEscapeString(message)+`</p><a href="/auth/oidc">Sign in with Shauth</a></main></body></html>`)
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
		ctx = oidc.ClientContext(ctx, h.client)
		provider, err := oidc.NewProvider(ctx, providerConfig.Issuer)
		if err != nil {
			return backchannelLogoutClaims{}, fmt.Errorf("discover OpenID Connect provider: %w", err)
		}
		verifier = provider.Verifier(&oidc.Config{ClientID: providerConfig.ClientID})
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

func (h LoginHandler) login(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Sign in · SameOldChat</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f8f8fa;color:#1d1c1d;font:16px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(420px,calc(100% - 32px));padding:32px;background:#fff;border:1px solid #ddd;border-radius:12px;box-shadow:0 12px 32px #1d1c1d18}h1{margin-top:0}.provider{display:block;margin:12px 0;padding:12px 16px;border-radius:6px;background:#611f69;color:#fff;text-align:center;text-decoration:none;font-weight:700}</style></head><body><main class="card"><h1>Sign in to SameOldChat</h1><p>Choose your organization’s authorization source.</p>`+h.providerLinks()+`</main></body></html>`)
}

func (h LoginHandler) providerLinks() string {
	var result strings.Builder
	names := make([]string, 0, len(h.providers))
	for name := range h.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result.WriteString(`<a class="provider" href="/auth/`)
		result.WriteString(name)
		result.WriteString(`">Continue with `)
		result.WriteString(providerLabel(name))
		result.WriteString(`</a>`)
	}
	return result.String()
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
	payload := name + "\x00" + state + "\x00" + verifier + "\x00" + nonce
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
	if len(parts) != 5 || parts[0] != name || !hmac.Equal([]byte(parts[4]), []byte(signState(h.stateKey, strings.Join(parts[:4], "\x00")))) || parts[1] != strings.TrimSpace(r.URL.Query().Get("state")) {
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
	user, err := h.resolveIdentityUser(r.Context(), name, identity)
	if err != nil || user.Deleted {
		http.Error(w, "authorization identity is not provisioned", http.StatusForbidden)
		return
	}
	sessionToken, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	record := domain.SessionRecord{WorkspaceID: user.WorkspaceID, UserID: user.ID, Scopes: auth.AllScopes(), ExpiresAt: sessionExpiresAt}
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
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func (h LoginHandler) resolveIdentityUser(ctx context.Context, provider string, identity externalIdentity) (domain.User, error) {
	link, err := h.service.GetExternalIdentity(ctx, h.workspace, provider, identity.Subject)
	if err == nil {
		user, lookupErr := h.service.UserInfo(ctx, h.workspace, h.lookupUser, link.UserID)
		if lookupErr != nil {
			return domain.User{}, lookupErr
		}
		return h.synchronizeOIDCRole(ctx, provider, identity.Role, user)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.User{}, err
	}

	user, err := h.service.UserByEmail(ctx, h.workspace, h.lookupUser, identity.Email)
	if errors.Is(err, store.ErrNotFound) && provider == "oidc" {
		role, roleErr := oidcWorkspaceRole(identity.Role)
		if roleErr != nil {
			return domain.User{}, roleErr
		}
		displayName := identity.Name
		if displayName == "" {
			displayName = identity.PreferredUsername
		}
		if displayName == "" {
			displayName = identity.Email
		}
		user, err = h.service.AdminCreateUser(ctx, h.workspace, h.lookupUser, identity.Email, displayName, role)
		if errors.Is(err, store.ErrAlreadyExists) {
			user, err = h.service.UserByEmail(ctx, h.workspace, h.lookupUser, identity.Email)
		}
	}
	if err != nil || user.Deleted {
		return domain.User{}, err
	}

	err = h.service.CreateExternalIdentity(ctx, domain.ExternalIdentity{WorkspaceID: h.workspace, Provider: provider, Subject: identity.Subject, UserID: user.ID})
	if errors.Is(err, store.ErrAlreadyExists) {
		link, err = h.service.GetExternalIdentity(ctx, h.workspace, provider, identity.Subject)
		if err == nil {
			user, err = h.service.UserInfo(ctx, h.workspace, h.lookupUser, link.UserID)
		}
	}
	if err != nil {
		return domain.User{}, err
	}
	return h.synchronizeOIDCRole(ctx, provider, identity.Role, user)
}

func (h LoginHandler) synchronizeOIDCRole(ctx context.Context, provider, role string, user domain.User) (domain.User, error) {
	if provider != "oidc" {
		return user, nil
	}
	workspaceRole, err := oidcWorkspaceRole(role)
	if err != nil {
		return domain.User{}, err
	}
	if err := h.service.SetUserRole(ctx, h.workspace, h.lookupUser, user.ID, workspaceRole); err != nil {
		return domain.User{}, err
	}
	return user, nil
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
		Subject           string `json:"sub"`
		ID                any    `json:"id"`
		Email             string `json:"email"`
		Login             string `json:"login"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Role              string `json:"role"`
	}
	if err := decodeAuthorizationJSON(response.Body, maxAuthorizationUserInfoResponse, &value); err != nil {
		return externalIdentity{}, err
	}
	identity := externalIdentity{
		Subject:           value.Subject,
		Email:             strings.ToLower(strings.TrimSpace(value.Email)),
		Name:              strings.TrimSpace(value.Name),
		PreferredUsername: strings.TrimSpace(value.PreferredUsername),
		Role:              strings.ToLower(strings.TrimSpace(value.Role)),
	}
	if identity.Subject == "" && value.ID != nil {
		identity.Subject = fmt.Sprint(value.ID)
	}
	if name == "entra" && identity.Email == "" {
		identity.Email = strings.ToLower(strings.TrimSpace(value.PreferredUsername))
	}
	if name == "github" && identity.Email == "" {
		if provider.EmailURL == "" {
			return externalIdentity{}, errors.New("github email endpoint is required")
		}
		identity.Email, err = h.githubEmail(ctx, provider.EmailURL, token)
		if err != nil {
			return externalIdentity{}, err
		}
	}
	if identity.Subject == "" || identity.Email == "" {
		return externalIdentity{}, errors.New("userinfo identity is incomplete")
	}
	return identity, nil
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
