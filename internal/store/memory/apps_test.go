package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestAppConfigurationTokenRotationIsOneTime(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	value := domain.AppConfigurationToken{WorkspaceID: "T1", UserID: "U1", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := s.CreateAppConfigurationToken(ctx, "access-one", "refresh-one", value); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupAppConfigurationToken(ctx, "access-one"); err != nil {
		t.Fatal(err)
	}
	next := value
	next.ExpiresAt = next.ExpiresAt.Add(time.Hour)
	if err := s.RotateAppConfigurationToken(ctx, "refresh-one", "access-two", "refresh-two", next); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupAppConfigurationToken(ctx, "access-one"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old access token error=%v, want %v", err, store.ErrNotFound)
	}
	if err := s.RotateAppConfigurationToken(ctx, "refresh-one", "access-replay", "refresh-replay", next); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refresh replay error=%v, want %v", err, store.ErrNotFound)
	}
	if got, err := s.LookupAppConfigurationToken(ctx, "access-two"); err != nil || got.UserID != "U1" {
		t.Fatalf("rotated token=%+v err=%v", got, err)
	}
	for _, plaintext := range []string{"access-one", "refresh-one", "access-two", "refresh-two"} {
		if _, found := s.appConfigurationTokens[plaintext]; found {
			t.Fatalf("plaintext token %q persisted as an access-token key", plaintext)
		}
		if _, found := s.appConfigurationRefreshTokens[plaintext]; found {
			t.Fatalf("plaintext token %q persisted as a refresh-token key", plaintext)
		}
	}
}

func TestAppManifestRevisionAndDeletionContract(t *testing.T) {
	ctx := context.Background()
	s := New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	app := domain.App{ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Example", ClientID: "client", SigningSecretHash: "signing-hash", SigningSecretCiphertext: "v1.encrypted", VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "v1.verification-encrypted", ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now}
	revision := domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"Example"}}`, CreatedBy: "U1", CreatedAt: now}
	if err := s.CreateApp(ctx, app, revision, domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(domain.User{ID: "Ubot", WorkspaceID: "T1", Name: "example-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, domain.Bot{ID: "B1", WorkspaceID: "T1", AppID: "A1", UserID: "Ubot", Name: "example-bot", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", "Ubot"); err != nil {
		t.Fatal(err)
	}
	app.Name = "Example 2"
	app.ManifestVersion = 2
	app.UpdatedAt = now.Add(time.Second)
	revision.Version = 2
	revision.Manifest = `{"display_information":{"name":"Example 2"}}`
	revision.CreatedAt = app.UpdatedAt
	if err := s.UpdateApp(ctx, app, revision); err != nil {
		t.Fatal(err)
	}
	stale := app
	stale.ManifestVersion = 2
	if err := s.UpdateApp(ctx, stale, revision); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update error=%v, want %v", err, store.ErrConflict)
	}
	got, latest, err := s.GetApp(ctx, "A1")
	if err != nil || got.Name != "Example 2" || latest.Version != 2 {
		t.Fatalf("app=%+v revision=%+v err=%v", got, latest, err)
	}
	if err := s.DeleteApp(ctx, "A1", "U1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetApp(ctx, "A1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted app error=%v, want %v", err, store.ErrNotFound)
	}
	if _, err := s.GetOAuthClient(ctx, "client"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted app client error=%v, want %v", err, store.ErrNotFound)
	}
	if botUser, err := s.GetUser(ctx, "Ubot"); err != nil || !botUser.Deleted {
		t.Fatalf("deleted app bot user=%+v err=%v", botUser, err)
	}
	if member, err := s.IsConversationMember(ctx, "C1", "Ubot"); err != nil || member {
		t.Fatalf("deleted app bot membership=%t err=%v", member, err)
	}
}

func TestAppInteractionCapabilitiesAreOneUseAndBounded(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC()
	trigger := domain.AppTrigger{
		TokenHash: "trigger-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	response := domain.AppResponseURL{
		TokenHash: "response-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
		ConversationID: "C1", CreatedAt: now, ExpiresAt: now.Add(time.Minute), UsesRemaining: 5,
	}
	if err := s.CreateAppInteractionCapabilities(ctx, trigger, response); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeAppTrigger(ctx, trigger.TokenHash, "A2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-app trigger error=%v, want %v", err, store.ErrNotFound)
	}
	if got, err := s.ConsumeAppTrigger(ctx, trigger.TokenHash, "A1"); err != nil || got.ConsumedAt.IsZero() {
		t.Fatalf("trigger=%+v err=%v", got, err)
	}
	if _, err := s.ConsumeAppTrigger(ctx, trigger.TokenHash, "A1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("trigger replay error=%v, want %v", err, store.ErrNotFound)
	}
	for remaining := 4; remaining >= 0; remaining-- {
		got, err := s.UseAppResponseURL(ctx, response.TokenHash)
		if err != nil || got.UsesRemaining != remaining {
			t.Fatalf("response use remaining=%d value=%+v err=%v", remaining, got, err)
		}
	}
	if _, err := s.UseAppResponseURL(ctx, response.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("exhausted response error=%v, want %v", err, store.ErrNotFound)
	}

	expired := trigger
	expired.TokenHash = "expired-trigger"
	expired.CreatedAt = now.Add(-time.Minute)
	expired.ExpiresAt = now.Add(-time.Second)
	expiredResponse := response
	expiredResponse.TokenHash = "expired-response"
	expiredResponse.CreatedAt = expired.CreatedAt
	expiredResponse.ExpiresAt = expired.ExpiresAt
	if err := s.CreateAppInteractionCapabilities(ctx, expired, expiredResponse); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeAppTrigger(ctx, expired.TokenHash, "A1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired trigger error=%v, want %v", err, store.ErrNotFound)
	}
	if _, err := s.UseAppResponseURL(ctx, expiredResponse.TokenHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired response error=%v, want %v", err, store.ErrNotFound)
	}
}

func TestSocketModeInteractionQueueTargetsOneAppAndRecoversLeases(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC()
	response := domain.AppResponseURL{
		TokenHash: "response-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
		ConversationID: "C1", CreatedAt: now, ExpiresAt: now.Add(time.Minute), UsesRemaining: 5,
	}
	if err := s.CreateAppInteractionCapabilities(ctx, domain.AppTrigger{
		TokenHash: "trigger-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}, response); err != nil {
		t.Fatal(err)
	}
	value := domain.SocketModeInteraction{
		EnvelopeID: "env-1", AppID: "A1", WorkspaceID: "T1", UserID: "U1",
		Type: "slash_commands", Payload: `{"command":"/hello"}`, Response: response, CreatedAt: now,
	}
	if err := s.CreateSocketModeInteraction(ctx, value); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ClaimSocketModeInteraction(ctx, "A2", "other-app", time.Minute); err != nil || found {
		t.Fatalf("cross-app claim found=%v err=%v", found, err)
	}
	claimed, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "connection-1", time.Minute)
	if err != nil || !found || claimed.EnvelopeID != value.EnvelopeID || claimed.Response.TokenHash != response.TokenHash {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	if _, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "connection-2", time.Minute); err != nil || found {
		t.Fatalf("concurrent claim found=%v err=%v", found, err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", value.EnvelopeID, "connection-2"); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("foreign acknowledgement error=%v", err)
	}
	if err := s.ReleaseSocketModeInteraction(ctx, "A1", value.EnvelopeID, "connection-1", "disconnected", now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	reclaimed, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "connection-2", time.Minute)
	if err != nil || !found || reclaimed.RetryCount != 1 || reclaimed.RetryReason != "disconnected" {
		t.Fatalf("reclaim=%+v found=%v err=%v", reclaimed, found, err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", value.EnvelopeID, "connection-2"); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetSocketModeInteraction(ctx, "A1", value.EnvelopeID)
	if err != nil || stored.AcknowledgedAt.IsZero() {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if _, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "connection-3", time.Minute); err != nil || found {
		t.Fatalf("acknowledged interaction was reclaimed: found=%v err=%v", found, err)
	}
}

func TestEphemeralMessageMutationRemainsRecipientScoped(t *testing.T) {
	ctx := context.Background()
	s := New()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	require(s.SeedWorkspace(domain.Workspace{ID: "T1"}))
	require(s.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "bot"}))
	require(s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}))
	require(s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"}))
	require(s.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}))
	now := domain.MessageInstant(time.Now().UTC())
	value := domain.EphemeralMessage{
		ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "UBOT", AppID: "A1",
		RecipientID: "U1", Text: "before", Timestamp: domain.NewMessageTimestamp(now), CreatedAt: now,
	}
	event := func(id domain.EventID) events.Event {
		result, err := events.New(id, "T1", "UBOT", events.NewPayload(events.EphemeralMessageTopic,
			events.String("user_id", "U1"), events.String("channel_id", "C1"), events.String("text", value.Text),
		), now)
		require(err)
		return result
	}
	require(s.CreateEphemeralMessage(ctx, value, event("E1")))
	if _, err := s.GetEphemeralMessage(ctx, "T1", "U2", value.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-recipient read error=%v", err)
	}
	value.Text = "after"
	require(s.UpdateEphemeralMessage(ctx, value, event("E2")))
	if got, err := s.GetEphemeralMessage(ctx, "T1", "U1", value.ID); err != nil || got.Text != "after" {
		t.Fatalf("updated=%+v err=%v", got, err)
	}
	require(s.DeleteEphemeralMessage(ctx, "T1", "U1", value.ID, event("E3")))
	if _, err := s.GetEphemeralMessage(ctx, "T1", "U1", value.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted read error=%v", err)
	}
}
