package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// authAdminSecurityHeaders hardens every authenticated administration response.
//
// The page renders a valid CSRF token into pre-filled mutation forms, so framing
// it is enough to turn an administrator's click into a role change or a provider
// shutdown: the token comes from the framed page itself, and SameSite=Lax does
// not stop a top-level-initiated POST from the victim's own origin. frame-ancestors
// plus X-Frame-Options removes the framing; form-action keeps the pre-filled
// forms pointed at this origin; default-src 'none' means the page cannot pull in
// script at all.
func authAdminSecurityHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
}

func writeAuthAdminJSON(w http.ResponseWriter, status int, value any) {
	authAdminSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const authAdminStyle = `body{font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f8f8fa;color:#1d1c1d;margin:0}.wrap{max-width:760px;margin:40px auto;background:#fff;padding:28px;border:1px solid #ddd;border-radius:10px}h1{margin-top:0}.row{display:flex;align-items:center;justify-content:space-between;border-top:1px solid #ddd;padding:16px 0}.toggle{background:#007a5a;color:#fff;border:0;border-radius:5px;padding:8px 12px}.toggle.off{background:#777}table{width:100%;border-collapse:collapse}th,td{text-align:left;border-top:1px solid #ddd;padding:10px 6px;vertical-align:top}.inline-form{display:inline-flex;gap:6px;align-items:center}.problem{border-left:4px solid #b4234b;background:#fdf0f3;padding:12px 16px;border-radius:6px}a{color:#611f69}`

var authAdminTemplate = template.Must(template.New("authAdmin").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Authorization methods · SameOldChat</title><style>` + authAdminStyle + `</style></head><body><main class="wrap"><h1>Authorization methods</h1><p>Provider secrets are deployment configuration. Enablement is durable workspace state.</p>{{range .Methods}}<div class="row"><span><strong>{{.Label}}</strong><br><small>{{.State}}</small></span>{{if $.CanWriteApps}}<form method="post" action="/api/admin.auth.methods.set"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="provider" value="{{.Name}}"><input type="hidden" name="enabled" value="{{if .Enabled}}false{{else}}true{{end}}"><button class="toggle{{if not .Enabled}} off{{end}}" type="submit">{{if .Enabled}}Disable{{else}}Enable{{end}}</button></form>{{end}}</div>{{end}}{{if .CanReadUsers}}<hr><h2>Workspace users</h2><p>Manage active membership and workspace roles. Deactivating a user revokes their sessions and access tokens.</p><table><thead><tr><th scope="col">User</th><th scope="col">Status</th><th scope="col">Role</th><th scope="col">Actions</th></tr></thead><tbody>{{range .Users}}<tr><td><strong>{{.Name}}</strong><br><small>{{.Email}}</small></td><td>{{.Status}}</td><td>{{.Role}}</td><td>{{if $.CanWriteUsers}}<form class="inline-form" method="post" action="/api/admin.auth.users.set"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="user_id" value="{{.ID}}"><input type="hidden" name="action" value="{{if .Active}}disable{{else}}enable{{end}}"><button class="toggle" type="submit">{{if .Active}}Disable{{else}}Enable{{end}}</button></form> <form class="inline-form" method="post" action="/api/admin.auth.users.set"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="user_id" value="{{.ID}}"><input type="hidden" name="action" value="role"><label>Role for {{.Name}} <select name="role"><option value="member"{{if .IsMember}} selected{{end}}>Member</option><option value="admin"{{if .IsAdmin}} selected{{end}}>Administrator</option></select></label><button class="toggle" type="submit">Save role</button></form>{{else}}<span>Read only</span>{{end}}</td></tr>{{end}}</tbody></table>{{if .NextPageURL}}<p><a href="{{.NextPageURL}}">Next page</a></p>{{end}}{{end}}{{if .CanWriteUsers}}<hr><h2>Manual user setup</h2><p>Create an active workspace member directly. External authorization still requires a matching verified email.</p><form method="post" action="/api/admin.auth.users.create"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label>Email <input name="email" type="email" maxlength="320" autocomplete="email" required></label> <label>Name <input name="real_name" maxlength="200" autocomplete="name" required></label> <label>Role <select name="role"><option value="member">Member</option><option value="admin">Administrator</option></select></label> <button class="toggle" type="submit">Create user</button></form>{{end}}</main></body></html>`))

var authAdminErrorTemplate = template.Must(template.New("authAdminError").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · SameOldChat</title><style>` + authAdminStyle + `</style></head><body><main class="wrap"><h1>{{.Title}}</h1><p class="problem" role="alert">{{.Message}}</p><p><a href="/app/admin/auth">Back to authorization methods</a></p></main></body></html>`))

type authAdminPageData struct {
	CSRFToken     string
	CanWriteApps  bool
	CanReadUsers  bool
	CanWriteUsers bool
	Methods       []authAdminMethodView
	Users         []authAdminUserView
	NextPageURL   string
}

type authAdminMethodView struct {
	Name    string
	Label   string
	State   string
	Enabled bool
}

type authAdminUserView struct {
	ID       domain.UserID
	Name     string
	Email    string
	Status   string
	Role     domain.WorkspaceRole
	Active   bool
	IsMember bool
	IsAdmin  bool
}

type authAdminErrorData struct {
	Title   string
	Message string
}

// authAdminProblem describes one rejected administration request. Both response
// shapes are rendered from the same value so a rejection can never be answered
// with an empty body, and the browser never receives a raw JSON envelope.
type authAdminProblem struct {
	Status  int
	Code    string
	Title   string
	Message string
}

var (
	problemNotAuthenticated = authAdminProblem{Status: http.StatusUnauthorized, Code: "not_authenticated", Title: "Sign in required", Message: "Your session has ended. Sign in again to administer authorization."}
	problemNotAuthorized    = authAdminProblem{Status: http.StatusForbidden, Code: "not_authorized", Title: "Not authorized", Message: "Your workspace role does not administer authorization for this workspace."}
	problemInvalidForm      = authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_form", Title: "Request rejected", Message: "The submitted form could not be read. Reload the page and try again."}
	problemSessionMissing   = authAdminProblem{Status: http.StatusUnauthorized, Code: "session_unavailable", Title: "Sign in required", Message: "No browser session accompanied this request."}
	problemRoleUnavailable  = authAdminProblem{Status: http.StatusServiceUnavailable, Code: "workspace_role_unavailable", Title: "Temporarily unavailable", Message: "Your workspace membership could not be read, so no administrative authority was granted."}
)

// writeAuthAdminProblem answers a rejected request in the shape the caller asked
// for. It always writes a status and a body: a mutation that was refused must
// never look like one that succeeded.
func (h Handler) writeAuthAdminProblem(w http.ResponseWriter, r *http.Request, problem authAdminProblem) {
	if wantsAuthAdminJSON(r) {
		writeAuthAdminJSON(w, problem.Status, map[string]any{"ok": false, "error": problem.Code})
		return
	}
	var rendered bytes.Buffer
	if err := authAdminErrorTemplate.Execute(&rendered, authAdminErrorData{Title: problem.Title, Message: problem.Message}); err != nil {
		writeAuthAdminJSON(w, problem.Status, map[string]any{"ok": false, "error": problem.Code})
		return
	}
	authAdminSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(problem.Status)
	_, _ = w.Write(rendered.Bytes())
}

func wantsAuthAdminJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// authAdminSuccess answers an applied mutation.
func (h Handler) authAdminSuccess(w http.ResponseWriter, r *http.Request, status int, value map[string]any) {
	if wantsAuthAdminJSON(r) {
		writeAuthAdminJSON(w, status, value)
		return
	}
	authAdminSecurityHeaders(w)
	http.Redirect(w, r, "/app/admin/auth", http.StatusSeeOther)
}

func (h Handler) authAdminPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemNotAuthenticated)
		return
	}
	if h.Login == nil || principal.WorkspaceID != h.Login.workspace {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return
	}
	if !h.authAdminRoleAllowed(w, r, principal) {
		return
	}
	canReadApps := principal.HasScope(auth.ScopeAdminAppsRead) || principal.HasScope(auth.ScopeAdminAppsWrite)
	canReadUsers := principal.HasScope(auth.ScopeAdminUsersRead) || principal.HasScope(auth.ScopeAdminUsersWrite)
	if !canReadApps && !canReadUsers {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return
	}
	h.setCSRFCookie(w, r)
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthAdminProblem(w, r, problemSessionMissing)
		return
	}
	data := authAdminPageData{
		CSRFToken:     auth.CSRFToken(sessionCookie.Value),
		CanWriteApps:  principal.HasScope(auth.ScopeAdminAppsWrite),
		CanReadUsers:  canReadUsers,
		CanWriteUsers: principal.HasScope(auth.ScopeAdminUsersWrite),
	}
	if canReadApps {
		for _, name := range h.authProviderNames() {
			method, methodErr := h.Login.service.GetAuthMethod(r.Context(), h.Login.workspace, name)
			if methodErr != nil {
				h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusServiceUnavailable, Code: "auth_methods_unavailable", Title: "Temporarily unavailable", Message: "Authorization settings could not be read."})
				return
			}
			view := authAdminMethodView{Name: name, Label: providerLabel(name), State: "disabled", Enabled: method.Enabled}
			if method.Enabled {
				view.State = "enabled"
			}
			data.Methods = append(data.Methods, view)
		}
	}
	if canReadUsers {
		request, requestErr := decodeAdminPageRequest(r)
		if requestErr != nil {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_pagination", Title: "Request rejected", Message: requestErr.Error()})
			return
		}
		page, pageErr := h.Login.service.AdminListUsers(r.Context(), h.Login.workspace, principal.UserID, request)
		if pageErr != nil {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusServiceUnavailable, Code: "users_unavailable", Title: "Temporarily unavailable", Message: "The workspace user list could not be read."})
			return
		}
		for _, item := range page.Users {
			name := item.User.RealName
			if strings.TrimSpace(name) == "" {
				name = item.User.Name
			}
			active := item.Membership.Active && !item.User.Deleted
			status := "deactivated"
			if active {
				status = "active"
			}
			data.Users = append(data.Users, authAdminUserView{
				ID:       item.User.ID,
				Name:     name,
				Email:    item.User.Email,
				Status:   status,
				Role:     item.Membership.Role,
				Active:   active,
				IsMember: item.Membership.Role == domain.WorkspaceRoleMember,
				IsAdmin:  item.Membership.Role == domain.WorkspaceRoleAdmin,
			})
		}
		if page.NextCursor != "" {
			data.NextPageURL = "/app/admin/auth?limit=" + strconv.Itoa(request.Limit) + "&cursor=" + url.QueryEscape(string(page.NextCursor))
		}
	}
	var rendered bytes.Buffer
	if err := authAdminTemplate.Execute(&rendered, data); err != nil {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusServiceUnavailable, Code: "rendering_unavailable", Title: "Temporarily unavailable", Message: "The authorization page could not be rendered."})
		return
	}
	authAdminSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(rendered.Bytes())
}

func (h Handler) authProviderNames() []string {
	names := make([]string, 0, len(h.Login.providers))
	for name := range h.Login.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	request, err := decodeAdminPageRequest(r)
	if err != nil {
		writeAuthAdminJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_pagination"})
		return
	}
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
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemInvalidForm)
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["user_id"]))
	if target == "" {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_user", Title: "Request rejected", Message: "A workspace user is required."})
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
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_role", Title: "Request rejected", Message: "A workspace role must be member or admin."})
			return
		}
		operationErr = h.Login.service.SetUserRole(r.Context(), h.Login.workspace, principal.UserID, target, role)
	default:
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_action", Title: "Request rejected", Message: "The requested user action is not supported."})
		return
	}
	if operationErr != nil {
		h.writeAuthAdminProblem(w, r, authAdminUserMutationProblem(operationErr))
		return
	}
	h.authAdminSuccess(w, r, http.StatusOK, map[string]any{"ok": true})
}

func authAdminUserMutationProblem(err error) authAdminProblem {
	if errors.Is(err, store.ErrNotFound) {
		return authAdminProblem{Status: http.StatusNotFound, Code: "user_not_found", Title: "User not found", Message: "That workspace user does not exist."}
	}
	if errors.Is(err, service.ErrInvalidInviteRequest) || errors.Is(err, service.ErrInvalidWorkspace) {
		return authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_user", Title: "Request rejected", Message: "The submitted user details are not valid."}
	}
	return authAdminProblem{Status: http.StatusServiceUnavailable, Code: "user_update_unavailable", Title: "Temporarily unavailable", Message: "The user could not be updated. Nothing was changed."}
}

func (h Handler) authMethodsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authAdminAllowed(w, r, auth.ScopeAdminAppsRead, auth.ScopeAdminAppsWrite); !ok {
		return
	}
	names := h.authProviderNames()
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
	if _, ok := h.authAdminAllowed(w, r, auth.ScopeAdminAppsWrite); !ok {
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemInvalidForm)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(fields["provider"]))
	if _, ok := h.Login.providers[provider]; !ok {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_provider", Title: "Request rejected", Message: "That authorization provider is not configured."})
		return
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(fields["enabled"]))
	if err != nil {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_enabled", Title: "Request rejected", Message: "The enablement value must be true or false."})
		return
	}
	if err := h.Login.service.SetAuthMethod(r.Context(), domain.AuthMethod{WorkspaceID: h.Login.workspace, Provider: provider, Enabled: enabled}); err != nil {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusServiceUnavailable, Code: "auth_method_unavailable", Title: "Temporarily unavailable", Message: "The authorization method could not be updated. Nothing was changed."})
		return
	}
	h.authAdminSuccess(w, r, http.StatusOK, map[string]any{"ok": true})
}

// authAdminAllowed is the single authorization gate for the workspace
// administration surface. Every caller runs the same four checks in the same
// order, so a new endpoint cannot accidentally ship with a weaker one.
func (h Handler) authAdminAllowed(w http.ResponseWriter, r *http.Request, scopes ...auth.Scope) (auth.Principal, bool) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemNotAuthenticated)
		return auth.Principal{}, false
	}
	if h.Login == nil || principal.WorkspaceID != h.Login.workspace {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return auth.Principal{}, false
	}
	held := false
	for _, scope := range scopes {
		if principal.HasScope(scope) {
			held = true
			break
		}
	}
	if !held {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return auth.Principal{}, false
	}
	if !h.authAdminRoleAllowed(w, r, principal) {
		return auth.Principal{}, false
	}
	return principal, true
}

// authAdminRoleAllowed asserts the caller's durable workspace role, not only the
// scope set stored on their session.
//
// A scope list is a snapshot taken when the session was minted; a role is the
// current state of the workspace. Requiring both means a session that carries a
// control-plane scope it should never have had — one minted before scopes were
// derived from roles, or a statically seeded deployment credential — still cannot
// reach the administration surface, and a demoted administrator loses it without
// waiting for their session to expire.
func (h Handler) authAdminRoleAllowed(w http.ResponseWriter, r *http.Request, principal auth.Principal) bool {
	role, err := h.Login.workspaceRole(r.Context(), principal.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.writeAuthAdminProblem(w, r, problemNotAuthorized)
			return false
		}
		h.writeAuthAdminProblem(w, r, problemRoleUnavailable)
		return false
	}
	if !auth.WorkspaceRoleHoldsControlPlane(role) {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return false
	}
	return true
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

func authAdminInvitationProblem(err error) authAdminProblem {
	if errors.Is(err, service.ErrInvalidInviteRequest) {
		return authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_invitation", Title: "Request rejected", Message: "The submitted invitation is not valid."}
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		return authAdminProblem{Status: http.StatusConflict, Code: "invitation_already_exists", Title: "Already invited", Message: "That address already has an invitation."}
	}
	return authAdminProblem{Status: http.StatusServiceUnavailable, Code: "user_invitation_unavailable", Title: "Temporarily unavailable", Message: "The invitation could not be recorded. Nothing was changed."}
}

func (h Handler) authUserInvite(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemInvalidForm)
		return
	}
	if strings.TrimSpace(fields["email"]) == "" || strings.TrimSpace(fields["real_name"]) == "" {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_user", Title: "Request rejected", Message: "An email address and a name are required."})
		return
	}
	channels := normalizeAdminInviteChannels(fields["channel_ids"])
	if err := h.Messages.AdminInviteUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], channels, fields["custom_message"], fields["real_name"], false, false, false, time.Time{}); err != nil {
		h.writeAuthAdminProblem(w, r, authAdminInvitationProblem(err))
		return
	}
	h.authAdminSuccess(w, r, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) authUserCreate(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	fields, err := decodeFormFields(w, r)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemInvalidForm)
		return
	}
	role := domain.WorkspaceRole(strings.ToLower(strings.TrimSpace(fields["role"])))
	user, err := h.Login.service.AdminCreateUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], fields["real_name"], role)
	if err != nil {
		problem := authAdminProblem{Status: http.StatusServiceUnavailable, Code: "user_creation_unavailable", Title: "Temporarily unavailable", Message: "The user could not be created. Nothing was changed."}
		if errors.Is(err, store.ErrAlreadyExists) {
			problem = authAdminProblem{Status: http.StatusConflict, Code: "user_already_exists", Title: "Already a member", Message: "A workspace user already has that address."}
		} else if errors.Is(err, service.ErrInvalidInviteRequest) {
			problem = authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_user", Title: "Request rejected", Message: "The submitted user details are not valid."}
		}
		h.writeAuthAdminProblem(w, r, problem)
		return
	}
	h.authAdminSuccess(w, r, http.StatusCreated, map[string]any{"ok": true, "user": user})
}
