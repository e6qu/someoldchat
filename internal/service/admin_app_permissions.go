package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// This file holds the organization-level app access controls Slack documents
// under admin.apps.permissions.*, admin.apps.mcp.servers.* and
// apps.managed.permissions.set.
//
// An app's access control list says who may use the app (everyone, named
// users and user groups, or no one) and, separately, in which channels it may
// be used. Each MCP server an app declares can narrow that further but never
// widen it: a server rule broader than its app's, or naming somebody the app's
// named list leaves out, is refused. The rule is checked when the server rule
// is written, which is the only place both lists are in hand.

// mcpAllowlistScanPage is how many approved apps one read of the allowlist
// takes from the store while it fills a page of servers.
const mcpAllowlistScanPage = 100

// AdminAppPermission reports one app's access control list. An app nobody has
// restricted answers the default rather than an error: admin.apps.permissions.list
// documents no code for an app without a list.
func (m Messages) AdminAppPermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID) (domain.AppPermission, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AppPermission{}, err
	}
	if appID == "" {
		return domain.AppPermission{}, domain.ErrInvalidAppPermission
	}
	if _, err := m.permissionedApp(ctx, appID); err != nil {
		return domain.AppPermission{}, err
	}
	return m.effectiveAppPermission(ctx, workspaceID, appID)
}

// AdminSetAppPermission replaces one app's access control list.
//
// The argument checks run in the order a caller can correct them: the shape of
// the request, then the app, then whether each named entity exists. An omitted
// channel restriction keeps the one the app already has, because the method
// documents channel_restriction_mode as optional and add and remove as the way
// to change the list without resending it; naming no_one clears it, because an
// app nobody may use can be used in no channel.
func (m Messages) AdminSetAppPermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, value domain.AppPermission) (domain.AppPermission, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AppPermission{}, err
	}
	value = normalizedAppPermission(value)
	value.WorkspaceID = workspaceID
	if value.AppID == "" {
		return domain.AppPermission{}, domain.ErrInvalidAppPermission
	}
	if !value.PermissionType.Valid() {
		return domain.AppPermission{}, domain.ErrAppPermissionType
	}
	if value.ChannelRestrictionMode != domain.ChannelRestrictionUnset && !value.ChannelRestrictionMode.Valid() {
		return domain.AppPermission{}, domain.ErrChannelRestrictionMode
	}
	if value.PermissionType == domain.AppPermissionNoOne && (value.ChannelRestrictionMode != domain.ChannelRestrictionUnset || len(value.ChannelIDs) != 0) {
		return domain.AppPermission{}, domain.ErrChannelRestrictionRequiresAppAccess
	}
	named := len(value.UserIDs)+len(value.UserGroupIDs) != 0
	if named && value.PermissionType != domain.AppPermissionNamedEntities {
		return domain.AppPermission{}, domain.ErrAppPermissionType
	}
	if !named && value.PermissionType == domain.AppPermissionNamedEntities {
		return domain.AppPermission{}, domain.ErrNamedEntitiesEmpty
	}
	if len(value.UserIDs) > domain.MaxAppPermissionSetUsers {
		return domain.AppPermission{}, domain.ErrTooManyNamedEntities
	}
	if value.ChannelRestrictionMode.Lists() && len(value.ChannelIDs) == 0 {
		return domain.AppPermission{}, domain.ErrChannelRestrictionIDsRequired
	}
	// A channel list given to all_channels, or with no mode to belong to, has
	// nothing it could mean, so it is refused rather than dropped.
	if len(value.ChannelIDs) != 0 && !value.ChannelRestrictionMode.Lists() {
		return domain.AppPermission{}, domain.ErrChannelRestrictionMode
	}
	if len(value.ChannelIDs) > domain.MaxAppRestrictedChannels {
		return domain.AppPermission{}, domain.ErrInvalidAppPermission
	}
	if _, err := m.permissionedApp(ctx, value.AppID); err != nil {
		return domain.AppPermission{}, err
	}
	if err := m.resolveNamedEntities(ctx, workspaceID, value.UserIDs, value.UserGroupIDs); err != nil {
		return domain.AppPermission{}, err
	}
	if err := m.resolveRestrictedChannels(ctx, workspaceID, value.ChannelIDs); err != nil {
		return domain.AppPermission{}, err
	}
	if value.ChannelRestrictionMode == domain.ChannelRestrictionUnset && value.PermissionType != domain.AppPermissionNoOne {
		current, err := m.Store.GetAppPermission(ctx, workspaceID, value.AppID)
		switch {
		case err == nil:
			value.ChannelRestrictionMode, value.ChannelIDs = current.ChannelRestrictionMode, current.ChannelIDs
		case !errors.Is(err, store.ErrNotFound):
			return domain.AppPermission{}, err
		}
	}
	return m.writeAppPermission(ctx, actorID, value, "app.permissions_set")
}

// AdminAddAppPermissionEntities grants the named users and user groups access
// to an app and adds channels to its channel restriction list.
func (m Messages) AdminAddAppPermissionEntities(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, change domain.AppPermissionChange) (domain.AppPermission, error) {
	current, err := m.appPermissionForChange(ctx, workspaceID, actorID, change)
	if err != nil {
		return domain.AppPermission{}, err
	}
	if err := m.resolveNamedEntities(ctx, workspaceID, change.UserIDs, change.UserGroupIDs); err != nil {
		return domain.AppPermission{}, err
	}
	if err := m.resolveRestrictedChannels(ctx, workspaceID, change.ChannelIDs); err != nil {
		return domain.AppPermission{}, err
	}
	current.UserIDs = unionIDs(current.UserIDs, change.UserIDs)
	current.UserGroupIDs = unionIDs(current.UserGroupIDs, change.UserGroupIDs)
	current.ChannelIDs = unionIDs(current.ChannelIDs, change.ChannelIDs)
	return m.writeAppPermission(ctx, actorID, current, "app.permissions_added")
}

// AdminRemoveAppPermissionEntities revokes the named users' and user groups'
// access to an app and removes channels from its channel restriction list.
// Removing the last named entity is refused: a named_entities list that names
// nobody would read as a restriction while admitting no one, and the way to
// admit no one is to say so.
func (m Messages) AdminRemoveAppPermissionEntities(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, change domain.AppPermissionChange) (domain.AppPermission, error) {
	current, err := m.appPermissionForChange(ctx, workspaceID, actorID, change)
	if err != nil {
		return domain.AppPermission{}, err
	}
	// An entity already on the list can always be taken off it, even once the
	// member is deactivated or the channel deleted; only an identifier that is
	// neither on the list nor here is a mistake.
	for _, userID := range change.UserIDs {
		if !slices.Contains(current.UserIDs, userID) && !m.workspaceUserExists(ctx, workspaceID, userID) {
			return domain.AppPermission{}, domain.ErrUserNotFound
		}
	}
	for _, channelID := range change.ChannelIDs {
		if slices.Contains(current.ChannelIDs, channelID) {
			continue
		}
		if err := m.resolveRestrictedChannels(ctx, workspaceID, []domain.ConversationID{channelID}); err != nil {
			return domain.AppPermission{}, err
		}
	}
	current.UserIDs = withoutIDs(current.UserIDs, change.UserIDs)
	current.UserGroupIDs = withoutIDs(current.UserGroupIDs, change.UserGroupIDs)
	current.ChannelIDs = withoutIDs(current.ChannelIDs, change.ChannelIDs)
	if current.PermissionType == domain.AppPermissionNamedEntities && len(current.UserIDs)+len(current.UserGroupIDs) == 0 {
		return domain.AppPermission{}, domain.ErrNamedEntitiesEmpty
	}
	return m.writeAppPermission(ctx, actorID, current, "app.permissions_removed")
}

// appPermissionForChange holds the checks add and remove share: the request
// names something, within the documented maxima, of an app that has a list,
// and each part of the change suits the list it would change.
func (m Messages) appPermissionForChange(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, change domain.AppPermissionChange) (domain.AppPermission, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.AppPermission{}, err
	}
	if change.AppID == "" || change.Empty() {
		return domain.AppPermission{}, domain.ErrInvalidAppPermission
	}
	if len(change.UserIDs) > domain.MaxAppPermissionChangeUsers {
		return domain.AppPermission{}, domain.ErrTooManyNamedEntities
	}
	if len(change.ChannelIDs) > domain.MaxAppRestrictedChannels {
		return domain.AppPermission{}, domain.ErrInvalidAppPermission
	}
	if _, err := m.permissionedApp(ctx, change.AppID); err != nil {
		return domain.AppPermission{}, err
	}
	current, err := m.Store.GetAppPermission(ctx, workspaceID, change.AppID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.AppPermission{}, domain.ErrAppAccessListNotFound
	}
	if err != nil {
		return domain.AppPermission{}, err
	}
	if len(change.UserIDs)+len(change.UserGroupIDs) != 0 && current.PermissionType != domain.AppPermissionNamedEntities {
		return domain.AppPermission{}, domain.ErrAppPermissionType
	}
	if len(change.ChannelIDs) != 0 && !current.ChannelRestrictionMode.Lists() {
		return domain.AppPermission{}, domain.ErrChannelRestrictionMode
	}
	return current, nil
}

func (m Messages) writeAppPermission(ctx context.Context, actorID domain.UserID, value domain.AppPermission, topic string) (domain.AppPermission, error) {
	value = normalizedAppPermission(value)
	if err := value.Check(); err != nil {
		return domain.AppPermission{}, err
	}
	value.UpdatedAt = time.Now().UTC()
	event, err := newEvent(value.WorkspaceID, actorID, events.NewPayload(topic,
		events.String("app_id", string(value.AppID)),
		events.String("permission_type", string(value.PermissionType)),
		events.String("channel_restriction_mode", string(value.ChannelRestrictionMode))), value.UpdatedAt)
	if err != nil {
		return domain.AppPermission{}, err
	}
	if err := m.Store.SetAppPermission(ctx, value, event); err != nil {
		return domain.AppPermission{}, err
	}
	return value, nil
}

func (m Messages) effectiveAppPermission(ctx context.Context, workspaceID domain.WorkspaceID, appID domain.AppID) (domain.AppPermission, error) {
	stored, err := m.Store.GetAppPermission(ctx, workspaceID, appID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.DefaultAppPermission(workspaceID, appID), nil
	}
	if err != nil {
		return domain.AppPermission{}, err
	}
	return normalizedAppPermission(stored), nil
}

// AdminMCPServers lists the MCP servers of the apps this organization has
// approved. Slack derives the list from the organization's MCP server
// allowlist, and the allowlist here is the app approval decision: an approved
// app's declared servers are on it, and they stay on it when the app is
// uninstalled, because approval outlives installation. A deleted app's
// servers are not listed.
//
// The cursor names the last server returned by its app and server
// identifiers, so a page resumes inside an app's server list as well as
// between apps.
func (m Messages) AdminMCPServers(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, request domain.PageRequest) (domain.MCPServerPage, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.MCPServerPage{}, err
	}
	if request.Limit <= 0 || request.Descending {
		return domain.MCPServerPage{}, domain.ErrInvalidAppPermission
	}
	position, err := domain.DecodePairCursor(request.Cursor)
	if err != nil {
		return domain.MCPServerPage{}, err
	}
	servers := make([]domain.MCPServer, 0, request.Limit+1)
	approvals := domain.PageRequest{Limit: mcpAllowlistScanPage}
	if position.First != "" {
		// The app the cursor stopped inside: what it declares after the last
		// server returned, if it is still approved.
		approval, err := m.Store.GetAppApproval(ctx, workspaceID, domain.AppID(position.First))
		switch {
		case err == nil && approval.Status == domain.AppApprovalApproved:
			declared, err := m.declaredMCPServers(ctx, approval.ID)
			if err != nil {
				return domain.MCPServerPage{}, err
			}
			for _, server := range declared {
				if string(server.ID) > position.Second {
					servers = append(servers, server)
				}
			}
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return domain.MCPServerPage{}, err
		}
		approvals.Cursor, err = domain.NewListCursor(position.First)
		if err != nil {
			return domain.MCPServerPage{}, err
		}
	}
	for len(servers) <= request.Limit {
		page, err := m.Store.ListAppApprovals(ctx, workspaceID, domain.AppApprovalApproved, approvals)
		if err != nil {
			return domain.MCPServerPage{}, err
		}
		for _, approval := range page.Apps {
			declared, err := m.declaredMCPServers(ctx, approval.ID)
			if err != nil {
				return domain.MCPServerPage{}, err
			}
			servers = append(servers, declared...)
		}
		if !page.HasMore {
			break
		}
		approvals.Cursor = page.NextCursor
	}
	result := domain.MCPServerPage{HasMore: len(servers) > request.Limit}
	if result.HasMore {
		servers = servers[:request.Limit]
		last := servers[len(servers)-1]
		result.NextCursor, err = domain.NewPairCursor(string(last.AppID), string(last.ID))
		if err != nil {
			return domain.MCPServerPage{}, err
		}
	}
	result.Servers = servers
	return result, nil
}

// AdminAppMCPServerPermissions lists every MCP server one app declares with the
// permission it holds. A server nobody has restricted answers everyone.
func (m Messages) AdminAppMCPServerPermissions(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, appID domain.AppID) ([]domain.MCPServerAccess, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return nil, err
	}
	if appID == "" {
		return nil, domain.ErrInvalidAppPermission
	}
	app, err := m.permissionedApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	declared, err := m.declaredMCPServers(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	stored, err := m.Store.ListMCPServerPermissions(ctx, workspaceID, appID)
	if err != nil {
		return nil, err
	}
	held := make(map[domain.MCPServerID]domain.MCPServerPermission, len(stored))
	for _, value := range stored {
		held[value.ServerID] = value
	}
	access := make([]domain.MCPServerAccess, 0, len(declared))
	for _, server := range declared {
		permission, exists := held[server.ID]
		if !exists {
			permission = domain.DefaultMCPServerPermission(workspaceID, appID, server.ID)
		}
		access = append(access, domain.MCPServerAccess{Server: server, Permission: permission})
	}
	return access, nil
}

// AdminSetMCPServerPermission replaces who may use one MCP server. A type that
// names no entities stores none, the way the automation permissions do.
func (m Messages) AdminSetMCPServerPermission(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, value domain.MCPServerPermission) (domain.MCPServerPermission, error) {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, actorID); err != nil {
		return domain.MCPServerPermission{}, err
	}
	value.WorkspaceID = workspaceID
	value.UserIDs = uniqueIDs(value.UserIDs)
	value.UserGroupIDs = uniqueIDs(value.UserGroupIDs)
	if value.AppID == "" || value.ServerID == "" {
		return domain.MCPServerPermission{}, domain.ErrInvalidAppPermission
	}
	if !value.PermissionType.Valid() {
		return domain.MCPServerPermission{}, domain.ErrAppPermissionType
	}
	if !value.PermissionType.Named() {
		value.UserIDs, value.UserGroupIDs = []domain.UserID{}, []domain.UserGroupID{}
	}
	if value.PermissionType.Named() && len(value.UserIDs)+len(value.UserGroupIDs) == 0 {
		return domain.MCPServerPermission{}, domain.ErrNamedEntitiesEmpty
	}
	if len(value.UserIDs) > domain.MaxMCPServerNamedEntities || len(value.UserGroupIDs) > domain.MaxMCPServerNamedEntities {
		return domain.MCPServerPermission{}, domain.ErrTooManyNamedEntities
	}
	app, err := m.permissionedApp(ctx, value.AppID)
	if err != nil {
		return domain.MCPServerPermission{}, err
	}
	declared, err := m.declaredMCPServers(ctx, app.ID)
	if err != nil {
		return domain.MCPServerPermission{}, err
	}
	if !slices.ContainsFunc(declared, func(server domain.MCPServer) bool { return server.ID == value.ServerID }) {
		return domain.MCPServerPermission{}, domain.ErrServerNotFound
	}
	if err := m.resolveNamedEntities(ctx, workspaceID, value.UserIDs, value.UserGroupIDs); err != nil {
		return domain.MCPServerPermission{}, err
	}
	appPermission, err := m.effectiveAppPermission(ctx, workspaceID, value.AppID)
	if err != nil {
		return domain.MCPServerPermission{}, err
	}
	if value.PermissionType.BroaderThan(appPermission.PermissionType) {
		return domain.MCPServerPermission{}, domain.ErrServerPermissionBroaderThanApp
	}
	if appPermission.PermissionType == domain.AppPermissionNamedEntities && value.PermissionType == domain.MCPServerPermissionNamedEntities {
		if err := m.requireEntitiesInScope(ctx, workspaceID, appPermission, value); err != nil {
			return domain.MCPServerPermission{}, err
		}
	}
	value.UpdatedAt = time.Now().UTC()
	event, err := newEvent(workspaceID, actorID, events.NewPayload("app.mcp_server_permission_set",
		events.String("app_id", string(value.AppID)),
		events.String("server_id", string(value.ServerID)),
		events.String("permission_type", string(value.PermissionType))), value.UpdatedAt)
	if err != nil {
		return domain.MCPServerPermission{}, err
	}
	if err := m.Store.SetMCPServerPermission(ctx, value, event); err != nil {
		return domain.MCPServerPermission{}, err
	}
	return value, nil
}

// requireEntitiesInScope refuses a server rule naming somebody the app's named
// list does not admit. A user is admitted by being named or by belonging to a
// named user group; a user group only by being named itself, since admitting a
// group admits its future members too.
func (m Messages) requireEntitiesInScope(ctx context.Context, workspaceID domain.WorkspaceID, app domain.AppPermission, server domain.MCPServerPermission) error {
	admitted := make(map[domain.UserID]struct{}, len(app.UserIDs))
	for _, userID := range app.UserIDs {
		admitted[userID] = struct{}{}
	}
	for _, groupID := range app.UserGroupIDs {
		group, err := m.Store.GetUserGroup(ctx, workspaceID, groupID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		for _, userID := range group.Users {
			admitted[userID] = struct{}{}
		}
	}
	for _, userID := range server.UserIDs {
		if _, ok := admitted[userID]; !ok {
			return domain.ErrServerPermissionOutOfAppScope
		}
	}
	for _, groupID := range server.UserGroupIDs {
		if !slices.Contains(app.UserGroupIDs, groupID) {
			return domain.ErrServerPermissionOutOfAppScope
		}
	}
	return nil
}

// SetManagedAppPermissions is apps.managed.permissions.set. Slack lets the
// manager app that created a managed app on a partner platform set who may
// interact with it before it is installed. This deployment has no manager
// apps: every app is created by its developer through the manifest APIs, so no
// app is managed, and an app the configuration token may see is answered
// app_not_managed after the request has been validated in full.
func (m Messages) SetManagedAppPermissions(ctx context.Context, configurationToken string, appID domain.AppID, permission domain.ManagedAppPermission) error {
	principal, err := m.appConfigurationPrincipal(ctx, configurationToken)
	if err != nil {
		return err
	}
	if appID == "" || !permission.Valid() {
		return domain.ErrInvalidAppPermission
	}
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if app.Deleted || app.DevelopmentWorkspaceID != principal.WorkspaceID || app.OwnerID != principal.UserID {
		return store.ErrNotFound
	}
	return domain.ErrAppNotManaged
}

// requireAppUse is where an app's access control list decides something: a
// member using the app — a slash command, a shortcut, an interactive element
// of its message or view, its Home or Messages tab — must be admitted by its
// permission type, and, when the use happens in a channel, by its channel
// restriction. An app nobody has restricted admits everyone everywhere.
// conversationID is empty for a use that happens in no channel.
func (m Messages) requireAppUse(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, conversationID domain.ConversationID) error {
	return requireAppUse(ctx, m.Store, workspaceID, userID, appID, conversationID)
}

// appPermissionReader is what deciding an app's access control reads; event
// delivery decides it from its own narrower store.
type appPermissionReader interface {
	GetAppPermission(context.Context, domain.WorkspaceID, domain.AppID) (domain.AppPermission, error)
	GetUserGroup(context.Context, domain.WorkspaceID, domain.UserGroupID) (domain.UserGroup, error)
}

func requireAppUse(ctx context.Context, state appPermissionReader, workspaceID domain.WorkspaceID, userID domain.UserID, appID domain.AppID, conversationID domain.ConversationID) error {
	permission, err := state.GetAppPermission(ctx, workspaceID, appID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	admitted, err := appPermissionAdmits(ctx, state, workspaceID, permission, userID)
	if err != nil {
		return err
	}
	if !admitted {
		return domain.ErrAppUseRestricted
	}
	if conversationID == "" {
		return nil
	}
	listed := slices.Contains(permission.ChannelIDs, conversationID)
	switch permission.ChannelRestrictionMode {
	case domain.ChannelRestrictionSpecificChannels:
		if !listed {
			return domain.ErrAppUseRestricted
		}
	case domain.ChannelRestrictionAllChannelsExcept:
		if listed {
			return domain.ErrAppUseRestricted
		}
	}
	return nil
}

// appPermissionAdmits reports whether a permission type lets a member use the
// app at all. A named list admits its users and the members of its groups.
func appPermissionAdmits(ctx context.Context, state appPermissionReader, workspaceID domain.WorkspaceID, permission domain.AppPermission, userID domain.UserID) (bool, error) {
	switch permission.PermissionType {
	case domain.AppPermissionEveryone:
		return true, nil
	case domain.AppPermissionNamedEntities:
		if slices.Contains(permission.UserIDs, userID) {
			return true, nil
		}
		for _, groupID := range permission.UserGroupIDs {
			group, err := state.GetUserGroup(ctx, workspaceID, groupID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return false, err
			}
			if group.DeletedAt.IsZero() && slices.Contains(group.Users, userID) {
				return true, nil
			}
		}
	}
	return false, nil
}

// permissionedApp is the app an access control names: one that exists and has
// not been deleted. A deleted app keeps its row, so the flag is the answer.
func (m Messages) permissionedApp(ctx context.Context, appID domain.AppID) (domain.App, error) {
	app, _, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return domain.App{}, err
	}
	if app.Deleted {
		return domain.App{}, store.ErrNotFound
	}
	return app, nil
}

// declaredMCPServers reads the MCP servers an app's current manifest declares.
// An app that is gone or deleted declares none, and so does a stored manifest
// this version can no longer parse: neither is a server anybody can reach.
func (m Messages) declaredMCPServers(ctx context.Context, appID domain.AppID) ([]domain.MCPServer, error) {
	app, revision, err := m.Store.GetApp(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if app.Deleted {
		return nil, nil
	}
	parsed, problems := appmanifest.Parse(revision.Manifest)
	if len(problems) != 0 {
		return nil, nil
	}
	servers := make([]domain.MCPServer, 0, len(parsed.MCPServers))
	for _, declared := range parsed.MCPServers {
		servers = append(servers, domain.MCPServer{
			ID: domain.NewMCPServerID(appID, declared.Name), AppID: appID, Name: declared.Name, URL: declared.URL,
		})
	}
	slices.SortFunc(servers, func(left, right domain.MCPServer) int { return strings.Compare(string(left.ID), string(right.ID)) })
	return servers, nil
}

// resolveNamedEntities checks that every named user and user group exists in
// the workspace. A list none of whose entries exists is a different mistake
// from one stray identifier, and Slack names the two differently.
func (m Messages) resolveNamedEntities(ctx context.Context, workspaceID domain.WorkspaceID, users []domain.UserID, groups []domain.UserGroupID) error {
	var missingUser, missingGroup bool
	valid := 0
	for _, userID := range users {
		if m.workspaceUserExists(ctx, workspaceID, userID) {
			valid++
		} else {
			missingUser = true
		}
	}
	for _, groupID := range groups {
		group, err := m.Store.GetUserGroup(ctx, workspaceID, groupID)
		switch {
		case err == nil && group.DeletedAt.IsZero():
			valid++
		case err == nil || errors.Is(err, store.ErrNotFound):
			missingGroup = true
		default:
			return err
		}
	}
	switch {
	case (missingUser || missingGroup) && valid == 0:
		return domain.ErrNoValidNamedEntities
	case missingUser:
		return domain.ErrUserNotFound
	case missingGroup:
		return domain.ErrNamedUserGroupNotFound
	}
	return nil
}

func (m Messages) workspaceUserExists(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID) bool {
	user, err := m.Store.GetUser(ctx, userID)
	return err == nil && user.WorkspaceID == workspaceID && !user.Deleted
}

func (m Messages) resolveRestrictedChannels(ctx context.Context, workspaceID domain.WorkspaceID, channels []domain.ConversationID) error {
	for _, channelID := range channels {
		channel, err := m.Store.GetConversation(ctx, channelID)
		if errors.Is(err, store.ErrNotFound) || err == nil && channel.WorkspaceID != workspaceID {
			return domain.ErrRestrictedChannelNotFound
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func normalizedAppPermission(value domain.AppPermission) domain.AppPermission {
	value.UserIDs = uniqueIDs(value.UserIDs)
	value.UserGroupIDs = uniqueIDs(value.UserGroupIDs)
	value.ChannelIDs = uniqueIDs(value.ChannelIDs)
	return value
}

// uniqueIDs keeps the first occurrence of each identifier, in order, and never
// answers nil.
func uniqueIDs[T ~string](values []T) []T {
	result := make([]T, 0, len(values))
	for _, value := range values {
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func unionIDs[T ~string](current, added []T) []T {
	return uniqueIDs(append(slices.Clone(current), added...))
}

func withoutIDs[T ~string](current, removed []T) []T {
	result := make([]T, 0, len(current))
	for _, value := range current {
		if !slices.Contains(removed, value) {
			result = append(result, value)
		}
	}
	return result
}
