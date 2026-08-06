package service

import (
	"context"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// A member who asks to invite an organization waits on somebody else's
// decision, and Activity is where they find out. Nobody else is told, and an
// administrator who approves their own request has not been told anything.
func TestASlackConnectDecisionReachesTheMemberWhoAskedForIt(t *testing.T) {
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "host"})
	repository.SeedWorkspace(domain.Workspace{ID: "T2", Name: "guest"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "requester"})
	repository.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"})
	if err := repository.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "shared"})
	repository.SeedConversationMember("C1", "U1")
	repository.SeedConversationMember("C1", "UA")
	messages := Messages{Store: repository}

	invite, err := messages.InviteShared(ctx, "T1", "U1", "C1", "T2", "")
	if err != nil {
		t.Fatal(err)
	}
	// Asking is not being told: nothing lands until somebody decides.
	waiting, err := messages.Activity(ctx, "T1", "U1", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range waiting.Items {
		if item.SharedInviteID == invite.ID {
			t.Fatalf("the requester was told before anyone decided: %+v", item)
		}
	}

	if _, err := messages.ApproveSharedInvite(ctx, "T1", "UA", invite.ID); err != nil {
		t.Fatal(err)
	}
	told, err := messages.Activity(ctx, "T1", "U1", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	var decision domain.ActivityItem
	for _, item := range told.Items {
		if item.SharedInviteID == invite.ID {
			decision = item
		}
	}
	if decision.SharedInviteID == "" {
		t.Fatalf("the requester was not told: %+v", told.Items)
	}
	if decision.SharedInviteStatus != domain.SharedInviteApproved || decision.Conversation != "C1" || !decision.SourceAvailable {
		t.Fatalf("decision = %+v, want an approved, reachable row naming the conversation", decision)
	}

	// The administrator who decided is told nothing: they already know.
	own, err := messages.Activity(ctx, "T1", "UA", domain.ActivityQuery{Page: domain.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range own.Items {
		if item.SharedInviteID == invite.ID {
			t.Fatalf("the decider was told about their own decision: %+v", item)
		}
	}
}
