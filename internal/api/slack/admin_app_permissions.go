package slack

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The organization-level app access controls: admin.apps.permissions.*,
// admin.apps.mcp.servers.* and apps.managed.permissions.set.
//
// The admin.* methods here take a user token only; authenticate refuses a bot
// with not_allowed_token_type, as it does for every admin scope.
// apps.managed.permissions.set takes an app configuration token, the
// credential the apps.manifest.* methods authenticate with.
func (h Handler) registerAdminAppPermissions(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin.apps.permissions.add", h.adminAppsPermissionsAdd)
	mux.HandleFunc("POST /api/admin.apps.permissions.add", h.adminAppsPermissionsAdd)
	mux.HandleFunc("GET /api/admin.apps.permissions.list", h.adminAppsPermissionsList)
	mux.HandleFunc("POST /api/admin.apps.permissions.list", h.adminAppsPermissionsList)
	mux.HandleFunc("GET /api/admin.apps.permissions.remove", h.adminAppsPermissionsRemove)
	mux.HandleFunc("POST /api/admin.apps.permissions.remove", h.adminAppsPermissionsRemove)
	mux.HandleFunc("GET /api/admin.apps.permissions.set", h.adminAppsPermissionsSet)
	mux.HandleFunc("POST /api/admin.apps.permissions.set", h.adminAppsPermissionsSet)
	mux.HandleFunc("GET /api/admin.apps.mcp.servers.list", h.adminAppsMCPServersList)
	mux.HandleFunc("POST /api/admin.apps.mcp.servers.list", h.adminAppsMCPServersList)
	mux.HandleFunc("GET /api/admin.apps.mcp.servers.permissions.list", h.adminAppsMCPServersPermissionsList)
	mux.HandleFunc("POST /api/admin.apps.mcp.servers.permissions.list", h.adminAppsMCPServersPermissionsList)
	mux.HandleFunc("GET /api/admin.apps.mcp.servers.permissions.set", h.adminAppsMCPServersPermissionsSet)
	mux.HandleFunc("POST /api/admin.apps.mcp.servers.permissions.set", h.adminAppsMCPServersPermissionsSet)
	mux.HandleFunc("GET /api/apps.managed.permissions.set", h.appsManagedPermissionsSet)
	mux.HandleFunc("POST /api/apps.managed.permissions.set", h.appsManagedPermissionsSet)
}

// adminAppAccessFields finishes what each admin.apps access-control handler
// starts by authenticating for its own scope: it decodes the arguments,
// answering the request itself when it cannot proceed.
func adminAppAccessFields(w http.ResponseWriter, r *http.Request, principal auth.Principal, err error) (map[string]string, bool) {
	if err != nil {
		writeAuthError(w, err)
		return nil, false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return nil, false
	}
	return fields, true
}

func (h Handler) adminAppsPermissionsList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsRead)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	if appID == "" {
		writeError(w, "invalid_arguments")
		return
	}
	value, err := h.Messages.AdminAppPermission(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		writeAppAccessError(w, err, "usergroup_not_found")
		return
	}
	writeJSON(w, http.StatusOK, appPermissionResponse(value))
}

// admin.apps.permissions.set replaces the list whole. channel_restriction_mode
// is optional: leaving it out keeps the app's channel restriction, which is
// what lets add and remove change the channel list without resending it.
func (h Handler) adminAppsPermissionsSet(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	permissionType := strings.TrimSpace(fields["permission_type"])
	if appID == "" || permissionType == "" {
		writeError(w, "invalid_arguments")
		return
	}
	value, err := h.Messages.AdminSetAppPermission(r.Context(), principal.WorkspaceID, principal.UserID, domain.AppPermission{
		AppID:                  appID,
		PermissionType:         domain.AppPermissionType(permissionType),
		UserIDs:                parseIDList[domain.UserID](fields["user_ids"]),
		UserGroupIDs:           parseIDList[domain.UserGroupID](fields["usergroup_ids"]),
		ChannelRestrictionMode: domain.ChannelRestrictionMode(strings.TrimSpace(fields["channel_restriction_mode"])),
		ChannelIDs:             parseIDList[domain.ConversationID](fields["channel_ids"]),
	})
	if err != nil {
		writeAppAccessError(w, err, "usergroup_not_found")
		return
	}
	writeJSON(w, http.StatusOK, appPermissionResponse(value))
}

// admin.apps.permissions.add names an unknown user group invalid_entities: its
// error table declares that code and no user-group code.
func (h Handler) adminAppsPermissionsAdd(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	change, ok := appPermissionChangeArguments(w, fields)
	if !ok {
		return
	}
	value, err := h.Messages.AdminAddAppPermissionEntities(r.Context(), principal.WorkspaceID, principal.UserID, change)
	if err != nil {
		writeAppAccessError(w, err, "invalid_entities")
		return
	}
	writeJSON(w, http.StatusOK, appPermissionResponse(value))
}

func (h Handler) adminAppsPermissionsRemove(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	change, ok := appPermissionChangeArguments(w, fields)
	if !ok {
		return
	}
	value, err := h.Messages.AdminRemoveAppPermissionEntities(r.Context(), principal.WorkspaceID, principal.UserID, change)
	if err != nil {
		writeAppAccessError(w, err, "invalid_arguments")
		return
	}
	writeJSON(w, http.StatusOK, appPermissionResponse(value))
}

// appPermissionChangeArguments reads what add and remove change. Both require
// the app and at least one of the three lists.
func appPermissionChangeArguments(w http.ResponseWriter, fields map[string]string) (domain.AppPermissionChange, bool) {
	change := domain.AppPermissionChange{
		AppID:        domain.AppID(strings.TrimSpace(fields["app_id"])),
		UserIDs:      parseIDList[domain.UserID](fields["user_ids"]),
		UserGroupIDs: parseIDList[domain.UserGroupID](fields["usergroup_ids"]),
		ChannelIDs:   parseIDList[domain.ConversationID](fields["channel_ids"]),
	}
	if change.AppID == "" || change.Empty() {
		writeError(w, "invalid_arguments")
		return domain.AppPermissionChange{}, false
	}
	return change, true
}

// appPermissionResponse is the shape admin.apps.permissions.remove and .set
// document: the permission type, the named entities when the type names them,
// and the channel restriction when one is configured. An app nobody may use
// reports its channel restriction as no_one, because it can be used in no
// channel.
func appPermissionResponse(value domain.AppPermission) map[string]any {
	response := map[string]any{"ok": true, "permission_type": string(value.PermissionType)}
	if value.PermissionType == domain.AppPermissionNamedEntities {
		response["user_ids"] = idList(value.UserIDs)
		response["usergroup_ids"] = idList(value.UserGroupIDs)
	}
	switch {
	case value.PermissionType == domain.AppPermissionNoOne:
		response["channel_restriction_mode"] = string(domain.AppPermissionNoOne)
	case value.ChannelRestrictionMode != domain.ChannelRestrictionUnset:
		response["channel_restriction_mode"] = string(value.ChannelRestrictionMode)
		if value.ChannelRestrictionMode.Lists() {
			response["channel_ids"] = idList(value.ChannelIDs)
		}
	}
	return response
}

// admin.apps.mcp.servers.list pages the organization's MCP server allowlist.
// The documented limit is 1 to 1000 with a default of 100.
func (h Handler) adminAppsMCPServersList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsRead)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 1000)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	cursor := domain.Cursor(strings.TrimSpace(fields["cursor"]))
	if _, err := domain.DecodePairCursor(cursor); err != nil {
		writeError(w, "invalid_cursor")
		return
	}
	page, err := h.Messages.AdminMCPServers(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeAppAccessError(w, err, "invalid_arguments")
		return
	}
	servers := make([]map[string]any, 0, len(page.Servers))
	for _, server := range page.Servers {
		servers = append(servers, mcpServerResponse(server))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "mcp_servers": servers, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)},
	})
}

func (h Handler) adminAppsMCPServersPermissionsList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsRead)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	if appID == "" {
		writeError(w, "invalid_arguments")
		return
	}
	access, err := h.Messages.AdminAppMCPServerPermissions(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		writeAppAccessError(w, err, "invalid_arguments")
		return
	}
	servers := make([]map[string]any, 0, len(access))
	for _, value := range access {
		server := mcpServerResponse(value.Server)
		server["permission_type"] = string(value.Permission.PermissionType)
		if value.Permission.PermissionType.Named() {
			server["user_ids"] = idList(value.Permission.UserIDs)
			server["usergroup_ids"] = idList(value.Permission.UserGroupIDs)
		}
		servers = append(servers, server)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app_id": string(appID), "mcp_servers": servers})
}

func (h Handler) adminAppsMCPServersPermissionsSet(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	fields, ok := adminAppAccessFields(w, r, principal, err)
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	serverID := domain.MCPServerID(strings.TrimSpace(fields["server_id"]))
	permissionType := strings.TrimSpace(fields["permission_type"])
	if appID == "" || serverID == "" || permissionType == "" {
		writeError(w, "invalid_arguments")
		return
	}
	if _, err := h.Messages.AdminSetMCPServerPermission(r.Context(), principal.WorkspaceID, principal.UserID, domain.MCPServerPermission{
		AppID: appID, ServerID: serverID, PermissionType: domain.MCPServerPermissionType(permissionType),
		UserIDs:      parseIDList[domain.UserID](fields["user_ids"]),
		UserGroupIDs: parseIDList[domain.UserGroupID](fields["usergroup_ids"]),
	}); err != nil {
		writeAppAccessError(w, err, "usergroup_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func mcpServerResponse(server domain.MCPServer) map[string]any {
	return map[string]any{"id": string(server.ID), "app_id": string(server.AppID), "name": server.Name, "url": server.URL}
}

// apps.managed.permissions.set authenticates with an app configuration token,
// as apps.manifest.* does, and the service validates the request in full
// before answering. No app here is managed by a manager app; see
// service.SetManagedAppPermissions.
func (h Handler) appsManagedPermissionsSet(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token, reason := appConfigurationToken(r, fields)
	if reason != "" {
		writeError(w, reason)
		return
	}
	err = h.Messages.SetManagedAppPermissions(r.Context(), token, domain.AppID(strings.TrimSpace(fields["app_id"])), domain.ManagedAppPermission(strings.TrimSpace(fields["permissions"])))
	switch {
	case errors.Is(err, domain.ErrAppConfigurationAuthentication):
		writeError(w, "invalid_auth")
	case err != nil:
		writeAppAccessError(w, err, "invalid_arguments")
	default:
		// Unreachable while no app is managed; kept so a future manager-app
		// model answers the documented success rather than nothing.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// writeAppAccessError names the failures of this family. Each sentinel is the
// cause one documented code names. The user-group code differs by method:
// .set declares usergroup_not_found, .add only invalid_entities, so the caller
// says which its own table declares.
func writeAppAccessError(w http.ResponseWriter, err error, userGroupCode string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, "app_not_found")
	case errors.Is(err, domain.ErrInvalidAppPermission):
		writeError(w, "invalid_arguments")
	case errors.Is(err, domain.ErrInvalidCursor):
		writeError(w, "invalid_cursor")
	case errors.Is(err, domain.ErrAppAccessListNotFound):
		writeError(w, "app_acl_not_found")
	case errors.Is(err, domain.ErrAppPermissionType):
		writeError(w, "invalid_permission_type")
	case errors.Is(err, domain.ErrChannelRestrictionMode):
		writeError(w, "invalid_channel_restriction_mode")
	case errors.Is(err, domain.ErrChannelRestrictionIDsRequired):
		writeError(w, "channel_ids_required")
	case errors.Is(err, domain.ErrChannelRestrictionRequiresAppAccess):
		writeError(w, "channel_restriction_requires_app_access")
	case errors.Is(err, domain.ErrNamedEntitiesEmpty):
		writeError(w, "named_entities_cannot_be_empty")
	case errors.Is(err, domain.ErrNoValidNamedEntities):
		writeError(w, "no_valid_named_entities")
	case errors.Is(err, domain.ErrTooManyNamedEntities):
		writeError(w, "too_many_named_entities")
	case errors.Is(err, domain.ErrNamedUserGroupNotFound):
		writeError(w, userGroupCode)
	case errors.Is(err, domain.ErrRestrictedChannelNotFound):
		writeError(w, "channel_not_found")
	case errors.Is(err, domain.ErrServerNotFound):
		writeError(w, "server_not_found")
	case errors.Is(err, domain.ErrServerPermissionBroaderThanApp):
		writeError(w, "server_acl_type_broader_than_app")
	case errors.Is(err, domain.ErrServerPermissionOutOfAppScope):
		writeError(w, "server_acl_entities_not_in_scope")
	case errors.Is(err, domain.ErrAppNotManaged):
		writeError(w, "app_not_managed")
	default:
		// The role denial, user_not_found and the engine failures keep the
		// names every admin.* method gives them.
		writeError(w, mapServiceError(err, "app_not_found"))
	}
}

func idList[T ~string](values []T) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
