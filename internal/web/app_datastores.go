package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type developerDatastoreAttributeData struct {
	Name       string
	Type       string
	PrimaryKey bool
	TimeToLive bool
}

type developerDatastoreItemData struct {
	ID   string
	JSON string
}

type developerDatastoreData struct {
	App                  domain.App
	Datastores           []string
	Name                 string
	Attributes           []developerDatastoreAttributeData
	Items                []developerDatastoreItemData
	PrimaryKey           string
	Expression           string
	ExpressionAttributes string
	ExpressionValues     string
	ItemJSON             string
	NextURL              string
	CSRFToken            string
	Error                string
	Notice               string
	Count                int
	Limit                int
}

const developerDatastoreMarkup = `{{define "title"}}{{.Name}} datastore · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar .theme-toggle{margin-left:auto}
.layout{max-width:1180px;margin:0 auto;padding:28px 22px}.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}.heading h1{margin:0 0 4px;font-size:26px}.muted{color:var(--muted)}
.grid{display:grid;grid-template-columns:minmax(220px,300px) minmax(0,1fr);gap:22px;align-items:start}.stack{display:grid;gap:18px}.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:20px}.card h2{margin-top:0}
.field{display:grid;gap:6px;margin:12px 0}.field input,.field select,.field textarea{width:100%;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);padding:9px 10px}.field textarea{min-height:150px;resize:vertical;font:13px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace}
.filter-grid{display:grid;grid-template-columns:2fr 1fr;gap:0 14px}.actions{display:flex;flex-wrap:wrap;gap:9px;align-items:center}.button{border:0;border-radius:5px;background:var(--ok);color:var(--on-strong);padding:9px 14px;font-weight:700}.button.secondary{background:var(--bg);color:var(--text);border:1px solid var(--field-line)}.button.danger{background:var(--danger);color:var(--on-strong)}
.schema{margin:0;padding:0;list-style:none}.schema li{padding:8px 0;border-bottom:1px solid var(--line)}.schema li:last-child{border:0}.pill{display:inline-block;border:1px solid var(--line);border-radius:99px;padding:2px 7px;font-size:11px;color:var(--muted)}
.items{display:grid;gap:12px;margin:0;padding:0;list-style:none}.item{border:1px solid var(--line);border-radius:8px;padding:13px}.item-head{display:flex;align-items:center;justify-content:space-between;gap:12px}.item pre{margin:10px 0 0;padding:12px;border-radius:6px;background:var(--bg);overflow:auto;font:12px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace}.empty{color:var(--muted)}
@media(max-width:760px){.grid,.filter-grid{grid-template-columns:minmax(0,1fr)}.layout{padding:20px 14px}}
</style>{{end}}
{{define "content"}}
<header class="bar"><a href="/app/developer/apps?app={{.App.ID}}">← Back to {{.App.Name}}</a><span>Hosted datastore</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header>
<main class="layout">
  <div class="heading"><h1>{{.Name}}</h1><p class="muted">Inspect and edit persisted data for <strong>{{.App.Name}}</strong>. Queries use Slack's hosted-datastore scan and expression semantics.</p></div>
  {{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{end}}{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
  <div class="grid">
    <aside class="stack">
      <section class="card" aria-labelledby="datastore-heading"><h2 id="datastore-heading">Datastore</h2>
        <form method="get" action="/app/developer/apps/datastore"><input type="hidden" name="app" value="{{.App.ID}}"><label class="field" for="datastore">Declared datastore<select id="datastore" name="datastore">{{range .Datastores}}<option value="{{.}}"{{if eq . $.Name}} selected{{end}}>{{.}}</option>{{end}}</select></label><button class="button secondary" type="submit">Open datastore</button></form>
      </section>
      <section class="card" aria-labelledby="schema-heading"><h2 id="schema-heading">Manifest schema</h2><ul class="schema">{{range .Attributes}}<li><strong>{{.Name}}</strong> <span class="pill">{{.Type}}</span>{{if .PrimaryKey}} <span class="pill">primary key</span>{{end}}{{if .TimeToLive}} <span class="pill">time to live</span>{{end}}</li>{{end}}</ul></section>
    </aside>
    <div class="stack">
      <section class="card" aria-labelledby="query-heading"><h2 id="query-heading">Query persisted items</h2><p class="muted">{{.Count}} matching item{{if ne .Count 1}}s{{end}}. A page can be empty and still have a next cursor because Slack applies the filter after scanning.</p>
        <form method="get" action="/app/developer/apps/datastore"><input type="hidden" name="app" value="{{.App.ID}}"><input type="hidden" name="datastore" value="{{.Name}}">
          <label class="field" for="expression">Expression<input id="expression" name="expression" value="{{.Expression}}" placeholder="contains (#title, :term)"></label>
          <div class="filter-grid"><label class="field" for="attributes">Expression attributes (JSON)<input id="attributes" name="attributes" value="{{.ExpressionAttributes}}" placeholder="{&quot;#title&quot;:&quot;title&quot;}"></label><label class="field" for="values">Expression values (JSON)<input id="values" name="values" value="{{.ExpressionValues}}" placeholder="{&quot;:term&quot;:&quot;alert&quot;}"></label></div>
          <label class="field" for="limit">Rows evaluated per page (1–1000)<input id="limit" name="limit" type="number" min="1" max="1000" value="{{.Limit}}"></label>
          <button class="button secondary" type="submit">Run query</button>
        </form>
      </section>
      <section class="card" aria-labelledby="items-heading"><h2 id="items-heading">Items</h2>
        <ul class="items">{{range .Items}}<li class="item"><div class="item-head"><strong>{{$.PrimaryKey}}: {{.ID}}</strong><form method="post" action="/app/developer/apps/datastore/delete"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="app_id" value="{{$.App.ID}}"><input type="hidden" name="datastore" value="{{$.Name}}"><input type="hidden" name="id" value="{{.ID}}"><button class="button danger" type="submit">Delete</button></form></div><pre>{{.JSON}}</pre></li>{{else}}<li class="empty">No items matched this page.</li>{{end}}</ul>
        {{if .NextURL}}<p><a href="{{.NextURL}}">Next scanned page →</a></p>{{end}}
      </section>
      <section class="card" aria-labelledby="edit-heading"><h2 id="edit-heading">Put an item</h2><p class="muted">Replace writes the complete object. Update merges the supplied fields into an existing object, matching apps.datastore.put and apps.datastore.update.</p>
        <form method="post" action="/app/developer/apps/datastore/put"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="app_id" value="{{.App.ID}}"><input type="hidden" name="datastore" value="{{.Name}}">
          <label class="field" for="mode">Operation<select id="mode" name="mode"><option value="replace">Replace item</option><option value="merge">Update item</option></select></label>
          <label class="field" for="item">Item (JSON)<textarea id="item" name="item" spellcheck="false" maxlength="409600" required>{{.ItemJSON}}</textarea></label>
          <button class="button" type="submit">Persist item</button>
        </form>
      </section>
    </div>
  </div>
</main>
{{end}}`

var developerDatastoreTemplate = mustPage(developerDatastoreMarkup)

func (h Handler) developerDatastore(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	h.renderDeveloperDatastore(w, r, principal, csrf, "", "", http.StatusOK)
}

func (h Handler) putDeveloperDatastoreItem(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The datastore item was not changed.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	name := strings.TrimSpace(fields["datastore"])
	item := strings.TrimSpace(fields["item"])
	merge := fields["mode"] == "merge"
	if fields["mode"] != "replace" && !merge {
		h.renderDeveloperDatastoreMutationError(w, r, principal, csrf, appID, name, item, "Choose replace or update.", http.StatusBadRequest)
		return
	}
	if _, _, _, err := h.developerDatastoreDefinition(r, principal, appID, name); err != nil {
		h.renderDeveloperDatastoreMutationError(w, r, principal, csrf, appID, name, item, developerDatastoreError(err), developerDatastoreStatus(err))
		return
	}
	if _, err := h.Messages.PutAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, appID, name, []string{item}, merge); err != nil {
		h.renderDeveloperDatastoreMutationError(w, r, principal, csrf, appID, name, item, developerDatastoreError(err), developerDatastoreStatus(err))
		return
	}
	h.redirectDeveloperDatastore(w, r, appID, name, "Item persisted.")
}

func (h Handler) deleteDeveloperDatastoreItem(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "The datastore item was not deleted.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	name := strings.TrimSpace(fields["datastore"])
	id := strings.TrimSpace(fields["id"])
	if _, _, _, err := h.developerDatastoreDefinition(r, principal, appID, name); err != nil {
		h.writeMutationError(w, r, developerDatastoreStatus(err), "The datastore item was not deleted", developerDatastoreError(err))
		return
	}
	if err := h.Messages.DeleteAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, appID, name, []string{id}); err != nil {
		h.writeMutationError(w, r, developerDatastoreStatus(err), "The datastore item was not deleted", developerDatastoreError(err))
		return
	}
	h.redirectDeveloperDatastore(w, r, appID, name, "Item deleted.")
}

func (h Handler) renderDeveloperDatastoreMutationError(w http.ResponseWriter, r *http.Request, principal auth.Principal, csrf string, appID domain.AppID, name, item, message string, status int) {
	query := r.URL.Query()
	query.Set("app", string(appID))
	query.Set("datastore", name)
	r.URL.RawQuery = query.Encode()
	h.renderDeveloperDatastore(w, r, principal, csrf, item, message, status)
}

func (h Handler) renderDeveloperDatastore(w http.ResponseWriter, r *http.Request, principal auth.Principal, csrf, itemJSON, mutationError string, status int) {
	appID := domain.AppID(strings.TrimSpace(r.URL.Query().Get("app")))
	requestedName := strings.TrimSpace(r.URL.Query().Get("datastore"))
	app, parsed, definition, err := h.developerDatastoreDefinition(r, principal, appID, requestedName)
	if err != nil {
		h.writePageError(w, developerDatastoreStatus(err), "Datastore unavailable", developerDatastoreError(err))
		return
	}

	names := make([]string, 0, len(parsed.Datastores))
	for name := range parsed.Datastores {
		names = append(names, name)
	}
	slices.Sort(names)
	name := definition.Name
	attributes := make([]developerDatastoreAttributeData, 0, len(definition.Attributes))
	for attributeName, attribute := range definition.Attributes {
		attributes = append(attributes, developerDatastoreAttributeData{
			Name:       attributeName,
			Type:       attribute.Type,
			PrimaryKey: attributeName == definition.PrimaryKey,
			TimeToLive: attributeName == definition.TimeToLiveAttribute,
		})
	}
	slices.SortFunc(attributes, func(left, right developerDatastoreAttributeData) int {
		if left.PrimaryKey != right.PrimaryKey {
			if left.PrimaryKey {
				return -1
			}
			return 1
		}
		return strings.Compare(left.Name, right.Name)
	})

	limit := 100
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 1000 {
			data := developerDatastoreData{
				App: app, Datastores: names, Name: name, Attributes: attributes, PrimaryKey: definition.PrimaryKey,
				Limit: 100, CSRFToken: csrf, ItemJSON: itemJSON, Error: "Rows evaluated per page must be between 1 and 1000.",
			}
			h.writeHTML(w, developerDatastoreTemplate, data, http.StatusBadRequest, "datastore console rendering unavailable")
			return
		}
	}
	expression := strings.TrimSpace(r.URL.Query().Get("expression"))
	expressionAttributes := strings.TrimSpace(r.URL.Query().Get("attributes"))
	expressionValues := strings.TrimSpace(r.URL.Query().Get("values"))
	query := domain.AppDatastoreQuery{
		Expression:           expression,
		ExpressionAttributes: expressionAttributes,
		ExpressionValues:     expressionValues,
		Page: domain.PageRequest{
			Limit:  limit,
			Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor"))),
		},
	}
	page, queryErr := h.Messages.QueryAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, app.ID, name, query)
	countQuery := query
	countQuery.Page = domain.PageRequest{}
	count, countErr := h.Messages.CountAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, app.ID, name, countQuery)

	data := developerDatastoreData{
		App: app, Datastores: names, Name: name, Attributes: attributes, PrimaryKey: definition.PrimaryKey,
		Expression: expression, ExpressionAttributes: expressionAttributes, ExpressionValues: expressionValues,
		Limit: limit, CSRFToken: csrf, ItemJSON: itemJSON, Count: count,
		Notice: strings.TrimSpace(r.URL.Query().Get("notice")),
	}
	if data.ItemJSON == "" {
		data.ItemJSON = fmt.Sprintf("{\n  %q: \"\"\n}", definition.PrimaryKey)
	}
	if mutationError != "" {
		data.Error = mutationError
	}
	if queryErr != nil {
		data.Error = developerDatastoreError(queryErr)
		status = developerDatastoreStatus(queryErr)
	} else if countErr != nil {
		data.Error = developerDatastoreError(countErr)
		status = developerDatastoreStatus(countErr)
	} else {
		data.Items = make([]developerDatastoreItemData, 0, len(page.Items))
		for _, raw := range page.Items {
			var item map[string]any
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&item); err != nil {
				data.Error = "A persisted item could not be decoded. The datastore may require repair."
				status = http.StatusServiceUnavailable
				break
			}
			data.Items = append(data.Items, developerDatastoreItemData{ID: fmt.Sprint(item[definition.PrimaryKey]), JSON: raw})
		}
		if page.HasMore {
			next := cloneQuery(r.URL.Query())
			next.Set("app", string(app.ID))
			next.Set("datastore", name)
			next.Set("limit", strconv.Itoa(limit))
			next.Set("cursor", string(page.NextCursor))
			data.NextURL = "/app/developer/apps/datastore?" + next.Encode()
		}
	}
	h.writeHTML(w, developerDatastoreTemplate, data, status, "datastore console rendering unavailable")
}

func (h Handler) developerDatastoreDefinition(r *http.Request, principal auth.Principal, appID domain.AppID, requestedName string) (domain.App, appmanifest.Parsed, appmanifest.Datastore, error) {
	if appID == "" {
		return domain.App{}, appmanifest.Parsed{}, appmanifest.Datastore{}, store.ErrNotFound
	}
	app, manifest, err := h.Messages.GetDeveloperApp(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	if err != nil {
		return domain.App{}, appmanifest.Parsed{}, appmanifest.Datastore{}, err
	}
	parsed, problems := appmanifest.Parse(manifest)
	if len(problems) != 0 {
		return domain.App{}, appmanifest.Parsed{}, appmanifest.Datastore{}, fmt.Errorf("%w: the saved manifest is invalid", store.ErrConflict)
	}
	if len(parsed.Datastores) == 0 {
		return domain.App{}, appmanifest.Parsed{}, appmanifest.Datastore{}, service.ErrAppDatastoreNotFound
	}
	if requestedName == "" {
		names := make([]string, 0, len(parsed.Datastores))
		for name := range parsed.Datastores {
			names = append(names, name)
		}
		slices.Sort(names)
		requestedName = names[0]
	}
	definition, exists := parsed.Datastores[requestedName]
	if !exists {
		return domain.App{}, appmanifest.Parsed{}, appmanifest.Datastore{}, service.ErrAppDatastoreNotFound
	}
	return app, parsed, definition, nil
}

func (h Handler) redirectDeveloperDatastore(w http.ResponseWriter, r *http.Request, appID domain.AppID, datastore, notice string) {
	http.Redirect(w, r, "/app/developer/apps/datastore?"+url.Values{
		"app":       {string(appID)},
		"datastore": {datastore},
		"notice":    {notice},
	}.Encode(), http.StatusSeeOther)
}

func cloneQuery(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for name, source := range values {
		cloned[name] = append([]string(nil), source...)
	}
	return cloned
}

func developerDatastoreStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrInvalidDatastoreItem),
		errors.Is(err, service.ErrInvalidDatastoreQuery),
		errors.Is(err, store.ErrInvalidArgument),
		errors.Is(err, domain.ErrInvalidCursor):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrAppNotHosted), errors.Is(err, store.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, service.ErrAppDatastoreNotFound), errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusServiceUnavailable
	}
}

func developerDatastoreError(err error) string {
	switch {
	case errors.Is(err, service.ErrInvalidDatastoreItem):
		return "The item does not match the datastore schema. Check its primary key, attribute names, types, and JSON syntax."
	case errors.Is(err, service.ErrInvalidDatastoreQuery):
		return "The query is invalid. Check its expression, attribute aliases, values, and supported Slack operators."
	case errors.Is(err, domain.ErrInvalidCursor), errors.Is(err, store.ErrInvalidArgument):
		return "The datastore request is invalid. Start again without the cursor and check each field."
	case errors.Is(err, service.ErrAppNotHosted):
		return "This datastore is unavailable because the app is not configured as a Slack-hosted app."
	case errors.Is(err, service.ErrAppDatastoreNotFound):
		return "That datastore is not declared in this app's current manifest."
	case errors.Is(err, store.ErrConflict):
		return "The app manifest is inconsistent. Save a valid hosted-app manifest before managing data."
	case errors.Is(err, store.ErrNotFound):
		return "The app or its workspace installation is unavailable. Install the app before managing its hosted data."
	default:
		return "The datastore is temporarily unavailable. Try again."
	}
}
