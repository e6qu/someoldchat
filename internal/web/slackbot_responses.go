package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The workspace's Slackbot custom responses, as Slack's "Customize workspace ›
// Slackbot" page keeps them: when someone says one of a response's phrases,
// Slackbot answers with one of its replies. Any full member may add or remove
// them, Slack's default; a guest may only read them.

type slackbotResponsesPage struct {
	Channel   string
	CSRFToken string
	Responses []domain.SlackbotResponse
	Notice    string
}

func (h Handler) slackbotResponsesPage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
	if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	responses, err := h.Messages.SlackbotResponses(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Slackbot responses are temporarily unavailable.")
		return
	}
	h.writeHTML(w, slackbotResponsesTemplate, slackbotResponsesPage{
		Channel:   strings.TrimSpace(r.URL.Query().Get("channel")),
		CSRFToken: auth.CSRFToken(sessionCookie.Value),
		Responses: responses,
		Notice:    boundedNotice(r.URL.Query().Get("notice")),
	}, http.StatusOK, "Slackbot responses rendering unavailable")
}

func (h Handler) addSlackbotResponse(w http.ResponseWriter, r *http.Request) {
	h.mutateSlackbotResponse(w, r, true)
}

func (h Handler) removeSlackbotResponse(w http.ResponseWriter, r *http.Request) {
	h.mutateSlackbotResponse(w, r, false)
}

func (h Handler) mutateSlackbotResponse(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The response could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	notice := "Removed the response."
	if add {
		// Phrases are separated by commas, as on Slack; replies by lines.
		_, err = h.Messages.AddSlackbotResponse(r.Context(), principal.WorkspaceID, principal.UserID,
			[]string{fields["triggers"]}, strings.Split(strings.ReplaceAll(fields["replies"], "\r\n", "\n"), "\n"))
		notice = "Added the response."
	} else {
		err = h.Messages.DeleteSlackbotResponse(r.Context(), principal.WorkspaceID, principal.UserID, domain.SlackbotResponseID(strings.TrimSpace(fields["id"])))
	}
	if err != nil {
		status, heading, reason := http.StatusServiceUnavailable, "The response was not saved", "The workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, domain.ErrUserIsRestricted), errors.Is(err, domain.ErrUserIsUltraRestricted):
			status, reason = http.StatusForbidden, "Guests cannot change Slackbot's responses."
		case errors.Is(err, domain.ErrInvalidSlackbotResponse):
			status, reason = http.StatusBadRequest, "Give at least one phrase, separated by commas, and at least one reply, one per line. Phrases are at most 200 characters and replies 4,000; a response has at most 50 of each."
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That response no longer exists."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	query := url.Values{"notice": {notice}}
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		query.Set("channel", channel)
	}
	h.redirectMutation(w, r, "/app/customize/slackbot?"+query.Encode())
}

var slackbotResponsesTemplate = mustPage(slackbotResponsesMarkup)

const slackbotResponsesMarkup = `{{define "title"}}Slackbot responses · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(760px,calc(100% - 32px));margin:28px auto 48px;display:grid;gap:20px}
.heading h2,.heading p{margin:0}.heading p{color:var(--muted);margin-top:4px}
.customize-tabs{display:flex;gap:6px}.customize-tabs a{padding:6px 12px;border-radius:999px;border:1px solid var(--line);color:var(--text);text-decoration:none;font-weight:700}.customize-tabs a[aria-current="page"]{background:var(--accent);border-color:var(--accent);color:var(--on-accent)}
.response-form{display:grid;gap:10px;padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}
.response-form h3{margin:0;font-size:16px}
.response-form label{display:grid;gap:4px;font-weight:700;font-size:14px}.response-form small{font-weight:400;color:var(--muted)}
.response-form textarea{border:1px solid var(--field-line);border-radius:6px;padding:7px 10px;background:var(--panel-strong);color:var(--text);font:inherit;min-height:64px}
.response-form button,.response-table button{justify-self:start;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:7px 12px;font-weight:700}
.response-form button{border-color:var(--ok);background:var(--ok);color:#fff}
.response-table{width:100%;border-collapse:collapse}.response-table th,.response-table td{padding:8px 10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}
.response-table ul{margin:0;padding-left:18px}
.empty{padding:24px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="/app{{if .Channel}}?channel={{.Channel}}{{end}}">← Back to chat</a><h1>Customize your workspace</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<nav class="customize-tabs" aria-label="Customize"><a href="/app/customize/emoji{{if .Channel}}?channel={{.Channel}}{{end}}">Emoji</a><a href="/app/customize/slackbot{{if .Channel}}?channel={{.Channel}}{{end}}" aria-current="page">Slackbot</a></nav>
<div class="heading"><h2>Slackbot responses</h2><p>When someone says one of these phrases in a channel or a direct message, Slackbot answers with one of the replies.</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<form class="response-form" method="post" action="/app/customize/slackbot/add{{if .Channel}}?channel={{.Channel}}{{end}}">
  <h3>Add a response</h3>
  <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
  <label for="slackbot-triggers">When someone says <small>Separate phrases with commas.</small></label><textarea id="slackbot-triggers" name="triggers" required maxlength="10000" placeholder="lunch, what's for lunch"></textarea>
  <label for="slackbot-replies">Slackbot responds <small>One reply per line; Slackbot picks one.</small></label><textarea id="slackbot-replies" name="replies" required maxlength="200000" placeholder="Tacos in the kitchen at noon!"></textarea>
  <button type="submit">Save response</button>
</form>
{{if .Responses}}<table class="response-table"><caption class="visually-hidden">Slackbot responses</caption><thead><tr><th scope="col">When someone says</th><th scope="col">Slackbot responds</th><th scope="col"><span class="visually-hidden">Actions</span></th></tr></thead><tbody>{{range .Responses}}<tr><td>{{range $index, $phrase := .Triggers}}{{if $index}}, {{end}}{{$phrase}}{{end}}</td><td><ul>{{range .Replies}}<li>{{.}}</li>{{end}}</ul></td><td><form method="post" action="/app/customize/slackbot/remove{{if $.Channel}}?channel={{$.Channel}}{{end}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="id" value="{{.ID}}"><button type="submit" aria-label="Remove the response to {{index .Triggers 0}}">Remove</button></form></td></tr>{{end}}</tbody></table>
{{else}}<p class="empty">This workspace has no Slackbot responses yet.</p>{{end}}
</main>{{end}}`
