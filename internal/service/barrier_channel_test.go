package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// seedBarrieredWorld builds two groups the workspace has barriered from each
// other: U2 is in the primary group, U3 in the barriered-from group, and U4 is in
// neither. C1 (general) has U2 in it. Returns the store, service and context.
func seedBarrieredWorld(t *testing.T) (context.Context, Messages) {
	t.Helper()
	ctx := context.Background()
	s, messages := twoMemberWorkspace(t)
	if err := s.SeedUser(domain.User{ID: "U4", WorkspaceID: "T1", Name: "outsider"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember("C1", "U2"); err != nil {
		t.Fatal(err)
	}
	primary, err := messages.CreateUserGroup(ctx, "T1", "U1", "Bankers", "bankers", "")
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := messages.CreateUserGroup(ctx, "T1", "U1", "Traders", "traders", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetUserGroupUsers(ctx, "T1", "U1", primary.ID, []domain.UserID{"U2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetUserGroupUsers(ctx, "T1", "U1", secondary.ID, []domain.UserID{"U3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AdminCreateBarrier(ctx, "T1", "U1", primary.ID, []domain.UserGroupID{secondary.ID}, domain.BarrierSubjects()); err != nil {
		t.Fatal(err)
	}
	return ctx, messages
}

// TestBarrierKeepsSeparatedGroupsOutOfOneChannel proves a barrier now stops the
// groups it separates from sharing a channel, not only from messaging directly:
// U3 cannot be invited into a channel that holds U2, whichever path invites them,
// while a member in neither group is admitted.
func TestBarrierKeepsSeparatedGroupsOutOfOneChannel(t *testing.T) {
	ctx, messages := seedBarrieredWorld(t)

	// A member's invite: barriered from U2 who is already in C1, U3 is refused.
	if _, err := messages.InviteConversationMembers(ctx, "T1", "U2", "C1", []domain.UserID{"U3"}); !errors.Is(err, ErrBarrieredFromMember) {
		t.Fatalf("inviting a barriered member into a shared channel: err=%v, want ErrBarrieredFromMember", err)
	}
	// Someone in neither group is admitted.
	if _, err := messages.InviteConversationMembers(ctx, "T1", "U2", "C1", []domain.UserID{"U4"}); err != nil {
		t.Fatalf("an unbarriered member was refused: %v", err)
	}

	// An administrator does not get to defeat the barrier either.
	if _, err := messages.AdminInviteConversationMembers(ctx, "T1", "U1", "C1", []domain.UserID{"U3"}); !errors.Is(err, ErrBarrieredFromMember) {
		t.Fatalf("admin invite across a barrier: err=%v, want ErrBarrieredFromMember", err)
	}
}

// TestBarrierHidesSeparatedMembersFromSearch proves the two groups a barrier
// separates do not find each other in people search, while everyone else does.
func TestBarrierHidesSeparatedMembersFromSearch(t *testing.T) {
	ctx, messages := seedBarrieredWorld(t)

	page, err := messages.SearchPeople(ctx, "T1", "U2", "u", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	present := make(map[domain.UserID]bool, len(page.Users))
	for _, user := range page.Users {
		present[user.ID] = true
	}
	if present["U3"] {
		t.Fatalf("a member across the barrier appeared in search: %v", present)
	}
	if !present["U4"] {
		t.Fatalf("an unbarriered member was hidden from search: %v", present)
	}

	// The far side is symmetric: U3 does not find U2 either.
	back, err := messages.SearchPeople(ctx, "T1", "U3", "u", domain.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range back.Users {
		if user.ID == "U2" {
			t.Fatal("the barrier is not symmetric: U3 found U2 in search")
		}
	}
}
