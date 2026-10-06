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
// response types expect. setStatus also takes the icon_emoji, icon_url and
// username python-slack-sdk 3.45 and slack-bolt 1.30 send, under the contract
// agents.sessions.setStatus documents for the same three: chat:write.customize
// on top of chat:write, and a JSON null the same as leaving one out.

func (h Handler) setAssistantThreadTitle(w http.ResponseWriter, r *http.Request) {
	principal, fields, target, thread, ok := h.assistantTarget(w, r, normalizeJSONField)
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
	principal, fields, target, thread, ok := h.assistantTarget(w, r, identityOverrideJSONMember)
	if !ok {
		return
	}
	// loading_messages is the list a client rotates through instead of the
	// bare status, at most ten.
	var loadingMessages []string
	if raw, present := fields["loading_messages"]; present && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &loadingMessages); err != nil || len(loadingMessages) > domain.AssistantLoadingMessageLimit {
			writeError(w, "invalid_arguments")
			return
		}
	}
	identity, ok := identityOverride(w, principal, fields)
	if !ok {
		return
	}
	if err := h.Messages.SetAssistantThreadStatus(r.Context(), principal.WorkspaceID, principal.UserID, target, thread, fields["status"], loadingMessages, identity); err != nil {
		writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setAssistantThreadSuggestedPrompts(w http.ResponseWriter, r *http.Request) {
	principal, fields, target, thread, ok := h.assistantTarget(w, r, normalizeJSONField)
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
// jsonMember decodes a JSON body's members: setStatus's accepts a null
// identity override, the other two refuse a null as every method does.
func (h Handler) assistantTarget(w http.ResponseWriter, r *http.Request, jsonMember func(string, json.RawMessage) (string, error)) (auth.Principal, map[string]string, domain.ConversationID, domain.MessageTimestamp, bool) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, "", "", false
	}
	fields, err := decodeArguments(w, r, jsonMember)
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
