package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const appConfigurationTokenLifetime = 12 * time.Hour

var (
	ErrAppConfigurationAuthentication = errors.New("app configuration token is invalid")
	ErrAppCredentialKeyUnavailable    = errors.New("application credential encryption key is unavailable")
	ErrInvalidAppManifest             = errors.New("app manifest is invalid")
)

func appSigningSecretAssociatedData(appID domain.AppID) string {
	return "app:" + string(appID) + ":signing-secret"
}

func appVerificationTokenAssociatedData(appID domain.AppID) string {
	return "app:" + string(appID) + ":verification-token"
}

// OpenAppSigningSecret recovers the signing credential for platform-owned
// Events API, interactivity, and slash-command requests. The ciphertext is
// bound to this application ID, so moving it between rows cannot turn one
// application's credential into another application's authority.
func (m Messages) OpenAppSigningSecret(app domain.App) (string, error) {
	if len(m.AppCredentialKey) != 32 {
		return "", ErrAppCredentialKeyUnavailable
	}
	secret, err := secretbox.Open(m.AppCredentialKey, appSigningSecretAssociatedData(app.ID), app.SigningSecretCiphertext)
	if err != nil {
		return "", fmt.Errorf("open signing secret for app %s: %w", app.ID, err)
	}
	return secret, nil
}

func (m Messages) openAppVerificationToken(app domain.App) (string, error) {
	if len(m.AppCredentialKey) != 32 {
		return "", ErrAppCredentialKeyUnavailable
	}
	secret, err := secretbox.Open(m.AppCredentialKey, appVerificationTokenAssociatedData(app.ID), app.VerificationTokenCiphertext)
	if err != nil {
		return "", fmt.Errorf("open verification token for app %s: %w", app.ID, err)
	}
	return secret, nil
}

func (m Messages) IssueAppConfigurationToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) (domain.AppConfigurationCredentials, error) {
	now := time.Now().UTC()
	accessToken, err := domain.NewRotatingUserToken()
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	refreshToken, err := domain.NewRefreshToken()
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	value := domain.AppConfigurationCredentials{
		Token:        accessToken,
		RefreshToken: refreshToken,
		WorkspaceID:  workspaceID,
		UserID:       userID,
		IssuedAt:     now,
		ExpiresAt:    now.Add(appConfigurationTokenLifetime),
	}
	if err := m.Store.CreateAppConfigurationToken(ctx, accessToken, refreshToken, domain.AppConfigurationToken{
		WorkspaceID: workspaceID,
		UserID:      userID,
		ExpiresAt:   value.ExpiresAt,
	}); err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	return value, nil
}

func (m Messages) RotateAppConfigurationToken(ctx context.Context, refreshToken string) (domain.AppConfigurationCredentials, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return domain.AppConfigurationCredentials{}, ErrAppConfigurationAuthentication
	}
	// The store owns the one-time refresh-token lookup and atomic compare-and-
	// replace. The identity is intentionally not accepted from the caller.
	identity, err := m.Store.LookupAppConfigurationRefreshToken(ctx, refreshToken)
	if errors.Is(err, store.ErrNotFound) {
		return domain.AppConfigurationCredentials{}, ErrAppConfigurationAuthentication
	}
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	now := time.Now().UTC()
	nextAccessToken, err := domain.NewRotatingUserToken()
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	nextRefreshToken, err := domain.NewRefreshToken()
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	value := domain.AppConfigurationCredentials{
		Token:        nextAccessToken,
		RefreshToken: nextRefreshToken,
		WorkspaceID:  identity.WorkspaceID,
		UserID:       identity.UserID,
		IssuedAt:     now,
		ExpiresAt:    now.Add(appConfigurationTokenLifetime),
	}
	err = m.Store.RotateAppConfigurationToken(ctx, refreshToken, nextAccessToken, nextRefreshToken, domain.AppConfigurationToken{
		WorkspaceID: identity.WorkspaceID,
		UserID:      identity.UserID,
		ExpiresAt:   value.ExpiresAt,
	})
	if errors.Is(err, store.ErrNotFound) {
		return domain.AppConfigurationCredentials{}, ErrAppConfigurationAuthentication
	}
	if err != nil {
		return domain.AppConfigurationCredentials{}, err
	}
	return value, nil
}

func (m Messages) ValidateAppManifest(ctx context.Context, configurationToken, appID, manifest string) ([]appmanifest.Error, error) {
	principal, err := m.appConfigurationPrincipal(ctx, configurationToken)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(appID) != "" {
		app, _, err := m.Store.GetApp(ctx, domain.AppID(strings.TrimSpace(appID)))
		if errors.Is(err, store.ErrNotFound) || err == nil && (app.DevelopmentWorkspaceID != principal.WorkspaceID || app.OwnerID != principal.UserID) {
			return nil, store.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
	}
	_, problems := appmanifest.Parse(manifest)
	return problems, nil
}

func (m Messages) CreateAppFromManifest(ctx context.Context, configurationToken, manifest string, teamID domain.WorkspaceID) (domain.App, domain.AppCredentials, error) {
	principal, err := m.appConfigurationPrincipal(ctx, configurationToken)
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	if teamID != "" && teamID != principal.WorkspaceID {
		return domain.App{}, domain.AppCredentials{}, store.ErrNotFound
	}
	parsed, problems := appmanifest.Parse(manifest)
	if len(problems) != 0 {
		return domain.App{}, domain.AppCredentials{}, ErrInvalidAppManifest
	}
	appID, err := domain.NewAppID()
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	clientID, err := domain.PublicID("client_")
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	clientSecret, err := credentialSecret("")
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	signingSecret, err := credentialSecret("")
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	if len(m.AppCredentialKey) != 32 {
		return domain.App{}, domain.AppCredentials{}, ErrAppCredentialKeyUnavailable
	}
	signingSecretCiphertext, err := secretbox.Seal(m.AppCredentialKey, appSigningSecretAssociatedData(appID), signingSecret)
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, fmt.Errorf("seal application signing secret: %w", err)
	}
	verificationToken, err := credentialSecret("")
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	verificationTokenCiphertext, err := secretbox.Seal(m.AppCredentialKey, appVerificationTokenAssociatedData(appID), verificationToken)
	if err != nil {
		return domain.App{}, domain.AppCredentials{}, fmt.Errorf("seal application verification token: %w", err)
	}
	if parsed.EventRequestURL != "" && !parsed.SocketModeEnabled {
		if err := m.verifyEventRequestURL(ctx, parsed.EventRequestURL, signingSecret, verificationToken); err != nil {
			return domain.App{}, domain.AppCredentials{}, fmt.Errorf("%w: event request URL: %v", ErrInvalidAppManifest, err)
		}
	}
	now := time.Now().UTC()
	app := domain.App{
		ID:                          appID,
		DevelopmentWorkspaceID:      principal.WorkspaceID,
		OwnerID:                     principal.UserID,
		Name:                        parsed.Name,
		Description:                 parsed.Description,
		ClientID:                    clientID,
		SigningSecretHash:           domain.HashToken(signingSecret),
		SigningSecretCiphertext:     signingSecretCiphertext,
		VerificationTokenHash:       domain.HashToken(verificationToken),
		VerificationTokenCiphertext: verificationTokenCiphertext,
		ManifestVersion:             1,
		Distribution:                "private",
		SocketModeEnabled:           parsed.SocketModeEnabled,
		TokenRotationEnabled:        parsed.TokenRotationEnabled,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	revision := domain.AppManifestRevision{AppID: appID, Version: 1, Manifest: parsed.JSON, CreatedBy: principal.UserID, CreatedAt: now}
	client := domain.OAuthClient{ID: clientID, SecretHash: domain.HashToken(clientSecret), AppID: appID}
	if err := m.Store.CreateApp(ctx, app, revision, client); err != nil {
		return domain.App{}, domain.AppCredentials{}, err
	}
	return app, domain.AppCredentials{
		ClientID:          clientID,
		ClientSecret:      clientSecret,
		SigningSecret:     signingSecret,
		VerificationToken: verificationToken,
	}, nil
}

func (m Messages) ExportAppManifest(ctx context.Context, configurationToken string, appID domain.AppID) (domain.App, string, error) {
	principal, err := m.appConfigurationPrincipal(ctx, configurationToken)
	if err != nil {
		return domain.App{}, "", err
	}
	app, revision, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return domain.App{}, "", err
	}
	if app.DevelopmentWorkspaceID != principal.WorkspaceID || app.OwnerID != principal.UserID {
		return domain.App{}, "", store.ErrNotFound
	}
	return app, revision.Manifest, nil
}

func (m Messages) UpdateAppFromManifest(ctx context.Context, configurationToken string, appID domain.AppID, manifest string) (domain.App, error) {
	principal, err := m.appConfigurationPrincipal(ctx, configurationToken)
	if err != nil {
		return domain.App{}, err
	}
	parsed, problems := appmanifest.Parse(manifest)
	if len(problems) != 0 {
		return domain.App{}, ErrInvalidAppManifest
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return domain.App{}, err
	}
	if app.DevelopmentWorkspaceID != principal.WorkspaceID || app.OwnerID != principal.UserID {
		return domain.App{}, store.ErrNotFound
	}
	if parsed.EventRequestURL != "" && !parsed.SocketModeEnabled {
		signingSecret, openErr := m.OpenAppSigningSecret(app)
		if openErr != nil {
			return domain.App{}, openErr
		}
		verificationToken, openErr := m.openAppVerificationToken(app)
		if openErr != nil {
			return domain.App{}, openErr
		}
		if verifyErr := m.verifyEventRequestURL(ctx, parsed.EventRequestURL, signingSecret, verificationToken); verifyErr != nil {
			return domain.App{}, fmt.Errorf("%w: event request URL: %v", ErrInvalidAppManifest, verifyErr)
		}
	}
	now := time.Now().UTC()
	app.Name = parsed.Name
	app.Description = parsed.Description
	app.SocketModeEnabled = parsed.SocketModeEnabled
	app.TokenRotationEnabled = parsed.TokenRotationEnabled
	app.ManifestVersion++
	app.UpdatedAt = now
	revision := domain.AppManifestRevision{AppID: app.ID, Version: app.ManifestVersion, Manifest: parsed.JSON, CreatedBy: principal.UserID, CreatedAt: now}
	if err := m.Store.UpdateApp(ctx, app, revision); err != nil {
		return domain.App{}, err
	}
	return app, nil
}

func (m Messages) verifyEventRequestURL(ctx context.Context, target, signingSecret, verificationToken string) error {
	challengeBytes := make([]byte, 24)
	if _, err := rand.Read(challengeBytes); err != nil {
		return err
	}
	challenge := hex.EncodeToString(challengeBytes)
	body, err := json.Marshal(map[string]string{
		"type":      "url_verification",
		"token":     verificationToken,
		"challenge": challenge,
	})
	if err != nil {
		return err
	}
	timestamp := time.Now().UTC()
	signature, err := events.SlackSignature(signingSecret, timestamp, body)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Slack-Request-Timestamp", fmt.Sprint(timestamp.Unix()))
	request.Header.Set("X-Slack-Signature", signature)
	client := &http.Client{Timeout: 3 * time.Second}
	if m.AppHTTPClient != nil {
		client = m.AppHTTPClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("returned HTTP %d", response.StatusCode)
	}
	if strings.TrimSpace(string(responseBody)) == challenge {
		return nil
	}
	var echoed struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(responseBody, &echoed); err != nil || echoed.Challenge != challenge {
		return errors.New("did not echo the URL verification challenge")
	}
	return nil
}

func (m Messages) DeleteDeveloperApp(ctx context.Context, configurationToken string, appID domain.AppID) error {
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
	return m.Store.DeleteApp(ctx, appID, principal.UserID, time.Now().UTC())
}

func (m Messages) ListDeveloperApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.App, error) {
	// The store scopes the listing to the apps this person owns, which is what
	// a developer is shown. Whether they are a member of the workspace at all
	// was nobody's decision until this line: a deactivated account and an
	// identifier belonging to nobody were both answered.
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	return m.Store.ListDeveloperApps(ctx, workspaceID, userID)
}

// AdminFunctions lists the functions the workspace's installed apps declare.
// It reads each app's manifest, which is where a function exists; the product
// stores no separate function record.
func (m Messages) AdminFunctions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID) ([]domain.AppFunction, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	installed, err := m.ListWorkspaceApps(ctx, workspaceID, actorID)
	if err != nil {
		return nil, err
	}
	functions := make([]domain.AppFunction, 0, len(installed))
	for _, app := range installed {
		_, revision, err := m.Store.GetApp(ctx, app.ID)
		if err != nil {
			continue
		}
		parsed, problems := appmanifest.Parse(revision.Manifest)
		if len(problems) != 0 {
			continue
		}
		for callback, function := range parsed.Functions {
			functions = append(functions, domain.AppFunction{
				AppID: app.ID, AppName: app.Name, CallbackID: callback,
				Title: function.Title, Description: function.Description,
			})
		}
	}
	sort.Slice(functions, func(left, right int) bool {
		if functions[left].AppID != functions[right].AppID {
			return functions[left].AppID < functions[right].AppID
		}
		return functions[left].CallbackID < functions[right].CallbackID
	})
	return functions, nil
}

func (m Messages) ListWorkspaceApps(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) ([]domain.InstalledApp, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	snapshots, err := m.Store.ListInstalledApps(ctx)
	if err != nil {
		return nil, err
	}
	values := make([]domain.InstalledApp, 0, len(snapshots))
	for _, snapshot := range snapshots {
		installed, err := m.appInstalledInWorkspace(ctx, snapshot.App.ID, workspaceID)
		if err != nil {
			return nil, err
		}
		if !installed {
			continue
		}
		parsed, problems := appmanifest.Parse(snapshot.Manifest)
		if len(problems) != 0 {
			continue
		}
		var botUserID domain.UserID
		if bot, botErr := m.Store.GetBotByApp(ctx, workspaceID, snapshot.App.ID); botErr == nil {
			botUserID = bot.UserID
		} else if !errors.Is(botErr, store.ErrNotFound) {
			return nil, botErr
		}
		values = append(values, installedAppProjection(snapshot.App, parsed, botUserID))
	}
	sort.Slice(values, func(left, right int) bool {
		if order := strings.Compare(strings.ToLower(values[left].Name), strings.ToLower(values[right].Name)); order != 0 {
			return order < 0
		}
		return values[left].ID < values[right].ID
	})
	return values, nil
}

func (m Messages) AppHome(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.InstalledApp, domain.View, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	snapshot, parsed, err := m.installedApp(ctx, workspaceID, appID)
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	if !parsed.HomeTabEnabled {
		return domain.InstalledApp{}, domain.View{}, ErrAppHomeNotEnabled
	}
	view, err := m.Store.GetPublishedView(ctx, workspaceID, userID, appID)
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	var botUserID domain.UserID
	if bot, botErr := m.Store.GetBotByApp(ctx, workspaceID, snapshot.App.ID); botErr == nil {
		botUserID = bot.UserID
	} else if !errors.Is(botErr, store.ErrNotFound) {
		return domain.InstalledApp{}, domain.View{}, botErr
	}
	return installedAppProjection(snapshot.App, parsed, botUserID), view, err
}

// OpenAppHome records the user journey Slack exposes as app_home_opened. The
// event is addressed to exactly one app, includes the app's DM channel, and
// includes the current view only after views.publish has created one.
func (m Messages) OpenAppHome(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.InstalledApp, domain.View, error) {
	app, view, err := m.AppHome(ctx, workspaceID, userID, appID)
	if err != nil || app.BotUserID == "" {
		return app, view, err
	}
	conversation, err := m.OpenConversation(ctx, workspaceID, userID, []domain.UserID{app.BotUserID})
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	fields := []events.Field{
		events.String("target_app_id", string(app.ID)),
		events.String("user_id", string(userID)),
		events.String("channel_id", string(conversation.ID)),
		events.String("tab", "home"),
	}
	if view.ID != "" {
		interactionView, renderErr := appInteractionView(view)
		if renderErr != nil {
			return domain.InstalledApp{}, domain.View{}, renderErr
		}
		encoded, renderErr := json.Marshal(interactionView)
		if renderErr != nil {
			return domain.InstalledApp{}, domain.View{}, renderErr
		}
		fields = append(fields, events.JSON("view", string(encoded)))
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload("app.home_opened", fields...), now)
	if err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	if err := m.Store.AppendEvent(ctx, event); err != nil {
		return domain.InstalledApp{}, domain.View{}, err
	}
	return app, view, nil
}

func installedAppProjection(app domain.App, parsed appmanifest.Parsed, botUserID domain.UserID) domain.InstalledApp {
	name := strings.TrimSpace(app.Name)
	if parsed.Name != "" {
		name = parsed.Name
	}
	description := strings.TrimSpace(app.Description)
	if parsed.Description != "" {
		description = parsed.Description
	}
	botName := strings.TrimSpace(parsed.BotDisplayName)
	if botName == "" {
		botName = name
	}
	return domain.InstalledApp{
		ID: app.ID, Name: name, Description: description,
		HomeTabEnabled: parsed.HomeTabEnabled, MessagesTabEnabled: parsed.MessagesTabEnabled,
		MessagesTabReadOnly: parsed.MessagesTabReadOnly, BotDisplayName: botName, BotUserID: botUserID,
	}
}

func (m Messages) GetDeveloperApp(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.App, string, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.App{}, "", err
	}
	app, revision, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return domain.App{}, "", err
	}
	if app.DevelopmentWorkspaceID != workspaceID || app.OwnerID != userID {
		return domain.App{}, "", store.ErrNotFound
	}
	return app, revision.Manifest, nil
}

func (m Messages) GetDeveloperAppDeliveryHealth(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) (domain.AppDeliveryHealth, error) {
	app, manifest, err := m.GetDeveloperApp(ctx, workspaceID, userID, appID)
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	parsed, problems := appmanifest.Parse(manifest)
	if len(problems) != 0 {
		return domain.AppDeliveryHealth{}, store.ErrConflict
	}
	health := domain.AppDeliveryHealth{AppID: app.ID}
	if parsed.SocketModeEnabled {
		health.Surface = "socket"
		health.Endpoint = "Socket Mode"
		health.Configured = true
	} else if parsed.EventRequestURL != "" {
		health.Surface = "http"
		health.Endpoint = parsed.EventRequestURL
		health.Configured = true
	}
	installations, err := m.Store.ListAppInstallations(ctx, app.ID)
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	for _, installation := range installations {
		if installation.Enabled {
			health.Installed = true
			break
		}
	}
	if !health.Configured || !health.Installed {
		return health, nil
	}
	cursor, err := m.Store.GetAppEventCursor(ctx, app.ID, health.Surface)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return domain.AppDeliveryHealth{}, err
	}
	if err == nil {
		health.AcknowledgedSequence = cursor.AcknowledgedSequence
		health.InFlightSequence = cursor.InFlightSequence
		health.InFlightUntil = cursor.InFlightUntil
		health.RetryAt = cursor.RetryAt
		health.RetryCount = cursor.RetryCount
		health.RetryReason = cursor.RetryReason
	}
	pending, err := m.Store.ListAppEventsAfter(ctx, app.ID, health.AcknowledgedSequence, 1)
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	if len(pending) != 0 {
		health.PendingEvaluation = true
		health.NextEventTopic = pending[0].Event.Topic
		health.NextEventAt = pending[0].Event.CreatedAt
	}
	attempts, err := m.Store.ListAppDeliveryAttempts(ctx, app.ID, health.Surface, store.AppDeliveryAttemptRetention)
	if err != nil {
		return domain.AppDeliveryHealth{}, err
	}
	health.RecentAttempts = attempts
	for _, attempt := range attempts {
		if attempt.Delivered {
			health.DeliveredCount++
		} else {
			health.FailedCount++
		}
	}
	return health, nil
}

func (m Messages) IssueDeveloperAppToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, scopes []string) (domain.AppTokenCredentials, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.AppTokenCredentials{}, err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return domain.AppTokenCredentials{}, err
	}
	if app.DevelopmentWorkspaceID != workspaceID || app.OwnerID != userID {
		return domain.AppTokenCredentials{}, store.ErrNotFound
	}
	if !app.SocketModeEnabled {
		return domain.AppTokenCredentials{}, store.InvalidArgument("Socket Mode is not enabled for this app")
	}
	scopes = domain.NormalizeScopes(scopes)
	if len(scopes) == 0 {
		scopes = []string{string(auth.ScopeConnectionsWrite)}
	}
	allowed := map[string]bool{string(auth.ScopeConnectionsWrite): true, "authorizations:read": true}
	for _, scope := range scopes {
		if !allowed[scope] {
			return domain.AppTokenCredentials{}, store.InvalidArgument("unsupported app token scope")
		}
	}
	token, err := domain.NewAppToken()
	if err != nil {
		return domain.AppTokenCredentials{}, err
	}
	if err := m.Store.CreateAppToken(ctx, token, domain.AppTokenRecord{AppID: appID, Scopes: scopes, IssuedAt: time.Now().UTC()}); err != nil {
		return domain.AppTokenCredentials{}, err
	}
	return domain.AppTokenCredentials{Token: token, AppID: appID, Scopes: scopes}, nil
}

// ListDeveloperAppTokens reports the app-level tokens an app has issued to its
// owner, each named by its stored hash rather than the secret and carrying its
// issue time and whether it is already revoked. The authority is the same as
// issuing and bulk-revoking one — the app's owner in its development workspace.
func (m Messages) ListDeveloperAppTokens(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) ([]domain.AppTokenSummary, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	if app.DevelopmentWorkspaceID != workspaceID || app.OwnerID != userID {
		return nil, store.ErrNotFound
	}
	return m.Store.ListAppTokens(ctx, appID)
}

// RevokeDeveloperAppToken revokes a single one of an app's tokens by its id,
// leaving the app's other tokens working. It sits beside the bulk revocation
// under the same owner authority; an id that names no token of this app answers
// as a missing one does, so it cannot be used to probe another app's tokens.
func (m Messages) RevokeDeveloperAppToken(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, id string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if app.DevelopmentWorkspaceID != workspaceID || app.OwnerID != userID {
		return store.ErrNotFound
	}
	return m.Store.RevokeAppTokenByID(ctx, appID, id)
}

// RevokeDeveloperAppTokens invalidates every app-level token an app has issued.
// The authority is the same as issuing one — the app's owner in its development
// workspace — because the ability to mint a credential and the ability to
// withdraw it belong together. Lookup already refuses a revoked token, so this
// is the write that makes the app's tokens revocable at all.
func (m Messages) RevokeDeveloperAppTokens(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if app.DevelopmentWorkspaceID != workspaceID || app.OwnerID != userID {
		return store.ErrNotFound
	}
	return m.Store.RevokeAppTokens(ctx, appID)
}

func (m Messages) InspectOAuthAuthorization(ctx context.Context, request domain.OAuthAuthorizationRequest) (domain.OAuthAuthorization, error) {
	request.ClientID = strings.TrimSpace(request.ClientID)
	request.RedirectURI = strings.TrimSpace(request.RedirectURI)
	request.State = strings.TrimSpace(request.State)
	request.CodeChallenge = strings.TrimSpace(request.CodeChallenge)
	request.CodeChallengeMethod = strings.TrimSpace(request.CodeChallengeMethod)
	if request.ClientID == "" || request.WorkspaceID == "" || request.UserID == "" {
		return domain.OAuthAuthorization{}, ErrInvalidOAuth
	}
	if err := m.authorizeWorkspace(ctx, request.WorkspaceID, request.UserID); err != nil {
		return domain.OAuthAuthorization{}, err
	}
	app, revision, err := m.Store.GetAppByClientID(ctx, request.ClientID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.OAuthAuthorization{}, ErrInvalidOAuthClient
	}
	if err != nil {
		return domain.OAuthAuthorization{}, err
	}
	if app.Distribution == "private" && app.DevelopmentWorkspaceID != request.WorkspaceID {
		return domain.OAuthAuthorization{}, ErrInvalidOAuth
	}
	manifest, problems := appmanifest.Parse(revision.Manifest)
	if len(problems) != 0 {
		return domain.OAuthAuthorization{}, ErrInvalidAppManifest
	}
	redirectURI, ok := selectOAuthRedirect(request.RedirectURI, manifest.RedirectURLs)
	if !ok {
		return domain.OAuthAuthorization{}, ErrInvalidOAuth
	}
	botScopes := domain.NormalizeScopes(request.BotScopes)
	userScopes := domain.NormalizeScopes(request.UserScopes)
	if len(botScopes) == 0 && len(userScopes) == 0 {
		botScopes = domain.NormalizeScopes(manifest.BotScopes)
		userScopes = domain.NormalizeScopes(manifest.UserScopes)
	}
	if !scopeSubset(botScopes, manifest.BotScopes) || !scopeSubset(userScopes, manifest.UserScopes) || len(botScopes) == 0 && len(userScopes) == 0 {
		return domain.OAuthAuthorization{}, ErrInvalidOAuth
	}
	if request.CodeChallenge == "" {
		if request.CodeChallengeMethod != "" {
			return domain.OAuthAuthorization{}, ErrInvalidOAuth
		}
	} else if request.CodeChallengeMethod != "S256" || len(request.CodeChallenge) < 43 || len(request.CodeChallenge) > 128 {
		return domain.OAuthAuthorization{}, ErrInvalidOAuth
	}
	return domain.OAuthAuthorization{
		AppID:                  app.ID,
		AppName:                app.Name,
		ClientID:               app.ClientID,
		WorkspaceID:            request.WorkspaceID,
		UserID:                 request.UserID,
		RedirectURI:            redirectURI,
		BotScopes:              botScopes,
		UserScopes:             userScopes,
		State:                  request.State,
		IncomingWebhookChannel: request.IncomingWebhookChannel,
		CodeChallenge:          request.CodeChallenge,
		CodeChallengeMethod:    request.CodeChallengeMethod,
	}, nil
}

func (m Messages) AuthorizeOAuth(ctx context.Context, request domain.OAuthAuthorizationRequest) (domain.OAuthAuthorization, error) {
	authorization, err := m.InspectOAuthAuthorization(ctx, request)
	if err != nil {
		return domain.OAuthAuthorization{}, err
	}
	// An install that asks to post through an incoming webhook must name the
	// channel it will post to, and it must be one the installer can reach — a
	// member cannot grant an app posting rights in a channel they cannot see. The
	// channel is validated now, at consent, so the redemption later has only to
	// mint the hook. A channel named without the scope is ignored rather than
	// stored, so it cannot smuggle a webhook the app never requested.
	if authorization.WantsIncomingWebhook() {
		channel := authorization.IncomingWebhookChannel
		if channel == "" {
			return domain.OAuthAuthorization{}, ErrInvalidOAuth
		}
		if err := m.requireConversationMembership(ctx, authorization.WorkspaceID, authorization.UserID, channel); err != nil {
			return domain.OAuthAuthorization{}, ErrInvalidOAuth
		}
		conversation, err := m.Store.GetConversation(ctx, channel)
		if err != nil || conversation.WorkspaceID != authorization.WorkspaceID || conversation.IsDirectOrGroup() || conversation.Archived {
			return domain.OAuthAuthorization{}, ErrInvalidOAuth
		}
	} else {
		authorization.IncomingWebhookChannel = ""
	}
	code, err := credentialSecret("code-")
	if err != nil {
		return domain.OAuthAuthorization{}, err
	}
	var botUser domain.User
	var bot domain.Bot
	if len(authorization.BotScopes) != 0 {
		botUserID, err := domain.NewUserID()
		if err != nil {
			return domain.OAuthAuthorization{}, err
		}
		botID, err := domain.NewBotID()
		if err != nil {
			return domain.OAuthAuthorization{}, err
		}
		now := time.Now().UTC()
		botUser = domain.User{ID: botUserID, WorkspaceID: authorization.WorkspaceID, Name: authorization.AppName, RealName: authorization.AppName, Presence: domain.PresenceAuto}
		bot = domain.Bot{ID: botID, WorkspaceID: authorization.WorkspaceID, AppID: authorization.AppID, UserID: botUserID, Name: authorization.AppName, UpdatedAt: now}
		authorization.BotID = botID
		authorization.BotUserID = botUserID
	}
	grant := domain.OAuthCode{
		Code:                   code,
		ClientID:               authorization.ClientID,
		WorkspaceID:            authorization.WorkspaceID,
		UserID:                 authorization.UserID,
		Scopes:                 append(append([]string(nil), authorization.BotScopes...), authorization.UserScopes...),
		BotID:                  authorization.BotID,
		BotUserID:              authorization.BotUserID,
		BotScopes:              authorization.BotScopes,
		UserScopes:             authorization.UserScopes,
		RedirectURI:            authorization.RedirectURI,
		IncomingWebhookChannel: authorization.IncomingWebhookChannel,
		CodeChallenge:          authorization.CodeChallenge,
		CodeChallengeMethod:    authorization.CodeChallengeMethod,
	}
	if err := m.Store.CreateOAuthAuthorization(ctx, botUser, bot, grant); err != nil {
		return domain.OAuthAuthorization{}, err
	}
	authorization.Code = code
	return authorization, nil
}

func (m Messages) appConfigurationPrincipal(ctx context.Context, token string) (domain.AppConfigurationToken, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.AppConfigurationToken{}, ErrAppConfigurationAuthentication
	}
	value, err := m.Store.LookupAppConfigurationToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return domain.AppConfigurationToken{}, ErrAppConfigurationAuthentication
	}
	return value, err
}

func selectOAuthRedirect(requested string, allowed []string) (string, bool) {
	if requested == "" {
		if len(allowed) != 1 {
			return "", false
		}
		return allowed[0], true
	}
	parsed, err := url.Parse(requested)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", false
	}
	for _, candidate := range allowed {
		if requested == candidate {
			return requested, true
		}
	}
	return "", false
}

func scopeSubset(requested, allowed []string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, scope := range allowed {
		set[scope] = struct{}{}
	}
	for _, scope := range requested {
		if _, ok := set[scope]; !ok {
			return false
		}
	}
	return true
}

func credentialSecret(prefix string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}
