package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// CONNECT-01 forbids promising a place from a stale count, so the capacity is
// enforced inside the transaction that appends the organization. This fills a
// channel to the documented maximum and shows the next acceptance refused —
// with the invitation left acceptable, so the refusal is the capacity and not
// a settled invitation.
func TestSlackConnectCapacityIsEnforcedAtAcceptance(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")
	messages := Messages{Store: store}

	// The host counts towards the capacity, so the channel is full once it and
	// the connected organizations reach the documented maximum.
	teams := make([]domain.WorkspaceID, 0, domain.SlackConnectCapacity)
	for index := 0; index < domain.SlackConnectCapacity-1; index++ {
		id := domain.WorkspaceID("T-seat-" + strconv.Itoa(index))
		store.SeedWorkspace(domain.Workspace{ID: id, Name: "seat"})
		teams = append(teams, id)
	}
	if err := store.SetConversationTeams(ctx, "T1", "C1", teams, false, events.Event{ID: "Eseats", WorkspaceID: "T1", Topic: "conversation.connected", Payload: `{"type":"conversation.connected"}`}); err != nil {
		t.Fatal(err)
	}

	store.SeedWorkspace(domain.Workspace{ID: "T-late", Name: "late"})
	store.SeedUser(domain.User{ID: "U-late", WorkspaceID: "T-late", Name: "late-admin"})
	if err := store.SeedWorkspaceRole("T-late", "U-late", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	invite, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T-late", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AcceptSharedInvite(ctx, "T-late", "U-late", invite.ID); !errors.Is(err, ErrSlackConnectFull) {
		t.Fatalf("acceptance into a full channel err=%v, want the capacity refusal", err)
	}
	// The refusal left the invitation acceptable: nothing was consumed by
	// being told there was no room.
	stored, err := store.GetSharedInvite(ctx, invite.ID)
	if err != nil || stored.Status != domain.SharedInviteApproved {
		t.Fatalf("invitation=%+v err=%v, want it still approved", stored, err)
	}
}

// CONNECT-02: approval and acceptance are different decisions taken by
// different organizations, so neither side can take the other's.
func TestSharedInviteDecisionsBelongToTheirOwnSide(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T2", Name: "guest-admin"})
	for workspace, user := range map[domain.WorkspaceID]domain.UserID{"T1": "U1", "T2": "U2"} {
		if err := store.SeedWorkspaceRole(workspace, user, domain.WorkspaceRoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")
	messages := Messages{Store: store}

	invite, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T2", "")
	if err != nil {
		t.Fatal(err)
	}
	// The invited organization cannot approve its own invitation.
	if _, err := messages.ApproveSharedInvite(ctx, "T2", "U2", invite.ID); err == nil {
		t.Fatal("the invited organization approved the host's invitation")
	}
	if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", invite.ID); err != nil {
		t.Fatal(err)
	}
	// And the host cannot accept on the invited organization's behalf.
	if _, err := messages.AcceptSharedInvite(ctx, "T1", "U1", invite.ID); err == nil {
		t.Fatal("the host accepted its own invitation")
	}
	if _, err := messages.AcceptSharedInvite(ctx, "T2", "U2", invite.ID); err != nil {
		t.Fatal(err)
	}
}

// One outstanding invitation per organization per conversation: two would let
// two acceptances each claim a place.
func TestASecondOutstandingInvitationIsRefused(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")
	messages := Messages{Store: store}
	if _, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T2", ""); err == nil {
		t.Fatal("a second outstanding invitation was recorded for the same organization")
	}
}

// CONNECT-01 requires the expired state to be explicit. Expiry used to be
// spelled out inside SharedInvite.Acceptable, which made acceptance the only
// operation that knew about the deadline: an invitation could be approved long
// after it lapsed, and the approval recorded a live invitation that nothing
// could ever accept.
func TestALapsedInvitationCannotBeApprovedButCanBeWithdrawn(t *testing.T) {
	ctx := context.Background()
	newFixture := func(t *testing.T) (*memory.Store, Messages) {
		t.Helper()
		store := memory.New()
		store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
		store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
		if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
			t.Fatal(err)
		}
		store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
		store.SeedConversationMember("C1", "U1")
		store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
		return store, Messages{Store: store}
	}
	// The deadline is written directly: InviteShared always dates it fourteen
	// days out, and waiting is not a test.
	lapsed := func(t *testing.T, store *memory.Store, id domain.SharedInviteID) {
		t.Helper()
		past := time.Now().UTC().Add(-time.Hour)
		invite := domain.SharedInvite{
			ID: id, WorkspaceID: "T1", ConversationID: "C1", TargetWorkspaceID: "T2",
			InvitedBy: "U1", Status: domain.SharedInvitePending,
			CreatedAt: past.Add(-SharedInviteLifetime), ExpiresAt: past,
		}
		event, err := events.New(domain.EventID("E"+string(id)), "T1", "U1",
			events.NewPayload("shared_invite.created", events.String("invite_id", string(id))), past)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CreateSharedInvite(ctx, invite, event); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("approval is refused", func(t *testing.T) {
		store, messages := newFixture(t)
		lapsed(t, store, "SI_lapsed")
		if _, err := messages.ApproveSharedInvite(ctx, "T1", "U1", "SI_lapsed"); !errors.Is(err, ErrInvitationExpired) {
			t.Fatalf("approving a lapsed invitation err=%v, want ErrInvitationExpired", err)
		}
		// The refusal changed nothing: the invitation is still pending, not
		// approved and not silently settled.
		stored, err := store.GetSharedInvite(ctx, "SI_lapsed")
		if err != nil || stored.Status != domain.SharedInvitePending {
			t.Fatalf("invitation=%+v err=%v, want it still pending", stored, err)
		}
	})

	// Withdrawing stays available, because clearing a queue of dead
	// invitations is the remaining useful action and refusing it would leave
	// them there permanently.
	t.Run("withdrawal is allowed", func(t *testing.T) {
		store, messages := newFixture(t)
		lapsed(t, store, "SI_withdrawn")
		invite, err := messages.DenySharedInvite(ctx, "T1", "U1", "SI_withdrawn")
		if err != nil {
			t.Fatalf("withdrawing a lapsed invitation: %v", err)
		}
		if invite.Status != domain.SharedInviteRevoked {
			t.Fatalf("status=%q, want revoked", invite.Status)
		}
	})
}

// TestExternalInvitePermissionIsStoredReadableAndEnforced closes the gap where
// SetExternalInvitePermissions wrote an event and changed no queryable state:
// the permission is durable now, read back through the service, and enforced
// when a connected organization tries to invite another.
func TestExternalInvitePermissionIsStoredReadableAndEnforced(t *testing.T) {
	store := memory.New()
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "host-admin"})
	if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	store.SeedConversationMember("C1", "U1")

	// A connected organization, and a member of it who is in the shared channel.
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest-org"})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T2", Name: "guest-admin"})
	if err := store.SeedWorkspaceRole("T2", "U2", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	store.SeedConversationMember("C1", "U2")
	if err := store.SetConversationTeams(ctx, "T1", "C1", []domain.WorkspaceID{"T2"}, false, events.Event{
		ID: "E-teams", WorkspaceID: "T1", Topic: "conversation.connected", Payload: `{"type":"conversation.connected"}`,
	}); err != nil {
		t.Fatal(err)
	}
	// A third organization the connected team might invite.
	store.SeedWorkspace(domain.Workspace{ID: "T3", Name: "third-org"})
	store.SeedWorkspace(domain.Workspace{ID: "T4", Name: "fourth-org"})
	messages := Messages{Store: store}

	// With no restriction recorded, the permission reads as allowed and the
	// connected team may invite.
	allowed, err := messages.ExternalInvitePermission(ctx, "T1", "U1", "C1", "T2")
	if err != nil || !allowed {
		t.Fatalf("default permission = %v err = %v, want may-invite", allowed, err)
	}
	if _, err := messages.InviteShared(ctx, "T2", "U2", "C1", "T3", ""); err != nil {
		t.Fatalf("a permitted connected team was refused: %v", err)
	}

	// Restrict it. The stored decision is read back, not merely announced.
	if _, err := messages.SetExternalInvitePermissions(ctx, "T1", "U1", "C1", "T2", false); err != nil {
		t.Fatal(err)
	}
	restricted, err := messages.ExternalInvitePermission(ctx, "T1", "U1", "C1", "T2")
	if err != nil || restricted {
		t.Fatalf("restricted permission = %v err = %v, want denied", restricted, err)
	}

	// Now the connected team is refused when it tries to invite, and with the
	// classified sentinel rather than a not-found.
	if _, err := messages.InviteShared(ctx, "T2", "U2", "C1", "T3", ""); !errors.Is(err, ErrExternalInviteNotPermitted) {
		t.Fatalf("a restricted connected team's invite = %v, want ErrExternalInviteNotPermitted", err)
	}

	// The host is never restricted by this: it owns the channel. A distinct
	// target, because T3 was already invited above and inviting it twice is a
	// separate refusal.
	if _, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T4", ""); err != nil {
		t.Fatalf("the host was refused its own channel's invite: %v", err)
	}
}
