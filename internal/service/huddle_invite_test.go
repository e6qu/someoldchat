package service

import (
	"context"
	"testing"

	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// huddleInviteWorld seeds a workspace, a channel, three members, and a huddle
// that U1 has started and U2 has joined. U3 is a channel member who is not in
// the huddle — the person an invitation can legitimately reach.
func huddleInviteWorld(t *testing.T) (context.Context, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	requireSeedErr(t, repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}))
	for _, id := range []domain.UserID{"U1", "U2", "U3", "U-outsider"} {
		requireSeedErr(t, repository.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)}))
	}
	requireSeedErr(t, repository.CreateConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}, "U1", events.Event{
		ID: "E-conv", WorkspaceID: "T1", Topic: "conversation.created", CreatedAt: time.Now().UTC(),
	}))
	for _, id := range []domain.UserID{"U1", "U2", "U3"} {
		requireSeedErr(t, repository.SeedConversationMember("C1", id))
	}
	messages := Messages{Store: repository}
	if _, err := messages.StartHuddle(ctx, "T1", "U1", "C1", "Standup"); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.JoinHuddle(ctx, "T1", "U2", "C1"); err != nil {
		t.Fatal(err)
	}
	return ctx, messages
}

func requireSeedErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestAHuddleInvitationReachesTheInviteesActivity is the read-back: the
// invitation is not asserted by ok, it is read back through the invitee's
// Activity, and everyone else's Activity is checked to be empty.
func TestAHuddleInvitationReachesTheInviteesActivity(t *testing.T) {
	ctx, messages := huddleInviteWorld(t)
	if err := messages.InviteToHuddle(ctx, "T1", "U1", "U3", "C1"); err != nil {
		t.Fatal(err)
	}
	page, err := messages.Activity(ctx, "T1", "U3", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("the invitee's activity = %+v, want one invitation", page.Items)
	}
	item := page.Items[0]
	if item.ActorID != "U1" || item.Conversation != "C1" {
		t.Fatalf("invitation item = %+v, want it from U1 in C1", item)
	}
	found := false
	for _, kind := range item.Kinds {
		if kind == domain.ActivityInvitation {
			found = true
		}
	}
	if !found {
		t.Fatalf("invitation item kinds = %v, want invitation", item.Kinds)
	}

	// The inviter is told nothing about their own invitation, and a member who
	// was not invited sees nothing either.
	for _, quiet := range []domain.UserID{"U1", "U2"} {
		other, err := messages.Activity(ctx, "T1", quiet, domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(other.Items) != 0 {
			t.Fatalf("%s activity = %+v, want none", quiet, other.Items)
		}
	}
}

// TestHuddleInvitationEligibility pins who may be invited and by whom.
func TestHuddleInvitationEligibility(t *testing.T) {
	for _, refused := range []struct {
		name    string
		actor   domain.UserID
		invitee domain.UserID
	}{
		{"self", "U1", "U1"},
		{"already in the huddle", "U1", "U2"},
		{"a member outside the huddle inviting", "U3", "U2"},
		{"an outsider to the conversation", "U1", "U-outsider"},
		{"a stranger to the workspace", "U1", "U-nobody"},
	} {
		t.Run(refused.name, func(t *testing.T) {
			ctx, messages := huddleInviteWorld(t)
			if err := messages.InviteToHuddle(ctx, "T1", refused.actor, refused.invitee, "C1"); err == nil {
				t.Fatalf("inviting %s as %s was accepted", refused.invitee, refused.actor)
			}
		})
	}
}
