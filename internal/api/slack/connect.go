package slack

import (
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// The nine conversations.*SharedInvite* methods. They were ledger rows with no
// route: the post-connection half of Slack Connect existed and there was no way
// to reach it.
//
// Read, send and decide carry three different scopes on purpose. Folding them
// into conversations:manage would let anyone who can rename a channel admit an
// outside organization to it.

func (h Handler) conversationInviteShared(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	invite, err := h.Messages.InviteShared(r.Context(), principal.WorkspaceID, principal.UserID, channel,
		domain.WorkspaceID(strings.TrimSpace(fields["external_limited"])), strings.TrimSpace(fields["emails"]))
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": sharedInviteResponse(invite)})
}

// conversationApproveSharedInvite and its three siblings are separate methods
// rather than one closure factory because both structural gates AST-parse the
// route table and cannot resolve a handler built at registration time.
func (h Handler) conversationApproveSharedInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	h.decideSharedInvite(w, r, principal, h.approveSharedInvite)
}

// conversationRequestSharedInviteApprove is the same decision reached through
// the request-oriented method name Slack also publishes.
func (h Handler) conversationRequestSharedInviteApprove(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	h.decideSharedInvite(w, r, principal, h.approveSharedInvite)
}

func (h Handler) conversationRequestSharedInviteDeny(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	h.decideSharedInvite(w, r, principal, h.denySharedInvite)
}

func (h Handler) conversationDeclineSharedInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	h.decideSharedInvite(w, r, principal, h.declineSharedInvite)
}

func (h Handler) decideSharedInvite(w http.ResponseWriter, r *http.Request, principal auth.Principal, decide func(*http.Request, auth.Principal, domain.SharedInviteID) (domain.SharedInvite, error)) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	id := domain.SharedInviteID(strings.TrimSpace(fields["invite_id"]))
	if id == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	invite, err := decide(r, principal, id)
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "invite": sharedInviteResponse(invite)})
}

func (h Handler) conversationAcceptSharedInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	id := domain.SharedInviteID(strings.TrimSpace(fields["invite_id"]))
	if id == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.AcceptSharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                    true,
		"implicit_approval":     false,
		"channel":               map[string]any{"id": string(conversation.ID)},
		"invite_id":             string(id),
		"is_ext_shared":         conversation.IsExtShared,
		"is_pending_ext_shared": conversation.IsPendingExtShared,
	})
}

func (h Handler) conversationListConnectInvites(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	h.writeSharedInviteList(w, r, principal, domain.SharedInviteApproved)
}

func (h Handler) conversationRequestSharedInviteList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	h.writeSharedInviteList(w, r, principal, domain.SharedInvitePending)
}

// writeSharedInviteList serves both listing methods. They differ only in which
// status they default to: listConnectInvites reports invitations that were
// issued, and requestSharedInvite.list reports the ones still awaiting a host
// decision. Either accepts an explicit status.
func (h Handler) writeSharedInviteList(w http.ResponseWriter, r *http.Request, principal auth.Principal, fallback domain.SharedInviteStatus) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	status := fallback
	if raw := strings.TrimSpace(fields["status"]); raw != "" {
		status = domain.SharedInviteStatus(raw)
	}
	limit, err := clampLimit(fields["limit"], 100, 200)
	if err != nil {
		writeError(w, "invalid_cursor")
		return
	}
	request := domain.PageRequest{Limit: limit, Cursor: domain.Cursor(strings.TrimSpace(fields["cursor"]))}
	page, err := h.Messages.ListSharedInvites(r.Context(), principal.WorkspaceID, principal.UserID, status, request)
	if err != nil {
		writeError(w, mapServiceError(err, "invite_not_found"))
		return
	}
	invites := make([]map[string]any, 0, len(page.Invites))
	for _, invite := range page.Invites {
		invites = append(invites, sharedInviteResponse(invite))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"invites":           invites,
		"response_metadata": map[string]any{"next_cursor": string(page.NextCursor)},
	})
}

func (h Handler) conversationExternalInvitePermissionsSet(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeConversationsConnectManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	target := domain.WorkspaceID(strings.TrimSpace(fields["target_team"]))
	action := strings.ToLower(strings.TrimSpace(fields["action"]))
	if channel == "" || target == "" || (action != "upgrade" && action != "downgrade") {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.SetExternalInvitePermissions(r.Context(), principal.WorkspaceID, principal.UserID, channel, target, action == "upgrade")
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": map[string]any{"id": string(conversation.ID)}})
}

func sharedInviteResponse(invite domain.SharedInvite) map[string]any {
	result := map[string]any{
		"id":           string(invite.ID),
		"channel_id":   string(invite.ConversationID),
		"status":       string(invite.Status),
		"invited_by":   string(invite.InvitedBy),
		"date_created": invite.CreatedAt.Unix(),
	}
	if invite.TargetWorkspaceID != "" {
		result["target_team"] = string(invite.TargetWorkspaceID)
	}
	if invite.TargetEmail != "" {
		result["target_email"] = invite.TargetEmail
	}
	if !invite.ExpiresAt.IsZero() {
		result["date_invalid"] = invite.ExpiresAt.Unix()
	}
	return result
}

// The three host-side decisions and the invited side's one, as methods so the
// route table reads as a list of operations rather than of closures.
func (h Handler) approveSharedInvite(r *http.Request, principal auth.Principal, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return h.Messages.ApproveSharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id)
}

func (h Handler) denySharedInvite(r *http.Request, principal auth.Principal, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return h.Messages.DenySharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id)
}

func (h Handler) declineSharedInvite(r *http.Request, principal auth.Principal, id domain.SharedInviteID) (domain.SharedInvite, error) {
	return h.Messages.DeclineSharedInvite(r.Context(), principal.WorkspaceID, principal.UserID, id)
}
