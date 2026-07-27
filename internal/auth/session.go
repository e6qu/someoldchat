package auth

import (
	"fmt"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// IsControlPlaneScope reports whether a scope grants authority over workspace
// administration rather than over the caller's own chat activity. Detection is
// by name shape, not by an enumeration, so a scope added to this package later
// is treated as control plane without a second edit here.
func IsControlPlaneScope(scope Scope) bool {
	return scope == ScopeAdmin || strings.HasPrefix(string(scope), "admin.")
}

// SessionScopes is the scope set a browser session is allowed to carry. It has
// no exported fields and exactly one constructor, so the authority of a signed
// in user can only be derived from that user's durable workspace role.
// AllScopes() remains available for the deployment's own API bearer tokens, but
// it is not a value a user session can be built from by accident.
type SessionScopes struct {
	role   domain.WorkspaceRole
	values []string
}

// Role is the durable workspace role these scopes were derived from.
func (s SessionScopes) Role() domain.WorkspaceRole { return s.role }

// Values is the scope list to persist on the session record.
func (s SessionScopes) Values() []string { return append([]string(nil), s.values...) }

// Has reports whether the derived set contains a scope.
func (s SessionScopes) Has(scope Scope) bool {
	for _, value := range s.values {
		if Scope(value) == scope {
			return true
		}
	}
	return false
}

// ScopesForWorkspaceRole derives the scopes a browser session may carry from a
// durable workspace role. A member never receives a control-plane scope, and an
// unrecognized or empty role fails closed rather than defaulting to anything.
func ScopesForWorkspaceRole(role domain.WorkspaceRole) (SessionScopes, error) {
	control := false
	switch role {
	case domain.WorkspaceRoleMember:
	case domain.WorkspaceRoleAdmin, domain.WorkspaceRoleOwner:
		control = true
	default:
		return SessionScopes{}, fmt.Errorf("workspace role %q has no session authority", string(role))
	}
	all := AllScopes()
	values := make([]string, 0, len(all))
	for _, value := range all {
		if !control && IsControlPlaneScope(Scope(value)) {
			continue
		}
		values = append(values, value)
	}
	return SessionScopes{role: role, values: values}, nil
}

// WorkspaceRoleHoldsControlPlane reports whether a durable workspace role is
// permitted to reach the workspace administration surface at all. Callers use
// it to reject a session whose stored scopes outlive the role that justified
// them, so a scope list alone is never the whole authorization decision.
func WorkspaceRoleHoldsControlPlane(role domain.WorkspaceRole) bool {
	return role == domain.WorkspaceRoleAdmin || role == domain.WorkspaceRoleOwner
}
