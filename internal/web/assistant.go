package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The assistant state in the thread pane: what an assistant app has set on the
// open thread through assistant.threads.* — a title, a transient status shown
// under the name it was set with, the loading messages that rotate beneath it,
// and prompts a member can send with one click.
//
// The state is its own live region: events.AssistantThreadUpdatedTopic is in
// liveEventTopics, so a status the app sets or clears while the thread is open
// appears or goes without a reload. A status that only showed on the next
// navigation said "is thinking..." long after the assistant had answered.

// assistantThreadView is what a member sees of an assistant's own state.
// Present distinguishes "no assistant has touched this thread" from "an
// assistant set everything to empty", which render differently.
type assistantThreadView struct {
	// FragmentURL is the region's own address; it is set for every thread so
	// state an app sets while the thread is open appears in place.
	FragmentURL string
	Present     bool
	Title       string
	Status      string
	// StatusBy is who the status is shown as: the identity override it was
	// set with, or the name of the app user that set it.
	StatusBy     agentIdentityView
	PromptsTitle string
	Prompts      []assistantPromptView
	// LoadingMessages rotate beneath the status while it shows: the first is
	// rendered, and LoadingMessagesJSON carries them all to the page script.
	LoadingMessages     []string
	LoadingMessagesJSON string
	// A prompt is sent as a reply in the thread through the thread composer.
	ComposeURL      string
	ThreadTimestamp string
	CSRFToken       string
}

type assistantPromptView struct {
	Title   string
	Message string
}

const assistantThreadPartial = `{{define "assistant-thread"}}{{if .Present}}<div class="assistant-state">
  {{if .Title}}<p class="assistant-title">{{.Title}}</p>{{end}}
  {{if .Status}}<p class="assistant-status" role="status">{{if .StatusBy.Name}}{{template "agent-identity-icon" .StatusBy}}<span class="assistant-status-name">{{.StatusBy.Name}}</span> {{end}}{{.Status}}</p>{{end}}
  {{if .LoadingMessages}}<p class="assistant-loading" aria-hidden="true" data-assistant-loading="{{.LoadingMessagesJSON}}">{{index .LoadingMessages 0}}</p>{{end}}
  {{if .Prompts}}<div class="assistant-prompts">
    {{if .PromptsTitle}}<p class="assistant-prompts-title">{{.PromptsTitle}}</p>{{end}}
    {{range .Prompts}}<form method="post" action="{{$.ComposeURL}}" hx-post="{{$.ComposeURL}}">
      <input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="thread_ts" value="{{$.ThreadTimestamp}}"><input type="hidden" name="text" value="{{.Message}}">
      <button class="assistant-prompt" type="submit" title="{{.Message}}">{{.Title}}</button>
    </form>{{end}}
  </div>{{end}}
</div>{{end}}{{end}}`

// assistantThreadView reads what an assistant app has set on the open thread.
// A thread nothing has touched is the overwhelmingly common case and answers an
// empty region rather than an error, so a missing row is not a failed page.
func (h Handler) assistantThreadView(ctx context.Context, principal auth.Principal, conversation domain.ConversationID, thread domain.MessageTimestamp, csrfToken string) assistantThreadView {
	view := assistantThreadView{FragmentURL: threadRegionAddress("/app/assistant-thread", conversation, thread)}
	if thread == "" {
		return view
	}
	value, err := h.Messages.AssistantThread(ctx, principal.WorkspaceID, principal.UserID, conversation, thread)
	if err != nil {
		return view
	}
	view.Present, view.Title, view.Status, view.PromptsTitle = true, value.Title, value.Status, value.PromptsTitle
	view.ComposeURL = mutationURL("/app/message", string(conversation), "", string(thread), "")
	view.ThreadTimestamp, view.CSRFToken = string(thread), csrfToken
	if value.Status != "" {
		setter := ""
		if value.StatusUserID != "" && value.StatusIdentity.Username == "" {
			setter = h.newUserNames(ctx, principal).name(value.StatusUserID)
		}
		view.StatusBy = newAgentIdentityView(value.StatusIdentity, setter)
	}
	if value.Status != "" && len(value.LoadingMessages) > 0 {
		if encoded, encodeErr := json.Marshal(value.LoadingMessages); encodeErr == nil {
			view.LoadingMessages, view.LoadingMessagesJSON = value.LoadingMessages, string(encoded)
		}
	}
	for _, prompt := range value.Prompts {
		view.Prompts = append(view.Prompts, assistantPromptView{Title: prompt.Title, Message: prompt.Message})
	}
	return view
}

// assistantThreadFragment re-renders the region for the live stream.
func (h Handler) assistantThreadFragment(w http.ResponseWriter, r *http.Request) {
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
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread")))
	view := h.assistantThreadView(r.Context(), principal, h.requestChannel(r), thread, auth.CSRFToken(sessionCookie.Value))
	h.writePartial(w, "assistant-thread", view, "the assistant state could not be rendered")
}
