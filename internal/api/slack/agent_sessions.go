package slack

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// agents.sessions.setStatus and agents.sessions.rename, as Slack's current
// reference pages define them: a granular bot token with chat:write, a session
// named by channel_id and thread_ts, and the error codes each page lists.
//
// A session is a thread's, named with thread_ts, or a session channel's — a
// code channel's (agents.conversations.create) — named without it. The
// service decides which: thread_ts_required for a thread session named
// without one, thread_ts_not_allowed for a session channel named with one.
// channel_id is required — the pages make it optional outside public
// channels without saying how the session is then found.

func (h Handler) setAgentSessionStatus(w http.ResponseWriter, r *http.Request) {
	principal, fields, channel, thread, ok := h.agentSessionRequest(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(fields["status"])
	if status == "" {
		writeError(w, "invalid_arguments")
		return
	}
	identity, ok := identityOverride(w, principal, fields)
	if !ok {
		return
	}
	result, err := h.Messages.SetAgentSessionStatus(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, channel, thread, domain.AgentSessionStatusRequest{
		Status:          domain.AgentSessionStatus(status),
		Title:           fields["title"],
		InitiatorUserID: domain.UserID(strings.TrimSpace(fields["initiator_user_id"])),
		Identity:        identity,
	})
	if err != nil {
		writeSetAgentSessionStatusError(w, err)
		return
	}
	// status is the session-level status derived from every agent; the
	// caller's own write is agent_status.
	response := map[string]any{"ok": true, "status": result.Session.Status(), "agent_status": result.AgentStatus}
	if result.Session.Title != "" {
		response["title"] = result.Session.Title
	}
	if len(result.Warnings) != 0 {
		response["warning"] = strings.Join(result.Warnings, ",")
		response["response_metadata"] = map[string]any{"warnings": result.Warnings}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) renameAgentSession(w http.ResponseWriter, r *http.Request) {
	principal, fields, channel, thread, ok := h.agentSessionRequest(w, r)
	if !ok {
		return
	}
	title := strings.TrimSpace(fields["title"])
	if !domain.ValidAgentSessionTitle(title) {
		writeError(w, "invalid_arguments")
		return
	}
	session, err := h.Messages.RenameAgentSession(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, channel, thread, title)
	if err != nil {
		writeRenameAgentSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "title": session.Title})
}

// agentSessionRequest authenticates a granular bot token holding chat:write
// and reads the session key both methods share.
func (h Handler) agentSessionRequest(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, domain.ConversationID, domain.MessageTimestamp, bool) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, "", "", false
	}
	if !principal.TokenType.IsBot() || principal.AppID == "" {
		writeError(w, "not_allowed_token_type")
		return auth.Principal{}, nil, "", "", false
	}
	fields, err := decodeArguments(w, r, identityOverrideJSONMember)
	if err != nil {
		writeDecodeError(w, err)
		return auth.Principal{}, nil, "", "", false
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	thread := domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"]))
	if channel == "" {
		writeError(w, "invalid_arguments")
		return auth.Principal{}, nil, "", "", false
	}
	return principal, fields, channel, thread, true
}

// identityOverride reads the icon_emoji, icon_url and username a setStatus
// call — agents.sessions' or assistant.threads' — shows its status with. The
// override needs chat:write.customize on top of the method's own scope, as it
// does on chat.postMessage; a call without it is answered missing_scope.
func identityOverride(w http.ResponseWriter, principal auth.Principal, fields map[string]string) (domain.AgentIdentity, bool) {
	identity := domain.AgentIdentity{
		Username:  strings.TrimSpace(fields["username"]),
		IconEmoji: strings.TrimSpace(fields["icon_emoji"]),
		IconURL:   strings.TrimSpace(fields["icon_url"]),
	}
	if !identity.Empty() && !principal.HasScope(auth.ScopeChatWriteCustomize) {
		writeAuthError(w, missingScopeError{needed: []auth.Scope{auth.ScopeChatWriteCustomize}, provided: permissionScopes(principal)})
		return domain.AgentIdentity{}, false
	}
	return identity, true
}

// identityOverrideJSONMember accepts the JSON null the agents.sessions.setStatus
// reference documents for clearing icon_emoji, icon_url and username, and
// assistant.threads.setStatus takes the same three the same way. A null there
// is the same as leaving the argument out — the page treats both alike — where
// every other argument still refuses a null as no value at all.
func identityOverrideJSONMember(name string, value json.RawMessage) (string, error) {
	switch name {
	case "icon_emoji", "icon_url", "username":
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return "", nil
		}
	}
	return normalizeJSONField(name, value)
}

func writeSetAgentSessionStatusError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotInConversation):
		// The setStatus page's own words: "The caller is not a member of the
		// specified channel."
		writeError(w, "not_authorized")
	case errors.Is(err, domain.ErrInvalidAgentSessionStatus):
		writeError(w, "invalid_status")
	case errors.Is(err, domain.ErrUserNotFound):
		writeError(w, "user_not_found")
	default:
		writeAgentSessionError(w, err)
	}
}

func writeRenameAgentSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotInConversation):
		// The rename page keeps not_authorized for an app that is not an
		// agent of the session and names no_permission for an app that is
		// not in the conversation.
		writeError(w, "no_permission")
	case errors.Is(err, domain.ErrAgentSessionNotAgent):
		writeError(w, "not_authorized")
	case errors.Is(err, domain.ErrAgentSessionNotFound):
		writeError(w, "session_not_found")
	// Renaming a session channel's session renames the channel, and these
	// are the channel's refusals the rename page lists.
	case errors.Is(err, domain.ErrInvalidCodeChannelName), errors.Is(err, domain.ErrInvalidConversation):
		writeError(w, "invalid_name")
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, "name_taken")
	case errors.Is(err, domain.ErrConversationAlreadyArchived):
		writeError(w, "is_archived")
	default:
		writeAgentSessionError(w, err)
	}
}

func writeAgentSessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrAgentSessionThreadRequired):
		writeError(w, "thread_ts_required")
	case errors.Is(err, domain.ErrAgentSessionThreadNotAllowed):
		writeError(w, "thread_ts_not_allowed")
	case errors.Is(err, domain.ErrInvalidAgentSession):
		writeError(w, "invalid_arguments")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}
