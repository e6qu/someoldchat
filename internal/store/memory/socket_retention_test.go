package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Acknowledged Socket Mode responses and interactions used to be kept for
// ever. Rows acknowledged past the retention window are pruned on the write
// paths; everything else is kept, including an acknowledged row still inside
// the window, whose replay must stay idempotent.
func TestAcknowledgedSocketModeRowsArePrunedAfterRetention(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC()
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
	s.mu.Lock()
	for key, value := range s.socketResponses {
		if value.EnvelopeID == "old" {
			value.AcknowledgedAt = now.Add(-store.SocketModeAcknowledgedRetention - time.Minute)
			s.socketResponses[key] = value
		}
	}
	s.mu.Unlock()
	record("next")
	if _, err := s.GetSocketModeResponse(ctx, "A1", "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("response acknowledged past retention: err=%v, want it pruned", err)
	}
	for _, kept := range []string{"recent", "pending", "next"} {
		if _, err := s.GetSocketModeResponse(ctx, "A1", kept); err != nil {
			t.Fatalf("response %q was pruned: %v", kept, err)
		}
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
	s.mu.Lock()
	value := s.socketInteractions[first]
	value.AcknowledgedAt = now.Add(-store.SocketModeAcknowledgedRetention - time.Minute)
	s.socketInteractions[first] = value
	s.mu.Unlock()
	second := acknowledge()
	if _, err := s.GetSocketModeInteraction(ctx, "A1", first); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("interaction acknowledged past retention: err=%v, want it pruned", err)
	}
	if _, err := s.GetSocketModeInteraction(ctx, "A1", second); err != nil {
		t.Fatalf("interaction inside retention was pruned: %v", err)
	}
}
