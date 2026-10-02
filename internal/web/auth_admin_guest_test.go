package web

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Saving a role on a guest makes them a full member of it, so the page must
// not present a guest as an already-selected "Member": the guest's tier is the
// selected, unsubmittable option and no role is pre-selected.
func TestAGuestRowNamesTheTierAndPreselectsNoRole(t *testing.T) {
	if got := guestTierLabel(domain.WorkspaceMembership{Restricted: true}); got != "Guest, several channels" {
		t.Fatalf("multi-channel label=%q", got)
	}
	if got := guestTierLabel(domain.WorkspaceMembership{UltraRestricted: true}); got != "Guest, one channel" {
		t.Fatalf("single-channel label=%q", got)
	}
	if got := guestTierLabel(domain.WorkspaceMembership{Role: domain.WorkspaceRoleMember}); got != "" {
		t.Fatalf("member label=%q", got)
	}
	options := guestRoleOptions("Guest, one channel", assignableRoles(domain.WorkspaceRoleAdmin, domain.WorkspaceRoleMember))
	if len(options) != 3 || options[0].Label != "Guest, one channel" || !options[0].Selected || !options[0].Disabled || options[0].Value != "" {
		t.Fatalf("options=%+v", options)
	}
	for _, option := range options[1:] {
		if option.Selected || option.Disabled {
			t.Fatalf("a role is pre-selected for a guest: %+v", options)
		}
	}
	if options := guestRoleOptions("Guest, one channel", nil); len(options) != 0 {
		t.Fatalf("a row the actor cannot edit gained options: %+v", options)
	}
}
