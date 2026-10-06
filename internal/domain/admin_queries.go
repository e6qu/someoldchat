package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// MemberActivity is the admin.users.list is_active filter. Slack's default is
// active members only; the empty value, which lists everyone, is what the
// administrative web pages that review deactivated accounts ask for.
type MemberActivity string

const (
	MemberActivityAny         MemberActivity = ""
	MemberActivityActive      MemberActivity = "active"
	MemberActivityDeactivated MemberActivity = "deactivated"
)

// Valid reports whether the filter is one of the three it can be.
func (activity MemberActivity) Valid() bool {
	return activity == MemberActivityAny || activity == MemberActivityActive || activity == MemberActivityDeactivated
}

// Includes reports whether a membership belongs on a page under the filter.
func (activity MemberActivity) Includes(membership WorkspaceMembership) bool {
	switch activity {
	case MemberActivityActive:
		return membership.Active
	case MemberActivityDeactivated:
		return !membership.Active
	default:
		return true
	}
}

// GuestTier is the membership admin.users.assign adds someone with: a full
// member, or one of Slack's two guest tiers. One value rather than the two
// request booleans, because "both guest tiers at once" is not a membership.
//
// The zero value keeps whatever tier the membership already has. Reactivating
// a deactivated guest (the admin pages' Enable, or an assign that names
// neither flag) must not quietly make them a full member, so changing the
// tier is always something a caller says.
type GuestTier int

const (
	GuestTierUnchanged GuestTier = iota
	// GuestTierNone is a full member: both flags sent and false.
	GuestTierNone
	// GuestTierMultiChannel is is_restricted.
	GuestTierMultiChannel
	// GuestTierSingleChannel is is_ultra_restricted.
	GuestTierSingleChannel
)

// Valid reports whether the tier is one of the four.
func (tier GuestTier) Valid() bool {
	return tier >= GuestTierUnchanged && tier <= GuestTierSingleChannel
}

// Guest reports whether the tier makes the member a guest.
func (tier GuestTier) Guest() bool {
	return tier == GuestTierMultiChannel || tier == GuestTierSingleChannel
}

// Apply is membership with the tier's restricted and ultra_restricted flags.
func (tier GuestTier) Apply(membership WorkspaceMembership) WorkspaceMembership {
	if tier != GuestTierUnchanged {
		membership.Restricted, membership.UltraRestricted = tier == GuestTierMultiChannel, tier == GuestTierSingleChannel
	}
	return membership
}

// SessionClients names which of a member's sessions a reset ends. Slack lets
// an administrator end only the mobile or only the web ones.
type SessionClients string

const (
	SessionClientsAll    SessionClients = ""
	SessionClientsWeb    SessionClients = "web"
	SessionClientsMobile SessionClients = "mobile"
)

// Valid reports whether the value is one of the three.
func (clients SessionClients) Valid() bool {
	return clients == SessionClientsAll || clients == SessionClientsWeb || clients == SessionClientsMobile
}

// EndsWebSessions reports whether a reset of these clients ends the sessions
// this deployment issues. Every session here is a browser (web) session: there
// is no mobile client, so a mobile-only reset has nothing to end.
func (clients SessionClients) EndsWebSessions() bool {
	return clients != SessionClientsMobile
}

// RoleAssignmentQuery scopes admin.roles.listAssignments. An empty list is no
// restriction; a non-empty one keeps only the assignments it names.
type RoleAssignmentQuery struct {
	RoleIDs   []string
	EntityIDs []string
}

// Includes reports whether an assignment belongs on a page of the query.
func (query RoleAssignmentQuery) Includes(assignment RoleAssignment) bool {
	return containsOrEmpty(query.RoleIDs, assignment.RoleID) && containsOrEmpty(query.EntityIDs, assignment.EntityID)
}

func containsOrEmpty(values []string, value string) bool {
	if len(values) == 0 {
		return true
	}
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// RoleAssignmentPosition is where an assignment falls in the listing order:
// member, then entity, then role. All three are needed because one member can
// hold several roles on one entity.
type RoleAssignmentPosition struct {
	UserID   UserID `json:"u"`
	EntityID string `json:"e"`
	RoleID   string `json:"r"`
}

// Position is the assignment's place in the listing order.
func (assignment RoleAssignment) Position() RoleAssignmentPosition {
	return RoleAssignmentPosition{UserID: assignment.UserID, EntityID: assignment.EntityID, RoleID: assignment.RoleID}
}

// Before reports whether position sorts ahead of other in ascending order.
func (position RoleAssignmentPosition) Before(other RoleAssignmentPosition) bool {
	if position.UserID != other.UserID {
		return position.UserID < other.UserID
	}
	if position.EntityID != other.EntityID {
		return position.EntityID < other.EntityID
	}
	return position.RoleID < other.RoleID
}

// NewRoleAssignmentCursor names the last assignment of a page. Like every
// cursor here it carries no direction: the request says which side of the
// position the next page lies on.
func NewRoleAssignmentCursor(assignment RoleAssignment) (Cursor, error) {
	position := assignment.Position()
	if position.UserID == "" || position.EntityID == "" || position.RoleID == "" ||
		!utf8.ValidString(string(position.UserID)) || !utf8.ValidString(position.EntityID) || !utf8.ValidString(position.RoleID) {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(position)
	if err != nil {
		return "", err
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(body)), nil
}

// DecodeRoleAssignmentCursor reads a NewRoleAssignmentCursor cursor. ok is
// false for the empty cursor, which starts at the beginning.
func DecodeRoleAssignmentCursor(cursor Cursor) (RoleAssignmentPosition, bool, error) {
	if cursor == "" {
		return RoleAssignmentPosition{}, false, nil
	}
	body, err := base64.RawURLEncoding.DecodeString(string(cursor))
	if err != nil {
		return RoleAssignmentPosition{}, false, ErrInvalidCursor
	}
	var position RoleAssignmentPosition
	if err := json.Unmarshal(body, &position); err != nil || position.UserID == "" || position.EntityID == "" || position.RoleID == "" {
		return RoleAssignmentPosition{}, false, ErrInvalidCursor
	}
	return position, true, nil
}

// WorkflowSource is admin.workflows.search's source: a workflow an app ships
// in code, or one a member built in Workflow Builder.
type WorkflowSource string

const (
	WorkflowSourceAny             WorkflowSource = ""
	WorkflowSourceCode            WorkflowSource = "code"
	WorkflowSourceWorkflowBuilder WorkflowSource = "workflow_builder"
)

// WorkflowSearch is an administrator's workflow search. Every set field
// narrows it; the zero value is every workflow.
type WorkflowSearch struct {
	// Query is matched, folded, against the title.
	Query string
	AppID AppID
	// CollaboratorIDs keeps a workflow any of them manages.
	CollaboratorIDs []UserID
	// NoCollaborators keeps only a workflow nobody manages.
	NoCollaborators bool
	Source          WorkflowSource
}

// Valid reports whether the search can match anything coherent: a source
// Slack names, and not both "managed by these" and "managed by nobody".
func (search WorkflowSearch) Valid() bool {
	if search.Source != WorkflowSourceAny && search.Source != WorkflowSourceCode && search.Source != WorkflowSourceWorkflowBuilder {
		return false
	}
	return !(search.NoCollaborators && len(search.CollaboratorIDs) > 0)
}

// Matches reports whether a workflow belongs in the search. A workflow's
// source is read from its app: one a developer app defines is code, and one a
// member built in Workflow Builder carries no app.
func (search WorkflowSearch) Matches(workflow WorkflowDefinition) bool {
	if needle := strings.ToLower(strings.TrimSpace(search.Query)); needle != "" && !strings.Contains(strings.ToLower(workflow.Title), needle) {
		return false
	}
	if search.AppID != "" && workflow.AppID != search.AppID {
		return false
	}
	switch search.Source {
	case WorkflowSourceCode:
		if workflow.AppID == "" {
			return false
		}
	case WorkflowSourceWorkflowBuilder:
		if workflow.AppID != "" {
			return false
		}
	}
	if search.NoCollaborators && len(workflow.ManagerIDs) != 0 {
		return false
	}
	if len(search.CollaboratorIDs) > 0 {
		for _, wanted := range search.CollaboratorIDs {
			for _, manager := range workflow.ManagerIDs {
				if manager == wanted {
					return true
				}
			}
		}
		return false
	}
	return true
}
