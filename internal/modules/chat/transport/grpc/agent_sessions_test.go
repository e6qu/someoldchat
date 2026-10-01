package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedAgentSessionParity is the baseline plus two agent apps whose bots are
// members of C1: AG subscribes to agent_session_stopped and AH does not, so
// the parity case compares the warning, the stop control and the stop itself.
func seedAgentSessionParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	for _, app := range []struct {
		id     domain.AppID
		bot    domain.UserID
		events string
	}{{"AG", "UB", `["agent_session_stopped"]`}, {"AH", "UH", `["message.channels"]`}} {
		requireSeed(t, target.SeedUser(domain.User{ID: app.bot, WorkspaceID: "T1", Name: "bot-" + string(app.id)}))
		requireSeed(t, target.SeedConversationMember("C1", app.bot))
		client := "client-" + string(app.id)
		requireSeed(t, target.CreateApp(ctx, domain.App{
			ID: app.id, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Agent " + string(app.id), ClientID: client,
			SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
			VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
			ManifestVersion: 1, Distribution: "private", CreatedAt: at, UpdatedAt: at,
		}, domain.AppManifestRevision{
			AppID: app.id, Version: 1, CreatedBy: "U1", CreatedAt: at,
			Manifest: `{"display_information":{"name":"Agent ` + string(app.id) + `"},"settings":{"socket_mode_enabled":true,"event_subscriptions":{"bot_events":` + app.events + `}}}`,
		}, domain.OAuthClient{ID: client, SecretHash: "client-hash", AppID: app.id}))
		requireSeed(t, target.CreateBot(ctx, domain.Bot{ID: domain.BotID("B" + string(app.id)), WorkspaceID: "T1", AppID: app.id, UserID: app.bot, Name: string(app.id), UpdatedAt: at}))
		requireSeed(t, target.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.id, WorkspaceID: "T1", Enabled: true, CreatedAt: at}))
	}
}

// projectAgentSession reduces a session to what both compositions must agree
// on: everything but the instants each one stamps itself.
func projectAgentSession(session domain.AgentSession) []any {
	projected := []any{session.WorkspaceID, session.Conversation, session.Title, session.InitiatorUserID, session.Status(), session.CreatedAt.IsZero()}
	for _, agent := range session.Agents {
		projected = append(projected, agent.AppID, agent.Status, agent.Identity, agent.UpdatedAt.IsZero())
	}
	return projected
}
