package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type workspaceAppsData struct {
	Apps          []domain.InstalledApp
	Selected      *domain.InstalledApp
	Home          *modalView
	Published     bool
	Tab           string
	Channel       string
	CSRFToken     string
	CanMessage    bool
	Notice        string
	WorkspaceName string
}

const workspaceAppsMarkup = `{{define "title"}}{{if .Selected}}{{.Selected.Name}}{{else}}Apps{{end}} · {{.WorkspaceName}}{{end}}
{{define "styles"}}<style>
.apps-shell{min-height:100vh;display:grid;grid-template-rows:48px minmax(0,1fr)}
.apps-topbar{display:flex;align-items:center;gap:14px;padding:0 16px;background:var(--accent);color:var(--on-accent)}
.apps-topbar a{color:inherit;text-decoration:none;font-weight:800}.apps-topbar .theme-toggle{margin-left:auto}
.apps-workspace{display:grid;grid-template-columns:260px minmax(0,1fr);min-height:0}
.apps-sidebar{padding:16px 10px;background:linear-gradient(180deg,var(--accent),#3f1645);color:var(--on-accent);overflow:auto}
.apps-sidebar h2{margin:0 10px 12px;font-size:14px;text-transform:uppercase;letter-spacing:.06em;color:#e8cbe9}
.installed-apps{display:grid;gap:3px;margin:0;padding:0;list-style:none}
.installed-app{display:grid;grid-template-columns:34px minmax(0,1fr);align-items:center;gap:9px;padding:7px 10px;border-radius:6px;color:inherit;text-decoration:none}
.installed-app:hover,.installed-app[aria-current=page]{background:#ffffff2b}.installed-app[aria-current=page]{background:#1264a3}
.app-avatar{display:grid;place-items:center;width:34px;height:34px;border-radius:8px;background:#fff;color:var(--accent);font-weight:900;text-transform:uppercase}
.installed-app strong,.installed-app small{display:block;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.installed-app small{color:#e8cbe9}
.apps-empty{padding:8px 10px;color:#e8cbe9}.developer-link{display:block;margin:20px 10px 0;padding-top:14px;border-top:1px solid #ffffff4f;color:inherit}
.apps-main{min-width:0;background:var(--panel-strong)}
.apps-heading{display:flex;align-items:center;gap:12px;min-height:66px;padding:10px 24px;border-bottom:1px solid var(--line)}
.apps-heading h1{margin:0;font-size:20px}.apps-heading p{margin:2px 0 0;color:var(--muted);font-size:13px}.apps-heading .app-avatar{background:var(--accent);color:var(--on-accent)}
.app-tabs{display:flex;align-items:end;gap:24px;min-height:45px;padding:0 24px;border-bottom:1px solid var(--line)}
.app-tab{display:inline-flex;align-items:center;min-height:45px;border:0;border-bottom:3px solid transparent;background:transparent;color:var(--muted);font:inherit;font-weight:700;text-decoration:none;padding:2px 1px 0}
.app-tab[aria-current=page]{border-bottom-color:var(--action);color:var(--text)}
.app-tab-form{display:contents}.app-tab:hover{color:var(--text)}
.app-notice{margin:14px auto 0;width:min(760px,calc(100% - 32px))}
.app-home{width:min(760px,calc(100% - 32px));margin:0 auto;padding:28px 0 48px}
.home-block{margin:0 0 18px;white-space:pre-wrap;overflow-wrap:anywhere}.home-block.header{font-size:20px;font-weight:850}.home-block.context{color:var(--muted);font-size:13px}.home-block.divider{border:0;border-top:1px solid var(--line)}
.formatted-text{white-space:normal;overflow-wrap:anywhere}.formatted-text>:first-child{margin-top:0}.formatted-text>:last-child{margin-bottom:0}.formatted-text p{margin:0 0 8px}.formatted-text h1,.formatted-text h2,.formatted-text h3{margin:12px 0 6px;line-height:1.25}.formatted-text ul,.formatted-text ol{margin:6px 0;padding-left:24px}.formatted-text blockquote{margin:6px 0;padding-left:12px;border-left:4px solid var(--line);color:var(--muted)}.formatted-text pre{max-width:100%;overflow:auto;margin:6px 0;padding:10px;border-radius:6px;background:var(--hover);white-space:pre}.formatted-text code{padding:1px 3px;border-radius:3px;background:var(--hover);font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.formatted-text pre code{padding:0;background:transparent}.formatted-text a{color:var(--action)}.slack-mention{padding:1px 3px;border-radius:3px;background:color-mix(in srgb,var(--action) 15%,transparent);color:var(--action)}
.home-actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.home-actions textarea{min-height:76px}.home-actions .block-action-options{width:100%}
.block-table-wrap{max-width:100%;overflow:auto}.block-table{border-collapse:collapse;width:max-content;min-width:100%;font-size:13px}.block-table th,.block-table td{border:1px solid var(--line);padding:6px 9px;vertical-align:top;white-space:pre-wrap}.block-table th{background:var(--hover);font-weight:700;text-align:left}
.message-block.card{max-width:560px;padding:0;border:1px solid var(--line);border-radius:10px;background:var(--panel);overflow:hidden}.block-card-hero{display:block;width:100%;max-height:280px;object-fit:cover}.block-card-content{padding:12px}.block-card-heading{display:flex;align-items:flex-start;gap:9px}.block-card-heading>div{display:grid;gap:2px}.block-card-icon{width:36px;height:36px;border-radius:6px;object-fit:cover}.block-card-title,.block-card-subtitle{display:block}.block-card-subtitle,.block-card-subtext{color:var(--muted);font-size:13px}.block-card-body{margin-top:10px}.block-card-subtext{margin-top:8px}.message-block.carousel{max-width:min(720px,100%);overflow-x:auto;padding-bottom:6px}.block-carousel-track{display:flex;gap:10px;scroll-snap-type:x mandatory}.block-carousel-card{min-width:min(320px,80vw);max-width:360px;border:1px solid var(--line);border-radius:10px;background:var(--panel);overflow:hidden;scroll-snap-align:start}
.message-block.task-card{max-width:560px;padding:10px 12px;border:1px solid var(--line);border-radius:8px;background:var(--panel)}.message-block.task-card.error{border-color:var(--danger)}.message-block.task-card.plan{border-left:4px solid var(--accent)}.message-block.task-card.dense{padding:6px 10px;font-size:13px}.stream-task-title{display:flex;align-items:center;justify-content:space-between;gap:12px}.stream-task-status{color:var(--muted);font-size:12px;font-weight:700}.stream-task-details,.stream-task-output{margin-top:6px;color:var(--muted)}.stream-task-sources{display:flex;flex-wrap:wrap;gap:8px;margin:6px 0 0;padding:0;list-style:none}
.message-block.plan{max-width:620px}.block-plan{border:1px solid var(--line);border-radius:9px;background:var(--panel);overflow:hidden}.block-plan-title{display:block;padding:10px 12px;border-bottom:1px solid var(--line)}.block-plan-tasks{display:grid}.block-plan-task{padding:10px 12px;border-bottom:1px solid var(--line)}.block-plan-task:last-child{border-bottom:0}
.external-select{display:flex;flex-wrap:wrap;align-items:center;gap:6px}.external-select-status{width:100%;margin:0;color:var(--muted);font-size:12px}
.home-actions .block-action,.home-actions select,.home-actions input,.home-actions textarea{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:8px 10px;font:inherit}
.home-actions button.block-action{font-weight:800}.home-actions button.block-action:hover{background:var(--hover)}
.home-empty,.app-about{width:min(680px,calc(100% - 32px));margin:36px auto;padding:26px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}
.home-empty h2,.app-about h2{margin-top:0}.muted{color:var(--muted)}
.directory{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:14px;padding:28px}.directory-card{display:grid;grid-template-columns:44px minmax(0,1fr);gap:12px;padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel);color:var(--text);text-decoration:none}.directory-card:hover{border-color:var(--action)}.directory-card .app-avatar{width:44px;height:44px;background:var(--accent);color:var(--on-accent)}.directory-card h2{margin:0;font-size:16px}.directory-card p{margin:5px 0;color:var(--muted)}
@media(max-width:720px){.apps-workspace{grid-template-columns:minmax(0,1fr)}.apps-sidebar{padding:10px}.apps-sidebar h2{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)}.installed-apps{display:flex;overflow:auto}.installed-app{min-width:180px}.developer-link{margin:10px}.apps-heading,.app-tabs{padding-left:14px;padding-right:14px}.directory{padding:16px}.app-home{width:calc(100% - 24px)}}
</style>{{end}}
{{define "content"}}
<div class="apps-shell">
  <header class="apps-topbar"><a href="/app?channel={{.Channel}}">← {{.WorkspaceName}}</a><span>Apps</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header>
  <div class="apps-workspace">
    <aside class="apps-sidebar" aria-label="Installed apps"><h2>Apps</h2><ul class="installed-apps">{{range .Apps}}<li><a class="installed-app" href="/app/apps/{{.ID}}?channel={{$.Channel}}"{{if and $.Selected (eq $.Selected.ID .ID)}} aria-current="page"{{end}}><span class="app-avatar" aria-hidden="true">{{slice .Name 0 1}}</span><span><strong>{{.Name}}</strong><small>{{if .HomeTabEnabled}}Home{{else}}About{{end}}</small></span></a></li>{{else}}<li class="apps-empty">No apps are installed in this workspace.</li>{{end}}</ul><a class="developer-link" href="/app/developer/apps">Developer apps</a></aside>
    <main class="apps-main">
    {{if .Selected}}
      <header class="apps-heading"><span class="app-avatar" aria-hidden="true">{{slice .Selected.Name 0 1}}</span><div><h1>{{.Selected.Name}}</h1><p>{{.Selected.BotDisplayName}}</p></div></header>
      <nav class="app-tabs" aria-label="{{.Selected.Name}}"><a class="app-tab" href="/app/apps/{{.Selected.ID}}?channel={{.Channel}}&tab=home"{{if eq .Tab "home"}} aria-current="page"{{end}}>Home</a>{{if and .Selected.MessagesTabEnabled .Selected.BotUserID .CanMessage}}<form class="app-tab-form" method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="users" value="{{.Selected.BotUserID}}"><button class="app-tab" type="submit">Messages</button></form>{{end}}<a class="app-tab" href="/app/apps/{{.Selected.ID}}?channel={{.Channel}}&tab=about"{{if eq .Tab "about"}} aria-current="page"{{end}}>About</a></nav>
      {{if .Notice}}<p class="notice app-notice" role="status">{{.Notice}}</p>{{end}}
      {{if eq .Tab "about"}}<section class="app-about"><h2>About {{.Selected.Name}}</h2>{{if .Selected.Description}}<p>{{.Selected.Description}}</p>{{else}}<p class="muted">This app has not provided a description.</p>{{end}}<dl><dt>App ID</dt><dd><code>{{.Selected.ID}}</code></dd><dt>Bot name</dt><dd>{{.Selected.BotDisplayName}}</dd></dl></section>
      {{else if .Published}}<form class="app-home" method="post" action="/app/apps/{{.Selected.ID}}/action?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="view_id" value="{{.Home.ID}}">
        {{range $block := .Home.Blocks}}{{if eq $block.Kind "divider"}}<hr class="home-block divider">{{else}}<section class="home-block message-block {{$block.Kind}}">{{if $block.HTML}}<div class="formatted-text">{{$block.HTML}}</div>{{else if $block.Text}}<div>{{$block.Text}}</div>{{end}}{{if $block.Fields}}<ul class="message-block-fields">{{range $index, $field := $block.Fields}}<li>{{with index $block.FieldHTML $index}}{{.}}{{else}}{{$field}}{{end}}</li>{{end}}</ul>{{end}}{{if $block.Table}}<div class="block-table-wrap"><table class="block-table">{{if $block.Caption}}<caption>{{$block.Caption}}</caption>{{end}}<tbody>{{range $rowIndex, $row := $block.Table}}<tr>{{range $cell := $row}}{{if and $block.HeaderRow (eq $rowIndex 0)}}<th scope="col">{{$cell}}</th>{{else}}<td>{{$cell}}</td>{{end}}{{end}}</tr>{{end}}</tbody></table></div>{{end}}{{if $block.ImageURL}}<img class="message-media" src="{{$block.ImageURL}}" alt="{{$block.ImageAlt}}" loading="lazy">{{end}}
          {{if $block.Actions}}<div class="home-actions" aria-label="App actions">{{range $action := $block.Actions}}
            {{if eq $action.Control "button"}}{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>{{$action.Text}}</button>{{end}}
            {{else if eq $action.Control "date"}}<label><span class="visually-hidden">{{$action.Text}}</span><input class="block-action" type="date" name="action_{{$action.Index}}" value="{{$action.Value}}"></label>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Choose</button>{{end}}
            {{else if eq $action.Control "time"}}<label><span class="visually-hidden">{{$action.Text}}</span><input class="block-action" type="time" name="action_{{$action.Index}}" value="{{$action.Value}}"></label>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Choose</button>{{end}}
            {{else if eq $action.Control "datetime"}}<label><span class="visually-hidden">{{$action.Text}}</span><input class="block-action" type="datetime-local" name="action_{{$action.Index}}" value="{{$action.Value}}"></label>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Choose</button>{{end}}
            {{else if eq $action.Control "radio"}}<fieldset class="block-action-options"><legend class="visually-hidden">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="radio" name="action_{{$action.Index}}" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}> {{$option.Text}}</label>{{end}}</fieldset>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Choose</button>{{end}}
            {{else if eq $action.Control "checkbox"}}<fieldset class="block-action-options"><legend class="visually-hidden">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="checkbox" name="action_{{$action.Index}}" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}> {{$option.Text}}</label>{{end}}</fieldset>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Choose</button>{{end}}
            {{else if or (eq $action.Control "text") (eq $action.Control "textarea") (eq $action.Control "email") (eq $action.Control "url") (eq $action.Control "number")}}{{if eq $action.Control "textarea"}}<textarea class="block-action" name="action_{{$action.Index}}" placeholder="{{$action.Text}}">{{$action.Value}}</textarea>{{else}}<input class="block-action" type="{{$action.Control}}" name="action_{{$action.Index}}" value="{{$action.Value}}" placeholder="{{$action.Text}}">{{end}}{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Send</button>{{end}}
            {{else if eq $action.Control "external"}}<div class="external-select" data-app-options data-app-id="{{$.Selected.ID}}" data-view-id="{{$.Home.ID}}" data-block-id="{{$action.BlockID}}" data-action-id="{{$action.ActionID}}" data-channel="{{$.Channel}}" data-min-query="{{$action.MinQueryLength}}"><label><span class="visually-hidden">{{$action.Text}}</span><input class="block-action" type="search" data-options-query placeholder="{{$action.Text}}" minlength="{{$action.MinQueryLength}}"></label><button class="block-action" type="button" data-options-load>Search</button><label><span class="visually-hidden">Results</span><select class="block-action block-action-select" name="action_{{$action.Index}}" data-options-results{{if $action.Multiple}} multiple{{end}}{{if not $action.Options}} disabled{{end}}>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" data-options-choose formnovalidate{{if not $action.Options}} disabled{{end}}>Choose</button>{{end}}<p class="external-select-status" data-options-status role="status"></p><noscript>Dynamic options require JavaScript in this client.</noscript></div>
            {{else}}<label><span class="visually-hidden">{{$action.Text}}</span><select class="block-action block-action-select" name="action_{{$action.Index}}"{{if $action.Multiple}} multiple{{end}}>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label>{{if $action.Dispatch}}<button class="block-action" type="submit" name="home_action" value="{{$action.Index}}" formnovalidate>Choose</button>{{end}}{{end}}
          {{end}}</div>{{end}}</section>{{end}}{{end}}
      </form>{{else}}<section class="home-empty"><h2>Nothing here yet</h2><p class="muted">{{.Selected.Name}} has not published a Home view for you.</p></section>{{end}}
    {{else}}<header class="apps-heading"><div><h1>Apps</h1><p>Open an installed app’s Home tab or learn what it does.</p></div></header><section class="directory">{{range .Apps}}<a class="directory-card" href="/app/apps/{{.ID}}?channel={{$.Channel}}"><span class="app-avatar" aria-hidden="true">{{slice .Name 0 1}}</span><span><h2>{{.Name}}</h2><p>{{if .Description}}{{.Description}}{{else}}{{.BotDisplayName}}{{end}}</p></span></a>{{else}}<div class="home-empty"><h2>No apps installed</h2><p class="muted">Install an app through its OAuth flow to see it here.</p></div>{{end}}</section>{{end}}
    </main>
  </div>
</div>
{{end}}
{{define "scripts"}}` + appOptionsScript + `{{end}}`

var workspaceAppsTemplate = mustPage(workspaceAppsMarkup)

func (h Handler) workspaceApps(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	h.renderWorkspaceApps(w, r, principal, csrf, "")
}

func (h Handler) appHome(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.developerPrincipal(w, r)
	if !ok {
		return
	}
	h.renderWorkspaceApps(w, r, principal, csrf, strings.TrimSpace(r.PathValue("appID")))
}

func (h Handler) renderWorkspaceApps(w http.ResponseWriter, r *http.Request, principal auth.Principal, csrf, selectedID string) {
	apps, err := h.Messages.ListWorkspaceApps(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writePageError(w, http.StatusServiceUnavailable, "Apps are temporarily unavailable", "Installed apps could not be read. Try again.")
		return
	}
	workspaceName := "SameOldChat"
	if workspace, workspaceErr := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID); workspaceErr == nil && strings.TrimSpace(workspace.Name) != "" {
		workspaceName = strings.TrimSpace(workspace.Name)
	}
	data := workspaceAppsData{
		Apps: apps, Channel: string(h.requestChannel(r)), CSRFToken: csrf,
		CanMessage: principal.HasScope(auth.ScopeChannelsManage), WorkspaceName: workspaceName,
	}
	if selectedID == "" {
		h.writeHTML(w, workspaceAppsTemplate, data, http.StatusOK, "installed apps rendering unavailable")
		return
	}
	for index := range apps {
		if apps[index].ID == domain.AppID(selectedID) {
			data.Selected = &apps[index]
			break
		}
	}
	if data.Selected == nil {
		h.writePageError(w, http.StatusNotFound, "That app is not installed", "It may have been removed from this workspace.")
		return
	}
	data.Tab = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	if data.Tab == "" {
		if data.Selected.HomeTabEnabled {
			data.Tab = "home"
		} else {
			data.Tab = "about"
		}
	}
	if data.Tab != "home" && data.Tab != "about" {
		h.writePageError(w, http.StatusBadRequest, "That app tab is not available", "Open the app’s Home or About tab.")
		return
	}
	if data.Tab == "home" {
		if !data.Selected.HomeTabEnabled {
			h.writePageError(w, http.StatusNotFound, "This app has no Home tab", "Open its About tab instead.")
			return
		}
		_, view, err := h.Messages.OpenAppHome(r.Context(), principal.WorkspaceID, principal.UserID, data.Selected.ID)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, store.ErrNotFound) {
				status = http.StatusNotFound
			}
			h.writePageError(w, status, "This app’s Home is unavailable", "The app may have been removed or its Home view could not be read.")
			return
		}
		if view.ID != "" {
			data.Home, err = h.newHomeView(r.Context(), principal, view)
			if err != nil {
				h.writePageError(w, http.StatusBadGateway, "This app published an invalid Home", "The view could not be rendered safely.")
				return
			}
			data.Published = true
		}
	}
	if r.URL.Query().Get("notice") == "action_sent" {
		data.Notice = "The app action ran."
	}
	h.writeHTML(w, workspaceAppsTemplate, data, http.StatusOK, "app home rendering unavailable")
}

func (h Handler) newHomeView(ctx context.Context, principal auth.Principal, value domain.View) (*modalView, error) {
	var envelope struct {
		Type   string           `json:"type"`
		Blocks []map[string]any `json:"blocks"`
	}
	if json.Unmarshal([]byte(value.Payload), &envelope) != nil || envelope.Type != "home" {
		return nil, errors.New("stored App Home view is invalid")
	}
	var persisted struct {
		Values map[string]map[string]map[string]any `json:"values"`
	}
	if strings.TrimSpace(value.State) != "" {
		_ = json.Unmarshal([]byte(value.State), &persisted)
	}
	result := &modalView{ID: string(value.ID), AppID: string(value.AppID)}
	catalog := appActionOptionCatalog{}
	actionIndex := 0
	for _, raw := range envelope.Blocks {
		blockID := strings.TrimSpace(stringValue(raw["block_id"]))
		block, ok := newMessageBlockView(raw)
		if !ok {
			continue
		}
		holder := []messageBlockView{block}
		catalog.enrich(ctx, h, principal, holder)
		block = holder[0]
		rendered := modalBlockView{
			ID: blockID, Kind: block.Kind, Text: block.Text, HTML: block.HTML,
			Fields: block.Fields, FieldHTML: block.FieldHTML,
			ImageURL: block.ImageURL, ImageAlt: block.ImageAlt, Table: block.Table,
			Caption: block.Caption, HeaderRow: block.HeaderRow,
		}
		for _, action := range block.Actions {
			if actionState := persisted.Values[blockID][action.ActionID]; actionState != nil {
				if values, ok := modalActionValues(action.Type, actionState); ok {
					action.InitialValues = append([]string(nil), values...)
					action.Value = firstValue(values)
					markSelectedOptions(action.Options, values)
				}
			}
			rendered.Actions = append(rendered.Actions, modalActionView{Index: actionIndex, messageActionView: action})
			actionIndex++
		}
		result.Blocks = append(result.Blocks, rendered)
	}
	return result, nil
}

func (h Handler) appHomeAction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, err)
		return
	}
	values, ok := h.decodeModalMutation(w, r)
	if !ok {
		return
	}
	if len(values["home_action"]) != 1 {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the app Home and try again.")
		return
	}
	if len(values["view_id"]) != 1 || strings.TrimSpace(values["view_id"][0]) == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app Home could not be read", "Reload the app and try again.")
		return
	}
	actionIndex, err := strconv.Atoi(strings.TrimSpace(values["home_action"][0]))
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the app Home and try again.")
		return
	}
	appID := domain.AppID(strings.TrimSpace(r.PathValue("appID")))
	_, current, err := h.Messages.AppHome(r.Context(), principal.WorkspaceID, principal.UserID, appID)
	viewID := domain.ViewID(strings.TrimSpace(values["view_id"][0]))
	if err != nil || current.ID == "" || current.ID != viewID {
		h.writeMutationError(w, r, http.StatusNotFound, "That app Home has changed", "Reload the app and try the action again.")
		return
	}
	rendered, err := h.newHomeView(r.Context(), principal, current)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadGateway, "That app Home is invalid", "The app supplied a view that could not be rendered safely.")
		return
	}
	action, exists := modalActionAt(rendered, actionIndex)
	if !exists {
		h.writeMutationError(w, r, http.StatusNotFound, "That app action is no longer available", "The app changed its Home. Reload it and try again.")
		return
	}
	stateJSON, _, err := modalStateJSON(rendered, values)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Review the fields and try again.")
		return
	}
	selected := append([]string(nil), values[fmt.Sprintf("action_%d", action.Index)]...)
	value, err := modalActionDispatchValue(action, selected)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose a valid value", "The app action was not sent.")
		return
	}
	err = h.Messages.DispatchViewBlockAction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), domain.AppViewBlockAction{
		ViewID: viewID, BlockID: action.BlockID, ActionID: action.ActionID,
		Type: action.Type, Value: value, State: stateJSON,
	}, h.responseBaseURL(r))
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadGateway, "The app action did not run", modalInteractionError(err))
		return
	}
	target := "/app/apps/" + string(appID) + "?channel=" + string(h.requestChannel(r)) + "&notice=action_sent"
	http.Redirect(w, r, target, http.StatusSeeOther)
}
