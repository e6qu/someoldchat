package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestSQLiteAppEventDeliveryCursorSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app-delivery.db")
	repository, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Events", ClientID: "client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", SocketModeEnabled: true, CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"Events"},"settings":{"socket_mode_enabled":true}}`, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	event, err := events.New("E1", "T1", "U1", events.NewPayload("app.test"), now)
	if err != nil {
		t.Fatal(err)
	}
	event.PrivatePayload = `{"content":"restart-safe-private-snapshot"}`
	if err := repository.AppendEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	claimed, _, _, found, err := repository.ClaimAppEvent(ctx, "A1", "socket", "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	if claimed.Event.PrivatePayload != event.PrivatePayload {
		t.Fatalf("claimed private payload=%q want %q", claimed.Event.PrivatePayload, event.PrivatePayload)
	}
	retryAt := now.Add(time.Minute)
	if err := repository.ReleaseAppEvent(ctx, "A1", "socket", "worker", claimed.Sequence, "connection_closed", retryAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	cursor, err := reopened.GetAppEventCursor(ctx, "A1", "socket")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.AppID != "A1" || cursor.Surface != "socket" || cursor.AcknowledgedSequence != 0 ||
		cursor.InFlightSequence != 0 || cursor.RetryCount != 1 || cursor.RetryReason != "connection_closed" ||
		!cursor.RetryAt.Equal(retryAt) {
		t.Fatalf("cursor after restart=%+v", cursor)
	}
	// The failed release recorded a delivery attempt that survives the restart.
	attempts, err := reopened.ListAppDeliveryAttempts(ctx, "A1", "socket", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Delivered || attempts[0].Reason != "connection_closed" ||
		attempts[0].Sequence != claimed.Sequence || attempts[0].Attempt != 1 {
		t.Fatalf("attempts after restart=%+v", attempts)
	}
}

func TestSQLiteAppDeliveryAttemptsAreNewestFirstAndBounded(t *testing.T) {
	ctx := context.Background()
	repository, err := Open(ctx, memoryDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
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
	// Fail the same event more times than the retained window, each release
	// immediately re-claimable, so only the newest attempts survive.
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
	// Newest first: the first row is the last (total-th) attempt.
	if attempts[0].Attempt != total || attempts[len(attempts)-1].Attempt != total-store.AppDeliveryAttemptRetention+1 {
		t.Fatalf("ordering wrong: newest=%d oldest=%d", attempts[0].Attempt, attempts[len(attempts)-1].Attempt)
	}
}
