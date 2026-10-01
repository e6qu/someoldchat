package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// shortTokenParityToken is a user token in the pre-August-2016 form Slack's
// rotation reference shows: three identifier sections and a six-character
// secret.
const shortTokenParityToken = "xoxp-111-222-333-d6bc76"

// seedShortTokenRotationParity gives the baseline workspace two apps with
// OAuth clients, a short-secret user token and a bot token issued to the
// first, and a token whose secret is already the full length.
func seedShortTokenRotationParity(t *testing.T, target *memory.Store) {
	t.Helper()
	seedBaseline(t, target)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, app := range []struct {
		id     domain.AppID
		client string
		secret string
	}{{"AS", "short-client", "short-secret"}, {"AO", "other-client", "other-secret"}} {
		requireSeed(t, target.CreateApp(ctx, domain.App{
			ID: app.id, DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: string(app.id), ClientID: app.client,
			SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
			VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
			ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
		}, domain.AppManifestRevision{
			AppID: app.id, Version: 1, CreatedBy: "U1", CreatedAt: now,
			Manifest: `{"display_information":{"name":"` + string(app.id) + `"}}`,
		}, domain.OAuthClient{ID: app.client, SecretHash: domain.HashToken(app.secret), AppID: app.id}))
	}
	requireSeed(t, target.SeedToken(ctx, shortTokenParityToken, domain.TokenRecord{
		WorkspaceID: "T1", UserID: "U1", AppID: "AS", TokenType: domain.TokenUser, Scopes: []string{"channels:read", "chat:write"},
	}))
	requireSeed(t, target.SeedToken(ctx, "xoxb-111-222-d6bc76", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "U1", AppID: "AS", TokenType: domain.TokenBot, Scopes: []string{"chat:write"},
	}))
	requireSeed(t, target.SeedToken(ctx, "xoxp-111-222-333-"+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", domain.TokenRecord{
		WorkspaceID: "T1", UserID: "U1", AppID: "AS", TokenType: domain.TokenUser, Scopes: []string{"chat:write"},
	}))
}
