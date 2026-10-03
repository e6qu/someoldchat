package web

import (
	"errors"
	"net/http"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// keepMemberPreference keeps one of the member's preferences for their
// account, so it follows them to every client. The page's script sends each
// change as it is made; it answers no content.
func (h Handler) keepMemberPreference(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The preference could not be read from the request.")
	if !ok {
		return
	}
	if err := h.Messages.SetMemberPreference(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["value"]); err != nil {
		if errors.Is(err, domain.ErrInvalidMemberPreference) {
			h.writeMutationError(w, r, http.StatusBadRequest, "The preference was not kept", "That preference is not one this workspace can keep.")
			return
		}
		h.writeStoreError(w, err, "Preferences are temporarily unavailable.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
