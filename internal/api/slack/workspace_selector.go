package slack

import (
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// namesForeignTeam reports whether a request's team_id names a workspace other
// than the token's own.
//
// team_id selects the workspace an organization-wide token acts in. A token
// here belongs to exactly one workspace, so the only workspace it can select
// is that one; any other is refused, never answered as if it were the token's
// own workspace. Ignoring the argument did exactly that: a caller asking about
// another workspace was told about its own, with nothing to say so.
//
// The refusal code is the caller's, because each method's enum declares its
// own name for a rejected argument.
func namesForeignTeam(fields map[string]string, principal auth.Principal) bool {
	team := strings.TrimSpace(fields["team_id"])
	return team != "" && domain.WorkspaceID(team) != principal.WorkspaceID
}
