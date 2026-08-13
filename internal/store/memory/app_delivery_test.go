package memory

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestMemoryAppDeliveryAttemptsAreNewestFirstAndBounded(t *testing.T) {
	ctx := context.Background()
	repository := New()
	now := time.Now().UTC()
	if err := repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Events", ClientID: "client",
		SigningSecretHash: "h", SigningSecretCiphertext: "c", VerificationTokenHash: "h", VerificationTokenCiphertext: "c",
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"Events"},"settings":{"socket_mode_enabled":true}}`, CreatedBy: "U1", CreatedAt: now},
		domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	event, err := events.New("E1", "T1", "U1", events.NewPayload("app.test"), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	total := store.AppDeliveryAttemptRetention + 5
	for i := 0; i < total; i++ {
		claimed, _, _, found, err := repository.ClaimAppEvent(ctx, "A1", "socket", "worker", time.Minute)
		if err != nil || !found {
			t.Fatalf("claim %d found=%v err=%v", i, found, err)
		}
		if err := repository.ReleaseAppEvent(ctx, "A1", "socket", "worker", claimed.Sequence, "connection_closed", now.Add(-time.Second)); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}
	attempts, err := repository.ListAppDeliveryAttempts(ctx, "A1", "socket", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != store.AppDeliveryAttemptRetention {
		t.Fatalf("retained %d attempts, want %d", len(attempts), store.AppDeliveryAttemptRetention)
	}
	if attempts[0].Attempt != total || attempts[len(attempts)-1].Attempt != total-store.AppDeliveryAttemptRetention+1 {
		t.Fatalf("ordering wrong: newest=%d oldest=%d", attempts[0].Attempt, attempts[len(attempts)-1].Attempt)
	}
	if attempts[0].Delivered || attempts[0].Reason != "connection_closed" {
		t.Fatalf("unexpected newest attempt %+v", attempts[0])
	}
}
