package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// timezoneAutomaticPreference is Language & region's "Set time zone
// automatically". On, the default, the activity heartbeat keeps the profile's
// zone in step with the browser (recordTimezone); off, the zone stays where the
// member put it.
const timezoneAutomaticPreference = "timezone-auto"

// setTimezone is Language & region's manual time zone. Choosing one turns the
// automatic zone off first, so the next heartbeat cannot undo it; the zone
// itself is validated by the profile write.
func (h Handler) setTimezone(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your time zone could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	zone := strings.TrimSpace(fields["timezone"])
	if zone == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose a time zone", "Pick a time zone from the list, such as Europe/Berlin.")
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your profile is temporarily unavailable.")
		return
	}
	if err := h.Messages.SetMemberPreference(r.Context(), principal.WorkspaceID, principal.UserID, timezoneAutomaticPreference, "false"); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Your time zone could not be saved", "Preferences are temporarily unavailable. Nothing was changed.")
		return
	}
	profile := current.Profile
	profile.Timezone = zone
	if _, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID, profile); err != nil {
		if errors.Is(err, domain.ErrInvalidProfile) {
			h.writeMutationError(w, r, http.StatusBadRequest, "That time zone is not recognised", "Pick a time zone from the list, such as Europe/Berlin.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Your time zone could not be saved", "Your profile is temporarily unavailable.")
		return
	}
	h.redirectMutation(w, r, h.viewURL(r, "")+"&notice="+url.QueryEscape("Your time zone is now "+zone+"."))
}
