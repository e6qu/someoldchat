package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func writeAuthAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h Handler) authAdminPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	if h.Login == nil || principal.WorkspaceID != h.Login.workspace {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	canReadApps := principal.HasScope(auth.ScopeAdminAppsRead) || principal.HasScope(auth.ScopeAdminAppsWrite)
	canWriteApps := principal.HasScope(auth.ScopeAdminAppsWrite)
	canReadUsers := principal.HasScope(auth.ScopeAdminUsersRead) || principal.HasScope(auth.ScopeAdminUsersWrite)
	canWriteUsers := principal.HasScope(auth.ScopeAdminUsersWrite)
	if !canReadApps && !canReadUsers {
		h.writeAuthError(w, auth.ErrMissingScope)
		return
	}
	h.setCSRFCookie(w, r)
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
		return
	}
	csrfToken := auth.CSRFToken(sessionCookie.Value)
	var output strings.Builder
	output.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorization methods · SameOldChat</title><style>body{font:15px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f8f8fa;color:#1d1c1d;margin:0}.wrap{max-width:760px;margin:40px auto;background:#fff;padding:28px;border:1px solid #ddd;border-radius:10px}h1{margin-top:0}.row{display:flex;align-items:center;justify-content:space-between;border-top:1px solid #ddd;padding:16px 0}.toggle{background:#007a5a;color:#fff;border:0;border-radius:5px;padding:8px 12px}.toggle.off{background:#777}</style></head><body><main class="wrap"><h1>Authorization methods</h1><p>Provider secrets are deployment configuration. Enablement is durable workspace state.</p>`)
	if canReadApps {
		names := make([]string, 0, len(h.Login.providers))
		for name := range h.Login.providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			method, methodErr := h.Login.service.GetAuthMethod(r.Context(), h.Login.workspace, name)
			if methodErr != nil {
				http.Error(w, "authorization settings unavailable", http.StatusServiceUnavailable)
				return
			}
			state, class := "enabled", "toggle"
			if !method.Enabled {
				state, class = "disabled", "toggle off"
			}
			button := "Disable"
			if !method.Enabled {
				button = "Enable"
			}
			if canWriteApps {
				output.WriteString(`<div class="row"><span><strong>` + providerLabel(name) + `</strong><br><small>` + state + `</small></span><form method="post" action="/api/admin.auth.methods.set"><input type="hidden" name="_csrf" value="` + csrfToken + `"><input type="hidden" name="provider" value="` + name + `"><input type="hidden" name="enabled" value="` + fmt.Sprint(!method.Enabled) + `"><button class="` + class + `" type="submit">` + button + `</button></form></div>`)
			} else {
				output.WriteString(`<div class="row"><span><strong>` + providerLabel(name) + `</strong><br><small>` + state + `</small></span></div>`)
			}
		}
	}
	if canReadUsers {
		page, pageErr := h.Login.service.AdminListUsers(r.Context(), h.Login.workspace, principal.UserID, domain.PageRequest{Limit: 50})
		if pageErr != nil {
			http.Error(w, "users unavailable", http.StatusServiceUnavailable)
			return
		}
		output.WriteString(`<hr><h2>Workspace users</h2><p>Manage active membership and workspace roles. Deactivating a user revokes their sessions and access tokens.</p><table><thead><tr><th scope="col">User</th><th scope="col">Status</th><th scope="col">Role</th><th scope="col">Actions</th></tr></thead><tbody>`)
		for _, item := range page.Users {
			status := "deactivated"
			if item.Membership.Active && !item.User.Deleted {
				status = "active"
			}
			name := item.User.RealName
			if strings.TrimSpace(name) == "" {
				name = item.User.Name
			}
			output.WriteString(`<tr><td><strong>` + html.EscapeString(name) + `</strong><br><small>` + html.EscapeString(item.User.Email) + `</small></td><td>` + status + `</td><td>` + html.EscapeString(string(item.Membership.Role)) + `</td><td>`)
			if canWriteUsers {
				action := "disable"
				label := "Disable"
				if status != "active" {
					action, label = "enable", "Enable"
				}
				output.WriteString(`<form class="inline-form" method="post" action="/api/admin.auth.users.set"><input type="hidden" name="_csrf" value="` + html.EscapeString(csrfToken) + `"><input type="hidden" name="user_id" value="` + html.EscapeString(string(item.User.ID)) + `"><input type="hidden" name="action" value="` + action + `"><button class="toggle" type="submit">` + label + `</button></form> <form class="inline-form" method="post" action="/api/admin.auth.users.set"><input type="hidden" name="_csrf" value="` + html.EscapeString(csrfToken) + `"><input type="hidden" name="user_id" value="` + html.EscapeString(string(item.User.ID)) + `"><input type="hidden" name="action" value="role"><select name="role" aria-label="Role for ` + html.EscapeString(name) + `"><option value="member"` + selectedRole(item.Membership.Role, domain.WorkspaceRoleMember) + `>Member</option><option value="admin"` + selectedRole(item.Membership.Role, domain.WorkspaceRoleAdmin) + `>Administrator</option></select><button class="toggle" type="submit">Save role</button></form>`)
			} else {
				output.WriteString(`<span>Read only</span>`)
			}
			output.WriteString(`</td></tr>`)
		}
		output.WriteString(`</tbody></table>`)
	}
	if canWriteUsers {
		output.WriteString(`<hr><h2>Manual user setup</h2><p>Create an active workspace member directly. External authorization still requires a matching verified email.</p><form method="post" action="/api/admin.auth.users.create"><input type="hidden" name="_csrf" value="` + html.EscapeString(csrfToken) + `"><label>Email <input name="email" type="email" maxlength="320" autocomplete="email" required></label> <label>Name <input name="real_name" maxlength="200" autocomplete="name" required></label> <label>Role <select name="role"><option value="member">Member</option><option value="admin">Administrator</option></select></label> <button class="toggle" type="submit">Create user</button></form>`)
	}
	output.WriteString(`</main></body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, output.String())
}

func selectedRole(actual, expected domain.WorkspaceRole) string {
	if actual == expected {
		return ` selected`
	}
	return ""
}

func decodeAdminPageRequest(r *http.Request) (domain.PageRequest, error) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return domain.PageRequest{}, errors.New("limit must be between 1 and 100")
		}
		limit = value
	}
	return domain.PageRequest{Limit: limit, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))}, nil
}

func (h Handler) authUsersList(w http.ResponseWriter, r *http.Request) {
	if !h.authAdminAllowed(w, r, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite) {
		return
	}
	request, err := decodeAdminPageRequest(r)
	if err != nil {
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_pagination"})
		return
	}
	principal, _ := h.Authenticator.Authenticate(r)
	page, err := h.Login.service.AdminListUsers(r.Context(), h.Login.workspace, principal.UserID, request)
	if err != nil {
		writeAuthAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "users_unavailable"})
		return
	}
	users := make([]map[string]any, 0, len(page.Users))
	for _, item := range page.Users {
		users = append(users, map[string]any{"user": item.User, "role": item.Membership.Role, "active": item.Membership.Active && !item.User.Deleted})
	}
	writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "users": users, "response_metadata": map[string]any{"next_cursor": page.NextCursor}})
}

func (h Handler) authUserSet(w http.ResponseWriter, r *http.Request) {
	if !h.authAdminAllowed(w, r, auth.ScopeAdminUsersWrite) {
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	principal, _ := h.Authenticator.Authenticate(r)
	target := domain.UserID(strings.TrimSpace(fields["user_id"]))
	if target == "" {
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_user"})
		return
	}
	var operationErr error
	switch strings.ToLower(strings.TrimSpace(fields["action"])) {
	case "disable":
		operationErr = h.Login.service.RemoveUser(r.Context(), h.Login.workspace, principal.UserID, target)
	case "enable":
		operationErr = h.Login.service.AdminAssignUser(r.Context(), h.Login.workspace, principal.UserID, target, []domain.ConversationID{})
	case "role":
		role := domain.WorkspaceRole(strings.ToLower(strings.TrimSpace(fields["role"])))
		if role != domain.WorkspaceRoleMember && role != domain.WorkspaceRoleAdmin {
			writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_role"})
			return
		}
		operationErr = h.Login.service.SetUserRole(r.Context(), h.Login.workspace, principal.UserID, target, role)
	default:
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_action"})
		return
	}
	if operationErr != nil {
		status, code := authAdminUserMutationError(operationErr)
		writeAuthAdminJSON(w, status, map[string]any{"ok": false, "error": code})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/app/admin/auth", http.StatusSeeOther)
}

func authAdminUserMutationError(err error) (int, string) {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound, "user_not_found"
	}
	if errors.Is(err, service.ErrInvalidInviteRequest) || errors.Is(err, service.ErrInvalidWorkspace) {
		return http.StatusBadRequest, "invalid_user"
	}
	return http.StatusServiceUnavailable, "user_update_unavailable"
}

func (h Handler) authMethodsList(w http.ResponseWriter, r *http.Request) {
	if !h.authAdminAllowed(w, r, auth.ScopeAdminAppsRead, auth.ScopeAdminAppsWrite) {
		return
	}
	names := make([]string, 0, len(h.Login.providers))
	for name := range h.Login.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	methods := make([]domain.AuthMethod, 0, len(names))
	for _, name := range names {
		method, err := h.Login.service.GetAuthMethod(r.Context(), h.Login.workspace, name)
		if err != nil {
			writeAuthAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auth_methods_unavailable"})
			return
		}
		methods = append(methods, method)
	}
	writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "methods": methods})
}

func (h Handler) authMethodSet(w http.ResponseWriter, r *http.Request) {
	if !h.authAdminAllowed(w, r, auth.ScopeAdminAppsWrite) {
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(fields["provider"]))
	if _, ok := h.Login.providers[provider]; !ok {
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_provider"})
		return
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(fields["enabled"]))
	if err != nil {
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_enabled"})
		return
	}
	if err := h.Login.service.SetAuthMethod(r.Context(), domain.AuthMethod{WorkspaceID: h.Login.workspace, Provider: provider, Enabled: enabled}); err != nil {
		writeAuthAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auth_method_unavailable"})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/app/admin/auth", http.StatusSeeOther)
}

func (h Handler) authAdminAllowed(w http.ResponseWriter, r *http.Request, scopes ...auth.Scope) bool {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		writeAuthAdminJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
		return false
	}
	if h.Login == nil || principal.WorkspaceID != h.Login.workspace {
		writeAuthAdminJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not_authorized"})
		return false
	}
	for _, scope := range scopes {
		if principal.HasScope(scope) {
			return true
		}
	}
	writeAuthAdminJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not_authorized"})
	return false
}

func normalizeAdminInviteChannels(raw string) []domain.ConversationID {
	values := strings.Split(raw, ",")
	channels := make([]domain.ConversationID, 0, len(values))
	seen := make(map[domain.ConversationID]struct{}, len(values))
	for _, value := range values {
		channel := domain.ConversationID(strings.TrimSpace(value))
		if channel == "" {
			continue
		}
		if _, exists := seen[channel]; exists {
			continue
		}
		seen[channel] = struct{}{}
		channels = append(channels, channel)
	}
	return channels
}

func authAdminInvitationError(err error) (int, string) {
	if errors.Is(err, service.ErrInvalidInviteRequest) {
		return http.StatusBadRequest, "invalid_invitation"
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		return http.StatusConflict, "invitation_already_exists"
	}
	return http.StatusServiceUnavailable, "user_invitation_unavailable"
}

func (h Handler) authUserInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		writeAuthAdminJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
		return
	}
	if h.Login == nil || principal.WorkspaceID != h.Login.workspace || !principal.HasScope(auth.ScopeAdminUsersWrite) {
		writeAuthAdminJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not_authorized"})
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	if strings.TrimSpace(fields["email"]) == "" || strings.TrimSpace(fields["real_name"]) == "" {
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_user"})
		return
	}
	channels := normalizeAdminInviteChannels(fields["channel_ids"])
	if err := h.Messages.AdminInviteUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], channels, fields["custom_message"], fields["real_name"], false, false, false, time.Time{}); err != nil {
		status, code := authAdminInvitationError(err)
		writeAuthAdminJSON(w, status, map[string]any{"ok": false, "error": code})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/app/admin/auth", http.StatusSeeOther)
}

func (h Handler) authUserCreate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		writeAuthAdminJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authenticated"})
		return
	}
	if h.Login == nil || principal.WorkspaceID != h.Login.workspace || !principal.HasScope(auth.ScopeAdminUsersWrite) {
		writeAuthAdminJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not_authorized"})
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		return
	}
	role := domain.WorkspaceRole(strings.ToLower(strings.TrimSpace(fields["role"])))
	user, err := h.Login.service.AdminCreateUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], fields["real_name"], role)
	if err != nil {
		status := http.StatusServiceUnavailable
		code := "user_creation_unavailable"
		if errors.Is(err, store.ErrAlreadyExists) {
			status, code = http.StatusConflict, "user_already_exists"
		} else if errors.Is(err, service.ErrInvalidInviteRequest) {
			status, code = http.StatusBadRequest, "invalid_user"
		}
		writeAuthAdminJSON(w, status, map[string]any{"ok": false, "error": code})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeAuthAdminJSON(w, http.StatusCreated, map[string]any{"ok": true, "user": user})
		return
	}
	http.Redirect(w, r, "/app/admin/auth", http.StatusSeeOther)
}
