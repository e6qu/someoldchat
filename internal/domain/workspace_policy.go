package domain

// WorkspacePolicy is the set of workspace-wide permissions an owner or
// administrator chooses from the administration settings. Each field is one
// setting Slack's help centre publishes, and each is consulted where the
// behavior it governs happens; none of them is shown without being applied.
//
// The zero value is not a usable policy, because an audience must be named.
// DefaultWorkspacePolicy is Slack's default and is what a workspace nobody has
// configured reads back.
type WorkspacePolicy struct {
	// BroadcastWarningOff turns off the confirmation the composer asks for
	// before @channel, @here or @everyone in a channel of at least six
	// members ("Manage who can notify a channel or workspace"). Slack shows
	// the confirmation by default, so false keeps it.
	BroadcastWarningOff bool
	// PrivateChannelCreators is who may create a private channel. Converting
	// a group DM makes a private channel, so the same audience governs that
	// conversion ("Manage settings and permissions for Slack Connect direct
	// messages").
	PrivateChannelCreators PolicyAudience
}

// DefaultWorkspacePolicy is Slack's default: the broadcast confirmation is
// shown, and every member may create a private channel.
func DefaultWorkspacePolicy() WorkspacePolicy {
	return WorkspacePolicy{PrivateChannelCreators: PolicyAudienceEveryone}
}

// Valid reports whether every field names a value the product applies.
func (policy WorkspacePolicy) Valid() bool {
	return policy.PrivateChannelCreators.Valid()
}

// PolicyAudience names which workspace roles a permission admits. Guests are
// not an audience here: they are governed by their own account type, which
// every operation already applies before it asks the workspace policy.
type PolicyAudience string

const (
	// PolicyAudienceEveryone admits every member, whatever their role.
	PolicyAudienceEveryone PolicyAudience = "everyone"
	// PolicyAudienceAdmins admits workspace administrators and owners.
	PolicyAudienceAdmins PolicyAudience = "admins"
	// PolicyAudienceOwners admits workspace owners only.
	PolicyAudienceOwners PolicyAudience = "owners"
)

func (audience PolicyAudience) Valid() bool {
	switch audience {
	case PolicyAudienceEveryone, PolicyAudienceAdmins, PolicyAudienceOwners:
		return true
	default:
		return false
	}
}

// Admits reports whether a member holding role belongs to the audience. It
// compares authority by rank, so an owner is always admitted where an
// administrator is, and an unrecognised role or audience admits nobody.
func (audience PolicyAudience) Admits(role WorkspaceRole) bool {
	switch audience {
	case PolicyAudienceEveryone:
		return role.Rank() >= WorkspaceRoleMember.Rank()
	case PolicyAudienceAdmins:
		return role.Rank() >= WorkspaceRoleAdmin.Rank()
	case PolicyAudienceOwners:
		return role.Rank() >= WorkspaceRoleOwner.Rank()
	default:
		return false
	}
}
