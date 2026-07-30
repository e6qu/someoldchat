package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

type developerAppDeliveryData struct {
	App     domain.App
	Health  domain.AppDeliveryHealth
	State   string
	Summary string
}

const developerAppDeliveryMarkup = `{{define "title"}}Event delivery · {{.App.Name}} · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar .theme-toggle{margin-left:auto}
.layout{max-width:920px;margin:0 auto;padding:28px 22px}.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}.heading h1{margin:0 0 4px;font-size:26px}.muted{color:var(--muted)}
.stack{display:grid;gap:18px}.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:20px}.card h2{margin-top:0}.state{display:flex;gap:10px;align-items:center}.state strong{font-size:18px}.dot{width:12px;height:12px;border-radius:50%;background:var(--muted)}.dot.ok{background:var(--ok)}.dot.warn{background:#c18401}.dot.error{background:var(--danger)}
.facts{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:9px 18px;margin:0}.facts dt{font-weight:700}.facts dd{margin:0;overflow-wrap:anywhere}.facts code{font-size:12px}.actions{display:flex;gap:12px;align-items:center;flex-wrap:wrap}
@media(max-width:600px){.layout{padding:20px 14px}.facts{grid-template-columns:minmax(0,1fr);gap:3px}.facts dd{margin-bottom:9px}}
</style>{{end}}
{{define "content"}}
<header class="bar"><a href="/app/developer/apps?app={{.App.ID}}">← Back to {{.App.Name}}</a><span>Event delivery</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header>
<main class="layout">
  <div class="heading"><h1>Event delivery health</h1><p class="muted">Live durable queue state for <strong>{{.App.Name}}</strong>. This page reports acknowledgements, active work, and scheduled retries; it does not invent historical delivery metrics that are not retained.</p></div>
  <div class="stack">
    <section class="card" aria-labelledby="state-heading"><div class="state"><span class="dot {{if eq .State "Caught up"}}ok{{else if eq .State "Retry scheduled"}}error{{else}}warn{{end}}" aria-hidden="true"></span><div><h2 id="state-heading">{{.State}}</h2><p>{{.Summary}}</p></div></div><div class="actions"><a href="/app/developer/apps/delivery?app={{.App.ID}}">Refresh live state</a></div></section>
    <section class="card" aria-labelledby="transport-heading"><h2 id="transport-heading">Configuration</h2><dl class="facts"><dt>App</dt><dd><code>{{.App.ID}}</code></dd><dt>Installed</dt><dd>{{if .Health.Installed}}Yes{{else}}No{{end}}</dd><dt>Transport</dt><dd>{{if .Health.Configured}}{{.Health.Surface}}{{else}}Not configured{{end}}</dd><dt>Endpoint</dt><dd>{{if .Health.Endpoint}}{{.Health.Endpoint}}{{else}}—{{end}}</dd></dl></section>
    <section class="card" aria-labelledby="cursor-heading"><h2 id="cursor-heading">Durable cursor</h2><dl class="facts"><dt>Last acknowledged sequence</dt><dd>{{.Health.AcknowledgedSequence}}</dd><dt>In-flight sequence</dt><dd>{{if .Health.InFlightSequence}}{{.Health.InFlightSequence}}{{else}}—{{end}}</dd><dt>Lease valid until</dt><dd>{{if not .Health.InFlightUntil.IsZero}}<time datetime="{{.Health.InFlightUntil.Format "2006-01-02T15:04:05Z07:00"}}">{{.Health.InFlightUntil.Format "Jan 2, 2006 15:04:05 UTC"}}</time>{{else}}—{{end}}</dd><dt>Retry count</dt><dd>{{.Health.RetryCount}}</dd><dt>Retry reason</dt><dd>{{if .Health.RetryReason}}<code>{{.Health.RetryReason}}</code>{{else}}—{{end}}</dd><dt>Retry at</dt><dd>{{if not .Health.RetryAt.IsZero}}<time datetime="{{.Health.RetryAt.Format "2006-01-02T15:04:05Z07:00"}}">{{.Health.RetryAt.Format "Jan 2, 2006 15:04:05 UTC"}}</time>{{else}}—{{end}}</dd></dl></section>
    {{if .Health.PendingEvaluation}}<section class="card" aria-labelledby="queued-heading"><h2 id="queued-heading">Next journal record awaiting evaluation</h2><dl class="facts"><dt>Topic</dt><dd><code>{{.Health.NextEventTopic}}</code></dd><dt>Created</dt><dd><time datetime="{{.Health.NextEventAt.Format "2006-01-02T15:04:05Z07:00"}}">{{.Health.NextEventAt.Format "Jan 2, 2006 15:04:05 UTC"}}</time></dd></dl><p class="muted">The worker still applies subscription and visibility rules before deciding whether this record produces a callback.</p></section>{{end}}
  </div>
</main>
{{end}}`

var developerAppDeliveryTemplate = mustPage(developerAppDeliveryMarkup)

func (h Handler) developerAppDelivery(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(r.URL.Query().Get("app")))
	app, _, err := h.Messages.GetDeveloperApp(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		h.writePageError(w, developerAppStatus(err), "Event delivery unavailable", developerAppError(err))
		return
	}
	health, err := h.Messages.GetDeveloperAppDeliveryHealth(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		h.writePageError(w, developerAppStatus(err), "Event delivery unavailable", developerAppError(err))
		return
	}
	state, summary := appDeliverySummary(health, time.Now().UTC())
	h.writeHTML(w, developerAppDeliveryTemplate, developerAppDeliveryData{
		App: app, Health: health, State: state, Summary: summary,
	}, http.StatusOK, "event delivery rendering unavailable")
}

func appDeliverySummary(health domain.AppDeliveryHealth, now time.Time) (string, string) {
	switch {
	case !health.Configured:
		return "Not configured", "Add an Events API request URL or enable Socket Mode in the app manifest."
	case !health.Installed:
		return "Not installed", "Install the app in a workspace before event delivery can run."
	case health.RetryCount > 0 && health.RetryAt.After(now):
		return "Retry scheduled", "The last attempt was not acknowledged. The durable worker will retry at the recorded time."
	case health.InFlightSequence != 0 && health.InFlightUntil.After(now):
		return "Delivery in progress", "A worker currently holds the durable lease for the next journal record."
	case health.PendingEvaluation:
		return "Queued", "At least one journal record is waiting for subscription and visibility evaluation."
	default:
		return "Caught up", "No journal record is waiting for this app transport and no retry is scheduled."
	}
}
