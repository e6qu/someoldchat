package slack

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The admin.usergroups.* organization methods: create, fetch, update,
// addUsers, removeUsers, uploadUsers and removeTeams. Each is a user-token
// method, and each answers a caller who may not act on the group with
// restricted_action, the code every one of their references declares for it.

// adminUserGroupFields refuses a bot token and decodes the request of a caller
// the handler has authenticated. A JSON body may carry `users` as an array,
// which each method's reference allows alongside a comma-separated string;
// every other argument decodes as it does everywhere.
func adminUserGroupFields(w http.ResponseWriter, r *http.Request, principal auth.Principal, err error) (map[string]string, bool) {
	if err != nil {
		writeAuthError(w, err)
		return nil, false
	}
	if isBotPrincipal(principal) {
		writeError(w, "not_allowed_token_type")
		return nil, false
	}
	fields, err := decodeArguments(w, r, func(name string, value json.RawMessage) (string, error) {
		if name == "users" {
			return normalizeJSONListField(value)
		}
		return normalizeJSONField(name, value)
	})
	if err != nil {
		writeDecodeError(w, err)
		return nil, false
	}
	return fields, true
}

// writeAdminUserGroupError names a failure of one of these methods.
func writeAdminUserGroupError(w http.ResponseWriter, err error, notFoundReason string) {
	switch {
	case errors.Is(err, domain.ErrNotWorkspaceAdmin):
		writeError(w, "restricted_action")
	case errors.Is(err, domain.ErrUnparseableUserGroupFile):
		writeError(w, "unable_to_parse_csv")
	case errors.Is(err, domain.ErrNoValidUserGroupUsers):
		writeError(w, "no_valid_users")
	case errors.Is(err, domain.ErrUserGroupNeedsHandle):
		writeError(w, "visible_group_needs_handle")
	default:
		writeError(w, mapServiceErrorNamed(err, notFoundReason, "invalid_arguments", ""))
	}
}

// adminUserGroupUsers reads a users argument: present, non-empty, and within
// the documented maximum.
func adminUserGroupUsers(fields map[string]string) ([]domain.UserID, bool) {
	users := parseIDList[domain.UserID](fields["users"])
	return users, len(users) > 0 && len(users) <= domain.MaxAdminUserGroupUsers
}

// optionalField is an argument the caller named, or nil.
func optionalField(fields map[string]string, name string) *string {
	value, present := fields[name]
	if !present {
		return nil
	}
	return &value
}

// orgSubteamResponse is the subteam object the organization methods return.
// It is the usergroups.* object with the organization properties, without the
// prefs those methods' examples do not carry. An organization here is one
// workspace, so the group's team_id and enterprise_id both name it. Every
// change to a group's properties or membership requires an administrator,
// which is what is_editing_restricted and is_membership_locked report.
func orgSubteamResponse(value domain.UserGroup, includeTeams bool) map[string]any {
	result := userGroupResponse(value, false)
	delete(result, "prefs")
	result["enterprise_id"] = value.WorkspaceID
	result["is_section"] = false
	result["is_editing_restricted"] = true
	result["is_membership_locked"] = true
	result["is_idp_group"] = false
	result["is_visible"] = !value.Hidden
	result["is_org_level"] = value.OrgLevel
	if includeTeams {
		teams := make([]string, 0, len(value.Teams))
		for _, team := range value.Teams {
			teams = append(teams, string(team))
		}
		result["teams"] = teams
	}
	return result
}

func memberRefusalsResponse(values []domain.UserGroupMemberRefusal) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{"user_id": value.UserID, "reason": value.Reason})
	}
	return result
}

func (h Handler) adminUserGroupCreate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		writeError(w, "invalid_arguments")
		return
	}
	visible := true
	if raw, present := fields["is_visible"]; present {
		value, err := parseBoolField(raw)
		if err != nil {
			writeError(w, "invalid_arguments")
			return
		}
		visible = value
	}
	value, err := h.Messages.AdminCreateUserGroup(r.Context(), principal.WorkspaceID, principal.UserID, name, fields["handle"], fields["purpose"], visible)
	if err != nil {
		writeAdminUserGroupError(w, err, "invalid_usergroup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subteam": orgSubteamResponse(value, false)})
}

func (h Handler) adminUserGroupFetch(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsRead)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	id := domain.UserGroupID(strings.TrimSpace(fields["id"]))
	if id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	value, err := h.Messages.AdminFetchUserGroup(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		writeAdminUserGroupError(w, err, "invalid_usergroup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subteam": orgSubteamResponse(value, true)})
}

func (h Handler) adminUserGroupUpdate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	id := domain.UserGroupID(strings.TrimSpace(fields["id"]))
	if id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	patch := domain.UserGroupPatch{
		Name: optionalField(fields, "name"), Handle: optionalField(fields, "handle"), Description: optionalField(fields, "description"),
	}
	if raw, present := fields["is_visible"]; present {
		visible, err := parseBoolField(raw)
		if err != nil {
			writeError(w, "invalid_arguments")
			return
		}
		patch.Visible = &visible
	}
	value, err := h.Messages.AdminUpdateUserGroup(r.Context(), principal.WorkspaceID, principal.UserID, id, patch)
	if err != nil {
		writeAdminUserGroupError(w, err, "invalid_usergroup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "subteam": orgSubteamResponse(value, true)})
}

func (h Handler) adminUserGroupAddUsers(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	id := domain.UserGroupID(strings.TrimSpace(fields["id"]))
	users, valid := adminUserGroupUsers(fields)
	if id == "" || !valid {
		writeError(w, "invalid_arguments")
		return
	}
	result, err := h.Messages.AdminAddUserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, id, users)
	if err != nil {
		writeAdminUserGroupError(w, err, "invalid_usergroup")
		return
	}
	response := map[string]any{"ok": true}
	if len(result.Invalid) > 0 {
		response["invalid_users"] = memberRefusalsResponse(result.Invalid)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) adminUserGroupRemoveUsers(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	id := domain.UserGroupID(strings.TrimSpace(fields["id"]))
	users, valid := adminUserGroupUsers(fields)
	if id == "" || !valid {
		writeError(w, "invalid_arguments")
		return
	}
	if err := h.Messages.AdminRemoveUserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, id, users); err != nil {
		writeAdminUserGroupError(w, err, "invalid_usergroup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUserGroupUploadUsers(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	id := domain.UserGroupID(strings.TrimSpace(fields["id"]))
	if id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	file, err := uploadedUserGroupFile(r, fields)
	if err != nil {
		writeError(w, "unable_to_parse_csv")
		return
	}
	result, err := h.Messages.AdminUploadUserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, id, file)
	if err != nil {
		writeAdminUserGroupError(w, err, "invalid_usergroup")
		return
	}
	response := map[string]any{"ok": true, "successful_user_count": result.Succeeded}
	if len(result.Invalid) > 0 {
		response["invalid_users"] = memberRefusalsResponse(result.Invalid)
	}
	writeJSON(w, http.StatusOK, response)
}

// uploadedUserGroupFile is the CSV text of uploadUsers' file argument: a form
// or JSON string, or the file part of a multipart request, which the decoder
// has already read within the request size limit.
func uploadedUserGroupFile(r *http.Request, fields map[string]string) (string, error) {
	if value, present := fields["file"]; present {
		return value, nil
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["file"]) == 0 {
		return "", nil
	}
	part, err := r.MultipartForm.File["file"][0].Open()
	if err != nil {
		return "", err
	}
	defer part.Close()
	body, err := io.ReadAll(part)
	return string(body), err
}

func (h Handler) adminUserGroupRemoveTeams(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	fields, ok := adminUserGroupFields(w, r, principal, err)
	if !ok {
		return
	}
	id := domain.UserGroupID(strings.TrimSpace(fields["usergroup_id"]))
	if id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	teams := parseIDList[domain.WorkspaceID](fields["team_ids"])
	if len(teams) == 0 {
		writeError(w, "no_team_ids_given")
		return
	}
	if len(teams) > domain.MaxAdminUserGroupTeams {
		writeError(w, "invalid_arguments")
		return
	}
	// Each workspace must belong to the token's organization, which in this
	// topology is the token's own workspace.
	invalid := make([]string, 0)
	for _, team := range teams {
		if team != principal.WorkspaceID {
			invalid = append(invalid, string(team))
		}
	}
	if len(invalid) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_team_ids", "errors": invalid})
		return
	}
	if err := h.Messages.AdminRemoveUserGroupTeams(r.Context(), principal.WorkspaceID, principal.UserID, id, teams); err != nil {
		writeAdminUserGroupError(w, err, "usergroup_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
