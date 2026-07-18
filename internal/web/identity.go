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
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type ProviderConfig struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthorizeURL string
	TokenURL     string
	UserInfoURL  string
	EmailURL     string
	Scopes       []string
}

type OpenIDConfiguration struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
}

type LoginHandler struct {
	service    chatapi.Service
	workspace  domain.WorkspaceID
	lookupUser domain.UserID
	publicURL  string
	stateKey   []byte
	providers  map[string]ProviderConfig
	client     *http.Client
}

var supportedAuthorizationProviders = map[string]struct{}{
	"entra":  {},
	"github": {},
	"google": {},
	"oidc":   {},
}

func DiscoverOpenIDConnectProvider(ctx context.Context, client *http.Client, issuer, clientID, clientSecret string) (ProviderConfig, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProviderConfig{}, errors.New("OpenID Connect issuer must be an absolute HTTPS URL")
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
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return ProviderConfig{}, fmt.Errorf("decode OpenID Connect discovery: %w", err)
	}
	if strings.TrimRight(document.Issuer, "/") != issuer {
		return ProviderConfig{}, errors.New("OpenID Connect discovery issuer does not match configured issuer")
	}
	for label, endpoint := range map[string]string{"authorization": document.AuthorizationEndpoint, "token": document.TokenEndpoint, "userinfo": document.UserInfoEndpoint} {
		parsedEndpoint, parseErr := url.Parse(strings.TrimSpace(endpoint))
		if parseErr != nil || parsedEndpoint.Scheme != "https" || parsedEndpoint.Host == "" {
			return ProviderConfig{}, fmt.Errorf("OpenID Connect %s endpoint must be an absolute HTTPS URL", label)
		}
	}
	return ProviderConfig{Name: "oidc", ClientID: clientID, ClientSecret: clientSecret, AuthorizeURL: document.AuthorizationEndpoint, TokenURL: document.TokenEndpoint, UserInfoURL: document.UserInfoEndpoint, Scopes: []string{"openid", "profile", "email"}}, nil
}

func NewLoginHandler(service chatapi.Service, workspace domain.WorkspaceID, lookupUser domain.UserID, publicURL string, stateKey []byte, providers []ProviderConfig) (LoginHandler, error) {
	if service == nil || workspace == "" || lookupUser == "" || strings.TrimSpace(publicURL) == "" || len(stateKey) < 32 {
		return LoginHandler{}, errors.New("login requires service, workspace, lookup user, public URL, and a 32-byte state key")
	}
	base, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return LoginHandler{}, errors.New("login public URL must be an absolute HTTPS URL")
	}
	configured := make(map[string]ProviderConfig, len(providers))
	for _, provider := range providers {
		provider.Name = strings.ToLower(strings.TrimSpace(provider.Name))
		provider.ClientID = strings.TrimSpace(provider.ClientID)
		provider.ClientSecret = strings.TrimSpace(provider.ClientSecret)
		provider.AuthorizeURL = strings.TrimSpace(provider.AuthorizeURL)
		provider.TokenURL = strings.TrimSpace(provider.TokenURL)
		provider.UserInfoURL = strings.TrimSpace(provider.UserInfoURL)
		provider.EmailURL = strings.TrimSpace(provider.EmailURL)
		if provider.Name == "" || provider.ClientID == "" || provider.ClientSecret == "" || provider.AuthorizeURL == "" || provider.TokenURL == "" || provider.UserInfoURL == "" {
			return LoginHandler{}, fmt.Errorf("provider %q is incomplete", provider.Name)
		}
		if _, supported := supportedAuthorizationProviders[provider.Name]; !supported {
			return LoginHandler{}, fmt.Errorf("provider %q is unsupported", provider.Name)
		}
		if provider.Name == "github" && provider.EmailURL == "" {
			return LoginHandler{}, errors.New("github provider requires an email endpoint")
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
	return LoginHandler{service: service, workspace: workspace, lookupUser: lookupUser, publicURL: strings.TrimRight(publicURL, "/"), stateKey: append([]byte(nil), stateKey...), providers: configured, client: &http.Client{Timeout: 10 * time.Second}}, nil
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
		return "Single Sign-On"
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
	payload := name + "\x00" + state + "\x00" + verifier
	signature := signState(h.stateKey, payload)
	http.SetCookie(w, &http.Cookie{Name: "sameoldchat_oauth_state", Value: base64.RawURLEncoding.EncodeToString([]byte(payload + "\x00" + signature)), Path: "/auth/", MaxAge: 600, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	query := url.Values{"client_id": {provider.ClientID}, "redirect_uri": {h.callbackURL(name)}, "response_type": {"code"}, "scope": {strings.Join(provider.Scopes, " ")}, "state": {state}, "code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"}}
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
	if len(parts) != 4 || parts[0] != name || !hmac.Equal([]byte(parts[3]), []byte(signState(h.stateKey, strings.Join(parts[:3], "\x00")))) || parts[1] != strings.TrimSpace(r.URL.Query().Get("state")) {
		http.Error(w, "authorization state is invalid", http.StatusBadRequest)
		return
	}
	token, err := h.exchangeCode(r.Context(), h.providers[name], r.URL.Query().Get("code"), parts[2], name)
	if err != nil {
		http.Error(w, "authorization token exchange failed", http.StatusBadGateway)
		return
	}
	identity, err := h.userInfo(r.Context(), h.providers[name], token, name)
	if err != nil || strings.TrimSpace(identity.Email) == "" {
		http.Error(w, "authorization identity is unavailable", http.StatusBadGateway)
		return
	}
	var user domain.User
	link, linkErr := h.service.GetExternalIdentity(r.Context(), h.workspace, name, identity.Subject)
	if linkErr == nil {
		user, err = h.service.UserInfo(r.Context(), h.workspace, h.lookupUser, link.UserID)
	} else if errors.Is(linkErr, store.ErrNotFound) {
		user, err = h.service.UserByEmail(r.Context(), h.workspace, h.lookupUser, identity.Email)
		if err == nil && !user.Deleted {
			linkErr = h.service.CreateExternalIdentity(r.Context(), domain.ExternalIdentity{WorkspaceID: h.workspace, Provider: name, Subject: identity.Subject, UserID: user.ID})
			if errors.Is(linkErr, store.ErrAlreadyExists) {
				link, linkErr = h.service.GetExternalIdentity(r.Context(), h.workspace, name, identity.Subject)
				if linkErr == nil {
					user, err = h.service.UserInfo(r.Context(), h.workspace, h.lookupUser, link.UserID)
				}
			}
		}
	} else {
		err = linkErr
	}
	if err != nil || linkErr != nil || user.Deleted {
		http.Error(w, "authorization identity is not provisioned", http.StatusForbidden)
		return
	}
	sessionToken, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.service.CreateSession(r.Context(), sessionToken, domain.SessionRecord{WorkspaceID: user.WorkspaceID, UserID: user.ID, Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}); err != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: sessionToken, Path: "/", MaxAge: 86400, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "sameoldchat_oauth_state", Value: "", Path: "/auth/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

type externalIdentity struct{ Subject, Email, Name string }

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

func (h LoginHandler) exchangeCode(ctx context.Context, provider ProviderConfig, code, verifier, name string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", errors.New("authorization code is required")
	}
	form := url.Values{"client_id": {provider.ClientID}, "client_secret": {provider.ClientSecret}, "code": {code}, "code_verifier": {verifier}, "grant_type": {"authorization_code"}, "redirect_uri": {h.callbackURL(name)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("token endpoint returned %s", response.Status)
	}
	var value tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil || strings.TrimSpace(value.AccessToken) == "" {
		return "", errors.New("token response did not contain an access token")
	}
	return value.AccessToken, nil
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
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return externalIdentity{}, err
	}
	identity := externalIdentity{Subject: value.Subject, Email: strings.ToLower(strings.TrimSpace(value.Email)), Name: value.Name}
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
	if err := json.NewDecoder(response.Body).Decode(&values); err != nil {
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
