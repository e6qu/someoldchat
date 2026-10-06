package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Slack's replacement for Later is two surfaces, and this file is both:
//
//   - To-dos (/app/todos), a tab in the rail: work with an optional reminder,
//     added directly ("Add To-do"), from a message ("Remind me about this")
//     or from a saved item, filtered by reminder (overdue, upcoming, none),
//     sorted by due date or creation, and marked done. /remind list shows the
//     channel reminders the member created in the same tab.
//   - Saved (/app/saved), a section of Home: private bookmarks on messages
//     and files, each opened in Home, cleared, or moved to To-dos, with a
//     clean-up action for all of them.
//
// /app/later is where both used to live, so it redirects to To-dos and the
// links people kept still open something.

// todoView is one row of To-dos.
type todoView struct {
	ID      string
	Title   string
	Details string
	// SourceURL opens the message the to-do was made from in its
	// conversation; SourceGone says the member can no longer read it.
	SourceURL   string
	SourceLabel string
	SourceGone  bool
	Status      domain.TodoReminderGroup
	Scheduled   bool
	DueMachine  string
	DueDisplay  string
	Recurrence  string
	Done        bool
	DoneMachine string
	DoneDisplay string
	Badged      bool
	Failed      bool
	FailureCode string
	DoneURL     string
	ReminderURL string
	EditURL     string
	DeleteURL   string
	DateValue   string
	TimeValue   string
	TimeZone    string
}

// channelReminderView is one /remind channel reminder the member created.
type channelReminderView struct {
	ID          string
	Text        string
	ChannelName string
	ChannelURL  string
	DueMachine  string
	DueDisplay  string
	Recurrence  string
	Finished    bool
	Failed      bool
	FailureCode string
	DeleteURL   string
}

// todoFilterOption is one checkbox of the To-dos filter.
type todoFilterOption struct {
	Value   domain.TodoReminderGroup
	Label   string
	Checked bool
}

// todoSortOption is one choice of the To-dos "Sort by".
type todoSortOption struct {
	Value    domain.TodoSort
	Label    string
	Selected bool
}

type todosData struct {
	Shell     shellView
	Channel   string
	CSRFToken string
	// View is "open", "done" or "channel-reminders".
	View             string
	Todos            []todoView
	ChannelReminders []channelReminderView
	Filters          []todoFilterOption
	Sorts            []todoSortOption
	FilterActive     bool
	OpenURL          string
	DoneURL          string
	ChannelURL       string
	CreateURL        string
	// ReturnQuery is the page's own filter, which every mutation carries so
	// the member comes back to the list they were looking at.
	ReturnQuery string
	MoreURL     string
	Notice      string
}

// savedItemView is one row of the Saved section.
type savedItemView struct {
	ID              string
	Text            template.HTML
	AuthorID        string
	AuthorName      string
	AvatarURL       string
	Initial         string
	ChannelName     string
	ChannelPrefix   string
	ChannelPrivate  bool
	MachineTime     string
	DisplayTime     string
	SourceURL       string
	SourceAvailable bool
	RemoveURL       string
	MoveURL         string
	RemindURL       string
	MarkUnreadURL   string
}

type savedData struct {
	Shell     shellView
	Channel   string
	CSRFToken string
	Items     []savedItemView
	// CleanUp shows the clean-up controls: a checkbox on each item, "Move
	// selected to To-dos" and "Remove all saved items".
	CleanUp    bool
	CleanUpURL string
	DoneURL    string
	MoveURL    string
	ClearURL   string
	MoreURL    string
	Notice     string
}

const todoStyle = `<style>
.todo-actions-head{display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.todo-check{display:grid;place-items:center;width:28px;height:28px;margin-top:2px;padding:0;border:2px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);font:inherit;font-weight:800;cursor:pointer}
.todo-check[aria-pressed=true]{border-color:var(--ok);background:var(--ok);color:#fff}
.todo-check:hover{border-color:var(--action)}
.todo-done .v-row-title{text-decoration:line-through;color:var(--muted)}
.todo-details{margin:3px 0 0;color:var(--muted);white-space:pre-wrap;overflow-wrap:anywhere}
.todo-source{display:inline-block;min-height:24px;line-height:24px;color:inherit;text-decoration:none}.todo-source:hover{text-decoration:underline}
.todo-author{display:inline-block;min-height:24px;line-height:24px;color:var(--text);font-weight:800;text-decoration:none}.todo-author:hover{text-decoration:underline}
.due-chip{display:inline-flex;align-items:center;gap:5px;margin-top:6px;padding:2px 8px;border:1px solid var(--line);border-radius:12px;background:var(--panel);color:var(--text);font-size:12px;font-weight:700}
.due-chip.overdue{border-color:var(--danger);background:var(--danger-bg);color:var(--danger)}
.due-chip.done{color:var(--muted)}
.todo-badge{display:inline-block;margin-left:6px;padding:1px 6px;border-radius:9px;background:#e8912d;color:#1d1c1d;font-size:11px;font-weight:800;vertical-align:middle}
.todo-unavailable{margin:0;color:var(--muted);font-weight:700}
.reminder-status{color:var(--muted);font-size:12px;font-weight:700}.reminder-status.failed{color:var(--danger)}
.todo-form{display:grid;gap:8px;min-width:280px;padding:6px}
.todo-form label,.todo-form legend{display:grid;gap:4px;font-size:12px;font-weight:700;color:var(--muted)}
.todo-form fieldset{display:grid;gap:8px;margin:0;padding:8px;border:1px solid var(--line);border-radius:6px}
.todo-form input,.todo-form select,.todo-form textarea{min-height:32px;padding:4px 8px;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);font:inherit}
.todo-form textarea{min-height:64px;resize:vertical}
.todo-form .choice{display:flex;align-items:center;gap:8px;color:var(--text);font-size:14px;font-weight:600}
.todo-form .choice input{min-height:0}
.todo-form .v-btn{justify-self:start}
.v-menu-list .todo-form label:hover,.v-menu-list .todo-form:hover,.v-menu-list .todo-form fieldset:hover{background:transparent;color:var(--muted)}
.saved-select{width:20px;height:20px;margin-top:8px}
.cleanup-bar{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin:0 0 12px;padding:10px 12px;border:1px solid var(--line);border-radius:8px;background:var(--panel)}
.cleanup-bar form{margin:0}
.channel-reminder-note{margin:0 0 12px;color:var(--muted);font-size:13px}
</style>`

// reminderPresetsPartial is the list of suggested reminder times shared by
// "Remind me about this" and Edit reminder: one form per time, so every
// choice is one keyboard-operable button, then Custom.
const reminderPresetsPartial = `{{define "reminder-presets"}}<form method="post" action="{{.URL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC"><button type="submit" name="preset" value="20m">In 20 minutes</button></form><form method="post" action="{{.URL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC"><button type="submit" name="preset" value="1h">In 1 hour</button></form><form method="post" action="{{.URL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC"><button type="submit" name="preset" value="3h">In 3 hours</button></form><form method="post" action="{{.URL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC"><button type="submit" name="preset" value="tomorrow">Tomorrow</button></form><form method="post" action="{{.URL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC"><button type="submit" name="preset" value="next_week">Next week</button></form><hr><form class="todo-form" method="post" action="{{.URL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC"><input type="hidden" name="preset" value="custom"><span class="v-menu-label">Custom</span><label>Date<input type="date" name="date" required></label><label>Time<input type="time" name="time" value="09:00"></label><label>Repeat<select name="recurrence"><option value="">Does not repeat</option><option value="daily">Daily</option><option value="weekly">Weekly</option><option value="monthly">Monthly</option><option value="yearly">Yearly</option></select></label><button class="v-btn primary" type="submit">Set reminder</button></form>{{end}}
{{define "remindMenu"}}<details class="v-menu"><summary class="v-icon" role="button" aria-label="Remind me about {{.Label}}" title="Remind me about this"><span aria-hidden="true">⏰</span></summary><div class="v-menu-list"><span class="v-menu-label">Remind me about this</span>{{template "reminder-presets" .}}</div></details>{{end}}`

const todosMarkup = `{{define "title"}}To-dos · SameOldChat{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + viewStyle + todoStyle + `{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + localTimeScript + todosLiveScript + reminderAcknowledgeScript + rowLinkScript + profilePanelScript + `{{end}}
` + reminderPresetsPartial + `
{{define "content"}}{{template "shell-open" .Shell}}{{template "todos-view" .}}{{template "shell-close" .Shell}}{{end}}
{{define "todos-view"}}<main class="v-page todos-page">
<div class="v-head"><h1>To-dos</h1><div class="todo-actions-head">
<details class="v-menu"><summary class="v-btn primary" role="button">Add To-do</summary><div class="v-menu-list"><form class="todo-form" method="post" action="{{.CreateURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC">
<label>To-do<input name="title" maxlength="3000" required></label>
<label>Details<textarea name="details" maxlength="4000"></textarea></label>
<fieldset><legend>Add reminder</legend>
<label>Remind me<select name="preset"><option value="none">No reminder</option><option value="20m">In 20 minutes</option><option value="1h">In 1 hour</option><option value="3h">In 3 hours</option><option value="tomorrow">Tomorrow at 9:00 AM</option><option value="next_week">Next week (Monday at 9:00 AM)</option><option value="custom">Custom</option></select></label>
<label>Custom date<input type="date" name="date"></label>
<label>Custom time (defaults to 9:00 AM)<input type="time" name="time"></label>
<label>Repeat<select name="recurrence"><option value="">Does not repeat</option><option value="daily">Daily</option><option value="weekly">Weekly</option><option value="monthly">Monthly</option><option value="yearly">Yearly</option></select></label>
</fieldset>
<button class="v-btn primary" type="submit">Add</button></form></div></details>
{{if ne .View "channel-reminders"}}<details class="v-menu"><summary class="v-btn{{if .FilterActive}} on{{end}}" role="button" aria-label="Filter and sort to-dos{{if .FilterActive}}, filtered{{end}}">Filter</summary><div class="v-menu-list"><form class="todo-form" method="get" action="/app/todos"><input type="hidden" name="channel" value="{{.Channel}}">{{if eq .View "done"}}<input type="hidden" name="view" value="done">{{end}}
<fieldset><legend>Reminders</legend>{{range .Filters}}<label class="choice"><input type="checkbox" name="reminders" value="{{.Value}}"{{if .Checked}} checked{{end}}>{{.Label}}</label>{{end}}</fieldset>
<fieldset><legend>Sort by</legend>{{range .Sorts}}<label class="choice"><input type="radio" name="sort" value="{{.Value}}"{{if .Selected}} checked{{end}}>{{.Label}}</label>{{end}}</fieldset>
<button class="v-btn primary" type="submit">Apply</button></form></div></details>{{end}}
<details class="v-menu"><summary class="v-icon" role="button" aria-label="To-dos options"><span aria-hidden="true">⋮</span></summary><div class="v-menu-list"><a href="{{.ChannelURL}}"{{if eq .View "channel-reminders"}} aria-current="page"{{end}}>Channel reminders you created</a></div></details>
</div></div>
<p class="v-sub">Your to-dos and their reminders are private to you.</p>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<nav class="v-tabs" aria-label="To-dos sections"><a href="{{.OpenURL}}"{{if eq .View "open"}} aria-current="page"{{end}}>To-do</a><a href="{{.DoneURL}}"{{if eq .View "done"}} aria-current="page"{{end}}>Done</a></nav>
{{if eq .View "channel-reminders"}}<p class="channel-reminder-note">Channel reminders you created. Slackbot posts each one in its channel when it comes due. They can’t be edited: delete one and set it again with /remind.</p>
<ul class="v-list todo-list" aria-label="Channel reminders">
{{range .ChannelReminders}}<li class="v-row todo-item channel-reminder-item"><span class="v-avatar glyph" aria-hidden="true">⏰</span><div class="v-row-main"><div class="v-row-meta"><strong>Channel reminder</strong>{{if .ChannelURL}}<span aria-hidden="true">·</span><a class="todo-source" href="{{.ChannelURL}}">{{.ChannelName}}</a>{{else}}<span aria-hidden="true">·</span><span>{{.ChannelName}}</span>{{end}}{{if .Recurrence}}<span aria-hidden="true">·</span><span>Repeats {{.Recurrence}}</span>{{end}}</div><p class="v-row-title">{{.Text}}</p><span class="due-chip{{if .Finished}} done{{end}}"><span aria-hidden="true">⏰</span>{{if .Finished}}Posted · {{else}}Due {{end}}<time datetime="{{.DueMachine}}">{{.DueDisplay}}</time></span>{{if .Failed}} <span class="reminder-status failed">Delivery failed: {{.FailureCode}}</span>{{end}}</div>
<div class="v-row-side"><div class="v-hover-actions"><details class="v-menu"><summary class="v-icon" role="button" aria-label="More actions for this reminder"><span aria-hidden="true">⋮</span></summary><div class="v-menu-list"><form method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger" type="submit">Delete reminder</button></form></div></details></div></div></li>
{{else}}<li class="v-empty">You have not created any channel reminders. Use /remind #channel what when in a conversation.</li>{{end}}
</ul>{{else}}
<ul class="v-list todo-list" aria-label="{{if eq .View "done"}}Done to-dos{{else}}To-dos{{end}}">
{{range .Todos}}<li class="v-row todo-item{{if .Done}} todo-done{{end}}"{{if .SourceURL}} data-row-href="{{.SourceURL}}"{{end}}><form method="post" action="{{.DoneURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="done" value="{{if .Done}}false{{else}}true{{end}}"><button class="todo-check" type="submit" aria-pressed="{{if .Done}}true{{else}}false{{end}}" aria-label="{{if .Done}}Mark not done{{else}}Mark done{{end}}: {{.Title}}" title="{{if .Done}}Mark not done{{else}}Mark done{{end}}">{{if .Done}}<span aria-hidden="true">✓</span>{{end}}</button></form>
<div class="v-row-main">{{if or .SourceURL .SourceGone}}<div class="v-row-meta">{{if .SourceURL}}<a class="todo-source" href="{{.SourceURL}}">{{.SourceLabel}}</a>{{else}}<span>The message this came from is no longer available.</span>{{end}}{{if .Recurrence}}<span aria-hidden="true">·</span><span>Repeats {{.Recurrence}}</span>{{end}}</div>{{else if .Recurrence}}<div class="v-row-meta"><span>Repeats {{.Recurrence}}</span></div>{{end}}
<p class="v-row-title">{{.Title}}{{if .Badged}}<span class="todo-badge">Reminder due</span>{{end}}</p>{{if .Details}}<p class="todo-details">{{.Details}}</p>{{end}}
{{if .Done}}<span class="due-chip done"><span aria-hidden="true">✓</span>Done · <time datetime="{{.DoneMachine}}">{{.DoneDisplay}}</time></span>{{else if .Scheduled}}<span class="due-chip{{if eq .Status "overdue"}} overdue{{end}}"><span aria-hidden="true">⏰</span>{{if eq .Status "overdue"}}Overdue · {{else}}Due {{end}}<time datetime="{{.DueMachine}}">{{.DueDisplay}}</time></span>{{end}}{{if .Failed}} <span class="reminder-status failed">Reminder failed: {{.FailureCode}}</span>{{end}}</div>
<div class="v-row-side"><div class="v-hover-actions">
{{if not .Done}}<details class="v-menu"><summary class="v-icon" role="button" aria-label="Edit reminder for {{.Title}}" title="Edit reminder"><span aria-hidden="true">⏰</span></summary><div class="v-menu-list"><span class="v-menu-label">Edit reminder</span>{{template "reminder-presets" (reminderMenu .ReminderURL $.CSRFToken .Title)}}{{if .Scheduled}}<hr><form method="post" action="{{.ReminderURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit" name="preset" value="none">Clear due date</button></form>{{end}}</div></details>{{end}}
<details class="v-menu"><summary class="v-icon" role="button" aria-label="More actions for this to-do"><span aria-hidden="true">⋮</span></summary><div class="v-menu-list">{{if .SourceURL}}<a href="{{.SourceURL}}">Open in Home</a><hr>{{end}}<form class="todo-form" method="post" action="{{.EditURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><span class="v-menu-label">Edit to-do</span><label>To-do<input name="title" maxlength="3000" value="{{.Title}}" required></label><label>Details<textarea name="details" maxlength="4000">{{.Details}}</textarea></label><button class="v-btn primary" type="submit">Save changes</button></form><hr><form method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger" type="submit">Delete to-do</button></form></div></details>
</div></div></li>
{{else}}<li class="v-empty">{{if eq $.View "done"}}No done to-dos{{if $.FilterActive}} match this filter{{end}}.{{else if $.FilterActive}}No to-dos match this filter.{{else}}<strong>Nothing to do</strong>Add a to-do, or choose Remind me about this on a message.{{end}}</li>{{end}}
</ul>{{end}}
<p class="visually-hidden" id="view-status" aria-live="polite"></p>
{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more</a></p>{{end}}
</main>{{end}}`

var todosTemplate = mustPage(todosMarkup)

const savedMarkup = `{{define "title"}}Saved · SameOldChat{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + viewStyle + todoStyle + `{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + localTimeScript + todosLiveScript + rowLinkScript + profilePanelScript + `{{end}}
` + reminderPresetsPartial + `
{{define "content"}}{{template "shell-open" .Shell}}{{template "saved-view" .}}{{template "shell-close" .Shell}}{{end}}
{{define "saved-view"}}<main class="v-page saved-page">
<div class="v-head"><h1>Saved</h1><div class="todo-actions-head">{{if .CleanUp}}<a class="v-btn" href="{{.DoneURL}}">Done</a>{{else if .Items}}<a class="v-btn" href="{{.CleanUpURL}}" aria-label="Clean up saved items">Clean up</a>{{end}}</div></div>
<p class="v-sub">Your saved items are only visible to you.</p>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .CleanUp}}<div class="cleanup-bar" role="group" aria-label="Clean up saved items"><form id="saved-cleanup" method="post" action="{{.MoveURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="v-btn primary" type="submit">Move selected to To-dos</button></form><form method="post" action="{{.ClearURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="v-btn danger" type="submit">Remove all saved items</button></form></div>{{end}}
<ul class="v-list saved-list" aria-label="Saved items">
{{range .Items}}<li class="v-row saved-item"{{if and .SourceAvailable (not $.CleanUp)}} data-row-href="{{.SourceURL}}"{{end}}>{{if $.CleanUp}}<input class="saved-select" type="checkbox" form="saved-cleanup" name="id" value="{{.ID}}" aria-label="Select the message from {{.AuthorName}}">{{else}}<span class="v-avatar" aria-hidden="true">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else if .Initial}}{{.Initial}}{{else}}?{{end}}</span>{{end}}
<div class="v-row-main">{{if .SourceAvailable}}<div class="v-row-meta"><a class="todo-source" href="{{.SourceURL}}">{{if .ChannelPrivate}}🔒 {{end}}{{.ChannelPrefix}}{{.ChannelName}}</a></div><p class="v-row-title">{{if .AuthorID}}<a class="todo-author" href="/app/members?user={{.AuthorID}}" data-profile-user="{{.AuthorID}}">{{.AuthorName}}</a>{{else}}{{.AuthorName}}{{end}} <time class="v-row-time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time></p><div class="v-row-text v-text clamp">{{.Text}}</div>{{else}}<p class="todo-unavailable">This message is no longer available.</p>{{end}}</div>
{{if not $.CleanUp}}<div class="v-row-side"><div class="v-hover-actions">
<form method="post" action="{{.RemoveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="v-icon" type="submit" aria-label="Remove from saved items" title="Remove from saved items"><span aria-hidden="true">✕</span></button></form>
{{if .RemindURL}}{{template "remindMenu" (reminderMenu .RemindURL $.CSRFToken .AuthorName)}}{{end}}
<details class="v-menu"><summary class="v-icon" role="button" aria-label="More actions for this saved item"><span aria-hidden="true">⋮</span></summary><div class="v-menu-list">{{if .SourceAvailable}}<a href="{{.SourceURL}}">Open in Home</a><button type="button" data-copy-text="{{.SourceURL}}" data-copy-done="Link copied.">Copy link</button><form method="post" action="{{.MarkUnreadURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Mark unread</button></form><form method="post" action="{{.MoveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Move to To-dos</button></form><hr>{{end}}<form method="post" action="{{.RemoveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger" type="submit">Remove from saved items</button></form></div></details>
</div></div>{{end}}</li>
{{else}}<li class="v-empty"><strong>No saved items</strong>Choose Add to saved on a message or file, and it appears here.</li>{{end}}
</ul>
<p class="visually-hidden" id="view-status" aria-live="polite"></p>
{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more saved items</a></p>{{end}}
</main>{{end}}`

var savedTemplate = mustPage(savedMarkup)

// todosLiveScript reloads To-dos and Saved when their records change. It
// skips the reload while any <details> editor is open: a reload re-renders the
// editor closed, so a live event arriving mid-edit would silently destroy the
// person's unsaved changes. Saving navigates anyway, which refreshes the page.
const todosLiveScript = `<script>(function(){
if(!window.EventSource)return;
var stream=` + liveStreamOpen + `;
['saved_item.created','saved_item.removed','saved_item.cleared','todo.created','todo.changed','todo.deleted','todo.reminder_delivered','todo.reminder_failed','channel_reminder.created','channel_reminder.deleted','channel_reminder.delivered','channel_reminder.failed'].forEach(function(topic){
stream.addEventListener(topic,function(){
if(document.querySelector('details[open]'))return;
window.location.reload()});
});
})();</script>`

// reminderAcknowledgeScript clears the due-reminder badge once the member has
// seen To-dos or Activity. The shell renders the acknowledgement form only
// while the badge is lit, so a page with nothing to acknowledge posts
// nothing; the badge leaves both tabs together because it is one fact.
const reminderAcknowledgeScript = `<script>(function(){
var form=document.getElementById('reminder-acknowledge');
if(!form||!window.fetch||!window.FormData)return;
fetch(form.action,{method:'POST',credentials:'same-origin',headers:{'content-type':'application/x-www-form-urlencoded','HX-Request':'true'},body:new URLSearchParams(new FormData(form)).toString()}).then(function(response){
if(!response.ok)return;
form.remove();
['todos-link','activity-link'].forEach(function(id){var link=document.getElementById(id);if(!link)return;var dot=link.querySelector('.rail-dot');if(dot)dot.remove();link.setAttribute('aria-label',(link.getAttribute('aria-label')||'').replace(', reminder due',''))});
}).catch(function(){});
})();</script>`

// todosFilter is the To-dos view a request names, normalised so every link
// and redirect writes it the same way.
type todosFilter struct {
	channel   string
	view      string
	reminders []domain.TodoReminderGroup
	sort      domain.TodoSort
}

func (h Handler) parseTodosFilter(values url.Values) (todosFilter, bool) {
	filter := todosFilter{channel: strings.TrimSpace(values.Get("channel")), view: strings.TrimSpace(values.Get("view")), sort: domain.TodoSort(strings.TrimSpace(values.Get("sort")))}
	if filter.channel == "" {
		filter.channel = string(h.Channel)
	}
	switch filter.view {
	case "":
		filter.view = "open"
	case "open", "done", "channel-reminders":
	default:
		return todosFilter{}, false
	}
	if filter.sort == "" {
		filter.sort = domain.TodoSortDueDate
	}
	if !filter.sort.Valid() {
		return todosFilter{}, false
	}
	seen := map[domain.TodoReminderGroup]bool{}
	for _, raw := range values["reminders"] {
		status := domain.TodoReminderGroup(strings.TrimSpace(raw))
		if !status.Valid() {
			return todosFilter{}, false
		}
		if !seen[status] {
			seen[status] = true
			filter.reminders = append(filter.reminders, status)
		}
	}
	return filter, true
}

// query is the filter as a query string, without the view when it is the
// default, so the canonical To-dos address stays /app/todos?channel=….
func (filter todosFilter) query() url.Values {
	values := url.Values{"channel": {filter.channel}}
	if filter.view != "open" {
		values.Set("view", filter.view)
	}
	for _, status := range filter.reminders {
		values.Add("reminders", string(status))
	}
	if filter.sort != domain.TodoSortDueDate {
		values.Set("sort", string(filter.sort))
	}
	return values
}

func (filter todosFilter) url(extra url.Values) string {
	values := filter.query()
	for key, list := range extra {
		values[key] = list
	}
	return "/app/todos?" + values.Encode()
}

func (filter todosFilter) withView(view string) todosFilter {
	filter.view = view
	return filter
}

// laterMoved keeps the links people kept to Later working: Later's reminders
// are To-dos now, and the channel-reminder list moved with them.
func (h Handler) laterMoved(w http.ResponseWriter, r *http.Request) {
	values := url.Values{}
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		values.Set("channel", channel)
	}
	switch {
	case r.URL.Query().Get("filter") == "channel-reminders":
		values.Set("view", "channel-reminders")
	case r.URL.Query().Get("state") == "completed":
		values.Set("view", "done")
	}
	target := "/app/todos"
	if len(values) > 0 {
		target += "?" + values.Encode()
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func (h Handler) todos(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	filter, ok := h.parseTodosFilter(r.URL.Query())
	if !ok {
		h.writePageError(w, http.StatusBadRequest, "That To-dos link is not valid", "Open To-dos from the workspace and choose a filter again.")
		return
	}
	head, err := h.readLiveHead(r.Context(), principal)
	if err != nil {
		h.writeStoreError(w, err, "To-dos are temporarily unavailable.")
		return
	}
	data := todosData{
		Channel: filter.channel, CSRFToken: csrf, View: filter.view,
		FilterActive: len(filter.reminders) > 0 || filter.sort != domain.TodoSortDueDate,
		OpenURL:      filter.withView("open").url(nil),
		DoneURL:      filter.withView("done").url(nil),
		ChannelURL:   todosFilter{channel: filter.channel, view: "channel-reminders", sort: domain.TodoSortDueDate}.url(nil),
		ReturnQuery:  filter.query().Encode(),
		Notice:       todoNotice(r.URL.Query().Get("changed")),
	}
	data.CreateURL = "/app/todos/create?" + data.ReturnQuery
	checked := map[domain.TodoReminderGroup]bool{}
	for _, status := range filter.reminders {
		checked[status] = true
	}
	data.Filters = []todoFilterOption{
		{Value: domain.TodoOverdue, Label: "Overdue", Checked: checked[domain.TodoOverdue]},
		{Value: domain.TodoUpcoming, Label: "Upcoming", Checked: checked[domain.TodoUpcoming]},
		{Value: domain.TodoNoReminder, Label: "No reminder scheduled", Checked: checked[domain.TodoNoReminder]},
	}
	data.Sorts = []todoSortOption{
		{Value: domain.TodoSortDueDate, Label: "Due date", Selected: filter.sort == domain.TodoSortDueDate},
		{Value: domain.TodoSortEarliestFirst, Label: "Earliest date created", Selected: filter.sort == domain.TodoSortEarliestFirst},
		{Value: domain.TodoSortLatestFirst, Label: "Latest date created", Selected: filter.sort == domain.TodoSortLatestFirst},
	}
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	now := time.Now().UTC()
	if filter.view == "channel-reminders" {
		page, err := h.Messages.ChannelReminders(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor})
		if err != nil {
			h.writeTodosReadError(w, err)
			return
		}
		for _, reminder := range page.Items {
			data.ChannelReminders = append(data.ChannelReminders, h.channelReminderRow(r, principal, filter, reminder))
		}
		if page.HasMore && page.NextCursor != "" {
			data.MoreURL = filter.url(url.Values{"cursor": {string(page.NextCursor)}})
		}
	} else {
		page, err := h.Messages.Todos(r.Context(), principal.WorkspaceID, principal.UserID, domain.TodoQuery{
			Done: filter.view == "done", Reminders: filter.reminders, Sort: filter.sort, Now: now,
			Page: domain.PageRequest{Limit: scheduledWindow, Cursor: cursor},
		})
		if err != nil {
			h.writeTodosReadError(w, err)
			return
		}
		for _, todo := range page.Items {
			data.Todos = append(data.Todos, h.todoRow(r, principal, filter, todo, now))
		}
		if page.HasMore && page.NextCursor != "" {
			data.MoreURL = filter.url(url.Values{"cursor": {string(page.NextCursor)}})
		}
	}
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationTodos})
	h.writeLivePage(w, head, todosTemplate, data, http.StatusOK, "To-dos rendering unavailable")
}

func (h Handler) writeTodosReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrInvalidCursor) || errors.Is(err, store.ErrInvalidArgument) || errors.Is(err, domain.ErrInvalidTodo) {
		h.writePageError(w, http.StatusBadRequest, "That To-dos link is not valid", "Open To-dos from the workspace and try again.")
		return
	}
	h.writeStoreError(w, err, "To-dos are temporarily unavailable.")
}

func todoNotice(changed string) string {
	return map[string]string{
		"created":          "To-do added.",
		"reminder":         "Reminder saved.",
		"reminder-cleared": "Due date cleared.",
		"edited":           "To-do saved.",
		"done":             "To-do marked done.",
		"undone":           "To-do moved back to To-do.",
		"deleted":          "To-do deleted.",
		"channel-reminder": "Channel reminder set.",
		"channel-deleted":  "Channel reminder deleted.",
	}[changed]
}

func todoActionURL(path string, id domain.TodoID, filter todosFilter) string {
	values := filter.query()
	values.Set("id", string(id))
	return path + "?" + values.Encode()
}

func (h Handler) todoRow(r *http.Request, principal auth.Principal, filter todosFilter, todo domain.Todo, now time.Time) todoView {
	view := todoView{
		ID: string(todo.ID), Title: todo.Title, Details: todo.Details,
		Status: todo.ReminderGroup(now), Scheduled: todo.Reminder.Scheduled(),
		Recurrence: string(todo.Reminder.Recurrence), Done: todo.Done(), Badged: todo.Badged(),
		Failed: !todo.Delivery.FailedAt.IsZero(), FailureCode: todo.Delivery.FailureCode,
		DoneURL:     todoActionURL("/app/todos/done", todo.ID, filter),
		ReminderURL: todoActionURL("/app/todos/reminder", todo.ID, filter),
		EditURL:     todoActionURL("/app/todos/edit", todo.ID, filter),
		DeleteURL:   todoActionURL("/app/todos/delete", todo.ID, filter),
	}
	if view.Done {
		view.DoneMachine = todo.CompletedAt.UTC().Format(time.RFC3339)
		view.DoneDisplay = formatTime(todo.CompletedAt)
	}
	if view.Scheduled {
		view.DueMachine = todo.Reminder.DueAt.UTC().Format(time.RFC3339)
		view.DueDisplay = formatTime(todo.Reminder.DueAt)
		view.TimeZone = todo.Reminder.TimeZone
		if location, err := time.LoadLocation(todo.Reminder.TimeZone); err == nil {
			local := todo.Reminder.DueAt.In(location)
			view.DateValue, view.TimeValue = local.Format("2006-01-02"), local.Format("15:04")
		}
	}
	if todo.Source.Set() {
		view.SourceGone = true
		conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, todo.Source.Conversation)
		if err == nil {
			view.SourceGone = false
			view.SourceURL = messageSourceURL(todo.Source)
			view.SourceLabel = "View message"
			if name := conversationName(conversation); name != "" {
				view.SourceLabel = "View message in " + conversationLabel(conversation, name)
			}
		}
	}
	return view
}

// conversationLabel names a conversation the way a source link reads it:
// a channel by #name, a direct message by who it is with.
func conversationLabel(conversation domain.Conversation, name string) string {
	if conversation.IsDirectOrGroup() || strings.HasPrefix(name, "#") {
		return name
	}
	return "#" + name
}

// messageSourceURL opens a message in its conversation, scrolled to it: the
// "Open in Home" Slack offers on a saved item and a to-do alike.
func messageSourceURL(source domain.TodoSource) string {
	sourceTime, err := domain.ParseMessageTimestamp(source.Timestamp)
	if err != nil {
		return appURL(string(source.Conversation), "", "", messageAnchor(source.MessageID), "")
	}
	before := ""
	if cursor, cursorErr := domain.NewMessageCursor(domain.Message{ID: source.MessageID, CreatedAt: sourceTime.Add(time.Nanosecond)}); cursorErr == nil {
		before = string(cursor)
	}
	return appURL(string(source.Conversation), "", before, messageAnchor(source.MessageID), "")
}

func (h Handler) channelReminderRow(r *http.Request, principal auth.Principal, filter todosFilter, reminder domain.ChannelReminder) channelReminderView {
	view := channelReminderView{
		ID: string(reminder.ID), Text: reminder.Text, ChannelName: "a channel you can no longer see",
		DueMachine: reminder.Reminder.DueAt.UTC().Format(time.RFC3339), DueDisplay: formatTime(reminder.Reminder.DueAt),
		Recurrence: string(reminder.Reminder.Recurrence), Finished: reminder.Finished(),
		Failed: !reminder.Delivery.FailedAt.IsZero(), FailureCode: reminder.Delivery.FailureCode,
	}
	values := filter.query()
	values.Set("id", string(reminder.ID))
	view.DeleteURL = "/app/reminders/channel/delete?" + values.Encode()
	if conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, reminder.Channel); err == nil {
		view.ChannelName = conversationLabel(conversation, conversationName(conversation))
		view.ChannelURL = appURL(string(reminder.Channel), "", "", "", "")
	}
	return view
}

// reminderTimingFromForm reads a reminder choice: one of Slack's suggested
// times, Custom (a date, and a time that defaults to 9:00 AM in the member's
// zone), or none. ok is false with a reason the member can act on.
func reminderTimingFromForm(fields map[string]string, now time.Time) (domain.ReminderTiming, string, bool) {
	timeZone := strings.TrimSpace(fields["timezone"])
	if timeZone == "" {
		timeZone = "UTC"
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return domain.ReminderTiming{}, "Choose a valid time zone and try again.", false
	}
	recurrence := domain.ReminderRecurrence(strings.TrimSpace(fields["recurrence"]))
	if !recurrence.Valid() {
		return domain.ReminderTiming{}, "Choose one of the available repeat options.", false
	}
	var due time.Time
	switch preset := strings.TrimSpace(fields["preset"]); preset {
	case "none":
		return domain.ReminderTiming{}, "", true
	case "20m", "1h", "3h", "tomorrow", "nextweek":
		due, _ = presetLocalTime(preset, now, location)
	case "next_week":
		// Slack's "Next week" is 9:00 on the coming Monday.
		due, _ = presetLocalTime("monday", now, location)
	case "", "custom":
		date := strings.TrimSpace(fields["date"])
		clock := strings.TrimSpace(fields["time"])
		if date == "" && preset == "" {
			return domain.ReminderTiming{}, "", true
		}
		if clock == "" {
			clock = "09:00"
		}
		due, err = time.ParseInLocation("2006-01-02 15:04", date+" "+clock, location)
		if err != nil || due.In(location).Format("2006-01-02 15:04") != date+" "+clock {
			return domain.ReminderTiming{}, "Choose a real calendar date and time.", false
		}
	default:
		return domain.ReminderTiming{}, "Choose one of the available reminder times.", false
	}
	return domain.ReminderTiming{DueAt: due.UTC(), TimeZone: location.String(), Recurrence: recurrence}, "", true
}

func (h Handler) writeTodoError(w http.ResponseWriter, r *http.Request, err error, heading string) {
	status := http.StatusServiceUnavailable
	reason := "Nothing was changed because the workspace store is temporarily unavailable."
	switch {
	case errors.Is(err, domain.ErrInvalidTodo):
		status, reason = http.StatusBadRequest, "Give the to-do a title of at most 3,000 characters, and details of at most 4,000."
	case errors.Is(err, domain.ErrInvalidReminderRequest), errors.Is(err, domain.ErrInvalidLaterReminder):
		status, reason = http.StatusBadRequest, "Choose a valid date and time and a supported repeat option."
	case errors.Is(err, domain.ErrReminderTimeInPast):
		status, reason = http.StatusBadRequest, "Choose a reminder time in the future."
	case errors.Is(err, domain.ErrInvalidTimestamp):
		status, reason = http.StatusBadRequest, "That message link is not valid."
	case errors.Is(err, domain.ErrNotInConversation):
		status, reason = http.StatusForbidden, "You can only be reminded about messages in conversations you can read."
	case errors.Is(err, store.ErrInvalidArgument):
		status, reason = http.StatusBadRequest, "That request is not valid. Reload the page and try again."
	case errors.Is(err, store.ErrNotFound):
		status, reason = http.StatusNotFound, "That to-do, reminder or message is no longer available, belongs to another member, or its reminder is being delivered right now."
	}
	h.writeMutationError(w, r, status, heading, reason)
}

// redirectTodos returns the member to the To-dos view they acted from, or
// to Saved when the action started there.
func (h Handler) redirectTodos(w http.ResponseWriter, r *http.Request, changed string) {
	if r.URL.Query().Get("return") == "saved" {
		h.redirectSaved(w, r, changed)
		return
	}
	filter, ok := h.parseTodosFilter(r.URL.Query())
	if !ok {
		filter = todosFilter{channel: string(h.requestChannel(r)), view: "open", sort: domain.TodoSortDueDate}
	}
	h.redirectMutation(w, r, filter.url(url.Values{"changed": {changed}}))
}

func todoIDFrom(r *http.Request) domain.TodoID {
	return domain.TodoID(strings.TrimSpace(r.URL.Query().Get("id")))
}

// createTodo is Add To-do, and "Remind me about this" on a message (the
// request then names the message by channel and ts).
func (h Handler) createTodo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The to-do could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	now := time.Now().UTC()
	timing, reason, ok := reminderTimingFromForm(fields, now)
	if !ok {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder time is not valid", reason)
		return
	}
	request := domain.TodoRequest{Title: fields["title"], Details: fields["details"], Reminder: timing}
	fromMessage := false
	if timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts"))); timestamp != "" {
		if _, parseErr := domain.ParseMessageTimestamp(timestamp); parseErr != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "Open the message menu again and choose a reminder time.")
			return
		}
		request.SourceChannel = h.requestChannel(r)
		request.SourceTimestamp = timestamp
		fromMessage = true
	}
	if fromMessage && !timing.Scheduled() {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose when to be reminded", "Pick one of the suggested times, or Custom and a date.")
		return
	}
	if _, err := h.Messages.CreateTodo(r.Context(), principal.WorkspaceID, principal.UserID, request); err != nil {
		h.writeTodoError(w, r, err, "The to-do was not added")
		return
	}
	// A reminder set from a message's menu is confirmed where the member is,
	// as Slack does, rather than by taking them to To-dos.
	if fromMessage && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Vary", "HX-Request")
		setMutationNotice(w, reminderConfirmation(timing.DueAt, timing.TimeZone, now))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if fromMessage {
		h.redirectTodos(w, r, "reminder")
		return
	}
	h.redirectTodos(w, r, "created")
}

func (h Handler) editTodo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The to-do could not be read from the form. Reload To-dos and try again.")
	if !ok {
		return
	}
	id := todoIDFrom(r)
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That to-do link is not valid", "Open To-dos and try again.")
		return
	}
	if _, err := h.Messages.EditTodo(r.Context(), principal.WorkspaceID, principal.UserID, id, domain.TodoEdit{Title: fields["title"], Details: fields["details"]}); err != nil {
		h.writeTodoError(w, r, err, "The to-do was not saved")
		return
	}
	h.redirectTodos(w, r, "edited")
}

// setTodoReminder is Edit reminder: a suggested time, Custom, or (preset
// "none") Clear due date.
func (h Handler) setTodoReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The reminder could not be read from the form. Reload To-dos and try again.")
	if !ok {
		return
	}
	id := todoIDFrom(r)
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That to-do link is not valid", "Open To-dos and try again.")
		return
	}
	timing, reason, ok := reminderTimingFromForm(fields, time.Now().UTC())
	if !ok {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder time is not valid", reason)
		return
	}
	if _, err := h.Messages.SetTodoReminder(r.Context(), principal.WorkspaceID, principal.UserID, id, timing); err != nil {
		h.writeTodoError(w, r, err, "The reminder was not saved")
		return
	}
	if !timing.Scheduled() {
		h.redirectTodos(w, r, "reminder-cleared")
		return
	}
	h.redirectTodos(w, r, "reminder")
}

func (h Handler) setTodoDone(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The to-do could not be read from the form. Reload To-dos and try again.")
	if !ok {
		return
	}
	id := todoIDFrom(r)
	done := strings.TrimSpace(fields["done"])
	if id == "" || (done != "true" && done != "false") {
		h.writeMutationError(w, r, http.StatusBadRequest, "That to-do action is not valid", "Open To-dos and try again.")
		return
	}
	if err := h.Messages.SetTodoDone(r.Context(), principal.WorkspaceID, principal.UserID, id, done == "true"); err != nil {
		h.writeTodoError(w, r, err, "The to-do was not changed")
		return
	}
	if done == "true" {
		h.redirectTodos(w, r, "done")
		return
	}
	h.redirectTodos(w, r, "undone")
}

func (h Handler) deleteTodo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The to-do could not be read from the form. Reload To-dos and try again."); !ok {
		return
	}
	id := todoIDFrom(r)
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That to-do link is not valid", "Open To-dos and try again.")
		return
	}
	if err := h.Messages.DeleteTodo(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		h.writeTodoError(w, r, err, "The to-do was not deleted")
		return
	}
	h.redirectTodos(w, r, "deleted")
}

// acknowledgeTodoReminders clears the due-reminder badge on To-dos and
// Activity. The pages post it themselves once they are shown.
func (h Handler) acknowledgeTodoReminders(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The reminder read marker could not be read. Reload To-dos and try again."); !ok {
		return
	}
	if err := h.Messages.AcknowledgeTodoReminders(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		h.writeTodoError(w, r, err, "Reminder badges are temporarily unavailable")
		return
	}
	h.redirectTodos(w, r, "")
}

func (h Handler) deleteChannelReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The reminder could not be read from the form. Reload To-dos and try again."); !ok {
		return
	}
	id := domain.ChannelReminderID(strings.TrimSpace(r.URL.Query().Get("id")))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder link is not valid", "Open your channel reminders and try again.")
		return
	}
	if err := h.Messages.DeleteChannelReminder(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		h.writeTodoError(w, r, err, "The reminder was not deleted")
		return
	}
	h.redirectTodos(w, r, "channel-deleted")
}

// --- Saved ---

func (h Handler) saved(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	head, err := h.readLiveHead(r.Context(), principal)
	if err != nil {
		h.writeStoreError(w, err, "Saved items are temporarily unavailable.")
		return
	}
	channel := string(h.requestChannel(r))
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	page, err := h.Messages.SavedItems(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) || errors.Is(err, store.ErrInvalidArgument) {
			h.writePageError(w, http.StatusBadRequest, "That Saved link is not valid", "Open Saved from Home and try again.")
			return
		}
		h.writeStoreError(w, err, "Saved items are temporarily unavailable.")
		return
	}
	query := url.Values{"channel": {channel}}
	data := savedData{
		Channel: channel, CSRFToken: csrf, CleanUp: r.URL.Query().Get("cleanup") == "1",
		CleanUpURL: "/app/saved?" + url.Values{"channel": {channel}, "cleanup": {"1"}}.Encode(),
		DoneURL:    "/app/saved?" + query.Encode(),
		MoveURL:    "/app/saved/move?" + query.Encode(),
		ClearURL:   "/app/saved/clear?" + query.Encode(),
		Notice:     savedNotice(r.URL.Query().Get("changed")),
	}
	names := h.newUserNames(r.Context(), principal)
	for _, item := range page.Items {
		data.Items = append(data.Items, h.savedItemRow(r, principal, channel, item, names))
	}
	if page.HasMore && page.NextCursor != "" {
		next := url.Values{"channel": {channel}, "cursor": {string(page.NextCursor)}}
		if data.CleanUp {
			next.Set("cleanup", "1")
		}
		data.MoreURL = "/app/saved?" + next.Encode()
	}
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationHome})
	h.writeLivePage(w, head, savedTemplate, data, http.StatusOK, "Saved rendering unavailable")
}

func savedNotice(changed string) string {
	return map[string]string{
		"removed":  "Removed from saved items.",
		"cleared":  "Your saved items were removed.",
		"moved":    "Moved to To-dos.",
		"reminder": "Got it! The reminder is in To-dos.",
	}[changed]
}

func (h Handler) savedItemRow(r *http.Request, principal auth.Principal, channel string, item domain.SavedItem, names *userNames) savedItemView {
	query := url.Values{"channel": {channel}, "id": {string(item.ID)}, "return": {"saved"}}
	view := savedItemView{
		ID: string(item.ID), SourceAvailable: item.SourceAvailable, AuthorName: "Unknown member",
		RemoveURL: "/app/saved/remove?" + query.Encode(),
		MoveURL:   "/app/saved/move?" + query.Encode(),
	}
	if !item.SourceAvailable {
		return view
	}
	timestamp := domain.NewMessageTimestamp(item.Message.CreatedAt)
	if rendered := h.newResultViews(r.Context(), principal, []domain.Message{item.Message}, names); len(rendered) == 1 {
		view.Text = rendered[0].DisplayText
		view.ChannelPrivate = rendered[0].ChannelPrivate
	}
	if strings.TrimSpace(item.Message.Text) == "" {
		view.Text = "File or rich message"
	}
	view.AvatarURL = names.avatarURL(item.Message.AuthorID)
	if item.Message.AppID == "" && item.Message.AuthorID != "" {
		view.AuthorID = string(item.Message.AuthorID)
	}
	view.RemindURL = "/app/todos/create?" + url.Values{"channel": {string(item.Conversation)}, "ts": {string(timestamp)}, "return": {"saved"}}.Encode()
	view.MarkUnreadURL = "/app/read/unread?" + url.Values{"channel": {string(item.Conversation)}, "ts": {string(timestamp)}}.Encode()
	view.MachineTime = item.Message.CreatedAt.UTC().Format(time.RFC3339Nano)
	view.DisplayTime = formatTime(item.Message.CreatedAt)
	boundary := item.Message
	boundary.CreatedAt = boundary.CreatedAt.Add(time.Nanosecond)
	before := ""
	if cursor, cursorErr := domain.NewMessageCursor(boundary); cursorErr == nil {
		before = string(cursor)
	}
	// Open in Home shows the rest of the conversation or thread the item was
	// shared in, scrolled to it.
	view.SourceURL = appURL(string(item.Conversation), string(item.Message.ThreadTimestamp), before, messageAnchor(item.Message.ID), "")
	if author, authorErr := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.Message.AuthorID); authorErr == nil {
		view.AuthorName = displayName(author)
	}
	view.Initial = initial(view.AuthorName)
	view.ChannelName = "Conversation"
	if conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.Conversation); conversationErr == nil {
		view.ChannelName = conversationName(conversation)
		view.ChannelPrefix = "#"
		if conversation.IsDirectOrGroup() {
			view.ChannelPrefix = ""
			if participants := h.participantNames(r.Context(), principal, conversation.ID); participants != "" {
				view.ChannelName = participants
			}
		}
	}
	return view
}

func (h Handler) redirectSaved(w http.ResponseWriter, r *http.Request, changed string) {
	query := url.Values{"channel": {string(h.requestChannel(r))}}
	if changed != "" {
		query.Set("changed", changed)
	}
	h.redirectMutation(w, r, "/app/saved?"+query.Encode())
}

// addToSaved is the "Add to saved" message action and the focused-message A
// shortcut. It answers in place, like every message action.
func (h Handler) addToSaved(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The save request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The message was not saved because the link does not identify a message in this conversation.")
		return
	}
	if _, err := h.Messages.AddToSaved(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp); err != nil {
		status, reason := http.StatusServiceUnavailable, "The message could not be saved because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, domain.ErrInvalidTimestamp):
			status, reason = http.StatusBadRequest, "That message link is not valid."
		case errors.Is(err, store.ErrNotFound), errors.Is(err, domain.ErrNotInConversation):
			status, reason = http.StatusNotFound, "That message is no longer available or you can no longer read it."
		}
		h.writeMutationError(w, r, status, "The message was not saved", reason)
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) removeSavedItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The remove request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	id := domain.SavedItemID(strings.TrimSpace(r.URL.Query().Get("id")))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That saved item link is not valid", "Open Saved from Home and try again.")
		return
	}
	if err := h.Messages.RemoveSavedItem(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		status, reason := http.StatusServiceUnavailable, "The saved item could not be removed because the workspace store is temporarily unavailable."
		if errors.Is(err, store.ErrNotFound) {
			status, reason = http.StatusNotFound, "That saved item was already removed or belongs to another member."
		}
		h.writeMutationError(w, r, status, "The saved item was not removed", reason)
		return
	}
	if r.URL.Query().Get("return") == "saved" {
		h.redirectSaved(w, r, "removed")
		return
	}
	h.completeMutation(w, r)
}

// clearSavedItems is the clean-up action "remove all saved items".
func (h Handler) clearSavedItems(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The clean-up request could not be read from the form. Reload Saved and try again."); !ok {
		return
	}
	if _, err := h.Messages.ClearSavedItems(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Your saved items were not removed", "Nothing was removed because the workspace store is temporarily unavailable.")
		return
	}
	h.redirectSaved(w, r, "cleared")
}

// moveSavedItemsToTodos moves one saved item (named in the query, from its
// More actions menu) or the items selected during clean-up to To-dos.
func (h Handler) moveSavedItemsToTodos(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	_, selected, err := decodeFormValues(w, r, "id")
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That form could not be read", "Reload Saved and try again.")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	ids := make([]domain.SavedItemID, 0, len(selected)+1)
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		ids = append(ids, domain.SavedItemID(id))
	}
	for _, value := range selected {
		if value = strings.TrimSpace(value); value != "" {
			ids = append(ids, domain.SavedItemID(value))
		}
	}
	if len(ids) == 0 {
		h.writeMutationError(w, r, http.StatusBadRequest, "No saved items were selected", "Select the items to move to To-dos, then choose Move selected to To-dos.")
		return
	}
	if len(ids) > scheduledWindow {
		h.writeMutationError(w, r, http.StatusBadRequest, "Too many saved items were selected", "Move at most 50 saved items at a time.")
		return
	}
	for _, id := range ids {
		if _, err := h.Messages.MoveSavedItemToTodo(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
			status, reason := http.StatusServiceUnavailable, "The saved item could not be moved because the workspace store is temporarily unavailable."
			switch {
			case errors.Is(err, store.ErrNotFound):
				status, reason = http.StatusNotFound, "That saved item was already removed, belongs to another member, or its message is no longer available. Any items before it were moved."
			case errors.Is(err, store.ErrInvalidArgument), errors.Is(err, domain.ErrInvalidTodo):
				status, reason = http.StatusBadRequest, "That saved item cannot become a to-do. Any items before it were moved."
			}
			h.writeMutationError(w, r, status, "The saved item was not moved to To-dos", reason)
			return
		}
	}
	h.redirectSaved(w, r, "moved")
}
