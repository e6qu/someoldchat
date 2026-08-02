package web

import (
	"bytes"
	"encoding/json"
	"errors"
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
	secureHeaders(w, authAdminContentSecurityPolicy)
}

// authAdminContentSecurityPolicy allows exactly the inline scripts this page
// carries and nothing else. The page renders through the shared layout, so it
// serves the theme bootstrap, the theme toggle and the local-time pass; under
// the previous `default-src 'none'` with no script-src at all, the browser
// would have blocked all three, leaving a theme button that does nothing.
//
// Unlike the workspace policy it keeps form-action 'self', because every form
// here posts to this origin and is answered with a redirect back to this page.
// It needs neither connect-src nor img-src: the page fetches nothing.
var authAdminContentSecurityPolicy = "default-src 'none'; script-src " +
	strings.Join(inlineScriptHashes(layoutMarkup, authAdminMarkup), " ") +
	"; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

func writeAuthAdminJSON(w http.ResponseWriter, status int, value any) {
	authAdminSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// authAdminStyle carries only what this page adds to sharedStyle. The palette,
// the reset, the focus ring and the theme tokens come from the layout: this
// page used to define its own copy of all of them, which meant it honoured the
// operating system's dark preference but ignored the theme the person had
// actually chosen in the workspace, so an administrator on a dark workspace
// arrived at a light administration page.
const authAdminStyle = `<style>
.bar{min-height:52px;display:flex;align-items:center;gap:18px;padding:10px max(18px,calc((100% - 960px)/2));background:var(--accent);color:var(--on-accent)}.bar strong{font-size:17px}.bar .bar-end{margin-left:auto;display:flex;align-items:center;gap:12px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}
.wrap{max-width:960px;margin:32px auto;padding:0 18px 48px}
.heading{margin-bottom:22px}.heading h1{margin:0 0 4px;font-size:clamp(1.8rem,5vw,2.5rem)}.heading p,.section-head p{margin:0;color:var(--muted)}
.card{margin:16px 0;padding:22px;background:var(--panel);border:1px solid var(--line);border-radius:12px;box-shadow:var(--shadow)}
.section-head{margin-bottom:14px}.section-head h2{margin:0 0 3px;font-size:20px}
.row{display:flex;align-items:center;justify-content:space-between;gap:18px;border-top:1px solid var(--line);padding:14px 0}.row:last-child{padding-bottom:0}
.status{display:inline-block;margin-top:3px;color:var(--muted);font-size:12px;font-weight:700;text-transform:capitalize}.status.active{color:var(--ok)}
select,input{font:inherit}
.toggle{background:var(--accent);color:var(--on-accent);border:0;border-radius:6px;padding:8px 12px;font-weight:800;white-space:nowrap}
.toggle.danger{background:var(--danger-bg);color:var(--danger);border:1px solid var(--danger)}
.toggle.secondary{background:transparent;color:var(--action);border:1px solid var(--field-line)}
.table-scroll{overflow-x:auto}table{width:100%;border-collapse:collapse}th,td{text-align:left;border-top:1px solid var(--line);padding:11px 8px;vertical-align:top}th{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
.user-email{color:var(--muted);overflow-wrap:anywhere}
.actions{display:flex;align-items:flex-start;gap:8px;flex-wrap:wrap}
.inline-form{display:inline-flex;gap:7px;align-items:center}.inline-form label{display:inline-flex;gap:6px;align-items:center}
.inline-form select,.setup input,.setup select{min-height:36px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel);color:var(--text);padding:6px 8px}
.read-only{color:var(--muted)}
.pager{text-align:center;margin:16px 0 0}.pager a{font-weight:700}
.setup{display:grid;grid-template-columns:repeat(3,minmax(0,1fr)) auto;align-items:end;gap:12px}.setup label{display:grid;gap:5px;color:var(--muted);font-weight:700}
.setup .wide{grid-column:1/-1}
.channel-choices{display:flex;flex-wrap:wrap;gap:10px;padding:8px 0}.channel-choices label{display:inline-flex;flex-direction:row;align-items:center;gap:6px;color:var(--text);font-weight:400}
.problem{border-left:4px solid var(--danger);background:var(--danger-bg);color:var(--danger);padding:12px 16px;border-radius:6px}
.empty{color:var(--muted);margin:0}
@media(max-width:720px){.bar{padding:10px 14px}.wrap{margin-top:22px;padding:0 12px 32px}.card{padding:16px}.setup{grid-template-columns:minmax(0,1fr)}th:nth-child(2),td:nth-child(2){display:none}.actions{min-width:210px}}
</style>`

const authAdminBar = `<a class="skip-link" href="#admin-main">Skip to content</a><header class="bar"><strong>SameOldChat</strong><span class="bar-end"><a href="/app">Back to chat</a><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></span></header>`

const authAdminMarkup = `{{define "title"}}Workspace administration · SameOldChat{{end}}
{{define "styles"}}` + authAdminStyle + `{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}` + authAdminBar + `<main class="wrap" id="admin-main">
<div class="heading"><h1>Workspace administration</h1><p>Manage access without leaving the workspace.</p><p><a href="/app/admin/settings">Workspace settings</a> · <a href="/app/admin/analytics">Analytics</a> · <a href="/app/admin/audit">Audit</a></p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .CanReadApps}}<section class="card" aria-labelledby="authorization-heading"><div class="section-head"><h2 id="authorization-heading">Authorization methods</h2><p>Provider secrets are deployment configuration. Enablement is durable workspace state.</p></div>{{range .Methods}}<div class="row"><span><strong>{{.Label}}</strong><br><span class="status{{if .Enabled}} active{{end}}">{{.State}}</span></span>{{if $.CanWriteApps}}<form method="post" action="/api/admin.auth.methods.set"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="provider" value="{{.Name}}"><input type="hidden" name="enabled" value="{{if .Enabled}}false{{else}}true{{end}}"><button class="toggle{{if .Enabled}} danger{{end}}" type="submit" aria-label="{{if .Enabled}}Disable{{else}}Enable{{end}} {{.Label}} authorization">{{if .Enabled}}Disable{{else}}Enable{{end}}</button></form>{{end}}</div>{{end}}</section>{{end}}
{{if .CanReadUsers}}<section class="card" aria-labelledby="users-heading"><div class="section-head"><h2 id="users-heading">Workspace users</h2><p>Manage active membership and roles. Deactivating a user revokes their sessions and access tokens.</p></div><div class="table-scroll"><table><thead><tr><th scope="col">User</th><th scope="col">Status</th><th scope="col">Role</th><th scope="col">Actions</th></tr></thead><tbody>{{range .Users}}<tr><td><strong>{{.Name}}</strong><br><span class="user-email">{{.Email}}</span></td><td><span class="status{{if .Active}} active{{end}}">{{.Status}}</span></td><td>{{.Role}}</td><td><div class="actions">{{if $.CanWriteUsers}}<form class="inline-form" method="post" action="/api/admin.auth.users.set"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="user_id" value="{{.ID}}"><input type="hidden" name="action" value="{{if .Active}}disable{{else}}enable{{end}}"><button class="toggle{{if .Active}} danger{{end}}" type="submit" aria-label="{{if .Active}}Disable{{else}}Enable{{end}} {{.Name}}">{{if .Active}}Disable{{else}}Enable{{end}}</button></form>{{if .RoleOptions}}<form class="inline-form" method="post" action="/api/admin.auth.users.set"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="user_id" value="{{.ID}}"><input type="hidden" name="action" value="role"><label>Role for {{.Name}} <select name="role">{{range .RoleOptions}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select></label><button class="toggle secondary" type="submit" aria-label="Save role for {{.Name}}">Save role</button></form>{{end}}{{else}}<span class="read-only">Read only</span>{{end}}</div></td></tr>{{end}}</tbody></table></div>{{if .NextPageURL}}<p class="pager"><a href="{{.NextPageURL}}">Next page</a></p>{{end}}</section>{{end}}
{{if .CanReadUsers}}<section class="card" aria-labelledby="invitations-heading"><div class="section-head"><h2 id="invitations-heading">Invitations</h2><p>An invitation records who may join, at what tier, and into which channels. It becomes an account when the person accepts it.</p></div>
{{if .CanWriteUsers}}<form class="setup" method="post" action="/api/admin.auth.users.invite">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label>Email<input name="email" type="email" maxlength="320" autocomplete="email" required></label>
<label>Name<input name="real_name" maxlength="200" autocomplete="name" required></label>
<label>Access<select name="tier">{{range .InviteTiers}}<option value="{{.Value}}">{{.Label}}</option>{{end}}</select></label>
<label>Guest access ends<input type="date" name="guest_expires_on"></label>
<label class="wide">Message<input name="custom_message" maxlength="1000"></label>
<fieldset class="wide channel-choices"><legend>Channels to join</legend>{{range .InviteChannels}}<label><input type="checkbox" name="channel_ids" value="{{.ID}}"> #{{.Name}}</label>{{else}}<span class="read-only">No channel is available to invite into.</span>{{end}}</fieldset>
<label class="wide"><input type="checkbox" name="resend" value="true"> Send again if this address was already invited</label>
<button class="toggle" type="submit">Record invitation</button>
</form>
<p class="read-only">A guest expiry applies only to the two guest tiers. A full member never expires.</p>{{end}}
<div class="table-scroll"><table><thead><tr><th scope="col">Invited</th><th scope="col">Access</th><th scope="col">Channels</th><th scope="col">Requested</th><th scope="col">Actions</th></tr></thead><tbody>{{range .InviteRequests}}<tr><td><strong>{{.RealName}}</strong><br><span class="user-email">{{.Email}}</span></td><td>{{.Tier}}{{if .GuestExpires}}<br><span class="status">guest until {{.GuestExpires}}</span>{{end}}</td><td>{{if .Channels}}{{range .Channels}}#{{.Name}} {{end}}{{else}}<span class="read-only">None recorded</span>{{end}}</td><td><time datetime="{{.RequestedMachine}}" data-local-time>{{.Requested}}</time><br><span class="status">by {{.RequestedBy}}</span></td><td><div class="actions">{{if $.CanWriteUsers}}<form class="inline-form" method="post" action="/app/admin/invites/approve"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_request_id" value="{{.ID}}"><button class="toggle" type="submit" aria-label="Approve the invitation for {{.Email}}">Approve</button></form><form class="inline-form" method="post" action="/app/admin/invites/deny"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_request_id" value="{{.ID}}"><button class="toggle danger" type="submit" aria-label="Deny the invitation for {{.Email}}">Deny</button></form>{{else}}<span class="read-only">Read only</span>{{end}}</div></td></tr>{{else}}<tr><td colspan="5"><p class="empty">No invitation is waiting for a decision.</p></td></tr>{{end}}</tbody></table></div>
{{if .MoreInvitesURL}}<p class="pager"><a href="{{.MoreInvitesURL}}">More invitations</a></p>{{end}}
<h3>Approved, waiting to be accepted</h3>
<p class="read-only">There is no mail transport here, so send the link yourself. Opening it grants nothing on its own: the account is created only when someone signs in with the invited address, verified by the identity provider.</p>
<div class="table-scroll"><table><thead><tr><th scope="col">Invited</th><th scope="col">Access</th><th scope="col">Link to send</th><th scope="col">Valid until</th><th scope="col">Actions</th></tr></thead><tbody>{{range .ApprovedInvites}}<tr><td><strong>{{.RealName}}</strong><br><span class="user-email">{{.Email}}</span></td><td>{{.Tier}}{{if .GuestExpires}}<br><span class="status">guest until {{.GuestExpires}}</span>{{end}}</td><td><a href="{{.Link}}">{{.Link}}</a></td><td>{{if .Expires}}<time datetime="{{.ExpiresMachine}}" data-local-time>{{.Expires}}</time>{{else}}<span class="read-only">No expiry</span>{{end}}</td><td><div class="actions">{{if $.CanWriteUsers}}<form class="inline-form" method="post" action="/app/admin/invites/deny"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_request_id" value="{{.ID}}"><button class="toggle danger" type="submit" aria-label="Withdraw the invitation for {{.Email}}">Withdraw</button></form>{{else}}<span class="read-only">Read only</span>{{end}}</div></td></tr>{{else}}<tr><td colspan="5"><p class="empty">No approved invitation is waiting to be accepted.</p></td></tr>{{end}}</tbody></table></div>
</section>{{end}}
{{if .CanReadApps}}<section class="card" aria-labelledby="apps-heading"><div class="section-head"><h2 id="apps-heading">App requests</h2><p>An app a member asked to install. Approving it lets the app be installed; restricting it refuses the request and keeps the record.</p></div>
<div class="table-scroll"><table><thead><tr><th scope="col">App</th><th scope="col">Status</th><th scope="col">Requested</th><th scope="col">Actions</th></tr></thead><tbody>{{range .AppRequests}}<tr><td><strong>{{.ID}}</strong></td><td><span class="status">{{.Status}}</span></td><td><time datetime="{{.RequestedMachine}}" data-local-time>{{.Requested}}</time></td><td><div class="actions">{{if $.CanWriteApps}}<form class="inline-form" method="post" action="/app/admin/apps/approve"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="app_id" value="{{.ID}}"><input type="hidden" name="request_id" value="{{.RequestID}}"><button class="toggle" type="submit" aria-label="Approve {{.ID}}">Approve</button></form><form class="inline-form" method="post" action="/app/admin/apps/restrict"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="app_id" value="{{.ID}}"><input type="hidden" name="request_id" value="{{.RequestID}}"><button class="toggle danger" type="submit" aria-label="Restrict {{.ID}}">Restrict</button></form>{{else}}<span class="read-only">Read only</span>{{end}}</div></td></tr>{{else}}<tr><td colspan="4"><p class="empty">No app is waiting for a decision.</p></td></tr>{{end}}</tbody></table></div>
{{if .MoreAppsURL}}<p class="pager"><a href="{{.MoreAppsURL}}">More app requests</a></p>{{end}}</section>{{end}}
{{if .CanWriteUsers}}<section class="card" aria-labelledby="setup-heading"><div class="section-head"><h2 id="setup-heading">Manual user setup</h2><p>Create an active workspace member directly, without an invitation. External authorization still requires a matching verified email.</p></div><form class="setup" method="post" action="/api/admin.auth.users.create"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label>Email<input name="email" type="email" maxlength="320" autocomplete="email" required></label><label>Name<input name="real_name" maxlength="200" autocomplete="name" required></label><label>Role<select name="role"><option value="member">Member</option><option value="admin">Administrator</option></select></label><button class="toggle" type="submit">Create user</button></form></section>{{end}}
</main>{{end}}`

var authAdminTemplate = mustPage(authAdminMarkup)

const authAdminErrorMarkup = `{{define "title"}}{{.Title}} · SameOldChat{{end}}
{{define "styles"}}` + authAdminStyle + `{{end}}
{{define "content"}}` + authAdminBar + `<main class="wrap" id="admin-main"><section class="card"><h1>{{.Title}}</h1><p class="problem" role="alert">{{.Message}}</p><p><a href="/app/admin/auth">Back to workspace administration</a></p></section></main>{{end}}`

var authAdminErrorTemplate = mustPage(authAdminErrorMarkup)

type authAdminPageData struct {
	CSRFToken      string
	Notice         string
	CanReadApps    bool
	CanWriteApps   bool
	CanReadUsers   bool
	CanWriteUsers  bool
	Methods        []authAdminMethodView
	Users          []authAdminUserView
	NextPageURL    string
	InviteTiers    []authAdminTierOption
	InviteChannels []conversationView
	InviteRequests  []authAdminInviteView
	ApprovedInvites []authAdminInviteView
	MoreInvitesURL  string
	AppRequests    []authAdminAppView
	MoreAppsURL    string
}

// authAdminTierOption is the invitation tier as one closed choice. The service
// validates restricted and ultra_restricted as mutually exclusive and rejects a
// guest expiry on a full member; two independent checkboxes could express both
// of the states it refuses, so the page offers the three reachable ones.
type authAdminTierOption struct {
	Value string
	Label string
}

var authAdminTiers = []authAdminTierOption{
	{Value: "member", Label: "Full member"},
	{Value: "multi_channel_guest", Label: "Guest, several channels"},
	{Value: "single_channel_guest", Label: "Guest, one channel"},
}

func authAdminTierLabel(request domain.InviteRequest) string {
	switch {
	case request.UltraRestricted:
		return "Guest, one channel"
	case request.Restricted:
		return "Guest, several channels"
	default:
		return "Full member"
	}
}

type authAdminInviteView struct {
	ID               domain.InviteRequestID
	// Link is the page to send to the invited person. It is only set for an
	// approved invitation, because that is the only state the page can be
	// acted on from.
	Link             string
	Expires          string
	ExpiresMachine   string
	Email            string
	RealName         string
	Tier             string
	Channels         []conversationView
	Requested        string
	RequestedMachine string
	RequestedBy      string
	GuestExpires     string
}

// authAdminAppView names the app by its identifier: an approval record carries
// the app and request identifiers and nothing else, and the app itself is not
// installed yet, so there is no name to read. Inventing one would be worse than
// showing the identifier the request is actually about.
type authAdminAppView struct {
	ID               domain.AppID
	RequestID        domain.AppRequestID
	Status           string
	Requested        string
	RequestedMachine string
}

type authAdminMethodView struct {
	Name    string
	Label   string
	State   string
	Enabled bool
}

type authAdminUserView struct {
	ID     domain.UserID
	Name   string
	Email  string
	Status string
	Role   domain.WorkspaceRole
	Active bool
	// RoleOptions is the set of roles this actor may write onto this row, with
	// the row's current role selected. It is empty when the actor may not change
	// the row at all, and the form is then not rendered.
	//
	// The two booleans it replaces could not represent owner: both were false
	// for an owner, so every browser default-selected the first option and the
	// page presented a workspace owner with a pre-loaded demotion — one click,
	// no field to change — that the handler then refused to reverse.
	RoleOptions []authAdminRoleOption
}

type authAdminRoleOption struct {
	Value    domain.WorkspaceRole
	Label    string
	Selected bool
}

// assignableRoles mirrors the authority the service enforces, so the page tells
// the truth about what this actor can do rather than offering a control whose
// submission is refused:
//
//   - a role may be granted up to, and including, the actor's own rank;
//   - a row that outranks the actor is not editable at all.
func assignableRoles(actor, target domain.WorkspaceRole) []authAdminRoleOption {
	if target.Outranks(actor) {
		return nil
	}
	options := make([]authAdminRoleOption, 0, 3)
	for _, role := range []domain.WorkspaceRole{domain.WorkspaceRoleMember, domain.WorkspaceRoleAdmin, domain.WorkspaceRoleOwner} {
		if role.Rank() > actor.Rank() {
			continue
		}
		options = append(options, authAdminRoleOption{Value: role, Label: workspaceRoleLabel(role), Selected: role == target})
	}
	return options
}

func workspaceRoleLabel(role domain.WorkspaceRole) string {
	switch role {
	case domain.WorkspaceRoleOwner:
		return "Owner"
	case domain.WorkspaceRoleAdmin:
		return "Administrator"
	default:
		return "Member"
	}
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

// authAdminSuccessWithNotice reports what was decided. A queue that empties one
// row with no other change on screen leaves an administrator unsure whether the
// approve or the deny was the one that landed.
func (h Handler) authAdminSuccessWithNotice(w http.ResponseWriter, r *http.Request, notice string, value map[string]any) {
	if wantsAuthAdminJSON(r) {
		writeAuthAdminJSON(w, http.StatusOK, value)
		return
	}
	authAdminSecurityHeaders(w)
	http.Redirect(w, r, "/app/admin/auth?notice="+url.QueryEscape(notice), http.StatusSeeOther)
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
	actorRole, roleAllowed := h.authAdminRoleAllowed(w, r, principal)
	if !roleAllowed {
		return
	}
	canReadApps := principal.HasScope(auth.ScopeAdminAppsRead) || principal.HasScope(auth.ScopeAdminAppsWrite)
	canReadUsers := principal.HasScope(auth.ScopeAdminUsersRead) || principal.HasScope(auth.ScopeAdminUsersWrite)
	if !canReadApps && !canReadUsers {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthAdminProblem(w, r, problemSessionMissing)
		return
	}
	data := authAdminPageData{
		CSRFToken:     auth.CSRFToken(sessionCookie.Value),
		Notice:        strings.TrimSpace(r.URL.Query().Get("notice")),
		CanReadApps:   canReadApps,
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
				ID:          item.User.ID,
				Name:        name,
				Email:       item.User.Email,
				Status:      status,
				Role:        item.Membership.Role,
				Active:      active,
				RoleOptions: assignableRoles(actorRole, item.Membership.Role),
			})
		}
		if page.NextCursor != "" {
			data.NextPageURL = "/app/admin/auth?limit=" + strconv.Itoa(request.Limit) + "&cursor=" + url.QueryEscape(string(page.NextCursor))
		}
	}
	names := h.newUserNames(r.Context(), principal)
	if canReadUsers {
		data.InviteTiers = authAdminTiers
		if data.CanWriteUsers {
			if options, optionsErr := h.visibleChannelOptions(r.Context(), principal); optionsErr == nil {
				data.InviteChannels = options
			}
		}
		queue := func(status domain.InviteRequestStatus) ([]authAdminInviteView, domain.Cursor, bool) {
			page, pageErr := h.Messages.AdminListInviteRequests(r.Context(), principal.WorkspaceID, principal.UserID, status, domain.PageRequest{Limit: 25})
			if pageErr != nil && !errors.Is(pageErr, service.ErrNotWorkspaceAdmin) {
				h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusServiceUnavailable, Code: "invitations_unavailable", Title: "Temporarily unavailable", Message: "Invitations could not be read."})
				return nil, "", false
			}
			views := make([]authAdminInviteView, 0, len(page.Requests))
			for _, request := range page.Requests {
				view := authAdminInviteView{
					ID: request.ID, Email: request.Email, RealName: request.RealName,
					Tier:             authAdminTierLabel(request),
					Requested:        formatTime(request.CreatedAt),
					RequestedMachine: request.CreatedAt.UTC().Format(time.RFC3339Nano),
					RequestedBy:      names.name(request.RequestedBy),
				}
				if !request.GuestExpirationAt.IsZero() {
					view.GuestExpires = formatTime(request.GuestExpirationAt)
				}
				if !request.ExpiresAt.IsZero() {
					view.Expires = formatTime(request.ExpiresAt)
					view.ExpiresMachine = request.ExpiresAt.UTC().Format(time.RFC3339Nano)
				}
				if status == domain.InviteRequestApproved {
					view.Link = h.invitationLink(request.ID)
				}
				for _, channelID := range request.ChannelIDs {
					view.Channels = append(view.Channels, conversationView{ID: string(channelID), Name: names.channelName(channelID)})
				}
				views = append(views, view)
			}
			return views, page.NextCursor, true
		}
		pending, nextPending, ok := queue(domain.InviteRequestPending)
		if !ok {
			return
		}
		data.InviteRequests = pending
		if nextPending != "" {
			data.MoreInvitesURL = "/app/admin/auth?invites_cursor=" + url.QueryEscape(string(nextPending))
		}
		approved, _, approvedOK := queue(domain.InviteRequestApproved)
		if !approvedOK {
			return
		}
		data.ApprovedInvites = approved
	}
	if canReadApps {
		apps, appsErr := h.Messages.AdminListApps(r.Context(), principal.WorkspaceID, principal.UserID, domain.AppApprovalRequested, domain.PageRequest{Limit: 25})
		if appsErr != nil && !errors.Is(appsErr, service.ErrNotWorkspaceAdmin) {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusServiceUnavailable, Code: "app_requests_unavailable", Title: "Temporarily unavailable", Message: "App requests could not be read."})
			return
		}
		for _, approval := range apps.Apps {
			data.AppRequests = append(data.AppRequests, authAdminAppView{
				ID: approval.ID, RequestID: approval.RequestID,
				Status:           string(approval.Status),
				Requested:        formatTime(approval.CreatedAt),
				RequestedMachine: approval.CreatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		if apps.NextCursor != "" {
			data.MoreAppsURL = "/app/admin/auth?apps_cursor=" + url.QueryEscape(string(apps.NextCursor))
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


// invitationLink is the address to hand to the invited person. It is absolute
// when the deployment knows its own public URL, because the link leaves this
// browser and a relative path is useless once it does.
func (h Handler) invitationLink(id domain.InviteRequestID) string {
	path := "/app/invite/" + url.PathEscape(string(id))
	if strings.TrimSpace(h.PublicURL) == "" {
		return path
	}
	return strings.TrimRight(h.PublicURL, "/") + path
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
		// owner is a real, reachable workspace role. Refusing it here left the
		// page unable to restore an owner it had just demoted; the service
		// enforces who may grant it.
		if role.Rank() == 0 {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_role", Title: "Request rejected", Message: "A workspace role must be member, admin, or owner."})
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
	// A refusal by the role hierarchy is an authorization answer, not an
	// outage: it used to be reported as "temporarily unavailable", which told an
	// administrator to try again at something that can never succeed.
	if errors.Is(err, service.ErrNotWorkspaceAdmin) {
		return authAdminProblem{Status: http.StatusForbidden, Code: "not_authorized", Title: "Not authorized", Message: "Your workspace role does not allow that change. Nothing was changed."}
	}
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
	if _, allowed := h.authAdminRoleAllowed(w, r, principal); !allowed {
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
func (h Handler) authAdminRoleAllowed(w http.ResponseWriter, r *http.Request, principal auth.Principal) (domain.WorkspaceRole, bool) {
	role, err := h.Login.workspaceRole(r.Context(), principal.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.writeAuthAdminProblem(w, r, problemNotAuthorized)
			return "", false
		}
		h.writeAuthAdminProblem(w, r, problemRoleUnavailable)
		return "", false
	}
	if !auth.WorkspaceRoleHoldsControlPlane(role) {
		h.writeAuthAdminProblem(w, r, problemNotAuthorized)
		return "", false
	}
	return role, true
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
	// channel_ids repeats: the page offers one checkbox per channel, and an
	// invitation into several channels is the ordinary case. It still accepts
	// the comma-separated single field the API callers send.
	fields, channelValues, err := decodeFormValues(w, r, "channel_ids")
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemInvalidForm)
		return
	}
	if strings.TrimSpace(fields["email"]) == "" || strings.TrimSpace(fields["real_name"]) == "" {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_user", Title: "Request rejected", Message: "An email address and a name are required."})
		return
	}
	channels := normalizeAdminInviteChannels(strings.Join(channelValues, ","))
	restricted, ultraRestricted, tierOK := adminInviteTier(fields)
	if !tierOK {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_tier", Title: "Request rejected", Message: "An invitation is for a full member, a guest with several channels, or a guest with one channel."})
		return
	}
	expiration, expirationErr := adminInviteExpiration(fields, restricted || ultraRestricted)
	if expirationErr != nil {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_expiration", Title: "Request rejected", Message: expirationErr.Error()})
		return
	}
	resend := adminInviteFlag(fields["resend"])
	if err := h.Messages.AdminInviteUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], channels, fields["custom_message"], fields["real_name"], resend, restricted, ultraRestricted, expiration); err != nil {
		h.writeAuthAdminProblem(w, r, authAdminInvitationProblem(err))
		return
	}
	h.authAdminSuccess(w, r, http.StatusOK, map[string]any{"ok": true})
}

// adminInviteTier reads the closed tier choice. The two boolean fields the
// service takes can express a state it always rejects (both set), so the form
// carries one value and this is the only place that widens it back out.
// restricted and ultra_restricted are still read for the API callers that
// predate the tier field.
func adminInviteTier(fields map[string]string) (restricted, ultraRestricted, ok bool) {
	switch strings.ToLower(strings.TrimSpace(fields["tier"])) {
	case "member":
		return false, false, true
	case "multi_channel_guest":
		return true, false, true
	case "single_channel_guest":
		return false, true, true
	case "":
		restricted, ultraRestricted = adminInviteFlag(fields["restricted"]), adminInviteFlag(fields["ultra_restricted"])
		return restricted, ultraRestricted, !(restricted && ultraRestricted)
	default:
		return false, false, false
	}
}

// adminInviteExpiration reads the guest expiry as a calendar date, which is
// what an administrator is choosing, and pins it to the end of that day in UTC
// so the guest keeps access for the whole day they were promised. A full member
// never expires, and the service refuses an expiry on one.
func adminInviteExpiration(fields map[string]string, guest bool) (time.Time, error) {
	raw := strings.TrimSpace(fields["guest_expires_on"])
	if raw == "" {
		return time.Time{}, nil
	}
	if !guest {
		return time.Time{}, errors.New("Only a guest invitation can expire. Choose a guest tier or clear the date.")
	}
	day, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		return time.Time{}, errors.New("The guest expiry must be a date.")
	}
	return day.Add(24*time.Hour - time.Nanosecond), nil
}

func adminInviteFlag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "on", "1", "yes":
		return true
	default:
		return false
	}
}

func (h Handler) authInviteRequestDecision(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		id := domain.InviteRequestID(strings.TrimSpace(fields["invite_request_id"]))
		if id == "" {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_invite_request", Title: "Request rejected", Message: "An invitation is required."})
			return
		}
		decide := h.Messages.AdminDenyInviteRequest
		notice := "Invitation denied"
		if approve {
			decide = h.Messages.AdminApproveInviteRequest
			notice = "Invitation approved"
		}
		if err := decide(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
			h.writeAuthAdminProblem(w, r, authAdminInvitationProblem(err))
			return
		}
		h.authAdminSuccessWithNotice(w, r, notice, map[string]any{"ok": true})
	}
}

func (h Handler) authAppDecision(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminAppsWrite)
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
		appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
		requestID := domain.AppRequestID(strings.TrimSpace(fields["request_id"]))
		if appID == "" || requestID == "" {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_app_request", Title: "Request rejected", Message: "An app request is required."})
			return
		}
		decide := h.Messages.AdminRestrictApp
		notice := "App restricted"
		if approve {
			decide = h.Messages.AdminApproveApp
			notice = "App approved"
		}
		if err := decide(r.Context(), principal.WorkspaceID, principal.UserID, appID, requestID); err != nil {
			h.writeAuthAdminProblem(w, r, authAdminAppDecisionProblem(err))
			return
		}
		h.authAdminSuccessWithNotice(w, r, notice, map[string]any{"ok": true})
	}
}

func authAdminAppDecisionProblem(err error) authAdminProblem {
	switch {
	case errors.Is(err, service.ErrNotWorkspaceAdmin):
		return authAdminProblem{Status: http.StatusForbidden, Code: "not_authorized", Title: "Not authorized", Message: "Your workspace role does not decide app requests. Nothing was changed."}
	case errors.Is(err, store.ErrNotFound):
		return authAdminProblem{Status: http.StatusNotFound, Code: "app_request_not_found", Title: "Request not found", Message: "That app request no longer exists. It may already have been decided."}
	default:
		return authAdminProblem{Status: http.StatusServiceUnavailable, Code: "app_decision_unavailable", Title: "Temporarily unavailable", Message: "The app request could not be decided. Nothing was changed."}
	}
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
