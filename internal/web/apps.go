package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

const starterAppManifest = `{
  "display_information": {
    "name": "My app",
    "description": "A SameOldChat app"
  },
  "oauth_config": {
    "redirect_urls": ["https://example.com/oauth/callback"],
    "scopes": {
      "bot": ["chat:write"]
    }
  },
  "features": {
    "app_home": {
      "home_tab_enabled": true,
      "messages_tab_enabled": true
    }
  },
  "settings": {
    "token_rotation_enabled": true,
    "socket_mode_enabled": false
  }
}`

// A one-time credential is rendered in the POST response because its plaintext
// cannot be recovered for a later GET. replaceState alone changed the visible
// URL but WebKit retained the POST method for reload. Replacing the POST entry
// and then pushing a same-document GET-shaped entry makes refresh fetch the
// safe app page. If Back reaches the replaced entry, immediately navigating to
// that safe GET prevents the cached secret document from resurfacing.
const developerAppsScript = `<script>(function(){var secret=document.querySelector('[data-one-time-secret][data-return]');if(!secret||!window.history||typeof window.history.replaceState!=='function'||typeof window.history.pushState!=='function')return;var target=secret.getAttribute('data-return');if(typeof target!=='string'||target.charAt(0)!=='/'||target.charAt(1)==='/')return;window.history.replaceState({oneTimeSecret:false},'',target);window.history.pushState({oneTimeSecret:true},'',target);window.addEventListener('popstate',function(){window.location.replace(target)},{once:true})})();</script>`

type developerAppsData struct {
	Apps            []domain.App
	Selected        *domain.App
	Manifest        string
	CSRFToken       string
	Error           string
	Notice          string
	Problems        []appmanifest.Error
	Credentials     *domain.AppCredentials
	Configuration   *domain.AppConfigurationCredentials
	AppToken        *domain.AppTokenCredentials
	AppTokens       []appTokenView
	StarterManifest string
	InstallURL      string
	DatastoreURL    string
	DeliveryURL     string
}

// appTokenView is one issued app-level token in the inventory. It is named by a
// short prefix of the stored hash — never the token, which is shown once at
// issuance and never again — with its issue time and whether it is still live.
type appTokenView struct {
	ID       string
	ShortID  string
	IssuedAt string
	Scopes   string
	Revoked  bool
}

func developerAppTokenViews(summaries []domain.AppTokenSummary) []appTokenView {
	views := make([]appTokenView, 0, len(summaries))
	for _, summary := range summaries {
		short := summary.ID
		if len(short) > 12 {
			short = short[:12]
		}
		issued := "Issue time not recorded"
		if !summary.IssuedAt.IsZero() {
			issued = summary.IssuedAt.UTC().Format("Jan 2, 2006 15:04 UTC")
		}
		views = append(views, appTokenView{
			ID: summary.ID, ShortID: short, IssuedAt: issued,
			Scopes: strings.Join(summary.Scopes, ", "), Revoked: summary.Revoked,
		})
	}
	return views
}

func (h Handler) loadDeveloperAppTokens(r *http.Request, principal auth.Principal, appID domain.AppID) []appTokenView {
	summaries, err := h.Messages.ListDeveloperAppTokens(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		return nil
	}
	return developerAppTokenViews(summaries)
}

const developerAppsMarkup = `{{define "title"}}Developer apps · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}
.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar .theme-toggle{margin-left:auto}
.layout{max-width:1180px;margin:0 auto;padding:28px 22px}.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}
.heading h1{margin:0 0 4px;font-size:26px}.muted{color:var(--muted)}
.grid{display:grid;grid-template-columns:minmax(220px,300px) minmax(0,1fr);gap:22px;align-items:start}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:20px}.card h2{margin-top:0}
.app-list{display:grid;gap:8px;margin:0 0 18px;padding:0;list-style:none}.app-link{display:block;border:1px solid var(--line);border-radius:7px;padding:10px 12px;text-decoration:none;color:var(--text);background:var(--bg)}
.app-link[aria-current=page]{border-color:var(--action);box-shadow:inset 3px 0 var(--action)}.app-link strong,.app-link small{display:block}.app-link small{color:var(--muted);margin-top:2px}
.field{display:grid;gap:6px;margin:12px 0}.field textarea{width:100%;min-height:430px;resize:vertical;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);padding:12px;font:13px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace;tab-size:2}
.actions{display:flex;flex-wrap:wrap;gap:9px;align-items:center}.button{border:0;border-radius:5px;background:var(--ok);color:var(--on-strong);padding:9px 14px;font-weight:700}.button.secondary{background:var(--bg);color:var(--text);border:1px solid var(--field-line)}.button.danger{background:var(--danger);color:var(--on-strong)}
.secret{margin:0 0 18px;padding:16px;border:2px solid var(--ok);border-radius:8px;background:var(--bg)}.secret h2{margin:0 0 4px}.secret dl{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:7px 14px}.secret dt{font-weight:700}.secret dd{margin:0;min-width:0}.secret code{overflow-wrap:anywhere;user-select:all}
.problems{margin:0 0 14px;padding:12px 16px 12px 34px;border:1px solid var(--danger);border-radius:7px;background:var(--danger-bg);color:var(--danger)}
.meta{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 12px}.pill{border:1px solid var(--line);border-radius:99px;padding:3px 8px;font-size:12px;color:var(--muted)}
.inline{display:inline}.empty{color:var(--muted)}
.app-tokens{margin-top:16px}.app-tokens h3{margin:0 0 8px}.app-token-table{width:100%;border-collapse:collapse;font-size:13px}.app-token-table th,.app-token-table td{text-align:left;padding:7px 10px;border-bottom:1px solid var(--line);vertical-align:middle}.app-token-table th{color:var(--muted);font-weight:700}.app-token-table code{overflow-wrap:anywhere}.app-token-table tr.token-revoked td{color:var(--muted)}.app-token-table .button{padding:5px 10px}
@media(max-width:760px){.grid{grid-template-columns:minmax(0,1fr)}.layout{padding:20px 14px}.field textarea{min-height:320px}.secret dl{grid-template-columns:1fr}.app-token-table{display:block;overflow-x:auto}}
</style>{{end}}
{{define "content"}}
<header class="bar"><a href="/app">← Back to chat</a><span>Developer apps</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header>
<main class="layout">
  <div class="heading"><h1>Developer apps</h1><p class="muted">Build OAuth apps, bots, Socket Mode clients, event subscriptions, and interactive experiences against the same APIs Slack SDKs use. <a href="/app/apps">Open installed apps</a>.</p></div>
  {{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{end}}{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
  {{if .Credentials}}<section class="secret" data-one-time-secret data-return="/app/developer/apps?app={{.Selected.ID}}" aria-labelledby="credentials-heading"><h2 id="credentials-heading">Save these app credentials now</h2><p class="muted">They are shown once. SameOldChat stores only secure digests.</p><dl><dt>Client ID</dt><dd><code>{{.Credentials.ClientID}}</code></dd><dt>Client secret</dt><dd><code>{{.Credentials.ClientSecret}}</code></dd><dt>Signing secret</dt><dd><code>{{.Credentials.SigningSecret}}</code></dd><dt>Verification token</dt><dd><code>{{.Credentials.VerificationToken}}</code></dd></dl></section>{{end}}
  {{if .Configuration}}<section class="secret" data-one-time-secret data-return="{{if .Selected}}/app/developer/apps?app={{.Selected.ID}}{{else}}/app/developer/apps{{end}}" aria-labelledby="configuration-heading"><h2 id="configuration-heading">Save this configuration token now</h2><p class="muted">Use it with apps.manifest.*. It expires in 12 hours; the refresh token is one-time-use.</p><dl><dt>Access token</dt><dd><code>{{.Configuration.Token}}</code></dd><dt>Refresh token</dt><dd><code>{{.Configuration.RefreshToken}}</code></dd><dt>Expires</dt><dd><time datetime="{{.Configuration.ExpiresAt.Format "2006-01-02T15:04:05Z07:00"}}">{{.Configuration.ExpiresAt.Format "Jan 2, 2006 15:04 UTC"}}</time></dd></dl></section>{{end}}
  {{if .AppToken}}<section class="secret" data-one-time-secret data-return="/app/developer/apps?app={{.Selected.ID}}" aria-labelledby="app-token-heading"><h2 id="app-token-heading">Save this app-level token now</h2><p class="muted">Use it to open Socket Mode connections. It is shown once and stored only as a secure digest.</p><dl><dt>Token</dt><dd><code>{{.AppToken.Token}}</code></dd><dt>Scopes</dt><dd>{{range $index, $scope := .AppToken.Scopes}}{{if $index}}, {{end}}<code>{{$scope}}</code>{{end}}</dd></dl></section>{{end}}
  <div class="grid">
    <aside class="card" aria-labelledby="app-list-heading"><h2 id="app-list-heading">Apps</h2><ul class="app-list">{{range .Apps}}<li><a class="app-link" href="/app/developer/apps?app={{.ID}}"{{if and $.Selected (eq $.Selected.ID .ID)}} aria-current="page"{{end}}><strong>{{.Name}}</strong><small>{{.ID}} · manifest v{{.ManifestVersion}}</small></a></li>{{else}}<li class="empty">You have not created an app yet.</li>{{end}}</ul>
      <form method="post" action="/app/developer/apps/configuration-token"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">{{if .Selected}}<input type="hidden" name="app_id" value="{{.Selected.ID}}">{{end}}<button class="button secondary" type="submit">Issue configuration token</button></form>
    </aside>
    <section class="card" aria-labelledby="manifest-heading">
      <h2 id="manifest-heading">{{if .Selected}}Edit {{.Selected.Name}}{{else}}Create an app{{end}}</h2>
      {{if .Selected}}<div class="meta"><span class="pill">{{.Selected.ID}}</span><span class="pill">Client {{.Selected.ClientID}}</span><span class="pill">Manifest v{{.Selected.ManifestVersion}}</span>{{if .Selected.SocketModeEnabled}}<span class="pill">Socket Mode</span>{{end}}{{if .Selected.TokenRotationEnabled}}<span class="pill">Token rotation</span>{{end}}{{if eq .Selected.Distribution "public"}}<span class="pill">Distributed</span>{{else}}<span class="pill">Private</span>{{end}}</div>{{end}}
      {{if .Problems}}<ul class="problems" aria-label="Manifest errors">{{range .Problems}}<li><strong>{{if .Pointer}}{{.Pointer}}: {{end}}</strong>{{.Message}}</li>{{end}}</ul>{{end}}
      <form method="post" action="{{if .Selected}}/app/developer/apps/update{{else}}/app/developer/apps/create{{end}}">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">{{if .Selected}}<input type="hidden" name="app_id" value="{{.Selected.ID}}">{{end}}
        <label class="field" for="manifest">App manifest (JSON)<textarea id="manifest" name="manifest" maxlength="1048576" spellcheck="false" required>{{.Manifest}}</textarea></label>
        <div class="actions"><button class="button" type="submit">{{if .Selected}}Save manifest{{else}}Create app{{end}}</button>{{if .InstallURL}}<a href="{{.InstallURL}}">Open install flow</a>{{end}}{{if .DatastoreURL}}<a href="{{.DatastoreURL}}">Manage hosted datastores</a>{{end}}{{if .DeliveryURL}}<a href="{{.DeliveryURL}}">View event delivery health</a>{{end}}</div>
      </form>
      {{if .Selected}}<hr>{{if .Selected.SocketModeEnabled}}<form method="post" action="/app/developer/apps/app-token"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="app_id" value="{{.Selected.ID}}"><button class="button secondary" type="submit">Generate app-level token</button></form><form method="post" action="/app/developer/apps/app-token/revoke"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="app_id" value="{{.Selected.ID}}"><button class="button secondary" type="submit">Revoke app-level tokens</button></form>{{if .AppTokens}}<div class="app-tokens"><h3 id="app-tokens-heading">Issued app-level tokens</h3><table class="app-token-table" aria-labelledby="app-tokens-heading"><thead><tr><th scope="col">Token</th><th scope="col">Issued</th><th scope="col">Scopes</th><th scope="col">Status</th><th scope="col"><span class="visually-hidden">Actions</span></th></tr></thead><tbody>{{range .AppTokens}}<tr{{if .Revoked}} class="token-revoked"{{end}}><td><code>{{.ShortID}}…</code></td><td>{{.IssuedAt}}</td><td>{{if .Scopes}}{{.Scopes}}{{else}}—{{end}}</td><td>{{if .Revoked}}Revoked{{else}}Active{{end}}</td><td>{{if not .Revoked}}<form class="inline" method="post" action="/app/developer/apps/app-token/revoke-one"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="app_id" value="{{$.Selected.ID}}"><input type="hidden" name="token_id" value="{{.ID}}"><button class="button secondary" type="submit">Revoke</button></form>{{end}}</td></tr>{{end}}</tbody></table></div>{{end}}{{end}}<div class="distribution"><h3 id="distribution-heading">Distribution</h3><p class="muted">{{if eq .Selected.Distribution "public"}}This app is distributed: it can be installed in any workspace through its install flow.{{else}}This app is private: only its development workspace can install it. Activating public distribution needs a redirect URL in the manifest.{{end}}</p><form method="post" action="/app/developer/apps/distribution"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="app_id" value="{{.Selected.ID}}">{{if eq .Selected.Distribution "public"}}<input type="hidden" name="public" value="false"><button class="button secondary" type="submit">Deactivate public distribution</button>{{else}}<input type="hidden" name="public" value="true"><button class="button" type="submit">Activate public distribution</button>{{end}}</form></div><form method="post" action="/app/developer/apps/delete"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="app_id" value="{{.Selected.ID}}"><button class="button danger" type="submit">Delete app</button></form>{{end}}
    </section>
  </div>
</main>
{{end}}
{{define "scripts"}}` + developerAppsScript + `{{end}}`

var developerAppsTemplate = mustPage(developerAppsMarkup)

func (h Handler) developerApps(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	h.renderDeveloperApps(w, r, principal, csrf, r.URL.Query().Get("app"), "", "", nil, nil, http.StatusOK)
}

// WebKit retains the method of the original POST response when History API
// replaces its URL, so refreshing a one-time credential page can POST the safe
// app URL. This endpoint is deliberately read-only: it authenticates and
// converts that retained-method reload into the canonical GET without
// repeating app creation or returning the secret again.
func (h Handler) reloadDeveloperApps(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.developerPrincipal(w, r); !ok {
		return
	}
	target := "/app/developer/apps"
	if appID := strings.TrimSpace(r.URL.Query().Get("app")); appID != "" {
		target += "?app=" + url.QueryEscape(appID)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h Handler) createDeveloperApp(w http.ResponseWriter, r *http.Request) {
	h.mutateDeveloperApp(w, r, false)
}

func (h Handler) updateDeveloperApp(w http.ResponseWriter, r *http.Request) {
	h.mutateDeveloperApp(w, r, true)
}

func (h Handler) mutateDeveloperApp(w http.ResponseWriter, r *http.Request, updating bool) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The app manifest was not changed.")
	if !ok {
		return
	}
	manifest, appID := fields["manifest"], strings.TrimSpace(fields["app_id"])
	configuration, err := h.Messages.IssueAppConfigurationToken(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.renderDeveloperApps(w, r, principal, csrf, appID, manifest, "A configuration token could not be issued. Try again.", nil, nil, http.StatusServiceUnavailable)
		return
	}
	problems, err := h.Messages.ValidateAppManifest(r.Context(), configuration.Token, appID, manifest)
	if err != nil {
		h.renderDeveloperApps(w, r, principal, csrf, appID, manifest, developerAppError(err), nil, nil, developerAppStatus(err))
		return
	}
	if len(problems) != 0 {
		h.renderDeveloperApps(w, r, principal, csrf, appID, manifest, "Fix the manifest errors below.", problems, nil, http.StatusBadRequest)
		return
	}
	if updating {
		app, err := h.Messages.UpdateAppFromManifest(r.Context(), configuration.Token, domain.AppID(appID), manifest)
		if err != nil {
			h.renderDeveloperApps(w, r, principal, csrf, appID, manifest, developerAppError(err), nil, nil, developerAppStatus(err))
			return
		}
		h.renderDeveloperApps(w, r, principal, csrf, string(app.ID), "", "Manifest saved.", nil, nil, http.StatusOK)
		return
	}
	app, credentials, err := h.Messages.CreateAppFromManifest(r.Context(), configuration.Token, manifest, "")
	if err != nil {
		h.renderDeveloperApps(w, r, principal, csrf, "", manifest, developerAppError(err), nil, nil, developerAppStatus(err))
		return
	}
	h.renderDeveloperApps(w, r, principal, csrf, string(app.ID), "", "App created.", nil, &credentials, http.StatusCreated)
}

func (h Handler) deleteDeveloperApp(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The app was not deleted.")
	if !ok {
		return
	}
	configuration, err := h.Messages.IssueAppConfigurationToken(r.Context(), principal.WorkspaceID, principal.UserID)
	if err == nil {
		err = h.Messages.DeleteDeveloperApp(r.Context(), configuration.Token, domain.AppID(strings.TrimSpace(fields["app_id"])))
	}
	if err != nil {
		h.writeMutationError(w, r, developerAppStatus(err), "The app was not deleted", developerAppError(err))
		return
	}
	http.Redirect(w, r, "/app/developer/apps", http.StatusSeeOther)
}

func (h Handler) setDeveloperAppDistribution(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The app distribution was not changed.")
	if !ok {
		return
	}
	appID := strings.TrimSpace(fields["app_id"])
	public := strings.TrimSpace(fields["public"]) == "true"
	configuration, err := h.Messages.IssueAppConfigurationToken(r.Context(), principal.WorkspaceID, principal.UserID)
	if err == nil {
		_, err = h.Messages.SetAppDistribution(r.Context(), configuration.Token, domain.AppID(appID), public)
	}
	if err != nil {
		h.writeMutationError(w, r, developerAppStatus(err), "The app distribution was not changed", developerAppError(err))
		return
	}
	http.Redirect(w, r, "/app/developer/apps?app="+url.QueryEscape(appID), http.StatusSeeOther)
}

func (h Handler) issueDeveloperConfigurationToken(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "No configuration token was issued.")
	if !ok {
		return
	}
	selectedID := strings.TrimSpace(fields["app_id"])
	configuration, err := h.Messages.IssueAppConfigurationToken(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.renderDeveloperApps(w, r, principal, csrf, selectedID, "", "A configuration token could not be issued. Try again.", nil, nil, http.StatusServiceUnavailable)
		return
	}
	h.renderDeveloperApps(w, r, principal, csrf, selectedID, "", "Configuration token issued.", nil, nil, http.StatusCreated, &configuration)
}

func (h Handler) issueDeveloperAppToken(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "No app-level token was issued.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	token, err := h.Messages.IssueDeveloperAppToken(r.Context(), principal.WorkspaceID, principal.UserID, appID, []string{"connections:write"})
	if err != nil {
		h.renderDeveloperApps(w, r, principal, csrf, string(appID), "", developerAppError(err), nil, nil, developerAppStatus(err))
		return
	}
	h.renderDeveloperAppsWithToken(w, r, principal, csrf, appID, token)
}

func (h Handler) revokeDeveloperAppTokens(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The app-level tokens were not revoked.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	if err := h.Messages.RevokeDeveloperAppTokens(r.Context(), principal.WorkspaceID, principal.UserID, appID); err != nil {
		h.writeMutationError(w, r, developerAppStatus(err), "The app-level tokens were not revoked", developerAppError(err))
		return
	}
	http.Redirect(w, r, "/app/developer/apps?app="+url.QueryEscape(string(appID)), http.StatusSeeOther)
}

func (h Handler) revokeDeveloperAppToken(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The app-level token was not revoked.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	tokenID := strings.TrimSpace(fields["token_id"])
	if err := h.Messages.RevokeDeveloperAppToken(r.Context(), principal.WorkspaceID, principal.UserID, appID, tokenID); err != nil {
		h.writeMutationError(w, r, developerAppStatus(err), "The app-level token was not revoked", developerAppError(err))
		return
	}
	http.Redirect(w, r, "/app/developer/apps?app="+url.QueryEscape(string(appID)), http.StatusSeeOther)
}

func (h Handler) renderDeveloperAppsWithToken(w http.ResponseWriter, r *http.Request, principal auth.Principal, csrf string, appID domain.AppID, token domain.AppTokenCredentials) {
	apps, err := h.Messages.ListDeveloperApps(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writePageError(w, http.StatusServiceUnavailable, "Apps are temporarily unavailable", "The app store could not be read. Try again.")
		return
	}
	app, manifest, err := h.Messages.GetDeveloperApp(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		h.writePageError(w, developerAppStatus(err), "The token was issued but the app could not be rendered", developerAppError(err))
		return
	}
	data := developerAppsData{Apps: apps, Selected: &app, Manifest: manifest, CSRFToken: csrf, Notice: "App-level token generated.", AppToken: &token}
	setDeveloperAppLinks(&data, app, manifest)
	data.AppTokens = h.loadDeveloperAppTokens(r, principal, app.ID)
	h.writeHTML(w, developerAppsTemplate, data, http.StatusCreated, "app console rendering unavailable")
}

func (h Handler) developerPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, string, bool) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		if r.Method == http.MethodGet && errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			http.Redirect(w, r, h.signInTarget(r), http.StatusSeeOther)
			return auth.Principal{}, "", false
		}
		h.writeAuthError(w, r, err)
		return auth.Principal{}, "", false
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return auth.Principal{}, "", false
	}
	return principal, auth.CSRFToken(cookie.Value), true
}

func (h Handler) renderDeveloperApps(w http.ResponseWriter, r *http.Request, principal auth.Principal, csrf, selectedID, submittedManifest, message string, problems []appmanifest.Error, credentials *domain.AppCredentials, status int, configuration ...*domain.AppConfigurationCredentials) {
	apps, err := h.Messages.ListDeveloperApps(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writePageError(w, http.StatusServiceUnavailable, "Apps are temporarily unavailable", "The app store could not be read. Try again.")
		return
	}
	data := developerAppsData{Apps: apps, CSRFToken: csrf, StarterManifest: starterAppManifest, Manifest: starterAppManifest, Problems: problems, Credentials: credentials}
	if len(configuration) != 0 {
		data.Configuration = configuration[0]
	}
	if strings.TrimSpace(selectedID) != "" {
		app, manifest, err := h.Messages.GetDeveloperApp(r.Context(), principal.WorkspaceID, principal.UserID, domain.AppID(strings.TrimSpace(selectedID)))
		if err != nil {
			data.Error = developerAppError(err)
			status = developerAppStatus(err)
		} else {
			data.Selected = &app
			data.Manifest = manifest
			setDeveloperAppLinks(&data, app, manifest)
			data.AppTokens = h.loadDeveloperAppTokens(r, principal, app.ID)
		}
	}
	if submittedManifest != "" {
		data.Manifest = submittedManifest
	}
	if status >= http.StatusBadRequest {
		data.Error = message
	} else {
		data.Notice = message
	}
	h.writeHTML(w, developerAppsTemplate, data, status, "app console rendering unavailable")
}

func setDeveloperAppLinks(data *developerAppsData, app domain.App, manifest string) {
	data.DeliveryURL = "/app/developer/apps/delivery?app=" + url.QueryEscape(string(app.ID))
	parsed, problems := appmanifest.Parse(manifest)
	if len(problems) != 0 {
		return
	}
	if len(parsed.RedirectURLs) != 0 {
		values := url.Values{
			"client_id":    {app.ClientID},
			"redirect_uri": {parsed.RedirectURLs[0]},
		}
		if len(parsed.BotScopes) != 0 {
			values.Set("scope", strings.Join(parsed.BotScopes, ","))
		}
		if len(parsed.UserScopes) != 0 {
			values.Set("user_scope", strings.Join(parsed.UserScopes, ","))
		}
		data.InstallURL = "/oauth/v2/authorize?" + values.Encode()
	}
	if parsed.IsHosted && parsed.FunctionRuntime == "slack" && len(parsed.Datastores) != 0 {
		names := make([]string, 0, len(parsed.Datastores))
		for name := range parsed.Datastores {
			names = append(names, name)
		}
		slices.Sort(names)
		data.DatastoreURL = "/app/developer/apps/datastore?" + url.Values{
			"app":       {string(app.ID)},
			"datastore": {names[0]},
		}.Encode()
	}
}

func developerAppStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrAppNotDistributable):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, store.ErrInvalidArgument):
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

func developerAppError(err error) string {
	switch {
	case errors.Is(err, service.ErrAppNotDistributable):
		return "Add a redirect URL to the manifest before activating public distribution: an install has nowhere to return to without one."
	case errors.Is(err, store.ErrNotFound):
		return "That app is not available. It may have been deleted or belong to another developer."
	case errors.Is(err, store.ErrConflict):
		return "The app changed while you were editing it. Reload it and apply your changes again."
	case errors.Is(err, store.ErrAlreadyExists):
		return "An app with those identifiers already exists."
	case errors.Is(err, store.ErrInvalidArgument):
		return "The app request is invalid."
	default:
		return "The app store is temporarily unavailable. Try again."
	}
}
