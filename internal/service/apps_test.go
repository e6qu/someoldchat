package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestAppEventRequestURLIsVerifiedWithSignedSlackChallenge(t *testing.T) {
	ctx := context.Background()
	type receivedRequest struct {
		body      []byte
		timestamp string
		signature string
	}
	received := make(chan receivedRequest, 2)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		var payload struct {
			Type      string `json:"type"`
			Token     string `json:"token"`
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Type != "url_verification" || payload.Token == "" || payload.Challenge == "" {
			t.Errorf("verification payload=%s err=%v", body, err)
			http.Error(w, "payload", http.StatusBadRequest)
			return
		}
		received <- receivedRequest{body: body, timestamp: r.Header.Get("X-Slack-Request-Timestamp"), signature: r.Header.Get("X-Slack-Signature")}
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": payload.Challenge})
	}))
	defer receiver.Close()

	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32)), AppHTTPClient: receiver.Client()}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Events"},"settings":{"event_subscriptions":{"request_url":"` + receiver.URL + `","bot_events":["reaction_added"]}}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	assertSigned := func(request receivedRequest) {
		t.Helper()
		instant := time.Unix(mustParseInt64(t, request.timestamp), 0).UTC()
		expected, err := events.SlackSignature(credentials.SigningSecret, instant, request.body)
		if err != nil || expected != request.signature {
			t.Fatalf("signature=%q want=%q err=%v", request.signature, expected, err)
		}
	}
	assertSigned(<-received)
	if _, err := messages.UpdateAppFromManifest(ctx, configuration.Token, app.ID, strings.Replace(manifest, `"Events"`, `"Events 2"`, 1)); err != nil {
		t.Fatal(err)
	}
	assertSigned(<-received)

	stored, _, err := repository.GetApp(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.SigningSecretCiphertext, credentials.SigningSecret) || strings.Contains(stored.VerificationTokenCiphertext, credentials.VerificationToken) {
		t.Fatal("stored application credential ciphertext contains plaintext")
	}
	if opened, err := messages.OpenAppSigningSecret(stored); err != nil || opened != credentials.SigningSecret {
		t.Fatalf("opened signing secret=%q err=%v", opened, err)
	}
}

func mustParseInt64(t *testing.T, value string) int64 {
	t.Helper()
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatalf("parse integer %q: %v", value, err)
	}
	return number
}

func TestAppManifestLifecycleAndConfigurationTokenRotation(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(configuration.Token, "xoxe.xoxp-") || !strings.HasPrefix(configuration.RefreshToken, "xoxe-") {
		t.Fatalf("configuration token formats=%q %q", configuration.Token, configuration.RefreshToken)
	}
	manifest := `{"display_information":{"name":"Example","description":"An app"},"oauth_config":{"redirect_urls":["https://example.test/oauth"],"scopes":{"bot":["chat:write"],"user":["users:read"]}},"settings":{"socket_mode_enabled":true,"token_rotation_enabled":true,"event_subscriptions":{"bot_events":["message.channels"]}}}`
	app, credentials, err := messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "Example" || app.ManifestVersion != 1 || credentials.ClientID != app.ClientID || len(credentials.ClientSecret) < 64 || len(credentials.SigningSecret) < 64 {
		t.Fatalf("app=%+v credentials=%+v", app, credentials)
	}
	if client, err := repository.GetOAuthClient(ctx, credentials.ClientID); err != nil || client.SecretHash != domain.HashToken(credentials.ClientSecret) {
		t.Fatalf("client=%+v err=%v", client, err)
	}
	_, exported, err := messages.ExportAppManifest(ctx, configuration.Token, app.ID)
	if err != nil || exported != manifest {
		t.Fatalf("export=%q err=%v", exported, err)
	}
	updatedManifest := strings.Replace(manifest, `"Example"`, `"Example 2"`, 1)
	updated, err := messages.UpdateAppFromManifest(ctx, configuration.Token, app.ID, updatedManifest)
	if err != nil || updated.Name != "Example 2" || updated.ManifestVersion != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	oauthRequest := domain.OAuthAuthorizationRequest{ClientID: credentials.ClientID, WorkspaceID: "T1", UserID: "U1", RedirectURI: "https://example.test/oauth", BotScopes: []string{"chat:write"}, UserScopes: []string{"users:read"}, State: "state"}
	authorization, err := messages.AuthorizeOAuth(ctx, oauthRequest)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Code == "" || authorization.BotID == "" || authorization.BotUserID == "" {
		t.Fatalf("oauth authorization=%+v", authorization)
	}
	exchanged, err := messages.OAuthV2Exchange(ctx, credentials.ClientID, credentials.ClientSecret, authorization.Code, authorization.RedirectURI, false)
	if err != nil {
		t.Fatal(err)
	}
	if exchanged.AppID != app.ID || exchanged.BotID != authorization.BotID || exchanged.UserID != authorization.BotUserID || exchanged.InstallerID != "U1" || !strings.HasPrefix(exchanged.AccessToken, "xoxe.xoxb-") || !strings.HasPrefix(exchanged.AuthedUserAccessToken, "xoxe.xoxp-") || !strings.HasPrefix(exchanged.RefreshToken, "xoxe-") || !strings.HasPrefix(exchanged.AuthedUserRefreshToken, "xoxe-") || exchanged.ExpiresAt.IsZero() || exchanged.AuthedUserExpiresAt.IsZero() || strings.Join(exchanged.AuthedUserScopes, " ") != "users:read" {
		t.Fatalf("oauth exchange=%+v authorization=%+v", exchanged, authorization)
	}
	refreshed, err := messages.OAuthV2Refresh(ctx, credentials.ClientID, credentials.ClientSecret, exchanged.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.TokenType != "bot" || !strings.HasPrefix(refreshed.AccessToken, "xoxe.xoxb-") || !strings.HasPrefix(refreshed.RefreshToken, "xoxe-") || refreshed.RefreshToken == exchanged.RefreshToken {
		t.Fatalf("oauth refresh=%+v", refreshed)
	}
	if _, err := messages.OAuthV2Refresh(ctx, credentials.ClientID, credentials.ClientSecret, exchanged.RefreshToken); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("oauth refresh replay error=%v, want %v", err, ErrInvalidOAuth)
	}
	if err := repository.SeedToken(ctx, "xoxb-legacy", domain.TokenRecord{WorkspaceID: "T1", UserID: authorization.BotUserID, AppID: app.ID, BotID: authorization.BotID, TokenType: "bot", Scopes: []string{"chat:write"}}); err != nil {
		t.Fatal(err)
	}
	converted, err := messages.OAuthV2ExchangeToken(ctx, credentials.ClientID, credentials.ClientSecret, "xoxb-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if converted.TokenType != "bot" || !strings.HasPrefix(converted.AccessToken, "xoxe.xoxb-") || !strings.HasPrefix(converted.RefreshToken, "xoxe-") {
		t.Fatalf("oauth token conversion=%+v", converted)
	}
	if _, err := messages.OAuthV2ExchangeToken(ctx, credentials.ClientID, credentials.ClientSecret, "xoxb-legacy"); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("oauth token conversion replay error=%v, want %v", err, ErrInvalidOAuth)
	}
	if _, err := messages.InspectOAuthAuthorization(ctx, domain.OAuthAuthorizationRequest{ClientID: credentials.ClientID, WorkspaceID: "T1", UserID: "U1", RedirectURI: "https://attacker.example/callback", BotScopes: []string{"chat:write"}}); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("unregistered redirect error=%v, want %v", err, ErrInvalidOAuth)
	}
	if _, err := messages.InspectOAuthAuthorization(ctx, domain.OAuthAuthorizationRequest{ClientID: credentials.ClientID, WorkspaceID: "T1", UserID: "U1", RedirectURI: "https://example.test/oauth", BotScopes: []string{"admin"}}); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("undeclared scope error=%v, want %v", err, ErrInvalidOAuth)
	}
	rotated, err := messages.RotateAppConfigurationToken(ctx, configuration.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := messages.ExportAppManifest(ctx, configuration.Token, app.ID); !errors.Is(err, ErrAppConfigurationAuthentication) {
		t.Fatalf("old access error=%v, want %v", err, ErrAppConfigurationAuthentication)
	}
	if _, err := messages.RotateAppConfigurationToken(ctx, configuration.RefreshToken); !errors.Is(err, ErrAppConfigurationAuthentication) {
		t.Fatalf("refresh replay error=%v, want %v", err, ErrAppConfigurationAuthentication)
	}
	if err := messages.DeleteDeveloperApp(ctx, rotated.Token, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.GetApp(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted app error=%v, want %v", err, store.ErrNotFound)
	}
}

func TestAppManifestValidationDoesNotCreatePartialApp(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: []byte(strings.Repeat("k", 32))}
	configuration, err := messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := messages.CreateAppFromManifest(ctx, configuration.Token, `{"display_information":{}}`, ""); !errors.Is(err, ErrInvalidAppManifest) {
		t.Fatalf("invalid manifest error=%v, want %v", err, ErrInvalidAppManifest)
	}
	if apps, err := messages.ListDeveloperApps(ctx, "T1", "U1"); err != nil || len(apps) != 0 {
		t.Fatalf("partial apps=%+v err=%v", apps, err)
	}
}
