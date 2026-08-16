package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func webhookInstallStore(t *testing.T) (context.Context, *memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "Uinstaller", WorkspaceID: "T1", Name: "alice"})
	s.SeedUser(domain.User{ID: "Ubot", WorkspaceID: "T1", Name: "app"})
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", "Uinstaller"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOAuthClient(ctx, domain.OAuthClient{ID: "client", SecretHash: domain.HashToken("secret"), AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "Ubot", Name: "app", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return ctx, s, Messages{Store: s, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef")}
}

// TestInstallWithIncomingWebhookMintsAWorkingHook drives the whole install-time
// flow: a code carrying the incoming-webhook scope and a chosen channel is
// redeemed, the app's bot is added to that channel, and the minted URL actually
// posts a message there.
func TestInstallWithIncomingWebhookMintsAWorkingHook(t *testing.T) {
	ctx, s, m := webhookInstallStore(t)
	if err := s.CreateOAuthCode(ctx, domain.OAuthCode{
		Code: "code", ClientID: "client", WorkspaceID: "T1", UserID: "Uinstaller",
		BotID: "B1", BotUserID: "Ubot", BotScopes: []string{"incoming-webhook"},
		IncomingWebhookChannel: "C1", RedirectURI: "https://callback",
	}); err != nil {
		t.Fatal(err)
	}
	token, err := m.OAuthV2Exchange(ctx, "client", "secret", "code", "https://callback", false)
	if err != nil {
		t.Fatal(err)
	}
	if token.IncomingWebhookURL == "" || token.IncomingWebhookChannel != "C1" || token.IncomingWebhookChannelName != "general" || token.IncomingWebhookID == "" {
		t.Fatalf("token webhook fields = %+v", token)
	}
	// The bot was added to the channel; otherwise the hook it posts through would
	// be refused for non-membership.
	if member, err := s.IsConversationMember(ctx, "C1", "Ubot"); err != nil || !member {
		t.Fatalf("app bot in channel = %v err=%v, want true", member, err)
	}
	// The minted URL actually posts. Its last path segment is the secret.
	parts := strings.Split(token.IncomingWebhookURL, "/")
	secret := parts[len(parts)-1]
	message, err := m.PostIncomingWebhook(ctx, "T1", "A1", secret, "from the hook", "", "", "")
	if err != nil {
		t.Fatalf("post through the minted hook: %v", err)
	}
	if message.Conversation != "C1" || message.Text != "from the hook" {
		t.Fatalf("hook posted %+v", message)
	}
}

// TestInstallWithoutIncomingWebhookScopeMintsNoHook proves the channel alone
// does not conjure a webhook: without the scope, the exchange leaves the token's
// webhook fields empty and the bot out of the channel.
func TestInstallWithoutIncomingWebhookScopeMintsNoHook(t *testing.T) {
	ctx, s, m := webhookInstallStore(t)
	if err := s.CreateOAuthCode(ctx, domain.OAuthCode{
		Code: "code", ClientID: "client", WorkspaceID: "T1", UserID: "Uinstaller",
		BotID: "B1", BotUserID: "Ubot", BotScopes: []string{"chat:write"},
		IncomingWebhookChannel: "C1", RedirectURI: "https://callback",
	}); err != nil {
		t.Fatal(err)
	}
	token, err := m.OAuthV2Exchange(ctx, "client", "secret", "code", "https://callback", false)
	if err != nil {
		t.Fatal(err)
	}
	if token.IncomingWebhookURL != "" || token.IncomingWebhookID != "" {
		t.Fatalf("a webhook was minted without the scope: %+v", token)
	}
	if member, _ := s.IsConversationMember(ctx, "C1", "Ubot"); member {
		t.Fatal("the bot was added to a channel for a webhook that was never asked for")
	}
}

// TestAuthorizeOAuthRequiresWebhookChannelMembership makes the consent-time
// guard load-bearing: an install that requests the incoming-webhook scope must
// name a channel, and one the installer is actually in — a member cannot grant
// an app posting rights where they cannot post. This lives in the service
// package because the guard-mutation gate does not exercise the web package.
func TestAuthorizeOAuthRequiresWebhookChannelMembership(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	m := Messages{Store: s, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef")}
	configuration, err := m.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	manifest := `{"display_information":{"name":"Hook App"},"oauth_config":{"redirect_urls":["https://client.example/callback"],"scopes":{"bot":["chat:write","incoming-webhook"]}},"settings":{"socket_mode_enabled":true}}`
	_, credentials, err := m.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	request := func(channel domain.ConversationID) domain.OAuthAuthorizationRequest {
		return domain.OAuthAuthorizationRequest{
			ClientID: credentials.ClientID, WorkspaceID: "T1", UserID: "U1", RedirectURI: "https://client.example/callback",
			BotScopes: []string{"chat:write", "incoming-webhook"}, IncomingWebhookChannel: channel,
		}
	}
	// No channel named for the webhook scope: refused.
	if _, err := m.AuthorizeOAuth(ctx, request("")); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("no-channel authorize = %v, want ErrInvalidOAuth", err)
	}
	// A channel the installer is not a member of: refused by the membership guard.
	if _, err := m.AuthorizeOAuth(ctx, request("C1")); !errors.Is(err, ErrInvalidOAuth) {
		t.Fatalf("non-member channel authorize = %v, want ErrInvalidOAuth", err)
	}
	// Once the installer is in the channel, the grant is created.
	if err := s.SeedConversationMember("C1", "U1"); err != nil {
		t.Fatal(err)
	}
	authorization, err := m.AuthorizeOAuth(ctx, request("C1"))
	if err != nil || authorization.Code == "" || authorization.IncomingWebhookChannel != "C1" {
		t.Fatalf("member authorize = %+v err=%v", authorization, err)
	}
}
