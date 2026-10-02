package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// AppPermissionType is who in the organization may use an app, the value
// admin.apps.permissions.set writes and admin.apps.permissions.list reports.
type AppPermissionType string

const (
	AppPermissionEveryone      AppPermissionType = "everyone"
	AppPermissionNamedEntities AppPermissionType = "named_entities"
	AppPermissionNoOne         AppPermissionType = "no_one"
)

func (kind AppPermissionType) Valid() bool {
	switch kind {
	case AppPermissionEveryone, AppPermissionNamedEntities, AppPermissionNoOne:
		return true
	}
	return false
}

// ChannelRestrictionMode is where an app may be used. The zero value is an app
// whose channel restriction nobody has configured, which Slack leaves out of
// its responses; all_channels is a configured restriction that restricts
// nothing, and is reported.
type ChannelRestrictionMode string

const (
	ChannelRestrictionUnset             ChannelRestrictionMode = ""
	ChannelRestrictionAllChannels       ChannelRestrictionMode = "all_channels"
	ChannelRestrictionSpecificChannels  ChannelRestrictionMode = "specific_channels"
	ChannelRestrictionAllChannelsExcept ChannelRestrictionMode = "all_channels_except"
)

// Valid reports whether a caller may select the mode. The unset value is not
// selectable: it is only what an app nobody has restricted holds.
func (mode ChannelRestrictionMode) Valid() bool {
	switch mode {
	case ChannelRestrictionAllChannels, ChannelRestrictionSpecificChannels, ChannelRestrictionAllChannelsExcept:
		return true
	}
	return false
}

// Lists reports whether the mode carries a channel list: an allowlist under
// specific_channels and an exclusion list under all_channels_except.
func (mode ChannelRestrictionMode) Lists() bool {
	return mode == ChannelRestrictionSpecificChannels || mode == ChannelRestrictionAllChannelsExcept
}

// The documented maxima of the admin.apps.permissions.* arguments.
const (
	MaxAppPermissionSetUsers    = 200
	MaxAppPermissionChangeUsers = 50
	MaxAppRestrictedChannels    = 200
	MaxMCPServerNamedEntities   = 200
)

// AppPermission is one app's organization-level access control list: who may
// use the app and in which channels.
type AppPermission struct {
	AppID                  AppID
	WorkspaceID            WorkspaceID
	PermissionType         AppPermissionType
	UserIDs                []UserID
	UserGroupIDs           []UserGroupID
	ChannelRestrictionMode ChannelRestrictionMode
	ChannelIDs             []ConversationID
	UpdatedAt              time.Time
}

// DefaultAppPermission is what an app nobody has restricted answers: everyone
// may use it, anywhere.
func DefaultAppPermission(workspace WorkspaceID, app AppID) AppPermission {
	return AppPermission{
		AppID: app, WorkspaceID: workspace, PermissionType: AppPermissionEveryone,
		UserIDs: []UserID{}, UserGroupIDs: []UserGroupID{}, ChannelIDs: []ConversationID{},
	}
}

// Check is the invariant every stored access control list holds, whichever
// operation produced it. A list naming entities is a named_entities list and a
// named_entities list names somebody; a channel list belongs to a mode that
// has one; and an app nobody may use has no channel restriction, because it
// can be used in no channel at all.
func (permission AppPermission) Check() error {
	if permission.AppID == "" || permission.WorkspaceID == "" {
		return ErrInvalidAppPermission
	}
	if !permission.PermissionType.Valid() {
		return ErrAppPermissionType
	}
	named := len(permission.UserIDs)+len(permission.UserGroupIDs) != 0
	if named && permission.PermissionType != AppPermissionNamedEntities {
		return ErrAppPermissionType
	}
	if !named && permission.PermissionType == AppPermissionNamedEntities {
		return ErrNamedEntitiesEmpty
	}
	if permission.ChannelRestrictionMode != ChannelRestrictionUnset && !permission.ChannelRestrictionMode.Valid() {
		return ErrChannelRestrictionMode
	}
	if permission.PermissionType == AppPermissionNoOne && (permission.ChannelRestrictionMode != ChannelRestrictionUnset || len(permission.ChannelIDs) != 0) {
		return ErrChannelRestrictionRequiresAppAccess
	}
	if len(permission.ChannelIDs) != 0 && !permission.ChannelRestrictionMode.Lists() {
		return ErrChannelRestrictionMode
	}
	if len(permission.UserIDs) > MaxAppPermissionSetUsers {
		return ErrTooManyNamedEntities
	}
	if len(permission.ChannelIDs) > MaxAppRestrictedChannels {
		return ErrInvalidAppPermission
	}
	return nil
}

// AppPermissionChange is the set of entities admin.apps.permissions.add grants
// or admin.apps.permissions.remove revokes.
type AppPermissionChange struct {
	AppID        AppID
	UserIDs      []UserID
	UserGroupIDs []UserGroupID
	ChannelIDs   []ConversationID
}

// Empty reports a change that names nothing to add or remove.
func (change AppPermissionChange) Empty() bool {
	return len(change.UserIDs)+len(change.UserGroupIDs)+len(change.ChannelIDs) == 0
}

// MCPServerID identifies one MCP server an app declares.
type MCPServerID string

// NewMCPServerID derives a server's identifier from its app and the name the
// app's manifest declares it under. The identifier is derived rather than
// minted so that it is the same on every replica and survives every manifest
// revision that keeps the server, which the permissions stored against it
// depend on; renaming the server is declaring a different one.
func NewMCPServerID(app AppID, name string) MCPServerID {
	sum := sha256.Sum256([]byte(string(app) + "\n" + name))
	return MCPServerID("Amcp" + strings.ToUpper(hex.EncodeToString(sum[:10])))
}

// MCPServer is one Model Context Protocol server an app's manifest declares.
type MCPServer struct {
	ID    MCPServerID
	AppID AppID
	Name  string
	URL   string
}

// MCPServerPage is one page of the organization's MCP server allowlist.
type MCPServerPage struct {
	Servers    []MCPServer
	NextCursor Cursor
	HasMore    bool
}

// MCPServerPermissionType is who may use one MCP server.
type MCPServerPermissionType string

const (
	MCPServerPermissionEveryone             MCPServerPermissionType = "everyone"
	MCPServerPermissionNoOne                MCPServerPermissionType = "no_one"
	MCPServerPermissionNamedEntities        MCPServerPermissionType = "named_entities"
	MCPServerPermissionNamedEntitiesExclude MCPServerPermissionType = "named_entities_exclude"
)

func (kind MCPServerPermissionType) Valid() bool {
	switch kind {
	case MCPServerPermissionEveryone, MCPServerPermissionNoOne, MCPServerPermissionNamedEntities, MCPServerPermissionNamedEntitiesExclude:
		return true
	}
	return false
}

// Named reports whether the type applies to a list of entities.
func (kind MCPServerPermissionType) Named() bool {
	return kind == MCPServerPermissionNamedEntities || kind == MCPServerPermissionNamedEntitiesExclude
}

// BroaderThan reports whether a server rule would admit somebody the app-level
// access control list keeps out. A server can only narrow the app it belongs
// to: under a named_entities app, everyone and named_entities_exclude both
// reach members the app does not, and under a no_one app only no_one does not.
func (kind MCPServerPermissionType) BroaderThan(app AppPermissionType) bool {
	switch app {
	case AppPermissionNoOne:
		return kind != MCPServerPermissionNoOne
	case AppPermissionNamedEntities:
		return kind == MCPServerPermissionEveryone || kind == MCPServerPermissionNamedEntitiesExclude
	}
	return false
}

// MCPServerPermission is who may use one MCP server of one app.
type MCPServerPermission struct {
	WorkspaceID    WorkspaceID
	AppID          AppID
	ServerID       MCPServerID
	PermissionType MCPServerPermissionType
	UserIDs        []UserID
	UserGroupIDs   []UserGroupID
	UpdatedAt      time.Time
}

// DefaultMCPServerPermission is what a server nobody has restricted answers.
func DefaultMCPServerPermission(workspace WorkspaceID, app AppID, server MCPServerID) MCPServerPermission {
	return MCPServerPermission{
		WorkspaceID: workspace, AppID: app, ServerID: server, PermissionType: MCPServerPermissionEveryone,
		UserIDs: []UserID{}, UserGroupIDs: []UserGroupID{},
	}
}

// MCPServerAccess is one of an app's MCP servers with the permission it holds.
type MCPServerAccess struct {
	Server     MCPServer
	Permission MCPServerPermission
}

// ManagedAppPermission is who may interact with a managed app, the value
// apps.managed.permissions.set takes.
type ManagedAppPermission string

const (
	ManagedAppPermissionEveryone ManagedAppPermission = "everyone"
	ManagedAppPermissionAppOwner ManagedAppPermission = "app_owner"
)

func (permission ManagedAppPermission) Valid() bool {
	return permission == ManagedAppPermissionEveryone || permission == ManagedAppPermissionAppOwner
}
