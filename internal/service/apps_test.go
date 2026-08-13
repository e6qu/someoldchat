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

// Journey 12 requires app policy to reach subsequent API, event, Socket Mode,
// scheduled and response-URL use, and states that a disabled app does not
// remain operational through a stale token.
//
// Restriction used to write an approval row and nothing else, so it moved the
// app between two administrative lists and changed nothing: the installation
// stayed enabled, its tokens stayed live, its bot stayed in every conversation,
// and it went on acting. An administrator shutting down a misbehaving app was
// told it had worked.
func TestRestrictingAnAppStopsItActing(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "admin"})
	seedWorkspaceAdmin(t, store, "T1", "U1")
	store.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "bot"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "UBOT")
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Restricted"},"features":{"app_home":{"home_tab_enabled":true}},"oauth_config":{"scopes":{"bot":["chat:write"]}},"settings":{"interactivity":{"is_enabled":true,"request_url":"https://apps.example.test/i"}}}`
	if err := store.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Restricted", ClientID: "client",
		SigningSecretHash: "hash", SigningSecretCiphertext: "cipher",
		VerificationTokenHash: "hash", VerificationTokenCiphertext: "cipher",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBot(ctx, domain.Bot{ID: "B1", AppID: "A1", WorkspaceID: "T1", UserID: "UBOT", Name: "bot", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedToken(ctx, "xoxb-restricted", domain.TokenRecord{
		WorkspaceID: "T1", AppID: "A1", BotID: "B1", UserID: "UBOT",
		Scopes: []string{"chat:write"}, TokenType: domain.TokenBot,
	}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: store}

	home := `{"type":"home","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"hello"}}]}`
	if _, err := messages.PublishView(ctx, "T1", "UBOT", "A1", "U1", home, ""); err != nil {
		t.Fatalf("the app could not act before being restricted: %v", err)
	}

	if err := messages.AdminRestrictApp(ctx, "T1", "U1", "A1", ""); err != nil {
		t.Fatalf("restrict: %v", err)
	}

	if _, err := messages.PublishView(ctx, "T1", "UBOT", "A1", "U1", home, ""); err == nil {
		t.Fatal("a restricted app published to a Home tab")
	}
	// The credential is the other half: a token that outlives the decision
	// leaves the app operational through the Web API whatever the surfaces say.
	record, err := store.LookupToken(ctx, "xoxb-restricted")
	if err != nil {
		t.Fatalf("look up the restricted app's token: %v", err)
	}
	if !record.Revoked {
		t.Fatal("a restricted app kept a live token")
	}
	// The policy is still recorded: applying it must not erase it.
	page, err := messages.AdminListApps(ctx, "T1", "U1", domain.AppApprovalRestricted, domain.PageRequest{Limit: 5})
	if err != nil || len(page.Apps) != 1 {
		t.Fatalf("restricted list=%+v err=%v, want the app recorded", page, err)
	}
}

// Restricting an app the workspace never installed is a legitimate pre-emptive
// decision and must not fail for want of something to uninstall.
func TestRevokingAppTokensRefusesNonOwnerAndMarksThemRevoked(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "member"})
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Sockets"},"settings":{"socket_mode_enabled":true}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Sockets", ClientID: "client",
		SigningSecretHash: "hash", SigningSecretCiphertext: "cipher",
		VerificationTokenHash: "hash", VerificationTokenCiphertext: "cipher",
		SocketModeEnabled: true, ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository}
	credentials, err := messages.IssueDeveloperAppToken(ctx, "T1", "U1", "A1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := repository.LookupAppToken(ctx, credentials.Token); err != nil || record.Revoked {
		t.Fatalf("freshly issued token: record=%+v err=%v", record, err)
	}
	// The authority to withdraw is the authority to mint: a member who does not
	// own the app cannot revoke its tokens, and is told nothing about it.
	if err := messages.RevokeDeveloperAppTokens(ctx, "T1", "U2", "A1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner revoke = %v, want not found", err)
	}
	if record, err := repository.LookupAppToken(ctx, credentials.Token); err != nil || record.Revoked {
		t.Fatalf("token revoked by a non-owner: record=%+v err=%v", record, err)
	}
	if err := messages.RevokeDeveloperAppTokens(ctx, "T1", "U1", "A1"); err != nil {
		t.Fatal(err)
	}
	// Lookup is the check every authenticated request runs; it now reports the
	// token revoked, which the auth layer refuses.
	if record, err := repository.LookupAppToken(ctx, credentials.Token); err != nil || !record.Revoked {
		t.Fatalf("token after the owner revoked: record=%+v err=%v", record, err)
	}
	// A deactivated owner is refused by the workspace-membership guard, not by the
	// ownership check — the check still sees them as the owner. This is the one
	// caller for whom that guard is the only thing standing in the way, so it is
	// what proves the guard on both operations is load-bearing rather than
	// shadowed by the ownership check.
	if err := repository.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U1", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := messages.RevokeDeveloperAppTokens(ctx, "T1", "U1", "A1"); err == nil {
		t.Fatal("a deactivated owner revoked the app's tokens")
	}
	if _, err := messages.IssueDeveloperAppToken(ctx, "T1", "U1", "A1", nil); err == nil {
		t.Fatal("a deactivated owner issued an app token")
	}
}

func TestAnAppCanBeRestrictedBeforeItIsInstalled(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "admin"})
	seedWorkspaceAdmin(t, store, "T1", "U1")
	messages := Messages{Store: store}
	if err := messages.AdminRestrictApp(ctx, "T1", "U1", "A-never", ""); err != nil {
		t.Fatalf("restrict an uninstalled app: %v", err)
	}
}
