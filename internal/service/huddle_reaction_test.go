package service

import (
	"errors"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/events"
)

// TestHuddleReactionReachesOnlyParticipants walks the standing a huddle reaction
// requires: a participant may send a valid emoji, an unknown emoji is refused, a
// channel member who is not in the huddle may not send one, and neither may a
// workspace member outside the conversation.
func TestHuddleReactionReachesOnlyParticipants(t *testing.T) {
	ctx, messages := huddleInviteWorld(t)
	call, err := messages.ActiveHuddle(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}

	// A participant sends a standard reaction.
	if err := messages.SendHuddleReaction(ctx, "T1", "U1", call.ID, "tada"); err != nil {
		t.Fatalf("participant reaction refused: %v", err)
	}
	// The colon form normalizes the same way a message reaction does.
	if err := messages.SendHuddleReaction(ctx, "T1", "U2", call.ID, ":heart:"); err != nil {
		t.Fatalf("participant reaction with colons refused: %v", err)
	}
	// An emoji the workspace does not hold is refused.
	if err := messages.SendHuddleReaction(ctx, "T1", "U1", call.ID, "definitely-not-an-emoji"); !errors.Is(err, ErrInvalidReaction) {
		t.Fatalf("unknown emoji error = %v, want ErrInvalidReaction", err)
	}
	// A channel member who has not joined the huddle is not a participant.
	if err := messages.SendHuddleReaction(ctx, "T1", "U3", call.ID, "tada"); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("non-participant channel member error = %v, want ErrInvalidCall", err)
	}
	// A workspace member outside the conversation is refused the same way.
	if err := messages.SendHuddleReaction(ctx, "T1", "U-outsider", call.ID, "tada"); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("outsider error = %v, want ErrInvalidCall", err)
	}
}

// TestHuddleReactionRequiresActiveMembership makes the workspace-membership
// guard load-bearing: a deactivated member cannot send a reaction even though
// they are still recorded as a participant of the running call.
func TestHuddleReactionRequiresActiveMembership(t *testing.T) {
	ctx, messages := huddleInviteWorld(t)
	call, err := messages.ActiveHuddle(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	store := messages.Store
	if err := store.SetUserDeleted(ctx, "T1", "U2", true, events.Event{
		ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U2", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := messages.SendHuddleReaction(ctx, "T1", "U2", call.ID, "tada"); err == nil {
		t.Fatal("a deactivated participant sent a huddle reaction")
	}
}
