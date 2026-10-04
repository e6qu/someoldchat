package web

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Slack's "Hide a person": a member hides someone from their profile, and
// that person's messages are then shown behind a click-through, without their
// name or photo, while still being delivered and counted. Preferences ›
// Privacy & visibility lists the people hidden, each with Unhide. Hiding is the
// member's own preference, so nobody — the hidden person and administrators
// included — is told or can see it.

// hiddenPeopleShown bounds the Privacy & visibility list, which reads one
// profile per entry.
const hiddenPeopleShown = 50

type hiddenPersonView struct {
	ID   string
	Name string
}

func (h Handler) setPersonHidden(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the profile and try again.")
	if !ok {
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["target"]))
	hide := fields["hidden"] == "true"
	if target == "" || target == principal.UserID {
		h.writeMutationError(w, r, http.StatusBadRequest, "That person was not hidden", "You can hide anyone in this workspace except yourself.")
		return
	}
	// Unhiding needs no lookup: a person who has since left can still be
	// removed from the list. Hiding names a current member of this workspace.
	if hide {
		if _, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, target); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				h.writeMutationError(w, r, http.StatusNotFound, "That person was not hidden", "They are not a member of this workspace.")
				return
			}
			h.writeStoreError(w, err, "People are temporarily unavailable.")
			return
		}
	}
	value := ""
	if hide {
		value = "true"
	}
	if err := h.Messages.SetMemberPreference(r.Context(), principal.WorkspaceID, principal.UserID, domain.HiddenPersonPreference(target), value); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "That change was not saved", "Your preferences are temporarily unavailable. Try again.")
		return
	}
	h.redirectMutation(w, r, returnTarget(fields, "/app/members?user="+string(target)))
}

// hiddenPeopleViews names the people a member has hidden, for Privacy &
// visibility. A person whose profile cannot be read is still listed, by
// identifier, so they can be unhidden.
func (h Handler) hiddenPeopleViews(r *http.Request, principal auth.Principal, preferences map[string]string) []hiddenPersonView {
	hidden := domain.HiddenPeople(preferences)
	ids := make([]domain.UserID, 0, len(hidden))
	for id := range hidden {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	if len(ids) > hiddenPeopleShown {
		ids = ids[:hiddenPeopleShown]
	}
	views := make([]hiddenPersonView, 0, len(ids))
	for _, id := range ids {
		name := string(id)
		if user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, id); err == nil {
			name = displayName(user)
		}
		views = append(views, hiddenPersonView{ID: string(id), Name: name})
	}
	sort.SliceStable(views, func(left, right int) bool { return strings.ToLower(views[left].Name) < strings.ToLower(views[right].Name) })
	return views
}
