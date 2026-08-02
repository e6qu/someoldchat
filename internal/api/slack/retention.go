package slack

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// admin.conversations.{get,set,remove}CustomRetention.
//
// Slack's own asymmetry is preserved: setCustomRetention takes one
// duration_days and it governs messages, because that is the only thing the
// per-channel API configures. File retention is workspace-level and has no
// Slack method at all.

func (h Handler) adminConversationGetCustomRetention(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	override, effective, err := h.Messages.ConversationRetention(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	// is_policy_enabled reports whether a duration governs this conversation at
	// all, which is true when the workspace default does even if the channel
	// has no override of its own. duration_days reports the duration that
	// actually applies, so a caller never has to resolve the two.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"is_policy_enabled": effective > 0,
		"duration_days":     effective,
		"is_custom":         override.DurationDays > 0,
	})
}

func (h Handler) adminConversationSetCustomRetention(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(fields["duration_days"]))
	if err != nil {
		// A duration that is not a number is the same caller mistake as one
		// out of range, and Slack declares one code for it.
		writeError(w, "invalid_duration")
		return
	}
	if err := h.Messages.SetConversationRetention(r.Context(), principal.WorkspaceID, principal.UserID, channel, days); err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminConversationRemoveCustomRetention(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.RemoveConversationRetention(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
