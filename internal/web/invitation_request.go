package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// requestInvitation is a member's Add coworkers: the request goes to the
// workspace administrators' queue and nobody is invited until one of them
// approves it, which the notice says so the member does not expect an
// invitation to have gone out already.
func (h Handler) requestInvitation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The invitation request could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	email := strings.TrimSpace(fields["email"])
	err = h.Messages.RequestInvitation(r.Context(), principal.WorkspaceID, principal.UserID, email, nil, strings.TrimSpace(fields["reason"]))
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrInvalidInviteRequest):
		h.writeMutationError(w, r, http.StatusBadRequest, "That invitation could not be requested", "Enter the email address of the person to invite, such as name@example.com.")
		return
	case errors.Is(err, domain.ErrUserIsRestricted), errors.Is(err, domain.ErrUserIsUltraRestricted):
		h.writeMutationError(w, r, http.StatusForbidden, "Guests cannot invite people", "Ask a member of this workspace to request the invitation.")
		return
	default:
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Invitation requests are temporarily unavailable", "Your request was not sent. Nothing else was changed.")
		return
	}
	h.redirectMutation(w, r, h.viewURL(r, "")+"&notice="+url.QueryEscape("Your request to invite "+strings.ToLower(email)+" was sent to your workspace administrators."))
}
