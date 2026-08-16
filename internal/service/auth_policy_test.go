package service

import (
	"context"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// TestEmailPasswordPolicyBindsTheSignInRead proves the member's own read of the
// email_password policy: a bound member reports it, an unbound one does not, and
// removing the assignment clears it. This is the value the SSO sign-in consults
// to refuse a member the policy forbids.
func TestEmailPasswordPolicyBindsTheSignInRead(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"})
	seedWorkspaceAdmin(t, s, "T1", "UA")
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "one"})
	s.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "two"})
	m := Messages{Store: s}

	// Nobody is bound before an assignment.
	if bound, err := m.MemberMustUsePasswordSignIn(ctx, "T1", "U1"); err != nil || bound {
		t.Fatalf("U1 before assignment = %v err=%v, want false", bound, err)
	}

	if err := m.AdminAssignAuthPolicy(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, []string{"U1"}); err != nil {
		t.Fatal(err)
	}
	if bound, err := m.MemberMustUsePasswordSignIn(ctx, "T1", "U1"); err != nil || !bound {
		t.Fatalf("U1 after assignment = %v err=%v, want true", bound, err)
	}
	// An unassigned member is not bound.
	if bound, err := m.MemberMustUsePasswordSignIn(ctx, "T1", "U2"); err != nil || bound {
		t.Fatalf("U2 = %v err=%v, want false", bound, err)
	}

	// Removing the assignment clears the binding.
	if err := m.AdminRemoveAuthPolicyEntities(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, []string{"U1"}); err != nil {
		t.Fatal(err)
	}
	if bound, err := m.MemberMustUsePasswordSignIn(ctx, "T1", "U1"); err != nil || bound {
		t.Fatalf("U1 after removal = %v err=%v, want false", bound, err)
	}
}

// TestSignInPolicyReadRequiresActiveMembership makes the workspace-membership
// guard on the sign-in read load-bearing: a deactivated member cannot read their
// own policy, so the check that refuses them is not dead code.
func TestSignInPolicyReadRequiresActiveMembership(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"})
	seedWorkspaceAdmin(t, s, "T1", "UA")
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "one"})
	m := Messages{Store: s}
	if err := m.AdminAssignAuthPolicy(ctx, "T1", "UA", domain.AuthPolicyEmailPassword, domain.PolicyEntityUser, []string{"U1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MemberMustUsePasswordSignIn(ctx, "T1", "U1"); err == nil {
		t.Fatal("a deactivated member read their own authentication policy")
	}
	// A non-member is refused as well.
	if _, err := m.MemberMustUsePasswordSignIn(ctx, "T1", "U-nobody"); err == nil {
		t.Fatal("a non-member read an authentication policy")
	}
}
