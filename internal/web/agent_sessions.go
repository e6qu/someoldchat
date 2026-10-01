package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/slackemoji"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The agent session panel in the thread pane: what an app working in the
// thread is doing, the stop control Slack shows while it is processing, and a
// way for a member to retitle the session. Stopping sends the processing
// agents agent_session_stopped and retitling sends every agent
// agent_session_title_changed; neither changes an agent's status, which is
// the app's to move.
//
// The panel is its own live region: the session's records are in
// liveEventTopics, so an app's status change re-renders it without a reload.

type agentSessionView struct {
	// FragmentURL is the region's own address; it is set for every thread so
	// a session an app starts while the thread is open appears in place.
	FragmentURL string
	Present     bool
	Title       string
	Status      domain.AgentSessionStatus
	StatusText  string
	Agents      []agentSessionAgentView
	// Stoppable shows the stop control: an agent is processing and its app
	// subscribes to agent_session_stopped. A processing session without it
	// shows the loading state alone, as Slack does.
	Stoppable bool
	// CanAct offers the controls to a member of the conversation; the
	// service refuses anyone else.
	CanAct    bool
	StopURL   string
	TitleURL  string
	CSRFToken string
}

type agentSessionAgentView struct {
	Name       string
	IconEmoji  string
	Status     domain.AgentSessionStatus
	StatusText string
}

const agentSessionPartial = `{{define "agent-session"}}{{if .Present}}<section class="agent-session" aria-label="Agent session" data-agent-session-status="{{.Status}}">
  <div class="agent-session-head">{{if .Title}}<p class="agent-session-title">{{.Title}}</p>{{else}}<p class="agent-session-title untitled">Untitled session</p>{{end}}
    <p class="agent-session-status" role="status">{{if eq .Status "processing"}}<span class="agent-session-spinner" aria-hidden="true"></span>{{end}}{{.StatusText}}</p></div>
  {{if .Agents}}<ul class="agent-session-agents">{{range .Agents}}<li>{{if .IconEmoji}}<span aria-hidden="true">{{.IconEmoji}}</span> {{end}}<span class="agent-session-agent">{{.Name}}</span> · {{.StatusText}}</li>{{end}}</ul>{{end}}
  {{if .CanAct}}<div class="agent-session-actions">
    {{if .Stoppable}}<form method="post" action="{{.StopURL}}" hx-post="{{.StopURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="agent-session-stop" type="submit">Stop</button></form>{{end}}
    <form class="agent-session-rename" method="post" action="{{.TitleURL}}" hx-post="{{.TitleURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <label for="agent-session-title-input">Session title</label>
      <input id="agent-session-title-input" name="title" type="text" required maxlength="200" value="{{.Title}}" autocomplete="off">
      <button type="submit">Rename</button></form>
  </div>{{end}}
</section>{{end}}{{end}}`

const agentSessionStyle = `
.agent-session{display:grid;gap:8px;padding:12px 14px;border-bottom:1px solid var(--line);background:var(--panel)}
.agent-session-head{display:grid;gap:2px}.agent-session-title{margin:0;font-weight:800}.agent-session-title.untitled{color:var(--muted);font-weight:600}
.agent-session-status{margin:0;color:var(--muted);font-size:13px;display:flex;align-items:center;gap:6px}
.agent-session-spinner{width:10px;height:10px;border:2px solid var(--line);border-top-color:var(--text);border-radius:50%;animation:agent-session-spin 1s linear infinite}
@keyframes agent-session-spin{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion:reduce){.agent-session-spinner{animation:none}}
.agent-session-agents{margin:0;padding:0;list-style:none;font-size:13px;color:var(--muted)}.agent-session-agent{color:var(--text);font-weight:600}
.agent-session-actions{display:flex;flex-wrap:wrap;gap:8px;align-items:end}.agent-session-actions form{margin:0}
.agent-session-rename{display:flex;flex-wrap:wrap;gap:6px;align-items:center}.agent-session-rename label{font-size:12px;font-weight:700;color:var(--muted)}
.agent-session-rename input{min-height:30px;padding:4px 8px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text)}
.agent-session-actions button{min-height:30px;padding:4px 10px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);font-weight:600}
.agent-session-actions button:hover{background:var(--hover)}.agent-session-stop{border-color:var(--danger,#c0392b)!important}
`

func agentSessionStatusText(status domain.AgentSessionStatus) string {
	switch status {
	case domain.AgentSessionProcessing:
		return "Working…"
	case domain.AgentSessionSuspended:
		return "Waiting for you"
	case domain.AgentSessionActive:
		return "Ready"
	case domain.AgentSessionClosed:
		return "Closed"
	}
	return string(status)
}

func agentSessionAddress(path string, channel domain.ConversationID, thread domain.MessageTimestamp) string {
	return path + "?" + url.Values{"channel": {string(channel)}, "thread": {string(thread)}}.Encode()
}

// agentSessionView reads the open thread's session. A thread no app has a
// session on is the common case and renders an empty region; a session that
// cannot be read renders empty too, so the thread itself still opens.
func (h Handler) agentSessionView(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, csrfToken string, member bool) agentSessionView {
	view := agentSessionView{FragmentURL: agentSessionAddress("/app/agent-session", channel, thread)}
	if thread == "" {
		return view
	}
	value, err := h.Messages.AgentSession(ctx, principal.WorkspaceID, principal.UserID, channel, thread)
	if err != nil {
		return view
	}
	names := map[domain.AppID]string{}
	if apps, err := h.Messages.ListWorkspaceApps(ctx, principal.WorkspaceID, principal.UserID); err == nil {
		for _, app := range apps {
			names[app.ID] = app.Name
		}
	}
	session := value.Session
	view.Present = true
	view.Title = session.Title
	view.Status = session.Status()
	view.StatusText = agentSessionStatusText(view.Status)
	view.Stoppable = value.Stoppable
	view.CanAct = member
	view.StopURL = agentSessionAddress("/app/agent-session/stop", channel, thread)
	view.TitleURL = agentSessionAddress("/app/agent-session/title", channel, thread)
	view.CSRFToken = csrfToken
	for _, agent := range session.Agents {
		name := agent.Identity.Username
		if name == "" {
			name = names[agent.AppID]
		}
		if name == "" {
			name = string(agent.AppID)
		}
		// icon_emoji is a shortcode; only one the catalog can draw is shown.
		icon, _ := slackemoji.ReactionUnicode(agent.Identity.IconEmoji)
		view.Agents = append(view.Agents, agentSessionAgentView{
			Name: name, IconEmoji: icon,
			Status: agent.Status, StatusText: agentSessionStatusText(agent.Status),
		})
	}
	return view
}

func (h Handler) agentSessionFragment(w http.ResponseWriter, r *http.Request) {
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
	channel := h.requestChannel(r)
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread")))
	member, err := h.Messages.IsConversationMember(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeFragmentError(w, err, "your conversation membership is temporarily unavailable")
		return
	}
	view := h.agentSessionView(r.Context(), principal, channel, thread, auth.CSRFToken(sessionCookie.Value), member)
	h.writePartial(w, "agent-session", view, "the agent session could not be rendered")
}

func (h Handler) stopAgentSession(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The stop could not be read from the form. Reload the thread and try again."); !ok {
		return
	}
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread")))
	if _, err := h.Messages.StopAgentSession(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), thread); err != nil {
		h.writeAgentSessionMutationError(w, r, err, "The agent was not stopped")
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) retitleAgentSession(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The title could not be read from the form. Reload the thread and try again.")
	if !ok {
		return
	}
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread")))
	if _, err := h.Messages.ChangeAgentSessionTitle(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), thread, fields["title"]); err != nil {
		h.writeAgentSessionMutationError(w, r, err, "The session was not renamed")
		return
	}
	h.completeMutation(w, r)
}

// writeAgentSessionMutationError names what went wrong in the member's
// terms. Each refusal the service can give is a handled outcome with its own
// status; only an unclassified failure is reported as the store being
// unavailable.
func (h Handler) writeAgentSessionMutationError(w http.ResponseWriter, r *http.Request, err error, heading string) {
	switch {
	case errors.Is(err, domain.ErrInvalidAgentSession):
		h.writeMutationError(w, r, http.StatusBadRequest, heading, "A session title needs 1 to 200 characters.")
	case errors.Is(err, domain.ErrAgentSessionThreadRequired):
		h.writeMutationError(w, r, http.StatusBadRequest, heading, "That link does not name a thread.")
	case errors.Is(err, domain.ErrAgentSessionNotStoppable):
		h.writeMutationError(w, r, http.StatusConflict, heading, "No agent in this thread is working on something it can stop.")
	case errors.Is(err, domain.ErrNotInConversation):
		h.writeMutationError(w, r, http.StatusForbidden, heading, "Only members of this conversation can do that.")
	case errors.Is(err, domain.ErrAgentSessionNotFound), errors.Is(err, store.ErrNotFound):
		h.writeMutationError(w, r, http.StatusNotFound, heading, "That agent session is no longer available.")
	case errors.Is(err, store.ErrConflict):
		h.writeMutationError(w, r, http.StatusConflict, heading, "The session changed while you were acting on it. Try again.")
	default:
		h.writeMutationError(w, r, http.StatusServiceUnavailable, heading, "The workspace store is temporarily unavailable.")
	}
}
