package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func writeAuthAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h Handler) authAdminPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil || h.Login == nil || principal.WorkspaceID != h.Login.workspace || !principal.HasScope(auth.ScopeAdminAppsWrite) {
		h.writeAuthError(w, auth.ErrNotAuthenticated)
		return
	}
	h.setCSRFCookie(w, r)
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		http.Error(w, "session unavailable", http.StatusUnauthorized)
		return
	}
	csrfToken := auth.CSRFToken(sessionCookie.Value)
	names := make([]string, 0, len(h.Login.providers))
	for name := range h.Login.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	var output strings.Builder
	output.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorization methods · SameOldChat</title><style>body{font:15px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f8f8fa;color:#1d1c1d;margin:0}.wrap{max-width:760px;margin:40px auto;background:#fff;padding:28px;border:1px solid #ddd;border-radius:10px}h1{margin-top:0}.row{display:flex;align-items:center;justify-content:space-between;border-top:1px solid #ddd;padding:16px 0}.toggle{background:#007a5a;color:#fff;border:0;border-radius:5px;padding:8px 12px}.toggle.off{background:#777}</style></head><body><main class="wrap"><h1>Authorization methods</h1><p>Provider secrets are deployment configuration. Enablement is durable workspace state.</p>`)
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
		output.WriteString(`<div class="row"><span><strong>` + providerLabel(name) + `</strong><br><small>` + state + `</small></span><form method="post" action="/api/admin.auth.methods.set"><input type="hidden" name="_csrf" value="` + csrfToken + `"><input type="hidden" name="provider" value="` + name + `"><input type="hidden" name="enabled" value="` + fmt.Sprint(!method.Enabled) + `"><button class="` + class + `" type="submit">` + button + `</button></form></div>`)
	}
	output.WriteString(`<hr><h2>Manual user setup</h2><p>Invite a user into the workspace; approval and membership remain durable admin workflows.</p><form method="post" action="/api/admin.auth.users.invite"><input type="hidden" name="_csrf" value="` + csrfToken + `"><label>Email <input name="email" type="email" required></label> <label>Name <input name="real_name" required></label> <button class="toggle" type="submit">Create invitation</button></form></main></body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, output.String())
}

func (h Handler) authMethodsList(w http.ResponseWriter, r *http.Request) {
	if !h.authAdminAllowed(w, r) {
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
	if !h.authAdminAllowed(w, r) {
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

func (h Handler) authAdminAllowed(w http.ResponseWriter, r *http.Request) bool {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil || h.Login == nil || principal.WorkspaceID != h.Login.workspace || !principal.HasScope(auth.ScopeAdminAppsWrite) {
		writeAuthAdminJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not_authorized"})
		return false
	}
	return true
}

func (h Handler) authUserInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil || h.Login == nil || principal.WorkspaceID != h.Login.workspace || !principal.HasScope(auth.ScopeAdminUsersWrite) {
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
	if err := h.Messages.AdminInviteUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], nil, fields["custom_message"], fields["real_name"], false, false, false, time.Time{}); err != nil {
		writeAuthAdminJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "user_invitation_unavailable"})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/app/admin/auth", http.StatusSeeOther)
}
