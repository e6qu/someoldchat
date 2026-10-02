package slack

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The assistant.threads.* methods. Argument names are taken from the pinned
// @slack/web-api type definitions rather than from memory: channel_id,
// thread_ts, and then title, status, or prompts with an optional title.
//
// All three write display state for a thread and none of them creates a
// message, so they carry chat:write — the caller must be able to post where the
// state will be shown — and they answer a bare {"ok": true} as the SDK's own
// response types expect.

// assistantLoadingMessageLimit is Slack's documented maximum for
// assistant.threads.setStatus loading_messages.
const assistantLoadingMessageLimit = 10

func (h Handler) setAssistantThreadTitle(w http.ResponseWriter, r *http.Request) {
	principal, fields, target, thread, ok := h.assistantTarget(w, r)
	if !ok {
		return
	}
	if err := h.Messages.SetAssistantThreadTitle(r.Context(), principal.WorkspaceID, principal.UserID, target, thread, fields["title"]); err != nil {
		writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setAssistantThreadStatus(w http.ResponseWriter, r *http.Request) {
	principal, fields, target, thread, ok := h.assistantTarget(w, r)
	if !ok {
		return
	}
	// loading_messages is the list a client rotates through instead of the
	// bare status, at most ten. It is validated so a malformed list is refused
	// as Slack refuses it, but not yet stored or shown: carrying it needs a
	// column, a wire field and a renderer, recorded as a deviation on
	// assistant.threads.setStatus in specs/compatibility.yaml.
	if raw, present := fields["loading_messages"]; present {
		var messages []string
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &messages); err != nil || len(messages) > assistantLoadingMessageLimit {
			writeError(w, "invalid_arguments")
			return
		}
	}
	if err := h.Messages.SetAssistantThreadStatus(r.Context(), principal.WorkspaceID, principal.UserID, target, thread, fields["status"]); err != nil {
		writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setAssistantThreadSuggestedPrompts(w http.ResponseWriter, r *http.Request) {
	principal, fields, target, thread, ok := h.assistantTarget(w, r)
	if !ok {
		return
	}
	var decoded []struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(fields["prompts"])), &decoded); err != nil {
		writeError(w, "invalid_arguments")
		return
	}
	prompts := make([]domain.AssistantPrompt, 0, len(decoded))
	for _, prompt := range decoded {
		prompts = append(prompts, domain.AssistantPrompt{Title: prompt.Title, Message: prompt.Message})
	}
	if err := h.Messages.SetAssistantThreadSuggestedPrompts(r.Context(), principal.WorkspaceID, principal.UserID, target, thread, fields["title"], prompts); err != nil {
		writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// assistantTarget resolves the three arguments every assistant write shares.
func (h Handler) assistantTarget(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, domain.ConversationID, domain.MessageTimestamp, bool) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, "", "", false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return auth.Principal{}, nil, "", "", false
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	thread := domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"]))
	if channel == "" || thread == "" {
		writeError(w, "invalid_arguments")
		return auth.Principal{}, nil, "", "", false
	}
	return principal, fields, channel, thread, true
}

func writeAssistantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidAssistantThread):
		writeError(w, "invalid_arguments")
	case errors.Is(err, domain.ErrInvalidTimestamp):
		writeError(w, "invalid_arguments")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}
