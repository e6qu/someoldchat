package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A code channel's agent command is a slash command only in its channel: a
// member typing it there reaches the agent that registered it, ahead of a
// workspace app command of the same name, and anywhere else it is not a
// command at all.
func TestCodeChannelCommandsReachTheirAgentInTheirChannel(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	for _, seed := range []func() error{
		func() error {
			return repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test", Domain: "test"})
		},
		func() error { return repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}) },
		func() error { return repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "agent"}) },
		func() error { return repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}) },
		func() error {
			return repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
		},
		func() error { return repository.SeedConversationMember("C1", "U1") },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	key := []byte(strings.Repeat("s", 32))
	verification, err := secretbox.Seal(key, appVerificationTokenAssociatedData("A1"), "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Agent"},"features":{"code_channels":{"enabled":true}},"oauth_config":{"scopes":{"bot":["commands"]}},"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	if err := repository.CreateApp(ctx,
		domain.App{
			ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Agent", ClientID: "client",
			SigningSecretHash: "signing", SigningSecretCiphertext: "sealed",
			VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verification,
			ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
		},
		domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: "A1"},
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: key}
	created, err := messages.CreateCodeChannel(ctx, "T1", "UBOT", "A1", domain.CodeChannelRequest{Name: "review work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember(created.Conversation, "U1"); err != nil {
		t.Fatal(err)
	}

	count, err := messages.SetCodeChannelCommands(ctx, "T1", "UBOT", "A1", created.Conversation, []domain.CodeChannelCommand{
		{Name: "Review", Description: "Review the diff", ArgumentHint: "[path]", ShouldEscape: true},
		{Name: "ship"},
	})
	if err != nil || count != 2 {
		t.Fatalf("setCommands count=%d err=%v", count, err)
	}
	record, err := messages.CodeChannel(ctx, "T1", "U1", created.Conversation)
	if err != nil || len(record.Commands) != 2 || record.Commands[0].Name != "review" || record.Commands[0].BotUserID != "UBOT" || record.Commands[0].AppID != "A1" {
		t.Fatalf("record commands=%+v err=%v", record.Commands, err)
	}

	if err := messages.DispatchSlashCommand(ctx, "T1", "U1", created.Conversation, "", "/review", "src/billing for @alice", "https://chat.example.test"); err != nil {
		t.Fatal(err)
	}
	interaction, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket", time.Minute)
	if err != nil || !found {
		t.Fatalf("interaction=%+v found=%v err=%v", interaction, found, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(interaction.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if interaction.Type != "slash_commands" || payload["command"] != "/review" || payload["channel_id"] != string(created.Conversation) ||
		payload["api_app_id"] != "A1" || payload["text"] != "src/billing for <@U1>" {
		t.Fatalf("payload=%s", interaction.Payload)
	}
	if err := messages.DispatchSlashCommand(ctx, "T1", "U1", "C1", "", "/review", "", "https://chat.example.test"); !errors.Is(err, domain.ErrSlashCommandNotFound) {
		t.Fatalf("an agent command outside its channel: %v", err)
	}
	// A member of the workspace who is not in the channel cannot reach its
	// agent: the command answers only where it was typed by a member.
	if err := messages.DispatchSlashCommand(ctx, "T1", "U2", created.Conversation, "", "/review", "", "https://chat.example.test"); err == nil {
		t.Fatal("a member outside the channel invoked its agent's command")
	}
	if _, found, err := repository.ClaimSocketModeInteraction(ctx, "A1", "socket", time.Minute); err != nil || found {
		t.Fatalf("a refused command still reached the agent: found=%v err=%v", found, err)
	}

	for name, commands := range map[string][]domain.CodeChannelCommand{
		"a Slack command":    {{Name: "remind"}},
		"a leading slash":    {{Name: "/review"}},
		"a duplicate":        {{Name: "a"}, {Name: "A"}},
		"an empty name":      {{Name: " "}},
		"a 32-character one": {{Name: strings.Repeat("x", 32)}},
		"eleven commands":    make([]domain.CodeChannelCommand, 11),
	} {
		if _, err := messages.SetCodeChannelCommands(ctx, "T1", "UBOT", "A1", created.Conversation, commands); !errors.Is(err, domain.ErrInvalidCodeChannel) {
			t.Errorf("%s: %v, want ErrInvalidCodeChannel", name, err)
		}
	}
	if count, err := messages.SetCodeChannelCommands(ctx, "T1", "UBOT", "A1", created.Conversation, nil); err != nil || count != 0 {
		t.Fatalf("clearing count=%d err=%v", count, err)
	}
	if err := messages.DispatchSlashCommand(ctx, "T1", "U1", created.Conversation, "", "/review", "", "https://chat.example.test"); !errors.Is(err, domain.ErrSlashCommandNotFound) {
		t.Fatalf("a cleared command: %v", err)
	}
}
