package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The browser side of Slack Connect. It lives in the conversation details
// modal because that is where the question "who else is in this channel" is
// already asked, and the answer for an external organization belongs beside
// the answer for a person.

func (h Handler) connectInvite(w http.ResponseWriter, r *http.Request) {
	principal, fields, channel, ok := h.connectMutation(w, r, auth.ScopeConversationsConnectWrite)
	if !ok {
		return
	}
	target := domain.WorkspaceID(strings.TrimSpace(fields["target"]))
	if target == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "No invitation was sent", "Choose an organization and try again.")
		return
	}
	if _, err := h.Messages.InviteShared(r.Context(), principal.WorkspaceID, principal.UserID, channel, target, ""); err != nil {
		h.writeConnectError(w, r, err, "sent")
		return
	}
	h.redirectConnect(w, r, channel, "Invitation recorded")
}

func (h Handler) connectApprove(w http.ResponseWriter, r *http.Request) {
	h.decideConnectInvite(w, r, "approved", "Invitation approved", h.Messages.ApproveSharedInvite)
}

// connectDeny withdraws, whichever side of the state machine the invitation is
// on: a pending one is denied and an approved one is revoked, and the service
// records those as the different facts they are.
func (h Handler) connectDeny(w http.ResponseWriter, r *http.Request) {
	h.decideConnectInvite(w, r, "withdrawn", "Invitation withdrawn", h.withdrawSharedInvite)
}

func (h Handler) withdrawSharedInvite(ctx context.Context, workspaceID domain.WorkspaceID, actorID domain.UserID, id domain.SharedInviteID) (domain.SharedInvite, error) {
	invite, err := h.Messages.DenySharedInvite(ctx, workspaceID, actorID, id)
	if err == nil || !errors.Is(err, service.ErrSharedInviteSettled) {
		return invite, err
	}
	// It was already approved, so withdrawing it is a revocation rather than a
	// denial. Trying the other transition here keeps one control honest for
	// both states instead of showing two buttons that mean the same thing.
	return h.Messages.RevokeSharedInvite(ctx, workspaceID, actorID, id)
}

func (h Handler) decideConnectInvite(w http.ResponseWriter, r *http.Request, action, notice string, decide func(context.Context, domain.WorkspaceID, domain.UserID, domain.SharedInviteID) (domain.SharedInvite, error)) {
	principal, fields, channel, ok := h.connectMutation(w, r, auth.ScopeConversationsConnectManage)
	if !ok {
		return
	}
	id := domain.SharedInviteID(strings.TrimSpace(fields["invite_id"]))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "Nothing was decided", "Reload the page and try again.")
		return
	}
	if _, err := decide(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		h.writeConnectError(w, r, err, action)
		return
	}
	h.redirectConnect(w, r, channel, notice)
}

func (h Handler) connectMutation(w http.ResponseWriter, r *http.Request, scope auth.Scope) (auth.Principal, map[string]string, domain.ConversationID, bool) {
	principal, err := h.authenticate(r, scope)
	if err != nil {
		h.writeAuthError(w, r, err)
		return auth.Principal{}, nil, "", false
	}
	fields, ok := h.decodeMutation(w, r, "Reload the page and try again.")
	if !ok {
		return auth.Principal{}, nil, "", false
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	if channel == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "Nothing was changed", "Open a conversation and try again.")
		return auth.Principal{}, nil, "", false
	}
	return principal, fields, channel, true
}

func (h Handler) redirectConnect(w http.ResponseWriter, r *http.Request, channel domain.ConversationID, notice string) {
	h.redirectMutation(w, r, "/app?channel="+url.QueryEscape(string(channel))+"&details=1&notice="+url.QueryEscape(notice))
}

func (h Handler) writeConnectError(w http.ResponseWriter, r *http.Request, err error, action string) {
	switch {
	case errors.Is(err, service.ErrSlackConnectFull):
		h.writeMutationError(w, r, http.StatusConflict, "There is no room in this channel",
			"A Slack Connect channel holds at most 250 organizations, including this one. Nothing was changed.")
	case errors.Is(err, service.ErrSharedInviteSettled):
		h.writeMutationError(w, r, http.StatusConflict, "That invitation was already decided",
			"Someone else answered it first. Reload to see where it stands.")
	case errors.Is(err, store.ErrAlreadyExists):
		h.writeMutationError(w, r, http.StatusConflict, "That organization already has an invitation",
			"Withdraw the outstanding one before sending another.")
	case errors.Is(err, service.ErrInvalidSharedInvite):
		h.writeMutationError(w, r, http.StatusBadRequest, "That invitation is not valid",
			"An invitation names one organization and one channel this workspace hosts.")
	case errors.Is(err, service.ErrNotWorkspaceAdmin):
		h.writeMutationError(w, r, http.StatusForbidden, "You cannot decide that invitation",
			"Deciding who joins from outside is a workspace administrator's call.")
	case errors.Is(err, store.ErrNotFound):
		h.writeMutationError(w, r, http.StatusNotFound, "That invitation no longer exists",
			"Reload to see the conversation as it is now.")
	default:
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "The invitation could not be "+action,
			"Nothing was changed. Try again in a moment.")
	}
}
