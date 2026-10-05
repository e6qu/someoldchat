package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// policyWorld is a workspace with one member of each role, and the three of
// them together in a group DM.
func policyWorld(t *testing.T) (context.Context, Messages, domain.ConversationID) {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	for id, role := range map[domain.UserID]domain.WorkspaceRole{"Uowner": domain.WorkspaceRoleOwner, "Uadmin": domain.WorkspaceRoleAdmin, "Umember": domain.WorkspaceRoleMember} {
		s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)})
		if err := s.SeedWorkspaceRole("T1", id, role); err != nil {
			t.Fatal(err)
		}
	}
	messages := Messages{Store: s}
	opening, err := messages.OpenConversation(ctx, "T1", "Umember", []domain.UserID{"Uadmin", "Uowner"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, messages, opening.Conversation.ID
}

func TestWorkspacePolicyDefaultsToSlacksAndOnlyAnAdministratorChangesIt(t *testing.T) {
	ctx, messages, _ := policyWorld(t)
	policy, err := messages.WorkspacePolicy(ctx, "T1", "Umember")
	if err != nil {
		t.Fatal(err)
	}
	if policy != domain.DefaultWorkspacePolicy() || policy.BroadcastWarningOff || policy.PrivateChannelCreators != domain.PolicyAudienceEveryone {
		t.Fatalf("an unconfigured workspace reads %+v, want Slack's defaults", policy)
	}
	restricted := domain.WorkspacePolicy{BroadcastWarningOff: true, PrivateChannelCreators: domain.PolicyAudienceAdmins}
	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Umember", restricted); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("a member setting the policy: err=%v, want ErrNotWorkspaceAdmin", err)
	}
	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uadmin", domain.WorkspacePolicy{PrivateChannelCreators: "guests"}); !errors.Is(err, domain.ErrInvalidWorkspacePolicy) {
		t.Fatalf("an unknown audience: err=%v, want ErrInvalidWorkspacePolicy", err)
	}
	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uadmin", domain.WorkspacePolicy{}); !errors.Is(err, domain.ErrInvalidWorkspacePolicy) {
		t.Fatalf("a policy naming no audience: err=%v, want ErrInvalidWorkspacePolicy", err)
	}
	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uadmin", restricted); err != nil {
		t.Fatal(err)
	}
	if read, err := messages.WorkspacePolicy(ctx, "T1", "Umember"); err != nil || read != restricted {
		t.Fatalf("a member reads %+v err=%v, want %+v", read, err, restricted)
	}
}

func TestPrivateChannelCreationFollowsTheWorkspacePolicy(t *testing.T) {
	ctx, messages, _ := policyWorld(t)
	if _, err := messages.CreateConversation(ctx, "T1", "Umember", "anyone-private", true); err != nil {
		t.Fatalf("by default a member creates a private channel: %v", err)
	}

	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uadmin", domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceAdmins}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateConversation(ctx, "T1", "Umember", "member-private", true); !errors.Is(err, domain.ErrPrivateChannelCreationRestricted) {
		t.Fatalf("a member under admins-only: err=%v, want ErrPrivateChannelCreationRestricted", err)
	}
	if _, err := messages.CreateConversation(ctx, "T1", "Umember", "member-public", false); err != nil {
		t.Fatalf("the policy governs private channels only, and refused a public one: %v", err)
	}
	if _, err := messages.CreateConversation(ctx, "T1", "Uadmin", "admin-private", true); err != nil {
		t.Fatalf("an administrator under admins-only: %v", err)
	}

	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uowner", domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceOwners}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateConversation(ctx, "T1", "Uadmin", "admin-private-2", true); !errors.Is(err, domain.ErrPrivateChannelCreationRestricted) {
		t.Fatalf("an administrator under owners-only: err=%v, want ErrPrivateChannelCreationRestricted", err)
	}
	if _, err := messages.CreateConversation(ctx, "T1", "Uowner", "owner-private", true); err != nil {
		t.Fatalf("an owner under owners-only: %v", err)
	}
}

func TestGroupDirectConversionFollowsThePrivateChannelPolicy(t *testing.T) {
	ctx, messages, group := policyWorld(t)
	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uadmin", domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceAdmins}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ConvertGroupDirectToPrivate(ctx, "T1", "Umember", group, "planning"); !errors.Is(err, domain.ErrPrivateChannelCreationRestricted) {
		t.Fatalf("a member under admins-only: err=%v, want ErrPrivateChannelCreationRestricted", err)
	}
	unchanged, err := messages.ConversationInfo(ctx, "T1", "Umember", group)
	if err != nil || unchanged.Kind != domain.ConversationTypeMPIM {
		t.Fatalf("a refused conversion left %+v err=%v, want the group DM unchanged", unchanged, err)
	}

	// A one-to-one DM is never convertible, which is a fact about the
	// conversation, not a permission the policy could grant.
	direct, err := messages.OpenConversation(ctx, "T1", "Uadmin", []domain.UserID{"Uowner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ConvertGroupDirectToPrivate(ctx, "T1", "Uadmin", direct.Conversation.ID, "pair"); !errors.Is(err, domain.ErrInvalidConversation) {
		t.Fatalf("a one-to-one DM: err=%v, want ErrInvalidConversation", err)
	}

	converted, err := messages.ConvertGroupDirectToPrivate(ctx, "T1", "Uadmin", group, "planning")
	if err != nil {
		t.Fatalf("an administrator under admins-only: %v", err)
	}
	if converted.Kind != domain.ConversationTypePrivate || converted.Name != "planning" {
		t.Fatalf("converted=%+v", converted)
	}
}

func TestGroupDirectConversionIsOpenToEveryoneByDefault(t *testing.T) {
	ctx, messages, group := policyWorld(t)
	converted, err := messages.ConvertGroupDirectToPrivate(ctx, "T1", "Umember", group, "planning")
	if err != nil {
		t.Fatalf("a member converting under Slack's default: %v", err)
	}
	if converted.Kind != domain.ConversationTypePrivate {
		t.Fatalf("converted=%+v", converted)
	}
}

// A private code channel is a private channel, so an agent acting as a member
// the policy leaves out is refused it, and still makes a public one.
func TestPrivateCodeChannelFollowsThePrivateChannelPolicy(t *testing.T) {
	ctx, messages, _ := policyWorld(t)
	if _, err := messages.SetWorkspacePolicy(ctx, "T1", "Uadmin", domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceAdmins}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateCodeChannel(ctx, "T1", "Umember", "A1", domain.CodeChannelRequest{SessionID: "private-session", Name: "private work", Private: true}); !errors.Is(err, domain.ErrPrivateChannelCreationRestricted) {
		t.Fatalf("a private code channel for a member under admins-only: err=%v, want ErrPrivateChannelCreationRestricted", err)
	}
	if _, err := messages.CreateCodeChannel(ctx, "T1", "Umember", "A1", domain.CodeChannelRequest{SessionID: "public-session", Name: "public work"}); err != nil {
		t.Fatalf("a public code channel under admins-only: %v", err)
	}
	if _, err := messages.CreateCodeChannel(ctx, "T1", "Uadmin", "A1", domain.CodeChannelRequest{SessionID: "admin-session", Name: "admin work", Private: true}); err != nil {
		t.Fatalf("a private code channel for an administrator under admins-only: %v", err)
	}
}

func TestPolicyAudienceAdmitsByRank(t *testing.T) {
	for _, test := range []struct {
		audience domain.PolicyAudience
		role     domain.WorkspaceRole
		want     bool
	}{
		{domain.PolicyAudienceEveryone, domain.WorkspaceRoleMember, true},
		{domain.PolicyAudienceAdmins, domain.WorkspaceRoleMember, false},
		{domain.PolicyAudienceAdmins, domain.WorkspaceRoleAdmin, true},
		{domain.PolicyAudienceAdmins, domain.WorkspaceRoleOwner, true},
		{domain.PolicyAudienceOwners, domain.WorkspaceRoleAdmin, false},
		{domain.PolicyAudienceOwners, domain.WorkspaceRoleOwner, true},
		{domain.PolicyAudienceEveryone, "", false},
		{"", domain.WorkspaceRoleOwner, false},
	} {
		if got := test.audience.Admits(test.role); got != test.want {
			t.Errorf("%q admits %q = %v, want %v", test.audience, test.role, got, test.want)
		}
	}
}
