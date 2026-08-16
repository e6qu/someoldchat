package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// externalAuthCallbackPath is the one route an external provider redirects back
// to. The app and provider ride in its query so the callback knows which
// connection the returned code completes; the redirect_uri sent to the provider
// carries them and nothing else, so the value the token exchange echoes matches.
const externalAuthCallbackPath = "/app/apps/external-auth/callback"

func (h Handler) externalAuthCallbackURL(r *http.Request, appID domain.AppID, provider string) string {
	query := url.Values{"app": {string(appID)}, "provider": {provider}}
	return h.responseBaseURL(r) + externalAuthCallbackPath + "?" + query.Encode()
}

// startExternalAuthConnection begins a member connecting an account for one of
// an installed app's declared providers, redirecting to the provider's own
// authorization page.
func (h Handler) startExternalAuthConnection(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The connection could not be started. Reload the app and try again.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	provider := strings.TrimSpace(fields["provider"])
	if appID == "" || provider == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The connection was not started", "Choose an app and provider and try again.")
		return
	}
	authorizeURL, err := h.Messages.StartExternalAuthConnection(r.Context(), principal.WorkspaceID, principal.UserID, appID, provider, h.externalAuthCallbackURL(r, appID, provider))
	if err != nil {
		h.writeMutationError(w, r, developerAppStatus(err), "The connection was not started", developerAppError(err))
		return
	}
	http.Redirect(w, r, authorizeURL, http.StatusSeeOther)
}

// externalAuthCallback completes a connect flow when the provider returns. It
// tolerates a denial or a failed exchange by returning to the app's About tab
// with a notice rather than an error page: the member can simply try again.
func (h Handler) externalAuthCallback(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(r.URL.Query().Get("app")))
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	notice := "connected"
	if appID == "" || provider == "" {
		h.writePageError(w, http.StatusBadRequest, "That connection is not valid", "The provider returned without naming the app it was for.")
		return
	}
	if err := h.Messages.CompleteExternalAuthConnection(r.Context(), principal.WorkspaceID, principal.UserID, appID, provider, code, state, h.externalAuthCallbackURL(r, appID, provider)); err != nil {
		notice = "connect_failed"
	}
	http.Redirect(w, r, "/app/apps/"+url.PathEscape(string(appID))+"?tab=about&notice="+notice, http.StatusSeeOther)
}

// setDeveloperExternalAuthProvider declares (or replaces) a provider for an app
// its owner controls, from the developer console.
func (h Handler) setDeveloperExternalAuthProvider(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The provider was not saved.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	config := domain.ExternalAuthProviderConfig{
		Name: strings.TrimSpace(fields["name"]), ClientID: strings.TrimSpace(fields["client_id"]),
		ClientSecret: fields["client_secret"], AuthorizationURL: strings.TrimSpace(fields["authorization_url"]),
		TokenURL: strings.TrimSpace(fields["token_url"]),
	}
	for _, scope := range strings.Fields(fields["scopes"]) {
		config.Scopes = append(config.Scopes, scope)
	}
	configuration, err := h.Messages.IssueAppConfigurationToken(r.Context(), principal.WorkspaceID, principal.UserID)
	if err == nil {
		err = h.Messages.SetAppExternalAuthProvider(r.Context(), configuration.Token, appID, config)
	}
	if err != nil {
		h.writeMutationError(w, r, developerAppStatus(err), "The provider was not saved", developerAppError(err))
		return
	}
	http.Redirect(w, r, "/app/developer/apps?app="+url.QueryEscape(string(appID)), http.StatusSeeOther)
}
