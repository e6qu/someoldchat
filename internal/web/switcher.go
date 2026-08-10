package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// AUTH-02 requires a switch into an isolated session context, with no leakage
// through browser history. Both fall out of how it is done here:
//
//   - the switch MINTS A NEW SESSION for the target workspace rather than
//     mutating the current one. A session record carries the workspace, the
//     user identity in it and the scopes that workspace's role justifies; the
//     same person is a different user row in each workspace, so rewriting the
//     workspace on one session would leave it naming a user that does not
//     belong to it.
//   - it is a POST with the target in the body, so no workspace identifier
//     lands in history, and the previous session is revoked rather than left
//     as a live credential nothing references.

type workspaceChoice struct {
	ID      string
	Name    string
	Role    string
	Current bool
}

// workspaceChoices lists what this reader may switch into. A failure to read
// them is an empty list, not a failed page: the switcher is navigation, and a
// workspace that will not render because a sibling deployment row could not be
// read is a worse outcome than a missing menu.
func (h Handler) workspaceChoices(r *http.Request, principal auth.Principal) []workspaceChoice {
	summaries, err := h.Messages.UserWorkspaces(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil || len(summaries) < 2 {
		// One workspace is not a choice, and drawing a menu with a single
		// entry implies there is somewhere else to go.
		return nil
	}
	choices := make([]workspaceChoice, 0, len(summaries))
	for _, summary := range summaries {
		choices = append(choices, workspaceChoice{
			ID:      string(summary.Workspace.ID),
			Name:    summary.Workspace.Name,
			Role:    workspaceRoleLabel(summary.Role),
			Current: summary.Workspace.ID == principal.WorkspaceID,
		})
	}
	return choices
}

func (h Handler) switchWorkspace(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the page and try again.")
	if !ok {
		return
	}
	target := domain.WorkspaceID(strings.TrimSpace(fields["workspace_id"]))
	if target == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "You were not switched", "Choose a workspace and try again.")
		return
	}
	if target == principal.WorkspaceID {
		h.redirectMutation(w, r, "/app")
		return
	}
	summaries, err := h.Messages.UserWorkspaces(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "You were not switched", "Your workspaces could not be read. Nothing changed.")
		return
	}
	// The membership list is the authorization: a workspace the actor's own
	// address is not an active member of is not switchable into, and saying
	// "not found" rather than "not allowed" declines to confirm it exists.
	var chosen domain.WorkspaceMembershipSummary
	for _, summary := range summaries {
		if summary.Workspace.ID == target {
			chosen = summary
		}
	}
	if chosen.Workspace.ID == "" {
		h.writeMutationError(w, r, http.StatusNotFound, "You were not switched", "You are not a member of that workspace.")
		return
	}
	scopes, err := auth.ScopesForWorkspaceRole(chosen.Role)
	if err != nil {
		h.writeMutationError(w, r, http.StatusForbidden, "You were not switched", "Your role in that workspace grants no access.")
		return
	}
	token, err := randomURLValue(32)
	if err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "You were not switched", "A session could not be created. Nothing changed.")
		return
	}
	// Session settings are held per workspace, so the policy that applies is the
	// target's, read as the identity held there. Without it the switcher would
	// be a way to hold a longer session in a workspace than signing into it
	// gives, which is the same defect as the sign-in path had and one door
	// further along.
	settings, err := h.Messages.MemberSessionSettings(r.Context(), chosen.Workspace.ID, chosen.UserID)
	if err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "You were not switched", "A session could not be created. Nothing changed.")
		return
	}
	// The switched session must not outlive the one it replaces. A sign-in
	// caps its session at what the identity provider asserted, and switching
	// carries no fresh assertion — minting a full-length session here would
	// let a switch quietly extend an expiry the provider set.
	expiresAt := time.Now().UTC().Add(settings.Lifetime())
	if cookie, cookieErr := r.Cookie(auth.SessionCookieName); cookieErr == nil {
		if current, lookupErr := h.Messages.LookupSession(r.Context(), cookie.Value); lookupErr == nil && !current.ExpiresAt.IsZero() && current.ExpiresAt.Before(expiresAt) {
			expiresAt = current.ExpiresAt
		}
	}
	if !expiresAt.After(time.Now().UTC()) {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	record := domain.SessionRecord{WorkspaceID: chosen.Workspace.ID, UserID: chosen.UserID, Scopes: scopes.Values(), ExpiresAt: expiresAt}
	if err := h.Messages.CreateSession(r.Context(), token, record); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "You were not switched", "A session could not be created. Nothing changed.")
		return
	}
	// The old session is revoked only after the new one exists, so a failure
	// above leaves the reader signed into the workspace they were already in.
	if cookie, cookieErr := r.Cookie(auth.SessionCookieName); cookieErr == nil && strings.TrimSpace(cookie.Value) != "" {
		// The switch has already happened durably, so a failure to revoke the
		// old session is not worth undoing it for; the cookie no longer names
		// that session either way.
		_ = h.Messages.RevokeSession(r.Context(), cookie.Value)
	}
	// A cookie with no Max-Age dies when the browser does, which is what Slack's
	// desktop_app_browser_quit asks for. The durable session keeps its expiry.
	cookieMaxAge := int(time.Until(expiresAt).Seconds())
	if settings.DesktopAppBrowserQuit {
		cookieMaxAge = 0
	}
	http.SetCookie(w, auth.SessionCookie(token, cookieMaxAge, h.CookieDomain))
	h.redirectMutation(w, r, "/app?notice="+url.QueryEscape("You are now in "+chosen.Workspace.Name))
}
