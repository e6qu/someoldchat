package service

import (
	"context"
	"errors"
	"strconv"
	"testing"

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
