package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type workflowFunctionOption struct {
	AppID       string
	AppName     string
	CallbackID  string
	Title       string
	Description string
}

type workflowAppOption struct {
	ID   string
	Name string
}

type workflowCardView struct {
	ID          string
	Title       string
	Description string
	Status      string
	Version     uint64
	Owned       bool
	UpdatedAt   string
}

type workflowStepSlot struct {
	Number            int
	Selected          string
	Change            string
	Mapping           string
	ConditionSource   string
	ConditionOperator string
	ConditionValue    string
}

type workflowDecodedStep struct {
	Callback          string
	Mapping           string
	ConditionSource   string
	ConditionOperator string
	ConditionValue    string
}

func decodeWorkflowSteps(raw string) []workflowDecodedStep {
	var steps []struct {
		FunctionID   string            `json:"function_id"`
		InputMapping map[string]string `json:"input_mapping"`
		Condition    *struct {
			Source   string `json:"source"`
			Operator string `json:"operator"`
			Value    string `json:"value"`
		} `json:"condition"`
	}
	if json.Unmarshal([]byte(raw), &steps) != nil {
		return nil
	}
	result := make([]workflowDecodedStep, 0, len(steps))
	for _, step := range steps {
		decoded := workflowDecodedStep{Callback: step.FunctionID}
		if len(step.InputMapping) > 0 {
			if encoded, err := json.Marshal(step.InputMapping); err == nil {
				decoded.Mapping = string(encoded)
			}
		}
		if step.Condition != nil {
			decoded.ConditionSource = step.Condition.Source
			decoded.ConditionOperator = step.Condition.Operator
			decoded.ConditionValue = step.Condition.Value
		}
		result = append(result, decoded)
	}
	return result
}

type workflowRemovedStep struct {
	Position   int
	FunctionID string
	Title      string
}

type workflowTriggerView struct {
	ID              string
	Title           string
	Type            string
	Enabled         bool
	Version         uint64
	Config          string
	Summary         string
	WebhookURL      string
	NextRun         string
	IdempotencyKey  string
	CanRun          bool
	CanManage       bool
	WorkflowVersion uint64
}

type workflowActivityRunView struct {
	RunID     string
	Status    string
	Trigger   string
	Started   string
	Completed string
}

type workflowActivityView struct {
	Queued    int
	Running   int
	Completed int
	Failed    int
	Cancelled int
	Runs      []workflowActivityRunView
}

type workflowChannelOption struct {
	ID   string
	Name string
}

type workflowListOption struct {
	ID    string
	Title string
}

type workflowsData struct {
	CSRFToken string
	Notice    string
	Apps      []workflowAppOption
	Functions []workflowFunctionOption
	Workflows []workflowCardView
	MoreURL   string
}

type workflowData struct {
	CSRFToken       string
	Notice          string
	ID              string
	AppID           string
	Title           string
	Description     string
	CallbackID      string
	Status          string
	Version         uint64
	Published       uint64
	UpdatedAt       string
	Owned           bool
	InputSchema     string
	Functions       []workflowFunctionOption
	Channels        []workflowChannelOption
	Lists           []workflowListOption
	StepSlots       []workflowStepSlot
	RemovedSteps    []workflowRemovedStep
	StepCount       int
	Triggers        []workflowTriggerView
	Activity        workflowActivityView
	HasActivity     bool
	HasFunctions    bool
	HasInputSchema  bool
	PublishedStatus bool
	StagedEdits     bool
}

type workflowRunData struct {
	WorkflowID string
	RunID      string
	Status     string
	Version    uint64
	Inputs     string
	Outputs    string
	Error      string
	CreatedAt  string
	UpdatedAt  string
	Completed  string
}

const workflowsMarkup = `{{define "title"}}Workflows · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(980px,calc(100% - 32px));margin:28px auto 56px}.heading{display:flex;justify-content:space-between;gap:20px;align-items:start}.heading h2,.heading p{margin:0}.heading p{margin-top:5px;color:var(--muted)}
.create{margin:20px 0;padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.create summary{font-weight:800;cursor:pointer}.fields{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:16px}.fields label{display:grid;gap:6px;font-weight:700}.fields .wide{grid-column:1/-1}.fields input,.fields textarea,.fields select{box-sizing:border-box;width:100%;padding:9px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.fields textarea{min-height:90px;resize:vertical}.fields button{justify-self:start;border:0;border-radius:7px;padding:9px 14px;background:var(--action);color:var(--on-strong);font-weight:800}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(270px,1fr));gap:12px}.card{display:grid;gap:9px;min-height:145px;padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel);color:var(--text);text-decoration:none}.card:hover{border-color:var(--action);background:var(--hover)}.card h3,.card p{margin:0}.card p{color:var(--muted)}.meta{display:flex;gap:8px;align-self:end;color:var(--muted);font-size:12px}.pill{padding:2px 7px;border-radius:999px;background:var(--panel-strong);font-weight:800}.empty{padding:32px;border:1px dashed var(--line);border-radius:10px;text-align:center;color:var(--muted)}.problem{padding:14px;border:1px solid var(--warning);border-radius:8px;background:var(--panel)}
@media(max-width:620px){.fields{grid-template-columns:1fr}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `<script>(function(){var app=document.getElementById('workflow-app');var fn=document.getElementById('workflow-function');if(!app||!fn)return;function sync(){var selected=app.value;Array.prototype.forEach.call(fn.options,function(option){if(!option.dataset.app)return;option.hidden=option.dataset.app!==selected;option.disabled=option.dataset.app!==selected});if(fn.selectedOptions.length&&fn.selectedOptions[0].disabled)fn.value=''}app.addEventListener('change',sync);sync()})();</script>{{end}}
{{define "content"}}<header class="bar"><a href="/app">← Back to chat</a><h1>Workflows</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">
<div class="heading"><div><h2>Workflows</h2><p>Build, publish, and run durable automations backed by installed app functions.</p></div></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Functions}}<details class="create"><summary>Create a workflow</summary><form class="fields" method="post" action="/app/workflows/create"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label>Name<input name="title" maxlength="255" required></label><label>Owning app<select id="workflow-app" name="app_id" required><option value="">Choose an app</option>{{range .Apps}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label><label class="wide">Description<textarea name="description" maxlength="2000"></textarea></label><label class="wide">First step<select id="workflow-function" name="function_callback" required><option value="">Choose a function</option>{{range .Functions}}<option value="{{.CallbackID}}" data-app="{{.AppID}}">{{.AppName}} · {{.Title}}</option>{{end}}</select></label><label>Workflow reference<input name="callback_id" maxlength="255" placeholder="triage-request"></label><button type="submit">Create workflow</button></form></details>{{else}}<p class="problem">Workflow Builder needs a developer app with at least one manifest function. <a href="/app/developer/apps">Create or update an app manifest</a>, then return here.</p>{{end}}
<div class="grid">{{range .Workflows}}<a class="card" href="/app/workflows/{{.ID}}"><h3>{{.Title}}</h3><p>{{if .Description}}{{.Description}}{{else}}No description{{end}}</p><div class="meta"><span class="pill">{{.Status}}</span><span>v{{.Version}}</span>{{if .Owned}}<span>Owned by you</span>{{end}}<time datetime="{{.UpdatedAt}}">{{.UpdatedAt}}</time></div></a>{{else}}<p class="empty">No workflows are available yet.</p>{{end}}</div>{{if .MoreURL}}<p><a href="{{.MoreURL}}">Show more workflows</a></p>{{end}}</main>{{end}}`

var workflowsTemplate = mustPage(workflowsMarkup)

const workflowMarkup = `{{define "title"}}{{.Title}} · Workflow · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(920px,calc(100% - 32px));margin:28px auto 60px}.hero{display:flex;align-items:start;justify-content:space-between;gap:18px}.hero h2,.hero p{margin:0}.hero p{margin-top:6px;color:var(--muted)}.status{padding:5px 10px;border-radius:999px;background:var(--panel-strong);font-weight:800;text-transform:capitalize}
.panel{margin-top:18px;padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.panel h3{margin:0 0 6px}.panel>p{margin:0 0 14px;color:var(--muted)}.fields{display:grid;grid-template-columns:1fr 1fr;gap:12px}.fields label{display:grid;gap:6px;font-weight:700}.fields .wide{grid-column:1/-1}.fields input,.fields textarea,.fields select{box-sizing:border-box;width:100%;padding:9px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.fields textarea{min-height:90px;resize:vertical}.actions{display:flex;gap:9px;flex-wrap:wrap;grid-column:1/-1}.actions button,.run button{border:0;border-radius:7px;padding:9px 14px;background:var(--action);color:var(--on-strong);font-weight:800}.actions .secondary{background:var(--panel-strong);color:var(--text);border:1px solid var(--field-line)}.actions form{margin:0}
.step-list,.trigger-list{display:grid;gap:10px;margin:12px 0}.step{display:grid;grid-template-columns:34px minmax(0,1fr);gap:10px;align-items:center}.step b{display:grid;place-items:center;width:28px;height:28px;border-radius:50%;background:var(--accent);color:var(--on-accent)}.trigger{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:12px;padding:13px;border:1px solid var(--line);border-radius:8px}.trigger h4,.trigger p{margin:0}.trigger p{color:var(--muted);font-size:13px}.trigger-actions{display:flex;gap:7px;align-items:center}.trigger-actions form{margin:0}.trigger-actions button{padding:7px 10px;border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);font-weight:800}.trigger-actions .run button{background:var(--action);color:var(--on-strong);border-color:transparent}.empty{padding:18px;border:1px dashed var(--line);border-radius:8px;color:var(--muted)}
.step-change{margin-left:8px;padding:2px 8px;border-radius:999px;background:var(--warning);font-size:12px;font-weight:800;text-transform:capitalize}
.removed-steps{display:grid;gap:6px;margin-top:10px;padding:10px;border:1px dashed var(--line);border-radius:8px;color:var(--muted);font-size:13px}.removed-steps strong{color:var(--text)}
.weekdays{display:flex;gap:12px;flex-wrap:wrap;margin-top:6px}.weekday{display:flex;gap:5px;align-items:center;font-weight:400}[data-frequency-config]{margin-top:10px}[data-frequency-config] p{margin:0;color:var(--muted);font-size:13px}
.activity-counts{display:flex;gap:16px;flex-wrap:wrap;margin:2px 0 6px;color:var(--muted);font-size:13px}.activity-counts b{color:var(--text);font-size:16px}.activity-status{padding:3px 9px;border-radius:999px;background:var(--panel-strong);font-weight:800;text-transform:capitalize;font-size:12px}.run-link{color:var(--action);font-weight:800;text-decoration:none}
.step-condition{display:flex;gap:6px;margin-top:5px}.step-condition input,.step-condition select{padding:6px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text);font-size:12px}.step-condition input{flex:1;min-width:0}
.step-mapping{margin-top:5px;font-family:monospace;font-size:11px;padding:6px!important;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}
@media(max-width:650px){.fields{grid-template-columns:1fr}.fields .wide{grid-column:1}.trigger{grid-template-columns:1fr}.trigger-actions{flex-wrap:wrap}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `<script>(function(){var type=document.getElementById('trigger-type');if(!type)return;var configs=document.querySelectorAll('[data-trigger-config]');function sync(){Array.prototype.forEach.call(configs,function(node){var show=node.getAttribute('data-trigger-config').split(' ').indexOf(type.value)!==-1;node.hidden=!show;node.disabled=!show;Array.prototype.forEach.call(node.querySelectorAll('[data-required]'),function(input){input.required=show})});}type.addEventListener('change',sync);sync()})();</script><script>(function(){var frequency=document.getElementById('schedule-frequency');if(!frequency)return;var configs=document.querySelectorAll('[data-frequency-config]');function sync(){Array.prototype.forEach.call(configs,function(node){var show=node.getAttribute('data-frequency-config')===frequency.value;node.hidden=!show;Array.prototype.forEach.call(node.querySelectorAll('input'),function(input){input.disabled=!show})});}frequency.addEventListener('change',sync);sync()})();</script>{{end}}
{{define "content"}}<header class="bar"><a href="/app/workflows">← Workflows</a><h1>Workflow Builder</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}<div class="hero"><div><h2>{{.Title}}</h2><p>{{if .Description}}{{.Description}}{{else}}No description{{end}} · version {{.Version}}{{if .Published}}, published version {{.Published}}{{end}}{{if .StagedEdits}} · your staged changes are not yet published{{end}}</p></div><span class="status">{{.Status}}</span></div>
{{if .Owned}}<section class="panel" aria-labelledby="builder-heading"><h3 id="builder-heading">Build workflow</h3><p>Steps run from top to bottom. Publishing makes the current version available to its enabled triggers; unpublished workflows can retain draft changes.</p><form class="fields" method="post" action="/app/workflows/{{.ID}}/update"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="version" value="{{.Version}}"><input type="hidden" name="step_count" value="{{.StepCount}}"><label>Name<input name="title" maxlength="255" value="{{.Title}}" required></label><label>Workflow reference<input name="callback_id" maxlength="255" value="{{.CallbackID}}"></label><label class="wide">Description<textarea name="description" maxlength="2000">{{.Description}}</textarea></label><label class="wide">Input metadata (JSON object; syntax validation only)<textarea name="input_schema" spellcheck="false">{{.InputSchema}}</textarea></label><fieldset class="wide"><legend>Steps</legend><div class="step-list">{{range .StepSlots}}{{$slot := .}}<label class="step"><b aria-hidden="true">{{.Number}}</b><span><span class="visually-hidden">Step {{.Number}}{{if .Change}} · {{.Change}}{{end}}</span><select name="step_{{.Number}}"{{if eq .Number 1}} required{{end}}><option value="">{{if eq .Number 1}}Choose a function{{else}}No step{{end}}</option>{{range $.Functions}}<option value="{{.CallbackID}}"{{if eq .CallbackID $slot.Selected}} selected{{end}}>{{.Title}} · {{.CallbackID}}</option>{{end}}</select><input class="step-mapping" name="mapping_{{.Number}}" value="{{.Mapping}}" maxlength="2000" spellcheck="false" placeholder="Input mapping: {&quot;item&quot;:&quot;inputs.item&quot;,&quot;prev&quot;:&quot;steps.id.outputs.x&quot;}" aria-label="Step {{.Number}} input mapping">{{if .Change}}<span class="step-change" data-step-change="{{.Number}}" aria-label="Step {{.Number}} {{.Change}}">{{.Change}}</span>{{end}}</span><span class="step-condition"><span class="visually-hidden">Step {{.Number}} condition</span><input name="condition_source_{{.Number}}" value="{{.ConditionSource}}" maxlength="255" placeholder="Only run if: inputs.flag or steps.id.outputs.name" aria-label="Step {{.Number}} condition source"><select name="condition_operator_{{.Number}}" aria-label="Step {{.Number}} condition operator"><option value="">always runs</option><option value="equals"{{if eq .ConditionOperator "equals"}} selected{{end}}>equals</option><option value="not_equals"{{if eq .ConditionOperator "not_equals"}} selected{{end}}>does not equal</option><option value="contains"{{if eq .ConditionOperator "contains"}} selected{{end}}>contains</option><option value="greater_than"{{if eq .ConditionOperator "greater_than"}} selected{{end}}>is greater than</option><option value="less_than"{{if eq .ConditionOperator "less_than"}} selected{{end}}>is less than</option></select><input name="condition_value_{{.Number}}" value="{{.ConditionValue}}" maxlength="255" placeholder="value" aria-label="Step {{.Number}} condition value"></span></label>{{end}}</div>{{if .RemovedSteps}}<div class="removed-steps"><strong>Removed from the published version</strong>{{range .RemovedSteps}}<span class="removed-step" data-removed-step="{{.Position}}">{{.Title}} · {{.FunctionID}}</span>{{end}}</div>{{end}}</fieldset><div class="actions">{{if .PublishedStatus}}{{if .StagedEdits}}<button class="secondary" name="action" value="discard" type="submit">Discard changes</button>{{end}}<button name="action" value="save" type="submit">Save staged changes</button><button name="action" value="publish" type="submit">Publish changes</button><button class="secondary" name="action" value="unpublish" type="submit">Unpublish</button>{{else}}<button name="action" value="save" type="submit">Save draft</button><button name="action" value="publish" type="submit">Publish</button>{{end}}</div></form><div class="actions"><form method="post" action="/app/workflows/{{.ID}}/copy"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="secondary" type="submit">Copy workflow</button></form><form method="post" action="/app/workflows/{{.ID}}/delete"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="version" value="{{.Version}}"><button class="secondary" type="submit">Delete workflow</button></form></div></section>
 <section class="panel" aria-labelledby="trigger-heading"><h3 id="trigger-heading">Triggers</h3><p>Link and shortcut triggers start from a conversation. Scheduled, webhook, message, reaction, join, and list triggers start from their configured condition once the workflow is published.</p><form class="fields" method="post" action="/app/workflows/{{.ID}}/triggers"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label>Trigger name<input name="title" maxlength="255" required></label><label>Trigger type<select id="trigger-type" name="type"><option value="link">Link</option><option value="shortcut">Shortcut</option><option value="scheduled">On a schedule</option><option value="webhook">From a webhook</option><option value="message">When a message is posted</option><option value="reaction">When an emoji reaction is used</option><option value="join">When a person joins a channel</option>{{if .Lists}}<option value="list">When a list record changes</option>{{end}}</select></label><fieldset class="wide trigger-config" data-trigger-config="scheduled"><legend>Schedule</legend><div class="fields"><label>Starts at<input type="datetime-local" name="schedule_start" step="60" data-required></label><label>Time zone<input name="schedule_timezone" maxlength="64" value="UTC"></label><label>Repeats<select id="schedule-frequency" name="schedule_frequency"><option value="hourly">Hourly</option><option value="daily" selected>Daily</option><option value="weekly">Weekly</option><option value="monthly">Monthly</option></select></label><label>Every<input type="number" name="schedule_interval" min="1" max="366" value="1"></label></div><div data-frequency-config="weekly"><p>On days (leave every day unchecked to repeat on the start day)</p><div class="weekdays"><label class="weekday"><input type="checkbox" name="schedule_weekday_mon" value="1"> Mon</label><label class="weekday"><input type="checkbox" name="schedule_weekday_tue" value="1"> Tue</label><label class="weekday"><input type="checkbox" name="schedule_weekday_wed" value="1"> Wed</label><label class="weekday"><input type="checkbox" name="schedule_weekday_thu" value="1"> Thu</label><label class="weekday"><input type="checkbox" name="schedule_weekday_fri" value="1"> Fri</label><label class="weekday"><input type="checkbox" name="schedule_weekday_sat" value="1"> Sat</label><label class="weekday"><input type="checkbox" name="schedule_weekday_sun" value="1"> Sun</label></div></div><div data-frequency-config="monthly"><label>Day of month (optional; shorter months fire on their last day)<input type="number" name="schedule_day" min="1" max="31"></label></div></fieldset><fieldset class="wide trigger-config" data-trigger-config="webhook"><legend>Webhook</legend><p>Create the trigger to generate its POST URL. The URL is revealed here to the workflow owner.</p></fieldset><fieldset class="wide trigger-config" data-trigger-config="message"><legend>Message event</legend><div class="fields"><label>Channel<select name="event_channel">{{range .Channels}}<option value="{{.ID}}">#{{.Name}}</option>{{end}}</select></label><label>Keyword (optional)<input name="event_keyword" maxlength="255"></label></div></fieldset><fieldset class="wide trigger-config" data-trigger-config="reaction"><legend>Reaction event</legend><div class="fields"><label>Channel<select name="event_channel_reaction">{{range .Channels}}<option value="{{.ID}}">#{{.Name}}</option>{{end}}</select></label><label>Emoji name (optional)<input name="event_reaction" maxlength="255" placeholder="eyes"></label></div></fieldset><fieldset class="wide trigger-config" data-trigger-config="join"><legend>Join event</legend><div class="fields"><label>Channel<select name="event_channel_join">{{range .Channels}}<option value="{{.ID}}">#{{.Name}}</option>{{end}}</select></label></div></fieldset>{{if .Lists}}<fieldset class="wide trigger-config" data-trigger-config="list"><legend>List record event</legend><div class="fields"><label>List<select name="list_id">{{range .Lists}}<option value="{{.ID}}">{{.Title}}</option>{{end}}</select></label><label>Fires when<select name="list_event"><option value="created">A record is created</option><option value="updated">A record is updated</option></select></label></div></fieldset>{{end}}<button type="submit">Create trigger</button></form></section>{{end}}
  <section class="panel" aria-labelledby="available-heading"><h3 id="available-heading">Available triggers</h3><div class="trigger-list">{{range .Triggers}}<article class="trigger"><div><h4>{{.Title}}</h4><p>{{.Type}} · {{if .Enabled}}enabled{{else}}disabled{{end}} · workflow v{{.WorkflowVersion}}</p>{{if .Summary}}<p>{{.Summary}}</p>{{end}}{{if .NextRun}}<p>Next run <time datetime="{{.NextRun}}">{{.NextRun}}</time></p>{{end}}{{if .WebhookURL}}<p>Webhook URL <code>{{.WebhookURL}}</code></p>{{end}}</div><div class="trigger-actions">{{if .CanRun}}<form class="run" method="post" action="/app/workflows/{{$.ID}}/triggers/{{.ID}}/run"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="idempotency_key" value="{{.IdempotencyKey}}">{{if $.HasInputSchema}}<label>Inputs (JSON)<textarea name="inputs">{}</textarea></label>{{end}}<button type="submit">Run</button></form>{{end}}{{if .CanManage}}<form method="post" action="/app/workflows/{{$.ID}}/triggers/{{.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="version" value="{{.Version}}"><input type="hidden" name="title" value="{{.Title}}"><input type="hidden" name="type" value="{{.Type}}"><input type="hidden" name="config" value="{{.Config}}"><input type="hidden" name="enabled" value="{{if .Enabled}}false{{else}}true{{end}}"><button type="submit">{{if .Enabled}}Disable{{else}}Enable{{end}}</button></form>{{end}}</div></article>{{else}}<p class="empty">No triggers have been configured.</p>{{end}}</div></section>
  {{if .HasActivity}}<section class="panel" aria-labelledby="activity-heading"><h3 id="activity-heading">Run activity</h3><div class="activity-counts">{{if .Activity.Queued}}<span><b>{{.Activity.Queued}}</b> queued</span>{{end}}<span><b>{{.Activity.Running}}</b> running</span><span><b>{{.Activity.Completed}}</b> completed</span><span><b>{{.Activity.Failed}}</b> failed</span><span><b>{{.Activity.Cancelled}}</b> cancelled</span></div>{{if .Activity.Runs}}<div class="trigger-list">{{range .Activity.Runs}}<article class="trigger" data-activity-run><div><h4>{{.Trigger}}</h4><p>Started <time datetime="{{.Started}}">{{.Started}}</time>{{if .Completed}} · completed <time datetime="{{.Completed}}">{{.Completed}}</time>{{end}}</p></div><div class="trigger-actions"><span class="activity-status">{{.Status}}</span><a class="run-link" href="/app/workflows/runs/{{.RunID}}">View</a></div></article>{{end}}</div>{{else}}<p class="empty">No runs yet.</p>{{end}}</section>{{end}}
 </main>{{end}}`

var workflowTemplate = mustPage(workflowMarkup)

const workflowRunMarkup = `{{define "title"}}Workflow run · SameOldChat{{end}}
{{define "styles"}}<style>.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}.layout{width:min(760px,calc(100% - 32px));margin:32px auto}.card{padding:22px;border:1px solid var(--line);border-radius:12px;background:var(--panel)}.card h2{margin-top:0}.status{font-weight:800;text-transform:capitalize}.facts{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:9px 16px}.facts dt{font-weight:800}.facts dd{margin:0;overflow-wrap:anywhere}.facts pre{white-space:pre-wrap;margin:0}.error{color:var(--danger)}</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app/workflows/{{.WorkflowID}}">← Workflow</a><h1>Run activity</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout"><article class="card"><h2>Workflow run</h2><p class="status">{{.Status}}</p><dl class="facts"><dt>Execution</dt><dd><code>{{.RunID}}</code></dd><dt>Version</dt><dd>{{.Version}}</dd><dt>Started</dt><dd><time datetime="{{.CreatedAt}}">{{.CreatedAt}}</time></dd><dt>Updated</dt><dd><time datetime="{{.UpdatedAt}}">{{.UpdatedAt}}</time></dd>{{if .Completed}}<dt>Completed</dt><dd><time datetime="{{.Completed}}">{{.Completed}}</time></dd>{{end}}<dt>Inputs</dt><dd><pre>{{.Inputs}}</pre></dd><dt>Outputs</dt><dd><pre>{{.Outputs}}</pre></dd>{{if .Error}}<dt>Error</dt><dd class="error">{{.Error}}</dd>{{end}}</dl>{{if eq .Status "running"}}<p role="status">An app function is running. Reload to see its latest durable state.</p>{{end}}</article></main>{{end}}`

var workflowRunTemplate = mustPage(workflowRunMarkup)

func (h Handler) workflowPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, string, bool) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		h.writeAuthError(w, err)
		return auth.Principal{}, "", false
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, err)
		return auth.Principal{}, "", false
	}
	return principal, csrf, true
}

func (h Handler) workflowOptions(ctx context.Context, principal auth.Principal, appFilter domain.AppID) ([]workflowAppOption, []workflowFunctionOption, error) {
	apps, err := h.Messages.ListDeveloperApps(ctx, principal.WorkspaceID, principal.UserID)
	if err != nil {
		return nil, nil, err
	}
	appOptions := make([]workflowAppOption, 0, len(apps))
	functions := make([]workflowFunctionOption, 0)
	for _, app := range apps {
		if appFilter != "" && app.ID != appFilter {
			continue
		}
		_, manifest, err := h.Messages.GetDeveloperApp(ctx, principal.WorkspaceID, principal.UserID, app.ID)
		if err != nil {
			return nil, nil, err
		}
		parsed, problems := appmanifest.Parse(manifest)
		if len(problems) != 0 || parsed.FunctionRuntime != "remote" {
			continue
		}
		before := len(functions)
		for callback, function := range parsed.Functions {
			functions = append(functions, workflowFunctionOption{
				AppID: string(app.ID), AppName: app.Name, CallbackID: callback,
				Title: function.Title, Description: function.Description,
			})
		}
		if len(functions) > before {
			appOptions = append(appOptions, workflowAppOption{ID: string(app.ID), Name: app.Name})
		}
	}
	sort.Slice(appOptions, func(left, right int) bool { return appOptions[left].Name < appOptions[right].Name })
	sort.Slice(functions, func(left, right int) bool {
		if functions[left].AppName != functions[right].AppName {
			return functions[left].AppName < functions[right].AppName
		}
		return functions[left].Title < functions[right].Title
	})
	return appOptions, functions, nil
}

func workflowFunctionExists(options []workflowFunctionOption, appID domain.AppID, callback string) bool {
	for _, option := range options {
		if option.AppID == string(appID) && option.CallbackID == callback {
			return true
		}
	}
	return false
}

func (h Handler) workflows(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	request := domain.PageRequest{Limit: 48, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))}
	values, more, next, err := h.Messages.ListWorkflows(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		h.writeStoreError(w, err, "Workflows are temporarily unavailable.")
		return
	}
	apps, functions, err := h.workflowOptions(r.Context(), principal, "")
	if err != nil {
		h.writeStoreError(w, err, "Workflow functions are temporarily unavailable.")
		return
	}
	cards := make([]workflowCardView, 0, len(values))
	for _, value := range values {
		cards = append(cards, workflowCardView{
			ID: string(value.ID), Title: value.Title, Description: value.Description,
			Status: string(value.Status), Version: value.Version, Owned: value.OwnerID == principal.UserID,
			UpdatedAt: value.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	moreURL := ""
	if more {
		moreURL = "/app/workflows?cursor=" + url.QueryEscape(string(next))
	}
	h.writeHTML(w, workflowsTemplate, workflowsData{
		CSRFToken: csrf, Notice: strings.TrimSpace(r.URL.Query().Get("notice")),
		Apps: apps, Functions: functions, Workflows: cards, MoreURL: moreURL,
	}, http.StatusOK, "workflow rendering unavailable")
}

func (h Handler) createWorkflow(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Workflows and try again.")
	if !ok {
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	callback := strings.TrimSpace(fields["function_callback"])
	_, options, err := h.workflowOptions(r.Context(), principal, appID)
	if err != nil || !workflowFunctionExists(options, appID, callback) {
		h.writeMutationError(w, r, http.StatusBadRequest, "The workflow was not created", "Choose a function belonging to the selected app.")
		return
	}
	steps, _ := json.Marshal([]map[string]string{{"function_id": callback, "title": workflowFunctionTitle(options, callback)}})
	value, err := h.Messages.CreateWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, domain.WorkflowDefinition{
		AppID: appID, CallbackID: strings.TrimSpace(fields["callback_id"]), Title: strings.TrimSpace(fields["title"]),
		Description: strings.TrimSpace(fields["description"]), InputSchema: `{}`, Steps: string(steps),
	})
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The workflow was not created", err)
		return
	}
	h.redirectMutation(w, r, "/app/workflows/"+url.PathEscape(string(value.ID))+"?notice=Draft+created")
}

func workflowFunctionTitle(options []workflowFunctionOption, callback string) string {
	for _, option := range options {
		if option.CallbackID == callback {
			return option.Title
		}
	}
	return callback
}

func workflowSlots(steps []workflowDecodedStep) []workflowStepSlot {
	count := max(5, len(steps)+1)
	result := make([]workflowStepSlot, count)
	for index := range result {
		result[index].Number = index + 1
		if index < len(steps) {
			result[index].Selected = steps[index].Callback
			result[index].Mapping = steps[index].Mapping
			result[index].ConditionSource = steps[index].ConditionSource
			result[index].ConditionOperator = steps[index].ConditionOperator
			result[index].ConditionValue = steps[index].ConditionValue
		}
	}
	return result
}

func (h Handler) workflowTriggerOptions(ctx context.Context, principal auth.Principal) ([]workflowChannelOption, []workflowListOption, error) {
	channels := make([]workflowChannelOption, 0)
	var cursor domain.Cursor
	for pageIndex := 0; pageIndex < 5; pageIndex++ {
		page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
			Limit: 200, Cursor: cursor, ExcludeArchived: true,
		})
		if err != nil {
			return nil, nil, err
		}
		for _, conversation := range page.Conversations {
			if conversation.IsDirect || conversation.IsGroupDirect {
				continue
			}
			channels = append(channels, workflowChannelOption{ID: string(conversation.ID), Name: conversationName(conversation)})
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(channels, func(left, right int) bool { return channels[left].Name < channels[right].Name })
	lists := make([]workflowListOption, 0)
	cursor = ""
	for pageIndex := 0; pageIndex < 5; pageIndex++ {
		page, err := h.Messages.Lists(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 100, Cursor: cursor})
		if err != nil {
			return nil, nil, err
		}
		for _, list := range page.Lists {
			lists = append(lists, workflowListOption{ID: string(list.ID), Title: list.Name})
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(lists, func(left, right int) bool { return lists[left].Title < lists[right].Title })
	return channels, lists, nil
}

type workflowTriggerConfigView struct {
	ChannelIDs []string `json:"channel_ids,omitempty"`
	Keyword    string   `json:"keyword,omitempty"`
	Reaction   string   `json:"reaction,omitempty"`
	ListID     string   `json:"list_id,omitempty"`
	Event      string   `json:"event,omitempty"`
	StartTime  string   `json:"start_time,omitempty"`
	Timezone   string   `json:"timezone,omitempty"`
	Frequency  struct {
		Type     string   `json:"type"`
		Interval int      `json:"interval,omitempty"`
		Weekdays []string `json:"weekdays,omitempty"`
		Day      int      `json:"day,omitempty"`
	} `json:"frequency,omitempty"`
}

func workflowTriggerSummary(trigger domain.WorkflowTrigger, channels []workflowChannelOption, lists []workflowListOption) string {
	var config workflowTriggerConfigView
	if err := json.Unmarshal([]byte(trigger.Config), &config); err != nil {
		return ""
	}
	channelName := func() string {
		if len(config.ChannelIDs) == 0 {
			return "unknown channel"
		}
		for _, channel := range channels {
			if channel.ID == config.ChannelIDs[0] {
				return "#" + channel.Name
			}
		}
		return "unknown channel"
	}
	switch trigger.Type {
	case "scheduled":
		interval := config.Frequency.Interval
		if interval == 0 {
			interval = 1
		}
		timezone := config.Timezone
		if timezone == "" {
			timezone = "UTC"
		}
		switch {
		case len(config.Frequency.Weekdays) > 0:
			days := strings.Join(config.Frequency.Weekdays, ", ")
			if interval == 1 {
				return fmt.Sprintf("Every week on %s · %s", days, timezone)
			}
			return fmt.Sprintf("Every %d weeks on %s · %s", interval, days, timezone)
		case config.Frequency.Day > 0:
			if interval == 1 {
				return fmt.Sprintf("Every month on day %d · %s", config.Frequency.Day, timezone)
			}
			return fmt.Sprintf("Every %d months on day %d · %s", interval, config.Frequency.Day, timezone)
		default:
			return fmt.Sprintf("Every %d %s · %s", interval, config.Frequency.Type, timezone)
		}
	case "webhook":
		return "Starts when the webhook URL receives a POST"
	case "message":
		if config.Keyword != "" {
			return fmt.Sprintf("New messages in %s containing %q", channelName(), config.Keyword)
		}
		return "New messages in " + channelName()
	case "reaction":
		if config.Reaction != "" {
			return fmt.Sprintf(":%s: reactions in %s", config.Reaction, channelName())
		}
		return "Reactions in " + channelName()
	case "join":
		return "People joining " + channelName()
	case "list":
		name := "unknown list"
		for _, list := range lists {
			if list.ID == config.ListID {
				name = list.Title
			}
		}
		if config.Event == "updated" {
			return "Records updated in " + name
		}
		return "Records created in " + name
	default:
		return ""
	}
}

func (h Handler) workflow(w http.ResponseWriter, r *http.Request) {
	principal, csrf, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	id := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	value, err := h.Messages.GetWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		h.writeStoreError(w, err, "That workflow is not available.")
		return
	}
	triggers, err := h.Messages.ListWorkflowTriggers(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		h.writeStoreError(w, err, "Workflow triggers are temporarily unavailable.")
		return
	}
	_, functions, err := h.workflowOptions(r.Context(), principal, value.AppID)
	if err != nil && value.OwnerID == principal.UserID {
		h.writeStoreError(w, err, "Workflow functions are temporarily unavailable.")
		return
	}
	channels := make([]workflowChannelOption, 0)
	lists := make([]workflowListOption, 0)
	if value.OwnerID == principal.UserID {
		channels, lists, err = h.workflowTriggerOptions(r.Context(), principal)
		if err != nil {
			h.writeStoreError(w, err, "Workflow trigger options are temporarily unavailable.")
			return
		}
	}
	triggerViews := make([]workflowTriggerView, 0, len(triggers))
	for _, trigger := range triggers {
		key, err := domain.PublicID("workflow_run_")
		if err != nil {
			h.writeStoreError(w, err, "Workflow run controls are temporarily unavailable.")
			return
		}
		canRun := false
		if trigger.Enabled && value.Status == domain.WorkflowPublished &&
			(trigger.Type == "link" || trigger.Type == "shortcut") {
			permission, err := h.Messages.GetTriggerPermission(r.Context(), principal.WorkspaceID, principal.UserID, value.AppID, trigger.ID)
			if err != nil {
				h.writeStoreError(w, err, "Workflow permissions are temporarily unavailable.")
				return
			}
			canRun = workflowPermissionAllows(permission, principal, value.OwnerID)
		}
		view := workflowTriggerView{
			ID: string(trigger.ID), Title: trigger.Title, Type: trigger.Type, Enabled: trigger.Enabled,
			Version: trigger.Version, Config: trigger.Config, IdempotencyKey: key, CanRun: canRun,
			CanManage: value.OwnerID == principal.UserID, WorkflowVersion: value.PublishedVersion,
		}
		if view.CanManage {
			view.Summary = workflowTriggerSummary(trigger, channels, lists)
			if !trigger.NextRunAt.IsZero() {
				view.NextRun = trigger.NextRunAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			}
			if trigger.Type == "webhook" {
				invokeURL, err := h.Messages.WebhookTriggerURL(r.Context(), principal.WorkspaceID, principal.UserID, trigger.ID)
				if err != nil {
					h.writeStoreError(w, err, "The webhook URL is temporarily unavailable.")
					return
				}
				view.WebhookURL = invokeURL
			}
		}
		triggerViews = append(triggerViews, view)
	}
	inputSchema := strings.TrimSpace(value.InputSchema)
	if inputSchema == "" {
		inputSchema = "{}"
	}
	slots := workflowSlots(decodeWorkflowSteps(value.Steps))
	removedSteps := make([]workflowRemovedStep, 0)
	activity := workflowActivityView{}
	owned := value.OwnerID == principal.UserID
	if owned {
		changes, err := h.Messages.WorkflowStepChanges(r.Context(), principal.WorkspaceID, principal.UserID, id)
		if err != nil {
			h.writeStoreError(w, err, "Workflow step changes are temporarily unavailable.")
			return
		}
		byPosition := make(map[int]domain.WorkflowStepChange, len(changes))
		for _, change := range changes {
			byPosition[change.Position] = change
		}
		for _, change := range changes {
			if change.Change == domain.WorkflowStepChangeRemoved {
				removedSteps = append(removedSteps, workflowRemovedStep{
					Position: change.Position, FunctionID: change.FunctionID,
					Title: workflowFunctionTitle(functions, change.FunctionID),
				})
			}
		}
		for index := range slots {
			if change, ok := byPosition[slots[index].Number]; ok {
				slots[index].Change = string(change.Change)
			}
		}
		summary, err := h.Messages.WorkflowActivity(r.Context(), principal.WorkspaceID, principal.UserID, id)
		if err != nil {
			h.writeStoreError(w, err, "Workflow activity is temporarily unavailable.")
			return
		}
		triggerTitles := make(map[domain.WorkflowTriggerID]string, len(triggers))
		for _, trigger := range triggers {
			triggerTitles[trigger.ID] = trigger.Title
		}
		activity.Queued = summary.Queued
		activity.Running = summary.Running
		activity.Completed = summary.Completed
		activity.Failed = summary.Failed
		activity.Cancelled = summary.Cancelled
		for _, run := range summary.RecentRuns {
			view := workflowActivityRunView{
				RunID: string(run.ID), Status: string(run.Status), Trigger: triggerTitles[run.TriggerID],
				Started: run.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			}
			if view.Trigger == "" {
				view.Trigger = "Manual run"
			}
			if !run.CompletedAt.IsZero() {
				view.Completed = run.CompletedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			}
			activity.Runs = append(activity.Runs, view)
		}
	}
	h.writeHTML(w, workflowTemplate, workflowData{
		CSRFToken: csrf, Notice: strings.TrimSpace(r.URL.Query().Get("notice")),
		ID: string(value.ID), AppID: string(value.AppID), Title: value.Title, Description: value.Description,
		CallbackID: value.CallbackID, Status: string(value.Status), Version: value.Version,
		Published: value.PublishedVersion, UpdatedAt: value.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Owned: value.OwnerID == principal.UserID, InputSchema: inputSchema, Functions: functions,
		Channels: channels, Lists: lists,
		StepSlots: slots, RemovedSteps: removedSteps, StepCount: len(slots), Triggers: triggerViews,
		Activity: activity, HasActivity: owned,
		HasFunctions: len(functions) != 0, HasInputSchema: inputSchema != "{}",
		PublishedStatus: value.Status == domain.WorkflowPublished,
		StagedEdits:     value.Status == domain.WorkflowPublished && value.Version != value.PublishedVersion,
	}, http.StatusOK, "workflow rendering unavailable")
}

func workflowPermissionAllows(permission domain.AutomationPermission, principal auth.Principal, ownerID domain.UserID) bool {
	switch permission.PermissionType {
	case "everyone":
		return true
	case "app_collaborators":
		return principal.UserID == ownerID
	case "named_entities":
		return slices.Contains(permission.UserIDs, principal.UserID) ||
			slices.Contains(permission.TeamIDs, principal.WorkspaceID)
	default:
		return false
	}
}

func encodeWorkflowSteps(fields map[string]string, options []workflowFunctionOption, appID domain.AppID) (string, error) {
	count := 10
	if raw := strings.TrimSpace(fields["step_count"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			return "", errors.New("workflow step count is invalid")
		}
		count = parsed
	}
	steps := make([]map[string]any, 0, count)
	for index := 1; index <= count; index++ {
		callback := strings.TrimSpace(fields[fmt.Sprintf("step_%d", index)])
		if callback == "" {
			continue
		}
		if !workflowFunctionExists(options, appID, callback) {
			return "", errors.New("function is not part of the workflow app")
		}
		step := map[string]any{"function_id": callback, "title": workflowFunctionTitle(options, callback)}
		if raw := strings.TrimSpace(fields[fmt.Sprintf("mapping_%d", index)]); raw != "" {
			var mapping map[string]string
			if json.Unmarshal([]byte(raw), &mapping) != nil || mapping == nil {
				return "", fmt.Errorf("step %d input mapping must be a JSON object of strings", index)
			}
			for name, value := range mapping {
				if strings.TrimSpace(name) == "" {
					return "", fmt.Errorf("step %d input mapping has an empty name", index)
				}
				mapping[name] = strings.TrimSpace(value)
			}
			step["input_mapping"] = mapping
		}
		source := strings.TrimSpace(fields[fmt.Sprintf("condition_source_%d", index)])
		if source != "" {
			operator := strings.TrimSpace(fields[fmt.Sprintf("condition_operator_%d", index)])
			if operator == "" {
				return "", fmt.Errorf("step %d has a condition source but no operator", index)
			}
			step["condition"] = map[string]string{
				"source": source, "operator": operator,
				"value": strings.TrimSpace(fields[fmt.Sprintf("condition_value_%d", index)]),
			}
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return "", errors.New("at least one workflow step is required")
	}
	encoded, err := json.Marshal(steps)
	return string(encoded), err
}

func (h Handler) updateWorkflow(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the workflow and try again.")
	if !ok {
		return
	}
	id := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	current, err := h.Messages.GetWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil || current.OwnerID != principal.UserID {
		h.writeMutationError(w, r, http.StatusNotFound, "The workflow was not saved", "It no longer exists or you are not its owner.")
		return
	}
	expected, err := strconv.ParseUint(strings.TrimSpace(fields["version"]), 10, 64)
	if err != nil || expected != current.Version {
		h.writeMutationError(w, r, http.StatusConflict, "The workflow was not saved", "It changed elsewhere. Reload before editing.")
		return
	}
	_, options, err := h.workflowOptions(r.Context(), principal, current.AppID)
	if err != nil || len(options) == 0 {
		h.writeMutationError(w, r, http.StatusConflict, "The workflow was not saved", "Its app no longer exposes the functions used by this workflow.")
		return
	}
	steps, err := encodeWorkflowSteps(fields, options, current.AppID)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The workflow was not saved", err.Error()+".")
		return
	}
	current.Title = strings.TrimSpace(fields["title"])
	current.Description = strings.TrimSpace(fields["description"])
	current.CallbackID = strings.TrimSpace(fields["callback_id"])
	current.InputSchema = strings.TrimSpace(fields["input_schema"])
	current.Steps = steps
	action := strings.TrimSpace(fields["action"])
	if action == "discard" {
		if err := h.Messages.DiscardWorkflowStagedChanges(r.Context(), principal.WorkspaceID, principal.UserID, id, expected); err != nil {
			h.writeWorkflowMutationError(w, r, "The staged changes were not discarded", err)
			return
		}
		h.redirectMutation(w, r, "/app/workflows/"+url.PathEscape(string(id))+"?notice="+url.QueryEscape("Staged changes discarded"))
		return
	}
	publish := action == "publish"
	if action != "save" && action != "publish" && action != "unpublish" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The workflow was not saved", "Choose Save draft, Publish, or Unpublish.")
		return
	}
	if action == "unpublish" {
		current.Status = domain.WorkflowDisabled
	}
	updated, err := h.Messages.UpdateWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, current, expected, publish)
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The workflow was not saved", err)
		return
	}
	notice := "Draft saved"
	if publish {
		notice = "Workflow published"
	} else if action == "unpublish" {
		notice = "Workflow unpublished"
	} else if current.Status == domain.WorkflowPublished {
		notice = "Staged changes saved"
	}
	h.redirectMutation(w, r, "/app/workflows/"+url.PathEscape(string(updated.ID))+"?notice="+url.QueryEscape(notice))
}

func (h Handler) duplicateWorkflow(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the workflow and try again."); !ok {
		return
	}
	id := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	duplicate, err := h.Messages.DuplicateWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The workflow was not copied", err)
		return
	}
	h.redirectMutation(w, r, "/app/workflows/"+url.PathEscape(string(duplicate.ID))+"?notice="+url.QueryEscape("Workflow copied"))
}

func (h Handler) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the workflow and try again.")
	if !ok {
		return
	}
	id := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	current, err := h.Messages.GetWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil || current.OwnerID != principal.UserID {
		h.writeMutationError(w, r, http.StatusNotFound, "The workflow was not deleted", "It no longer exists or you are not its owner.")
		return
	}
	expected, err := strconv.ParseUint(strings.TrimSpace(fields["version"]), 10, 64)
	if err != nil || expected != current.Version {
		h.writeMutationError(w, r, http.StatusConflict, "The workflow was not deleted", "It changed elsewhere. Reload before deleting.")
		return
	}
	if err := h.Messages.DeleteWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, id, expected); err != nil {
		h.writeWorkflowMutationError(w, r, "The workflow was not deleted", err)
		return
	}
	h.redirectMutation(w, r, "/app/workflows?notice="+url.QueryEscape("Workflow deleted"))
}

func buildWorkflowTriggerConfig(triggerType string, fields map[string]string) (string, error) {
	encode := func(value any) (string, error) {
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}
	switch triggerType {
	case "link", "shortcut", "webhook":
		return "{}", nil
	case "scheduled":
		startRaw := strings.TrimSpace(fields["schedule_start"])
		timezone := strings.TrimSpace(fields["schedule_timezone"])
		if timezone == "" {
			timezone = "UTC"
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			return "", errors.New("enter a valid IANA time zone such as UTC or Europe/Helsinki")
		}
		start, err := time.ParseInLocation("2006-01-02T15:04", startRaw, location)
		if err != nil {
			return "", errors.New("choose when the schedule starts")
		}
		frequency := strings.TrimSpace(fields["schedule_frequency"])
		if frequency != "hourly" && frequency != "daily" && frequency != "weekly" && frequency != "monthly" {
			return "", errors.New("choose how often the schedule repeats")
		}
		interval := 1
		if raw := strings.TrimSpace(fields["schedule_interval"]); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 366 {
				return "", errors.New("the schedule interval must be between 1 and 366")
			}
			interval = parsed
		}
		frequencyValue := map[string]any{"type": frequency, "interval": interval}
		if frequency == "weekly" {
			weekdays := make([]string, 0, 7)
			for _, day := range []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} {
				if strings.TrimSpace(fields["schedule_weekday_"+day]) != "" {
					weekdays = append(weekdays, day)
				}
			}
			if len(weekdays) > 0 {
				frequencyValue["weekdays"] = weekdays
			}
		}
		if frequency == "monthly" {
			if raw := strings.TrimSpace(fields["schedule_day"]); raw != "" {
				day, err := strconv.Atoi(raw)
				if err != nil || day < 1 || day > 31 {
					return "", errors.New("the day of the month must be between 1 and 31")
				}
				frequencyValue["day"] = day
			}
		}
		return encode(map[string]any{
			"start_time": start.UTC().Format(time.RFC3339), "timezone": timezone,
			"frequency": frequencyValue,
		})
	case "message", "reaction", "join":
		field := "event_channel"
		if triggerType == "reaction" {
			field = "event_channel_reaction"
		} else if triggerType == "join" {
			field = "event_channel_join"
		}
		channel := strings.TrimSpace(fields[field])
		if channel == "" {
			return "", errors.New("choose the channel the trigger watches")
		}
		config := map[string]any{"channel_ids": []string{channel}}
		if triggerType == "message" {
			if keyword := strings.TrimSpace(fields["event_keyword"]); keyword != "" {
				config["keyword"] = keyword
			}
		}
		if triggerType == "reaction" {
			reaction := strings.Trim(strings.TrimSpace(fields["event_reaction"]), ":")
			if reaction != "" {
				config["reaction"] = reaction
			}
		}
		return encode(config)
	case "list":
		listID := strings.TrimSpace(fields["list_id"])
		if listID == "" {
			return "", errors.New("choose the list the trigger watches")
		}
		event := strings.TrimSpace(fields["list_event"])
		if event != "created" && event != "updated" {
			return "", errors.New("choose when the list trigger fires")
		}
		return encode(map[string]any{"list_id": listID, "event": event})
	default:
		return "", errors.New("choose a trigger type")
	}
}

func (h Handler) createWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the workflow and try again.")
	if !ok {
		return
	}
	workflowID := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	triggerType := strings.TrimSpace(fields["type"])
	config, err := buildWorkflowTriggerConfig(triggerType, fields)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The trigger was not created", err.Error()+".")
		return
	}
	_, err = h.Messages.SetWorkflowTrigger(r.Context(), principal.WorkspaceID, principal.UserID, domain.WorkflowTrigger{
		WorkflowID: workflowID, Title: strings.TrimSpace(fields["title"]), Type: triggerType, Config: config, Enabled: true,
	}, 0)
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The trigger was not created", err)
		return
	}
	h.redirectMutation(w, r, "/app/workflows/"+url.PathEscape(string(workflowID))+"?notice=Trigger+created")
}

func (h Handler) updateWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the workflow and try again.")
	if !ok {
		return
	}
	workflowID := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	triggerID := domain.WorkflowTriggerID(strings.TrimSpace(r.PathValue("triggerID")))
	version, err := strconv.ParseUint(strings.TrimSpace(fields["version"]), 10, 64)
	if err != nil || version == 0 {
		h.writeMutationError(w, r, http.StatusConflict, "The trigger was not updated", "Reload the workflow and try again.")
		return
	}
	_, err = h.Messages.SetWorkflowTrigger(r.Context(), principal.WorkspaceID, principal.UserID, domain.WorkflowTrigger{
		ID: triggerID, WorkflowID: workflowID, Title: strings.TrimSpace(fields["title"]),
		Type: strings.TrimSpace(fields["type"]), Config: strings.TrimSpace(fields["config"]), Enabled: fields["enabled"] == "true",
	}, version)
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The trigger was not updated", err)
		return
	}
	h.redirectMutation(w, r, "/app/workflows/"+url.PathEscape(string(workflowID))+"?notice=Trigger+updated")
}

func (h Handler) runWorkflow(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the workflow and try again.")
	if !ok {
		return
	}
	workflowID := domain.WorkflowID(strings.TrimSpace(r.PathValue("workflowID")))
	triggerID := domain.WorkflowTriggerID(strings.TrimSpace(r.PathValue("triggerID")))
	triggers, err := h.Messages.ListWorkflowTriggers(r.Context(), principal.WorkspaceID, principal.UserID, workflowID)
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The workflow did not start", err)
		return
	}
	found := false
	for _, trigger := range triggers {
		if trigger.ID == triggerID {
			found = true
			break
		}
	}
	if !found {
		h.writeMutationError(w, r, http.StatusNotFound, "The workflow did not start", "That trigger does not belong to this workflow.")
		return
	}
	inputs := strings.TrimSpace(fields["inputs"])
	if inputs == "" {
		inputs = "{}"
	}
	run, err := h.Messages.RunWorkflow(r.Context(), principal.WorkspaceID, principal.UserID, triggerID, "", inputs, strings.TrimSpace(fields["idempotency_key"]))
	if err != nil {
		h.writeWorkflowMutationError(w, r, "The workflow did not start", err)
		return
	}
	h.redirectMutation(w, r, "/app/workflows/runs/"+url.PathEscape(string(run.ID)))
}

func (h Handler) workflowRun(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.workflowPrincipal(w, r)
	if !ok {
		return
	}
	run, err := h.Messages.GetWorkflowRun(r.Context(), principal.WorkspaceID, principal.UserID, domain.WorkflowRunID(strings.TrimSpace(r.PathValue("runID"))))
	if err != nil {
		h.writeStoreError(w, err, "That workflow run is not available.")
		return
	}
	completed := ""
	if !run.CompletedAt.IsZero() {
		completed = run.CompletedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	h.writeHTML(w, workflowRunTemplate, workflowRunData{
		WorkflowID: string(run.WorkflowID), RunID: string(run.ID), Status: string(run.Status),
		Version: run.WorkflowVersion, Inputs: run.Inputs, Outputs: run.Outputs, Error: run.Error,
		CreatedAt: run.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt: run.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Completed: completed,
	}, http.StatusOK, "workflow run rendering unavailable")
}

func (h Handler) writeWorkflowMutationError(w http.ResponseWriter, r *http.Request, title string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.writeMutationError(w, r, http.StatusNotFound, title, "The workflow, trigger, app, or function is no longer available.")
	case errors.Is(err, store.ErrConflict):
		h.writeMutationError(w, r, http.StatusConflict, title, "The workflow changed elsewhere or is not currently runnable. Reload and try again.")
	default:
		h.writeMutationError(w, r, http.StatusBadRequest, title, "Check the workflow fields and try again.")
	}
}
