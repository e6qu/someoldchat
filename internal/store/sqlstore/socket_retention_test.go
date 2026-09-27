package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The SQL profile prunes acknowledged Socket Mode rows on the same write paths
// and with the same window as the memory profile; see the memory test of the
// same name.
func TestAcknowledgedSocketModeRowsArePrunedAfterRetention(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "socket-retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expired := now.Add(-store.SocketModeAcknowledgedRetention - time.Minute).UnixNano()
	record := func(envelopeID string) {
		t.Helper()
		if err := s.RecordSocketModeResponse(ctx, domain.SocketModeResponse{AppID: "A1", EnvelopeID: envelopeID, Payload: `{"text":"x"}`, ReceivedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	record("old")
	record("recent")
	record("pending")
	claimed, err := s.ClaimSocketModeResponses(ctx, "A1", "worker", 10, time.Minute)
	if err != nil || len(claimed) != 3 {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if err := s.AckSocketModeResponses(ctx, "worker", claimed[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE socket_mode_responses SET acknowledged_at = ? WHERE envelope_id = 'old'`, expired); err != nil {
		t.Fatal(err)
	}
	record("next")
	if _, err := s.GetSocketModeResponse(ctx, "A1", "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("response acknowledged past retention: err=%v, want it pruned", err)
	}
	for _, kept := range []string{"recent", "pending", "next"} {
		if _, err := s.GetSocketModeResponse(ctx, "A1", kept); err != nil {
			t.Fatalf("response %q was pruned: %v", kept, err)
		}
	}

	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	app := domain.App{ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Example", ClientID: "client", SigningSecretHash: "signing-hash", SigningSecretCiphertext: "v1.encrypted", VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "v1.verification-encrypted", ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateApp(ctx, app, domain.AppManifestRevision{AppID: "A1", Version: 1, Manifest: `{"display_information":{"name":"Example"}}`, CreatedBy: "U1", CreatedAt: now}, domain.OAuthClient{ID: "client", SecretHash: "client-hash", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	response := domain.AppResponseURL{TokenHash: "response-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1", ConversationID: "C1", CreatedAt: now, ExpiresAt: now.Add(time.Minute), UsesRemaining: 5}
	if err := s.CreateAppInteractionCapabilities(ctx, domain.AppTrigger{TokenHash: "trigger-hash", AppID: "A1", WorkspaceID: "T1", UserID: "U1", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, response); err != nil {
		t.Fatal(err)
	}
	for _, envelopeID := range []string{"interaction-old", "interaction-new"} {
		if err := s.CreateSocketModeInteraction(ctx, domain.SocketModeInteraction{EnvelopeID: envelopeID, AppID: "A1", WorkspaceID: "T1", UserID: "U1", Type: "slash_commands", Payload: `{}`, Response: response, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	acknowledge := func() string {
		t.Helper()
		claimed, found, err := s.ClaimSocketModeInteraction(ctx, "A1", "connection", time.Minute)
		if err != nil || !found {
			t.Fatalf("claim found=%v err=%v", found, err)
		}
		if err := s.AckSocketModeInteraction(ctx, "A1", claimed.EnvelopeID, "connection"); err != nil {
			t.Fatal(err)
		}
		return claimed.EnvelopeID
	}
	first := acknowledge()
	if _, err := s.db.ExecContext(ctx, `UPDATE socket_mode_interactions SET acknowledged_at = ? WHERE envelope_id = ?`, expired, first); err != nil {
		t.Fatal(err)
	}
	second := acknowledge()
	if _, err := s.GetSocketModeInteraction(ctx, "A1", first); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("interaction acknowledged past retention: err=%v, want it pruned", err)
	}
	if _, err := s.GetSocketModeInteraction(ctx, "A1", second); err != nil {
		t.Fatalf("interaction inside retention was pruned: %v", err)
	}
	if err := s.AckSocketModeInteraction(ctx, "A1", second, "connection"); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("second acknowledgement error=%v, want %v", err, store.ErrLeaseConflict)
	}
}
