package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// ADMIN-03 asks for entries that identify actor, action, target, time and
// result while redacting secrets, and for the export and the API to agree with
// what the page shows. Both halves of that already existed with no way to look
// at either:
//
//   - the durable event journal is the record of what was done. It is the
//     actor, the topic and the identifiers, committed in the same transaction
//     as the change it describes, so an entry existing IS the result: a
//     refused mutation writes nothing. Its payloads deliberately carry
//     identifiers and never content, and the delivery snapshot each event also
//     carries — the one an app is authorized against — is never rendered here.
//   - the access log is who signed in, from where and when.
//
// The journal is read through the same visibility-filtered path the event
// stream uses, so this page cannot show an administrator the existence of a
// private conversation they are not in. The page says so rather than implying
// it is showing everything.
//
// One handler answers HTML and JSON from one query, so the export cannot drift
// from the page: ADMIN-03 requires them to agree, and two code paths are how
// they stop agreeing.

type auditEntryView struct {
	Sequence    uint64 `json:"sequence"`
	Time        string `json:"-"`
	MachineTime string `json:"time"`
	Actor       string `json:"actor"`
	ActorID     string `json:"actor_id"`
	Action      string `json:"action"`
	Target      string `json:"target"`
}

type auditAccessView struct {
	User        string `json:"user"`
	UserID      string `json:"user_id"`
	Time        string `json:"-"`
	MachineTime string `json:"time"`
	IP          string `json:"ip"`
	UserAgent   string `json:"user_agent"`
}

type auditPageData struct {
	Entries    []auditEntryView  `json:"entries"`
	Access     []auditAccessView `json:"access"`
	MoreURL    string            `json:"-"`
	NextAfter  uint64            `json:"next_after"`
	AccessPage int               `json:"access_page"`
	MoreAccess string            `json:"-"`
	Limit      int               `json:"limit"`
}

const auditMarkup = `{{define "title"}}Audit · SameOldChat{{end}}
{{define "styles"}}` + authAdminStyle + `{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}` + authAdminBar + `<main class="wrap" id="admin-main">
<div class="heading"><h1>Audit</h1><p>What was done in this workspace, and who signed in. Entries are the durable record written with the change they describe, so an entry is a change that happened — a refused one records nothing.</p></div>
<section class="card" aria-labelledby="activity-heading">
<div class="section-head"><h2 id="activity-heading">Activity</h2><p>Actor, action, target and time. Payloads carry identifiers, never message content, and never the delivery snapshots apps are authorized against. Conversations you cannot see are not listed.</p></div>
<div class="table-scroll"><table><thead><tr><th scope="col">Time</th><th scope="col">Actor</th><th scope="col">Action</th><th scope="col">Target</th></tr></thead><tbody>{{range .Entries}}<tr><td><time datetime="{{.MachineTime}}" data-local-time>{{.Time}}</time></td><td>{{if .Actor}}{{.Actor}}{{else}}<span class="read-only">the workspace</span>{{end}}</td><td>{{.Action}}</td><td>{{if .Target}}{{.Target}}{{else}}<span class="read-only">—</span>{{end}}</td></tr>{{else}}<tr><td colspan="4"><p class="empty">Nothing has been recorded yet.</p></td></tr>{{end}}</tbody></table></div>
{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Older activity</a></p>{{end}}
</section>
<section class="card" aria-labelledby="access-heading">
<div class="section-head"><h2 id="access-heading">Access</h2><p>Each sign-in from a browser and each authenticated API call, with the address and client that made it.</p></div>
<div class="table-scroll"><table><thead><tr><th scope="col">Time</th><th scope="col">User</th><th scope="col">Address</th><th scope="col">Client</th></tr></thead><tbody>{{range .Access}}<tr><td><time datetime="{{.MachineTime}}" data-local-time>{{.Time}}</time></td><td>{{.User}}</td><td>{{.IP}}</td><td>{{.UserAgent}}</td></tr>{{else}}<tr><td colspan="4"><p class="empty">Nobody has signed in yet.</p></td></tr>{{end}}</tbody></table></div>
{{if .MoreAccess}}<p class="pager"><a href="{{.MoreAccess}}">Older access</a></p>{{end}}
</section>
<p><a href="/app/admin/auth">Back to workspace administration</a></p>
</main>{{end}}`

var auditTemplate = mustPage(auditMarkup)

const (
	auditDefaultLimit = 50
	auditMaxLimit     = 200
)

func (h Handler) auditPage(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authAdminAllowed(w, r, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite)
	if !ok {
		return
	}
	limit := auditDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > auditMaxLimit {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_pagination", Title: "Request rejected", Message: "limit must be between 1 and 200."})
			return
		}
		limit = value
	}
	after := uint64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_pagination", Title: "Request rejected", Message: "after must be an event sequence."})
			return
		}
		after = value
	}
	accessPage := 1
	if raw := strings.TrimSpace(r.URL.Query().Get("access_page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			h.writeAuthAdminProblem(w, r, authAdminProblem{Status: http.StatusBadRequest, Code: "invalid_pagination", Title: "Request rejected", Message: "access_page must be a positive page number."})
			return
		}
		accessPage = value
	}

	records, err := h.Messages.ListUserEventsAfter(r.Context(), principal.WorkspaceID, principal.UserID, after, limit)
	if err != nil {
		h.writeAuthAdminProblem(w, r, auditReadProblem(err, "The activity record could not be read."))
		return
	}
	logs, hasMoreAccess, err := h.Messages.ListAccessLogs(r.Context(), principal.WorkspaceID, principal.UserID, time.Time{}, limit, accessPage)
	if err != nil {
		h.writeAuthAdminProblem(w, r, auditReadProblem(err, "The access record could not be read."))
		return
	}

	names := h.newUserNames(r.Context(), principal)
	data := auditPageData{Limit: limit, AccessPage: accessPage, NextAfter: after}
	for _, record := range records {
		entry := auditEntryView{
			Sequence:    record.Sequence,
			Time:        formatTime(record.Event.CreatedAt),
			MachineTime: record.Event.CreatedAt.UTC().Format(time.RFC3339Nano),
			ActorID:     string(record.Event.ActorID),
			Action:      record.Event.Topic,
			Target:      auditTarget(record.Event),
		}
		if record.Event.ActorID != "" {
			entry.Actor = names.name(record.Event.ActorID)
		}
		data.Entries = append(data.Entries, entry)
		if record.Sequence > data.NextAfter {
			data.NextAfter = record.Sequence
		}
	}
	for _, entry := range logs {
		data.Access = append(data.Access, auditAccessView{
			User:        entry.Username,
			UserID:      string(entry.UserID),
			Time:        formatTime(entry.CreatedAt),
			MachineTime: entry.CreatedAt.UTC().Format(time.RFC3339Nano),
			IP:          entry.IP,
			UserAgent:   entry.UserAgent,
		})
	}
	if len(data.Entries) == limit {
		data.MoreURL = auditURL(limit, data.NextAfter, accessPage)
	}
	if hasMoreAccess {
		data.MoreAccess = auditURL(limit, after, accessPage+1)
	}

	if wantsAuthAdminJSON(r) {
		authAdminSecurityHeaders(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "audit": data})
		return
	}
	h.writeHTMLWithPolicy(w, auditTemplate, data, http.StatusOK, "the audit page could not be rendered", authAdminContentSecurityPolicy)
}

func auditURL(limit int, after uint64, accessPage int) string {
	query := url.Values{
		"limit":       {strconv.Itoa(limit)},
		"after":       {strconv.FormatUint(after, 10)},
		"access_page": {strconv.Itoa(accessPage)},
	}
	return "/app/admin/audit?" + query.Encode()
}

func auditReadProblem(err error, message string) authAdminProblem {
	if errors.Is(err, service.ErrNotWorkspaceAdmin) {
		return authAdminProblem{Status: http.StatusForbidden, Code: "not_authorized", Title: "Not authorized", Message: "Your workspace role does not read the audit record."}
	}
	return authAdminProblem{Status: http.StatusServiceUnavailable, Code: "audit_unavailable", Title: "Temporarily unavailable", Message: message}
}

// auditTarget names what an entry is about, from the identifiers the payload
// already carries. It reads only the public payload: the private one is the
// per-app delivery snapshot and can contain message text, which this page must
// never render.
func auditTarget(event events.Event) string {
	delivered, err := events.Deliverable(event)
	if err != nil {
		return ""
	}
	for _, key := range []string{
		"channel_id", "conversation_id", "channel", "message_id", "file_id",
		"user_id", "app_id", "invite_request_id", "workflow_id", "canvas_id", "list_id",
	} {
		text, ok := delivered.Field(key)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		return key + " " + text
	}
	return ""
}
