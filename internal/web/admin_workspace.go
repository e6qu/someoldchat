package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ADMIN-02 is explicit that a policy the deployment cannot apply must not be
// rendered as a working control. This page therefore carries exactly the
// settings with a durable backend and an enforced effect — the workspace name,
// description, discoverability, icon and default channels — and says in plain
// words which governance controls do not exist here rather than drawing an
// inert switch for them.
//
// Retention used to be the largest of those, and this page said so. It now has
// a policy, storage, a sweep and an API, so the control is real — and it says
// two things a working toggle still owes the person using it: that deletion is
// permanent, and that it runs on a schedule rather than the instant content
// expires, which is Slack's behaviour too. The last-swept line exists so a
// stalled worker is visible; without it, a workspace could believe a policy was
// being applied for weeks after it stopped.

type workspaceSettingsData struct {
	CSRFToken       string
	Notice          string
	Name            string
	Description     string
	IconURL         string
	Discoverability string
	// Retention. The summary and the swept line are what make the control
	// honest: a duration alone would not tell an administrator whether the
	// policy is actually being applied.
	MessageRetentionDays  int
	FileRetentionDays     int
	RetentionSummary      string
	RetentionSweptAt      string
	RetentionSweptMachine string
	Options               []workspaceDiscoverabilityOption
	Channels              []workspaceDefaultChannelOption
	CanWrite              bool
}

type workspaceDiscoverabilityOption struct {
	Value    domain.WorkspaceDiscoverability
	Label    string
	Detail   string
	Selected bool
}

type workspaceDefaultChannelOption struct {
	ID       string
	Name     string
	Selected bool
}

// workspaceDiscoverabilityChoices names every value domain.Workspace accepts,
// with what each one means. A bare enum value in a dropdown makes an
// administrator guess which of "closed" and "invite_only" is stricter.
var workspaceDiscoverabilityChoices = []workspaceDiscoverabilityOption{
	{Value: domain.WorkspaceDiscoverabilityOpen, Label: "Open", Detail: "Anyone with an allowed email domain can find and join."},
	{Value: domain.WorkspaceDiscoverabilityInviteOnly, Label: "Invite only", Detail: "Findable, but joining requires an invitation."},
	{Value: domain.WorkspaceDiscoverabilityUnlisted, Label: "Unlisted", Detail: "Not listed anywhere; reachable only by a direct link."},
	{Value: domain.WorkspaceDiscoverabilityClosed, Label: "Closed", Detail: "Neither findable nor joinable."},
}

const workspaceSettingsMarkup = `{{define "title"}}Workspace settings · SameOldChat{{end}}
{{define "styles"}}` + authAdminStyle + `{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}` + authAdminBar + `<main class="wrap" id="admin-main">
<div class="heading"><h1>Workspace settings</h1><p>Every control here changes durable workspace state and takes effect immediately.</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<section class="card" aria-labelledby="identity-heading">
<div class="section-head"><h2 id="identity-heading">Identity</h2><p>What this workspace is called, and how it introduces itself.</p></div>
<form class="setup" method="post" action="/app/admin/settings/identity">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label>Name<input name="name" maxlength="200" value="{{.Name}}" {{if not .CanWrite}}disabled{{end}} required></label>
<label>Icon URL<input name="icon_url" type="url" maxlength="2048" value="{{.IconURL}}" {{if not .CanWrite}}disabled{{end}}></label>
<label class="wide">Description<input name="description" maxlength="1000" value="{{.Description}}" {{if not .CanWrite}}disabled{{end}}></label>
{{if .CanWrite}}<button class="toggle" type="submit">Save identity</button>{{end}}
</form>
</section>
<section class="card" aria-labelledby="discoverability-heading">
<div class="section-head"><h2 id="discoverability-heading">Who can find and join</h2><p>This is enforced when someone tries to discover or join the workspace.</p></div>
<form method="post" action="/app/admin/settings/discoverability">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
{{range .Options}}<div class="row"><span><strong>{{.Label}}</strong><br><span class="status">{{.Detail}}</span></span><label class="inline-form"><input type="radio" name="discoverability" value="{{.Value}}"{{if .Selected}} checked{{end}} {{if not $.CanWrite}}disabled{{end}}> <span class="visually-hidden">Choose {{.Label}}</span></label></div>{{end}}
{{if .CanWrite}}<p><button class="toggle" type="submit">Save who can join</button></p>{{end}}
</form>
</section>
<section class="card" aria-labelledby="defaults-heading">
<div class="section-head"><h2 id="defaults-heading">Default channels</h2><p>Every new member joins these when their account is created.</p></div>
<form method="post" action="/app/admin/settings/default-channels">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<fieldset class="channel-choices"><legend class="visually-hidden">Default channels</legend>{{range .Channels}}<label><input type="checkbox" name="channel_ids" value="{{.ID}}"{{if .Selected}} checked{{end}} {{if not $.CanWrite}}disabled{{end}}> #{{.Name}}</label>{{else}}<span class="read-only">This workspace has no channel to default into.</span>{{end}}</fieldset>
{{if .CanWrite}}<p><button class="toggle" type="submit">Save default channels</button></p>{{end}}
</form>
</section>
<section class="card" aria-labelledby="retention-heading">
<div class="section-head"><h2 id="retention-heading">Message and file retention</h2><p>Deletion under this policy is permanent and cannot be undone. It runs on a schedule rather than the instant something expires, so content stays readable for up to a day after its age passes the limit.</p></div>
<form class="setup" method="post" action="/app/admin/settings/retention">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label>Keep messages for<input name="message_days" type="number" min="0" max="36499" value="{{.MessageRetentionDays}}" {{if not .CanWrite}}disabled{{end}}><span class="read-only">days · 0 keeps them forever</span></label>
<label>Keep files for<input name="file_days" type="number" min="0" max="36499" value="{{.FileRetentionDays}}" {{if not .CanWrite}}disabled{{end}}><span class="read-only">days · 0 keeps them forever</span></label>
{{if .CanWrite}}<button class="toggle" type="submit">Save retention</button>{{end}}
</form>
<p class="read-only">{{.RetentionSummary}}</p>
{{if .RetentionSweptAt}}<p class="read-only">Last swept <time datetime="{{.RetentionSweptMachine}}" data-local-time>{{.RetentionSweptAt}}</time>. A conversation is swept about once a day; if this is much older, the retention worker is not running.</p>{{else}}<p class="read-only">Nothing has been swept yet.</p>{{end}}
<p class="read-only">A channel can be given a shorter limit of its own from its conversation details. Canvases and lists are not covered by retention here and are kept indefinitely.</p>
</section>
<section class="card" aria-labelledby="absent-heading">
<div class="section-head"><h2 id="absent-heading">Not governed here</h2><p>These are absent rather than off. A switch that changed nothing would be worse than no switch, because you would stop looking for the missing capability.</p></div>
<ul>
<li><strong>Audio and video.</strong> Huddles and calls carry no media transport here.</li>
</ul>
<p><a href="/app/admin/analytics">Workspace analytics</a> · <a href="/app/admin/audit">Audit</a> · <a href="/app/admin/auth">Access and invitations</a></p>
</section>
</main>{{end}}`

var workspaceSettingsTemplate = mustPage(workspaceSettingsMarkup)

type analyticsData struct {
	Since            string
	Days             int
	DayOptions       []int
	Members          int
	ActiveMembers    int
	Guests           int
	Admins           int
	PublicChannels   int
	PrivateChannels  int
	ArchivedChannels int
	Messages         int
	RecentMessages   int
	Files            int
	RecentFiles      int
	Busiest          []domain.ChannelActivity
}

const analyticsMarkup = `{{define "title"}}Workspace analytics · SameOldChat{{end}}
{{define "styles"}}` + authAdminStyle + `<style>
.figures{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:14px;margin:0}
.figure{padding:14px;border:1px solid var(--line);border-radius:10px}
.figure dt{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
.figure dd{margin:4px 0 0;font-size:26px;font-weight:800}
.figure .qualifier{display:block;font-size:12px;font-weight:400;color:var(--muted)}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}` + authAdminBar + `<main class="wrap" id="admin-main">
<div class="heading"><h1>Workspace analytics</h1><p>Counted from the durable rows at the moment you loaded this page. There is no sampling and no delay, and nothing here is retained separately.</p></div>
<section class="card" aria-labelledby="window-heading">
<div class="section-head"><h2 id="window-heading">Window</h2><p>Recent figures cover the last {{.Days}} days, from {{.Since}}.</p></div>
<form method="get" action="/app/admin/analytics"><label class="inline-form">Days<select name="days">{{range .DayOptions}}<option value="{{.}}"{{if eq . $.Days}} selected{{end}}>{{.}}</option>{{end}}</select></label> <button class="toggle secondary" type="submit">Change window</button></form>
</section>
<section class="card" aria-labelledby="people-heading">
<div class="section-head"><h2 id="people-heading">People</h2></div>
<dl class="figures">
<div class="figure"><dt>Members</dt><dd>{{.Members}}</dd></div>
<div class="figure"><dt>Active</dt><dd>{{.ActiveMembers}}<span class="qualifier">not deactivated</span></dd></div>
<div class="figure"><dt>Guests</dt><dd>{{.Guests}}</dd></div>
<div class="figure"><dt>Administrators</dt><dd>{{.Admins}}<span class="qualifier">including owners</span></dd></div>
</dl>
</section>
<section class="card" aria-labelledby="places-heading">
<div class="section-head"><h2 id="places-heading">Channels</h2><p>Direct conversations are not counted: they are private between their members.</p></div>
<dl class="figures">
<div class="figure"><dt>Public</dt><dd>{{.PublicChannels}}</dd></div>
<div class="figure"><dt>Private</dt><dd>{{.PrivateChannels}}</dd></div>
<div class="figure"><dt>Archived</dt><dd>{{.ArchivedChannels}}</dd></div>
</dl>
</section>
<section class="card" aria-labelledby="activity-heading">
<div class="section-head"><h2 id="activity-heading">Activity</h2></div>
<dl class="figures">
<div class="figure"><dt>Messages</dt><dd>{{.Messages}}<span class="qualifier">all time, excluding deleted</span></dd></div>
<div class="figure"><dt>Messages recently</dt><dd>{{.RecentMessages}}<span class="qualifier">last {{.Days}} days</span></dd></div>
<div class="figure"><dt>Files</dt><dd>{{.Files}}</dd></div>
<div class="figure"><dt>Files recently</dt><dd>{{.RecentFiles}}<span class="qualifier">last {{.Days}} days</span></dd></div>
</dl>
<h3>Busiest channels</h3>
<div class="table-scroll"><table><thead><tr><th scope="col">Channel</th><th scope="col">Messages</th></tr></thead><tbody>{{range .Busiest}}<tr><td>#{{.Name}}</td><td>{{.Messages}}</td></tr>{{else}}<tr><td colspan="2"><p class="empty">Nothing was posted in this window.</p></td></tr>{{end}}</tbody></table></div>
</section>
<p><a href="/app/admin/settings">Workspace settings</a> · <a href="/app/admin/audit">Audit</a></p>
</main>{{end}}`

var analyticsTemplate = mustPage(analyticsMarkup)

func (h Handler) workspaceSettingsPage(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthAdminProblem(w, r, problemSessionMissing)
		return
	}
	workspace, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The workspace settings could not be read."))
		return
	}
	data := workspaceSettingsData{
		CSRFToken:       auth.CSRFToken(sessionCookie.Value),
		Notice:          strings.TrimSpace(r.URL.Query().Get("notice")),
		Name:            workspace.Name,
		Description:     workspace.Description,
		IconURL:         workspace.IconURL,
		Discoverability: string(workspace.Discoverability),
		CanWrite:        principal.HasScope(auth.ScopeAdminUsersWrite),
	}
	if policy, policyErr := h.Messages.WorkspaceRetention(r.Context(), principal.WorkspaceID, principal.UserID); policyErr == nil {
		data.MessageRetentionDays = policy.MessageDays
		data.FileRetentionDays = policy.FileDays
		data.RetentionSummary = retentionSummary(policy)
	} else {
		// A policy that cannot be read must not render as "keep forever": that
		// would tell an administrator their retention is off when it may be
		// deleting content right now.
		data.RetentionSummary = "The retention policy could not be read, so what is shown above may not be what is being applied."
	}
	if swept, sweptErr := h.Messages.LastRetentionSweep(r.Context(), principal.WorkspaceID, principal.UserID); sweptErr == nil && !swept.IsZero() {
		data.RetentionSweptAt = formatTime(swept)
		data.RetentionSweptMachine = swept.UTC().Format(time.RFC3339Nano)
	}
	for _, option := range workspaceDiscoverabilityChoices {
		option.Selected = option.Value == workspace.Discoverability
		data.Options = append(data.Options, option)
	}
	defaults := make(map[domain.ConversationID]struct{}, len(workspace.DefaultChannelIDs))
	for _, channelID := range workspace.DefaultChannelIDs {
		defaults[channelID] = struct{}{}
	}
	if options, optionsErr := h.visibleChannelOptions(r.Context(), principal); optionsErr == nil {
		for _, option := range options {
			_, selected := defaults[domain.ConversationID(option.ID)]
			data.Channels = append(data.Channels, workspaceDefaultChannelOption{ID: option.ID, Name: option.Name, Selected: selected})
		}
	}
	h.writeHTMLWithPolicy(w, workspaceSettingsTemplate, data, http.StatusOK, "the workspace settings could not be rendered", authAdminContentSecurityPolicy)
}

// retentionSummary says in words what the two numbers mean together, because
// "90" and "0" in adjacent boxes do not tell an administrator that their files
// outlive their messages.
func retentionSummary(policy domain.RetentionPolicy) string {
	switch {
	case policy.MessageDays == 0 && policy.FileDays == 0:
		return "Nothing is deleted by policy. Messages and files are kept until someone removes them."
	case policy.MessageDays == 0:
		return "Messages are kept forever. Files are deleted permanently once they pass their limit."
	case policy.FileDays == 0:
		return "Messages are deleted permanently once they pass their limit. Files are kept forever."
	default:
		return "Messages and files are both deleted permanently once they pass their limits."
	}
}

func (h Handler) workspaceRetentionSet(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.workspaceSettingsMutation(w, r, "")
	if !ok {
		return
	}
	messageDays, messageErr := strconv.Atoi(strings.TrimSpace(fields["message_days"]))
	fileDays, fileErr := strconv.Atoi(strings.TrimSpace(fields["file_days"]))
	if messageErr != nil || fileErr != nil {
		h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_duration", Title: "Request rejected", Message: "A retention limit is a number of days, or 0 to keep everything."})
		return
	}
	if _, err := h.Messages.SetWorkspaceRetention(r.Context(), principal.WorkspaceID, principal.UserID, domain.RetentionPolicy{MessageDays: messageDays, FileDays: fileDays}); err != nil {
		if errors.Is(err, service.ErrInvalidRetentionDuration) {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_duration", Title: "Request rejected", Message: "A retention limit must be between 0 and 36499 days."})
			return
		}
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The retention policy was not changed."))
		return
	}
	h.redirectSettings(w, r, "Retention saved")
}

func (h Handler) workspaceIdentitySet(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.workspaceSettingsMutation(w, r, "")
	if !ok {
		return
	}
	// Each field is a separate durable mutation with its own validation, so a
	// rejected icon must not silently discard an accepted name: the first
	// refusal stops and reports, and what was already applied stays applied.
	if _, err := h.Messages.AdminSetWorkspaceName(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["name"])); err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The workspace name was not changed."))
		return
	}
	if _, err := h.Messages.AdminSetWorkspaceDescription(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["description"])); err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The name was saved; the description was not."))
		return
	}
	if _, err := h.Messages.AdminSetWorkspaceIcon(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["icon_url"])); err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The name and description were saved; the icon was not."))
		return
	}
	h.redirectSettings(w, r, "Workspace identity saved")
}

func (h Handler) workspaceDiscoverabilitySet(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.workspaceSettingsMutation(w, r, "")
	if !ok {
		return
	}
	value := domain.WorkspaceDiscoverability(strings.TrimSpace(fields["discoverability"]))
	if _, err := h.Messages.AdminSetWorkspaceDiscoverability(r.Context(), principal.WorkspaceID, principal.UserID, value); err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "Who can join was not changed."))
		return
	}
	h.redirectSettings(w, r, "Who can join saved")
}

func (h Handler) workspaceDefaultChannelsSet(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.workspaceSettingsMutation(w, r, "channel_ids")
	if !ok {
		return
	}
	channels := normalizeAdminInviteChannels(fields["channel_ids"])
	if _, err := h.Messages.AdminSetWorkspaceDefaultChannels(r.Context(), principal.WorkspaceID, principal.UserID, channels); err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The default channels were not changed."))
		return
	}
	h.redirectSettings(w, r, "Default channels saved")
}

func (h Handler) workspaceSettingsMutation(w http.ResponseWriter, r *http.Request, repeated string) (auth.Principal, map[string]string, bool) {
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersWrite)
	if !ok {
		return auth.Principal{}, nil, false
	}
	if !h.requireCSRF(w, r) {
		return auth.Principal{}, nil, false
	}
	fields, _, err := decodeFormValues(w, r, repeated)
	if err != nil {
		h.writeAuthAdminProblem(w, r, problemInvalidForm)
		return auth.Principal{}, nil, false
	}
	return principal, fields, true
}

func (h Handler) redirectSettings(w http.ResponseWriter, r *http.Request, notice string) {
	if wantsAuthAdminJSON(r) {
		writeAuthAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	authAdminSecurityHeaders(w)
	http.Redirect(w, r, "/app/admin/settings?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func workspaceSettingsProblem(err error, message string) authAdminProblem {
	switch {
	case errors.Is(err, service.ErrNotWorkspaceAdmin):
		return authAdminProblem{Status: http.StatusForbidden, Code: "not_authorized", Title: "Not authorized", Message: "Your workspace role does not change workspace settings. " + message}
	case errors.Is(err, service.ErrInvalidWorkspace), errors.Is(err, store.ErrInvalidArgument):
		return authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_workspace", Title: "Request rejected", Message: "That value is not one this workspace accepts. " + message}
	case errors.Is(err, store.ErrNotFound):
		return authAdminProblem{Status: http.StatusNotFound, Code: "not_found", Title: "Not found", Message: message}
	default:
		return authAdminProblem{Status: http.StatusServiceUnavailable, Code: "workspace_settings_unavailable", Title: "Temporarily unavailable", Message: message}
	}
}

// analyticsWindows are the windows the dashboard offers. They are a closed set
// so the page cannot be asked for a window the counts would take a table scan
// per day to produce.
var analyticsWindows = []int{7, 30, 90}

func (h Handler) analyticsPage(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	days := analyticsWindows[0]
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || !slicesContainsInt(analyticsWindows, value) {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_window", Title: "Request rejected", Message: "The window must be 7, 30 or 90 days."})
			return
		}
		days = value
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	analytics, err := h.Messages.WorkspaceAnalytics(r.Context(), principal.WorkspaceID, principal.UserID, since)
	if err != nil {
		h.writeAuthAdminProblem(w, r, workspaceSettingsProblem(err, "The analytics could not be read."))
		return
	}
	h.writeHTMLWithPolicy(w, analyticsTemplate, analyticsData{
		Since: formatTime(since), Days: days, DayOptions: analyticsWindows,
		Members: analytics.Members, ActiveMembers: analytics.ActiveMembers, Guests: analytics.Guests, Admins: analytics.Admins,
		PublicChannels: analytics.PublicChannels, PrivateChannels: analytics.PrivateChannels, ArchivedChannels: analytics.ArchivedChannels,
		Messages: analytics.Messages, RecentMessages: analytics.RecentMessages,
		Files: analytics.Files, RecentFiles: analytics.RecentFiles, Busiest: analytics.BusiestChannels,
	}, http.StatusOK, "the analytics could not be rendered", authAdminContentSecurityPolicy)
}

func slicesContainsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
