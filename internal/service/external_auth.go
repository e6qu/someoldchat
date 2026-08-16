package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// externalAuthStateLifetime bounds how long a connect flow may sit between the
// redirect out to a provider and its callback.
const externalAuthStateLifetime = 15 * time.Minute

// SetAppExternalAuthProvider declares, or replaces, one OAuth provider an app's
// members may connect an account with. Only the app's owner may declare one; the
// client secret is sealed before storage and never read back out.
func (m Messages) SetAppExternalAuthProvider(ctx context.Context, configurationToken string, appID domain.AppID, config domain.ExternalAuthProviderConfig) error {
	principal, err := m.appConfigurationPrincipal(ctx, configurationToken)
	if err != nil {
		return err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if app.DevelopmentWorkspaceID != principal.WorkspaceID || app.OwnerID != principal.UserID {
		return store.ErrNotFound
	}
	name := strings.TrimSpace(config.Name)
	clientID := strings.TrimSpace(config.ClientID)
	authorizationURL := strings.TrimSpace(config.AuthorizationURL)
	tokenURL := strings.TrimSpace(config.TokenURL)
	if name == "" || len(name) > 80 || clientID == "" || strings.TrimSpace(config.ClientSecret) == "" || !httpsURL(authorizationURL) || !httpsURL(tokenURL) {
		return ErrInvalidExternalAuthProvider
	}
	if len(m.AppCredentialKey) != 32 {
		return ErrAppCredentialKeyUnavailable
	}
	ciphertext, err := secretbox.Seal(m.AppCredentialKey, externalAuthProviderAssociatedData(appID, name), config.ClientSecret)
	if err != nil {
		return err
	}
	scopes := make([]string, 0, len(config.Scopes))
	for _, scope := range config.Scopes {
		if trimmed := strings.TrimSpace(scope); trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	return m.Store.SetExternalAuthProvider(ctx, domain.ExternalAuthProvider{
		AppID: appID, Name: name, ClientID: clientID, ClientSecretCiphertext: ciphertext,
		AuthorizationURL: authorizationURL, TokenURL: tokenURL, Scopes: scopes, CreatedAt: time.Now().UTC(),
	})
}

// AppExternalAuthProviders lists the providers an app declares, without their
// secrets, for any member of the workspace the app is installed in — they are
// the accounts a member may connect.
func (m Messages) AppExternalAuthProviders(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) ([]domain.ExternalAuthProvider, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	if err := m.requireInstalledApp(ctx, workspaceID, appID); err != nil {
		return nil, err
	}
	return m.Store.ListExternalAuthProviders(ctx, appID)
}

// StartExternalAuthConnection begins a member connecting an account with one of
// an app's declared providers. It returns the provider's own authorization URL,
// carrying a sealed state that binds the callback to this member, app, provider
// and workspace so a stray callback cannot mint a connection for anyone else.
func (m Messages) StartExternalAuthConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, providerName, callbackURL string) (string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return "", err
	}
	if err := m.requireInstalledApp(ctx, workspaceID, appID); err != nil {
		return "", err
	}
	provider, err := m.Store.GetExternalAuthProvider(ctx, appID, strings.TrimSpace(providerName))
	if err != nil {
		return "", err
	}
	if len(m.AppCredentialKey) != 32 {
		return "", ErrAppCredentialKeyUnavailable
	}
	state, err := m.sealExternalAuthState(workspaceID, userID, appID, provider.Name)
	if err != nil {
		return "", err
	}
	authorize, err := url.Parse(provider.AuthorizationURL)
	if err != nil {
		return "", ErrInvalidExternalAuthProvider
	}
	query := authorize.Query()
	query.Set("client_id", provider.ClientID)
	query.Set("redirect_uri", callbackURL)
	query.Set("response_type", "code")
	query.Set("state", state)
	if len(provider.Scopes) != 0 {
		query.Set("scope", strings.Join(provider.Scopes, " "))
	}
	authorize.RawQuery = query.Encode()
	return authorize.String(), nil
}

// CompleteExternalAuthConnection finishes a connect flow: it verifies the sealed
// state, exchanges the code at the provider's token endpoint, and stores the
// resulting credential sealed. The credential's secret is never returned — it
// lives only in the store, the way apps.auth.external.get already promises.
func (m Messages) CompleteExternalAuthConnection(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, providerName, code, state, callbackURL string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if strings.TrimSpace(code) == "" {
		return ErrExternalAuthConnection
	}
	if !m.verifyExternalAuthState(state, workspaceID, userID, appID, strings.TrimSpace(providerName)) {
		return ErrExternalAuthConnection
	}
	provider, err := m.Store.GetExternalAuthProvider(ctx, appID, strings.TrimSpace(providerName))
	if err != nil {
		return err
	}
	if len(m.AppCredentialKey) != 32 {
		return ErrAppCredentialKeyUnavailable
	}
	clientSecret, err := secretbox.Open(m.AppCredentialKey, externalAuthProviderAssociatedData(appID, provider.Name), provider.ClientSecretCiphertext)
	if err != nil {
		return err
	}
	accessToken, expiresAt, err := m.exchangeExternalAuthCode(ctx, provider, clientSecret, code, callbackURL)
	if err != nil {
		return ErrExternalAuthConnection
	}
	sealed, err := secretbox.Seal(m.AppCredentialKey, externalAuthTokenAssociatedData(appID, provider.Name), accessToken)
	if err != nil {
		return err
	}
	id, err := domain.PublicID("Et")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload("app.external_token_connected",
		events.String("app_id", string(appID)), events.String("provider_name", provider.Name)), now)
	if err != nil {
		return err
	}
	return m.Store.SetExternalAuthToken(ctx, domain.ExternalAuthToken{
		ID: id, AppID: appID, WorkspaceID: workspaceID, UserID: userID, Provider: provider.Name,
		Ciphertext: sealed, ExpiresAt: expiresAt, CreatedAt: now,
	}, event)
}

// exchangeExternalAuthCode redeems an authorization code at the provider's token
// endpoint. The server reads only the access token and its lifetime; the request
// is the standard authorization-code grant a provider expects.
func (m Messages) exchangeExternalAuthCode(ctx context.Context, provider domain.ExternalAuthProvider, clientSecret, code, callbackURL string) (string, time.Time, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {callbackURL},
		"client_id":     {provider.ClientID},
		"client_secret": {clientSecret},
	}
	requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, provider.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	client := m.AppHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", time.Time{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, err
	}
	if response.StatusCode != http.StatusOK {
		return "", time.Time{}, ErrExternalAuthConnection
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", time.Time{}, err
	}
	if decoded.Error != "" || strings.TrimSpace(decoded.AccessToken) == "" {
		return "", time.Time{}, ErrExternalAuthConnection
	}
	expiresAt := time.Time{}
	if decoded.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(decoded.ExpiresIn) * time.Second)
	}
	return decoded.AccessToken, expiresAt, nil
}

type externalAuthState struct {
	Workspace string `json:"w"`
	User      string `json:"u"`
	App       string `json:"a"`
	Provider  string `json:"p"`
	Expires   int64  `json:"e"`
}

func (m Messages) sealExternalAuthState(workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, provider string) (string, error) {
	encoded, err := json.Marshal(externalAuthState{
		Workspace: string(workspaceID), User: string(userID), App: string(appID), Provider: provider,
		Expires: time.Now().UTC().Add(externalAuthStateLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	return secretbox.Seal(m.AppCredentialKey, "external-auth-state", string(encoded))
}

func (m Messages) verifyExternalAuthState(state string, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, provider string) bool {
	if len(m.AppCredentialKey) != 32 || strings.TrimSpace(state) == "" {
		return false
	}
	opened, err := secretbox.Open(m.AppCredentialKey, "external-auth-state", state)
	if err != nil {
		return false
	}
	var decoded externalAuthState
	if err := json.Unmarshal([]byte(opened), &decoded); err != nil {
		return false
	}
	if decoded.Expires < time.Now().UTC().Unix() {
		return false
	}
	return decoded.Workspace == string(workspaceID) && decoded.User == string(userID) &&
		decoded.App == string(appID) && decoded.Provider == provider
}

func httpsURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

// requireInstalledApp refuses an operation about an app that is not installed
// and enabled in the workspace: a member cannot connect an account for, or list
// the providers of, an app their workspace does not run.
func (m Messages) requireInstalledApp(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID) error {
	if strings.TrimSpace(string(appID)) == "" {
		return ErrInvalidWorkspace
	}
	installations, err := m.Store.ListAppInstallations(ctx, appID)
	if err != nil {
		return err
	}
	for _, installation := range installations {
		if installation.WorkspaceID == workspaceID && installation.Enabled {
			return nil
		}
	}
	return store.ErrNotFound
}
