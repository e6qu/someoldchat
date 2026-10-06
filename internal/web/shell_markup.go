package web

import "html/template"

// icon renders one symbol from the shell's sprite. The icons are this
// product's own monochrome line drawings, drawn in currentColor so they follow
// the theme and the state of the control they sit in; they are decorative, so
// the control around them carries the accessible name.
func icon(name string) template.HTML {
	return template.HTML(`<svg class="icon" aria-hidden="true" focusable="false"><use href="#i-` + template.HTMLEscapeString(name) + `"></use></svg>`)
}

// iconSprite is referenced by every icon() on the page. It is one hidden SVG,
// so an icon costs a <use> element rather than a copy of its path.
const iconSprite = `<svg class="icon-sprite" aria-hidden="true" focusable="false" xmlns="http://www.w3.org/2000/svg"><defs>
<symbol id="i-home" viewBox="0 0 20 20"><path d="M3.5 9 10 3.8 16.5 9v7a1 1 0 0 1-1 1h-3.3v-4.6H7.8V17H4.5a1 1 0 0 1-1-1z"/></symbol>
<symbol id="i-dms" viewBox="0 0 20 20"><path d="M3 5.5A2.5 2.5 0 0 1 5.5 3h6A2.5 2.5 0 0 1 14 5.5v3a2.5 2.5 0 0 1-2.5 2.5H8.2L5 13.4V11A2 2 0 0 1 3 9z"/><path d="M14 7.2h.6A2.4 2.4 0 0 1 17 9.6V12a2 2 0 0 1-1.8 2v2.2L12.4 14h-1.9a2.4 2.4 0 0 1-2.1-1.3"/></symbol>
<symbol id="i-activity" viewBox="0 0 20 20"><path d="M10 3.2a4.5 4.5 0 0 0-4.5 4.5v2.8L4 13.5h12l-1.5-3V7.7A4.5 4.5 0 0 0 10 3.2z"/><path d="M8.2 15.8a1.9 1.9 0 0 0 3.6 0"/></symbol>
<symbol id="i-saved" viewBox="0 0 20 20"><path d="M6 3.5h8a1 1 0 0 1 1 1v12.3l-5-3.2-5 3.2V4.5a1 1 0 0 1 1-1z"/></symbol>
<symbol id="i-todos" viewBox="0 0 20 20"><rect x="3.5" y="3.5" width="13" height="13" rx="2.5"/><path d="m6.8 10.2 2.3 2.3 4.2-4.6"/></symbol>
<symbol id="i-clock" viewBox="0 0 20 20"><circle cx="10" cy="10" r="7"/><path d="M10 6v4.3l2.8 1.7"/></symbol>
<symbol id="i-more" viewBox="0 0 20 20"><circle cx="4.8" cy="10" r="1.3" class="fill"/><circle cx="10" cy="10" r="1.3" class="fill"/><circle cx="15.2" cy="10" r="1.3" class="fill"/></symbol>
<symbol id="i-kebab" viewBox="0 0 20 20"><circle cx="10" cy="4.8" r="1.3" class="fill"/><circle cx="10" cy="10" r="1.3" class="fill"/><circle cx="10" cy="15.2" r="1.3" class="fill"/></symbol>
<symbol id="i-plus" viewBox="0 0 20 20"><path d="M10 4v12M4 10h12"/></symbol>
<symbol id="i-search" viewBox="0 0 20 20"><circle cx="8.6" cy="8.6" r="5.3"/><path d="m12.6 12.6 4 4"/></symbol>
<symbol id="i-help" viewBox="0 0 20 20"><circle cx="10" cy="10" r="7"/><path d="M8 8a2 2 0 1 1 2.8 1.8c-.5.3-.8.7-.8 1.3v.4"/><circle cx="10" cy="14" r=".6" class="fill"/></symbol>
<symbol id="i-hash" viewBox="0 0 20 20"><path d="M8.2 3.5 6.6 16.5M13.4 3.5l-1.6 13M4 7.6h12.3M3.7 12.4H16"/></symbol>
<symbol id="i-lock" viewBox="0 0 20 20"><rect x="5" y="9" width="10" height="7.5" rx="1.5"/><path d="M7.2 9V6.8a2.8 2.8 0 0 1 5.6 0V9"/></symbol>
<symbol id="i-chevron" viewBox="0 0 20 20"><path d="m6 8 4 4 4-4"/></symbol>
<symbol id="i-caret" viewBox="0 0 20 20"><path d="M8 6.2 12.2 10 8 13.8z" class="fill"/></symbol>
<symbol id="i-compose" viewBox="0 0 20 20"><path d="M9 4H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-4"/><path d="m14 3.8 2.2 2.2L10 12.2H7.8V10z"/></symbol>
<symbol id="i-threads" viewBox="0 0 20 20"><path d="M3.5 4.5h13v8.5H9.2L5.8 15.8V13H3.5z"/><path d="M6.8 7.6h6.4M6.8 10.2h4"/></symbol>
<symbol id="i-send" viewBox="0 0 20 20"><path d="M3.5 9.6 16.5 4l-4.4 12.2-2.7-4.8z"/><path d="m9.4 11.4 7.1-7.4"/></symbol>
<symbol id="i-unreads" viewBox="0 0 20 20"><path d="M3.5 11.2 5.6 4.5h8.8l2.1 6.7V16h-13z"/><path d="M3.5 11.2h3.6l1 1.9h3.8l1-1.9h3.6"/></symbol>
<symbol id="i-huddle" viewBox="0 0 20 20"><path d="M4 12.5V11a6 6 0 0 1 12 0v1.5"/><rect x="3.3" y="11.6" width="3.2" height="5" rx="1.2"/><rect x="13.5" y="11.6" width="3.2" height="5" rx="1.2"/></symbol>
<symbol id="i-people" viewBox="0 0 20 20"><circle cx="8" cy="7" r="2.7"/><path d="M3.3 16a4.7 4.7 0 0 1 9.4 0"/><path d="M12.8 4.5a2.6 2.6 0 0 1 0 5M14.5 11.8A4.4 4.4 0 0 1 17 16"/></symbol>
<symbol id="i-star" viewBox="0 0 20 20"><path d="m10 3.3 2 4.2 4.6.6-3.4 3.1.9 4.6L10 13.6l-4.1 2.2.9-4.6-3.4-3.1 4.6-.6z"/></symbol>
<symbol id="i-mute" viewBox="0 0 20 20"><path d="M10 3.2a4.5 4.5 0 0 0-4.5 4.5v2.8L4 13.5h12l-1.5-3V7.7A4.5 4.5 0 0 0 10 3.2z"/><path d="M8.2 15.8a1.9 1.9 0 0 0 3.6 0M3.5 3.5l13 13"/></symbol>
<symbol id="i-link" viewBox="0 0 20 20"><path d="m8.4 11.6 3.2-3.2"/><path d="m7.2 9.3-1.7 1.7a2.6 2.6 0 0 0 3.6 3.6l1.6-1.7M12.8 10.7l1.7-1.7A2.6 2.6 0 0 0 10.9 5.4L9.3 7.1"/></symbol>
<symbol id="i-copy" viewBox="0 0 20 20"><rect x="7" y="7" width="9" height="9" rx="1.5"/><path d="M13 7V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h2"/></symbol>
<symbol id="i-gear" viewBox="0 0 20 20"><circle cx="10" cy="10" r="2.6"/><path d="M10 2.8v2.1M10 15.1v2.1M2.8 10h2.1M15.1 10h2.1M4.9 4.9l1.5 1.5M13.6 13.6l1.5 1.5M4.9 15.1l1.5-1.5M13.6 6.4l1.5-1.5"/></symbol>
<symbol id="i-signout" viewBox="0 0 20 20"><path d="M8 4H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"/><path d="M12 6.5 15.5 10 12 13.5M15.5 10H8"/></symbol>
<symbol id="i-files" viewBox="0 0 20 20"><path d="M6 3h5.5L15 6.5V16a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M11 3v4h4"/></symbol>
<symbol id="i-canvas" viewBox="0 0 20 20"><rect x="4" y="3" width="12" height="14" rx="1.5"/><path d="M7 7h6M7 10h6M7 13h3.5"/></symbol>
<symbol id="i-list" viewBox="0 0 20 20"><path d="M8 6h8M8 10h8M8 14h8"/><circle cx="4.6" cy="6" r=".9" class="fill"/><circle cx="4.6" cy="10" r=".9" class="fill"/><circle cx="4.6" cy="14" r=".9" class="fill"/></symbol>
<symbol id="i-workflow" viewBox="0 0 20 20"><path d="M11.2 2.8 5 11h4.6L8.8 17.2 15 9h-4.6z"/></symbol>
<symbol id="i-apps" viewBox="0 0 20 20"><rect x="3.5" y="3.5" width="5" height="5" rx="1"/><rect x="11.5" y="3.5" width="5" height="5" rx="1"/><rect x="3.5" y="11.5" width="5" height="5" rx="1"/><rect x="11.5" y="11.5" width="5" height="5" rx="1"/></symbol>
<symbol id="i-user" viewBox="0 0 20 20"><circle cx="10" cy="7" r="3"/><path d="M4 17a6 6 0 0 1 12 0"/></symbol>
<symbol id="i-status" viewBox="0 0 20 20"><circle cx="10" cy="10" r="7"/><circle cx="7.6" cy="8.4" r=".7" class="fill"/><circle cx="12.4" cy="8.4" r=".7" class="fill"/><path d="M7 12a3.6 3.6 0 0 0 6 0"/></symbol>
<symbol id="i-pause" viewBox="0 0 20 20"><path d="M15.6 12.4A6.4 6.4 0 0 1 7.6 4.4a6.4 6.4 0 1 0 8 8z"/></symbol>
<symbol id="i-close" viewBox="0 0 20 20"><path d="m5 5 10 10M15 5 5 15"/></symbol>
<symbol id="i-pin" viewBox="0 0 20 20"><path d="m11.6 3.4 5 5-2.3.8-2.8 2.8.4 3.3-1.3 1.3-6.2-6.2 1.3-1.3 3.3.4 2.8-2.8z"/><path d="M6.8 13.2 3.5 16.5"/></symbol>
<symbol id="i-sidebar" viewBox="0 0 20 20"><rect x="3" y="4" width="14" height="12" rx="1.5"/><path d="M8 4v12"/></symbol>
<symbol id="i-check" viewBox="0 0 20 20"><path d="m4.5 10.5 3.5 3.5 7.5-8"/></symbol>
<symbol id="i-info" viewBox="0 0 20 20"><circle cx="10" cy="10" r="7"/><path d="M10 9v5"/><circle cx="10" cy="6.4" r=".7" class="fill"/></symbol>
<symbol id="i-keyboard" viewBox="0 0 20 20"><rect x="2.8" y="5.5" width="14.4" height="9" rx="1.5"/><path d="M5.5 8.5h1M8.5 8.5h1M11.5 8.5h1M14 8.5h.5M7 11.5h6"/></symbol>
<symbol id="i-bookmark-add" viewBox="0 0 20 20"><path d="M10 4v12M4 10h12"/></symbol>
</defs></svg>`

// shellPartials is parsed into every page template (mustPage), so any page can
// draw the frame by rendering these with its shellView.
const shellPartials = `{{define "shell-open"}}<a class="skip-link" href="#content">Skip to the content</a>
<div class="shell" data-channel="{{.Channel}}"{{if .Preferences}} data-preferences="{{.Preferences}}" data-preferences-csrf="{{.CSRFToken}}"{{end}}>
{{template "shell-top" .}}
<div class="workspace without-pane">
{{template "shell-rail" .}}
<div class="shell-main" id="content" tabindex="-1">{{end}}

{{define "shell-close"}}</div>
</div>
</div>
{{template "shell-dialogs" .}}{{end}}

{{define "shell-top"}}<header class="topbar">
  <button class="nav-toggle" id="nav-toggle" type="button" aria-controls="workspace-sidebar" aria-expanded="false" aria-label="Open navigation">` + `{{icon "sidebar"}}` + `</button>
  <form class="top-search" method="get" action="/app/search" role="search" aria-label="Search {{.WorkspaceName}}">
    {{icon "search"}}
    <label class="visually-hidden" for="workspace-search">Search {{.WorkspaceName}}</label>
    <input id="workspace-search" type="search" name="q" maxlength="500" value="{{.SearchQuery}}" placeholder="Search {{.WorkspaceName}}" role="combobox" {{ariaKeyshortcuts "Search the workspace"}} autocomplete="off" aria-autocomplete="list" aria-haspopup="listbox" aria-expanded="false" required>
    <input type="hidden" name="channel" value="{{.Channel}}">
  </form>
  <div class="top-actions">
    <details class="menu help-menu" data-menu>
      <summary class="top-button" role="button" aria-haspopup="menu" aria-expanded="false" aria-label="Help">{{icon "help"}}</summary>
      <div class="menu-list menu-end" role="menu" aria-label="Help">
        <button type="button" role="menuitem" id="open-keyboard-help" data-dialog-open="keyboard-help" aria-haspopup="dialog" aria-controls="keyboard-help" {{ariaKeyshortcuts "Keyboard shortcuts"}}>{{icon "keyboard"}}<span>Keyboard shortcuts</span></button>
        <a role="menuitem" href="{{.With "/app/preferences"}}" data-dialog-open="preferences">{{icon "gear"}}<span>Preferences</span></a>
      </div>
    </details>
  </div>
</header>{{end}}

{{define "workspace-menu"}}<div class="menu-list" role="menu" aria-label="{{.WorkspaceName}}">
  <div class="menu-identity" role="none"><span class="team-icon" aria-hidden="true">{{.WorkspaceInitial}}</span><strong>{{.WorkspaceName}}</strong></div>
  {{if .ShowAuthAdmin}}<a role="menuitem" href="/app/admin/auth">{{icon "people"}}<span>Invite people to {{.WorkspaceName}}</span></a>{{end}}
  <a role="menuitem" href="{{.With "/app/preferences"}}" data-dialog-open="preferences" {{ariaKeyshortcuts "Preferences"}}>{{icon "gear"}}<span>Preferences</span></a>
  <details class="submenu" data-menu>
    <summary role="menuitem" aria-haspopup="menu" aria-expanded="false">{{icon "apps"}}<span>Tools &amp; settings</span></summary>
    <div class="menu-list" role="menu" aria-label="Tools and settings">
      {{if .ShowAdmin}}<a role="menuitem" href="/app/admin/settings"><span>Workspace settings</span></a>
      <a role="menuitem" href="/app/admin/analytics"><span>Analytics</span></a>
      <a role="menuitem" href="/app/admin/audit"><span>Audit logs</span></a>{{end}}
      {{if .ShowAuthAdmin}}<a role="menuitem" href="/app/admin/auth"><span>Members and authorization</span></a>{{end}}
      <a role="menuitem" href="/app/customize/emoji"><span>Customize workspace</span></a>
      <a role="menuitem" href="/app/developer/apps"><span>Developer apps</span></a>
      <a role="menuitem" href="/app/workflows"><span>Workflow Builder</span></a>
      {{if .ShowIdentity}}<a role="menuitem" href="/me"><span>Your account and release</span></a>{{end}}
    </div>
  </details>
  {{if .Workspaces}}<form class="menu-form" method="post" action="/app/workspace/switch" role="none">
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <div role="group" aria-label="Switch workspace">{{range .Workspaces}}{{if .Current}}<span class="menu-note" role="none">{{.Name}} · {{.Role}} · you are here</span>{{else}}<button type="submit" role="menuitem" name="workspace_id" value="{{.ID}}"><span>Switch to {{.Name}}</span><small>{{.Role}}</small></button>{{end}}{{end}}</div>
  </form>{{end}}
  <hr role="separator">
  <form class="menu-form" method="post" action="/app/session/revoke" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem">{{icon "signout"}}<span>Sign out of {{.WorkspaceName}}</span></button></form>
</div>{{end}}

{{define "profile-menu"}}<div class="menu-list profile-menu" role="menu" aria-label="Your profile and status">
  <div class="menu-identity" role="none">{{template "self-avatar" .}}<span><strong>{{.Username}}</strong><small>{{if .Away}}Away{{else}}Active{{end}}{{if .NotificationsPaused}} · Notifications paused{{end}}</small></span></div>
  {{if .CanSetStatus}}<a role="menuitem" class="status-item" href="{{.With "/app/status"}}" data-dialog-open="status-dialog" {{ariaKeyshortcuts "Set your status"}}>{{if or .StatusDisplay .StatusText}}<span class="status-now">{{if .StatusDisplay}}{{.StatusDisplay}}{{else}}{{icon "status"}}{{end}}</span><span>{{if .StatusText}}{{.StatusText}}{{else}}Edit your status{{end}}</span>{{else}}{{icon "status"}}<span>Update your status</span>{{end}}</a>
  <form class="menu-form" method="post" action="/app/presence" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="presence" value="{{if .Away}}auto{{else}}away{{end}}"><input type="hidden" name="return" value="{{.ReturnTo}}"><button type="submit" role="menuitem">{{icon "user"}}<span>Set yourself as <strong>{{if .Away}}active{{else}}away{{end}}</strong></span></button></form>{{end}}
  <details class="submenu" data-menu>
    <summary role="menuitem" aria-haspopup="menu" aria-expanded="false">{{icon "pause"}}<span>{{if .NotificationsPaused}}Notifications paused{{else}}Pause notifications{{end}}</span>{{if .PausedUntil}}<small>until <time datetime="{{.PausedUntil}}" data-local-time>{{.PausedUntil}}</time></small>{{end}}</summary>
    <div class="menu-list" role="menu" aria-label="Pause notifications">
      {{if .NotificationsPaused}}<form class="menu-form" method="post" action="/app/notifications/dnd" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="return" value="{{.ReturnTo}}"><button type="submit" role="menuitem" name="action" value="resume"><span>Resume notifications</span></button></form>{{end}}
      <form class="menu-form" method="post" action="/app/notifications/dnd" role="none">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="return" value="{{.ReturnTo}}">
        <input type="hidden" name="action" value="pause">
        <button type="submit" role="menuitem" name="minutes" value="30"><span>For 30 minutes</span></button>
        <button type="submit" role="menuitem" name="minutes" value="60"><span>For 1 hour</span></button>
        <button type="submit" role="menuitem" name="minutes" value="120"><span>For 2 hours</span></button>
        <button type="submit" role="menuitem" name="minutes" value="1440" data-until-tomorrow><span>Until tomorrow</span></button>
      </form>
      <a role="menuitem" href="{{.With "/app/notifications"}}#schedule-heading"><span>Set a notification schedule</span></a>
    </div>
  </details>
  <hr role="separator">
  <a role="menuitem" href="/app/members?user={{.UserID}}" data-profile-user="{{.UserID}}">{{icon "user"}}<span>Profile</span></a>
  <a role="menuitem" href="{{.With "/app/preferences"}}" data-dialog-open="preferences">{{icon "gear"}}<span>Preferences</span></a>
  <hr role="separator">
  <form class="menu-form" method="post" action="/app/session/revoke" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem" data-shauth-sign-out>{{icon "signout"}}<span>Sign out of {{.WorkspaceName}}</span></button></form>
</div>{{end}}

{{define "self-avatar"}}<span class="self-avatar" aria-hidden="true">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else}}{{.UserInitial}}{{end}}<span class="presence-dot{{if .Away}} away{{end}}"></span></span>{{end}}

{{define "shell-rail"}}<nav class="rail" id="workspace-rail" aria-label="Workspace">
  <details class="menu rail-workspace" data-menu>
    <summary class="rail-team" role="button" aria-haspopup="menu" aria-expanded="false" aria-label="{{.WorkspaceName}} workspace menu"><span class="team-icon" aria-hidden="true">{{.WorkspaceInitial}}</span></summary>
    {{template "workspace-menu" .}}
  </details>
  <a class="rail-item" href="{{.HomeURL}}"{{if eq .Destination "home"}} aria-current="page"{{end}}>{{icon "home"}}<span class="rail-label">Home</span></a>
  <a class="rail-item" data-rail="dms" href="{{.With "/app/dms"}}"{{if eq .Destination "dms"}} aria-current="page"{{end}} {{ariaKeyshortcuts "Direct messages"}}>{{icon "dms"}}<span class="rail-label">DMs</span></a>
  <a class="rail-item" data-rail="activity" id="activity-link" href="{{.With "/app/activity"}}"{{if eq .Destination "activity"}} aria-current="page"{{end}} aria-label="Activity{{if .ReminderUnread}}, reminder due{{end}}" {{ariaKeyshortcuts "Activity"}}>{{icon "activity"}}<span class="rail-label">Activity</span>{{if .ReminderUnread}}<span class="rail-dot" aria-hidden="true"></span>{{end}}</a>
  <a class="rail-item" data-rail="todos" id="todos-link" href="{{.With "/app/todos"}}"{{if eq .Destination "todos"}} aria-current="page"{{end}} aria-label="To-dos{{if .ReminderUnread}}, reminder due{{end}}">{{icon "todos"}}<span class="rail-label">To-dos</span>{{if .ReminderUnread}}<span class="rail-dot" aria-hidden="true"></span>{{end}}</a>
  {{if and .ReminderUnread .CSRFToken}}<form id="reminder-acknowledge" method="post" action="/app/todos/acknowledge" hidden><input type="hidden" name="_csrf" value="{{.CSRFToken}}"></form>{{end}}
  <a class="rail-item" data-rail="files" href="{{.With "/app/files"}}">{{icon "files"}}<span class="rail-label">Files</span></a>
  <details class="menu rail-more" data-menu>
    <summary class="rail-item" role="button" aria-haspopup="menu" aria-expanded="false"{{if eq .Destination "more"}} aria-current="page"{{end}}>{{icon "more"}}<span class="rail-label">More</span></summary>
    <div class="menu-list" role="menu" aria-label="More">
      <a role="menuitem" data-more-tab="dms" href="{{.With "/app/dms"}}">{{icon "dms"}}<span>DMs</span></a>
      <a role="menuitem" data-more-tab="activity" href="{{.With "/app/activity"}}">{{icon "activity"}}<span>Activity</span></a>
      <a role="menuitem" data-more-tab="todos" href="{{.With "/app/todos"}}">{{icon "todos"}}<span>To-dos</span></a>
      <a role="menuitem" data-more-tab="files" href="{{.With "/app/files"}}">{{icon "files"}}<span>Files</span></a>
      <a role="menuitem" href="{{.With "/app/canvases"}}">{{icon "canvas"}}<span>Canvases</span></a>
      <a role="menuitem" href="{{.With "/app/lists"}}">{{icon "list"}}<span>Lists</span></a>
      <a role="menuitem" href="{{.With "/app/workflows"}}">{{icon "workflow"}}<span>Workflows</span></a>
      <a role="menuitem" href="{{.With "/app/members"}}" {{ariaKeyshortcuts "People"}}>{{icon "people"}}<span>People</span></a>
      <a role="menuitem" href="{{.With "/app/apps"}}">{{icon "apps"}}<span>Apps</span></a>
    </div>
  </details>
  <div class="rail-end">
    <details class="menu rail-create" data-menu>
      <summary class="rail-create-button" role="button" aria-haspopup="menu" aria-expanded="false" aria-label="Create new">{{icon "plus"}}</summary>
      <div class="menu-list" role="menu" aria-label="Create">
        {{if .CanMessage}}<a role="menuitem" href="{{.With "/app/dms"}}#new-dm" {{ariaKeyshortcuts "New message"}}>{{icon "compose"}}<span>Message</span></a>{{end}}
        {{if .CanCreate}}<a role="menuitem" href="{{.With "/app/channels/new"}}" data-dialog-open="create-channel">{{icon "hash"}}<span>Channel</span></a>{{end}}
        <a role="menuitem" href="{{.With "/app/canvases"}}#new-canvas">{{icon "canvas"}}<span>Canvas</span></a>
        <a role="menuitem" href="{{.With "/app/lists"}}#new-list">{{icon "list"}}<span>List</span></a>
        {{if .Channel}}<form class="menu-form" method="post" action="/app/huddle/start?channel={{.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem">{{icon "huddle"}}<span>Huddle in this conversation</span></button></form>{{end}}
        <a role="menuitem" href="{{.With "/app/workflows"}}#new-workflow">{{icon "workflow"}}<span>Workflow</span></a>
      </div>
    </details>
    <details class="menu rail-avatar" data-menu>
      <summary class="rail-me" role="button" aria-haspopup="menu" aria-expanded="false" aria-label="{{.Username}}, {{if .Away}}away{{else}}active{{end}}. Profile and status menu" data-shauth-user="{{.Username}}">{{template "self-avatar" .}}</summary>
      {{template "profile-menu" .}}
    </details>
  </div>
</nav>{{end}}

{{define "shell-dialogs"}}` + iconSprite + `
<template data-dialog-template="conversation-switcher"><dialog class="shell-dialog conversation-switcher" id="conversation-switcher" aria-labelledby="conversation-switcher-title">
  <div class="switcher-head">
    <h2 class="visually-hidden" id="conversation-switcher-title">Jump to a conversation</h2>
    {{icon "search"}}
    <input id="conversation-switcher-query" type="text" role="combobox" aria-label="Jump to a conversation" aria-autocomplete="list" aria-expanded="true" aria-controls="conversation-switcher-results" autocomplete="off" spellcheck="false" placeholder="Jump to a conversation, person or app">
    <button class="dialog-close switcher-close" type="button" data-dialog-close aria-label="Close conversation switcher">{{icon "close"}}</button>
  </div>
  <p class="switcher-group" id="conversation-switcher-group" aria-hidden="true">Conversations</p>
  <ul class="switcher-results" id="conversation-switcher-results" role="listbox" aria-label="Conversations">{{range .Switcher}}<li role="option" id="switch-{{.Kind}}-{{.ID}}" aria-selected="false" data-href="{{.Href}}" data-conversation-id="{{.ID}}" data-conversation-name="{{.Name}}" data-kind="{{.Kind}}"><span class="switcher-option"><span class="switcher-icon" aria-hidden="true">{{if eq .Kind "private"}}{{icon "lock"}}{{else if eq .Kind "dm"}}{{icon "user"}}{{else if eq .Kind "group"}}{{icon "people"}}{{else if eq .Kind "app"}}{{icon "apps"}}{{else}}{{icon "hash"}}{{end}}</span><span class="switcher-name{{if .Unread}} unread{{end}}">{{.Name}}</span><span class="switcher-type">{{.KindText}}</span>{{if .Context}}<span class="switcher-context">{{.Context}}</span>{{end}}</span></li>{{end}}</ul>
  <p class="switcher-empty" id="conversation-switcher-empty" role="status" aria-live="polite"></p>
  <p class="switcher-hint" aria-hidden="true"><kbd>↑</kbd><kbd>↓</kbd> to navigate · <kbd>Enter</kbd> to open · <kbd>Esc</kbd> to close</p>
</dialog></template>
<template data-dialog-template="keyboard-help"><dialog class="shell-dialog keyboard-help" id="keyboard-help" aria-labelledby="keyboard-help-title">
  <div class="dialog-head keyboard-help-head">
    <h2 id="keyboard-help-title" tabindex="-1">Keyboard shortcuts</h2>
    <label class="visually-hidden" for="keyboard-help-query">Search shortcuts</label>
    <input id="keyboard-help-query" type="search" autocomplete="off" maxlength="80" placeholder="Search shortcuts">
    <button class="dialog-close" id="keyboard-help-close" type="button" data-dialog-close aria-label="Close keyboard shortcuts">{{icon "close"}}</button>
  </div>
  <div class="keyboard-help-body">{{range .Keyboard}}
    <section data-keyboard-section aria-labelledby="keyboard-section-{{.Title}}">
      <h3 id="keyboard-section-{{.Title}}">{{.Title}}</h3>
      <dl>{{range .Shortcuts}}
        <div data-keyboard-row data-keyboard-search="{{.Search}}">
          <dt>{{.Action}}{{if .Note}}<small>{{.Note}}</small>{{end}}</dt>
          <dd><kbd data-keyboard-apple>{{.Apple}}</kbd><kbd data-keyboard-other>{{.Other}}</kbd></dd>
        </div>{{end}}
      </dl>
    </section>{{end}}
  </div>
  <p class="keyboard-help-empty" id="keyboard-help-empty" role="status" hidden>No matching shortcuts.</p>
</dialog></template>
<template data-dialog-template="preferences"><dialog class="shell-dialog preferences-dialog" id="preferences" aria-labelledby="preferences-title">
  <div class="dialog-head"><h2 id="preferences-title" tabindex="-1">Preferences</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close preferences">{{icon "close"}}</button></div>
  {{template "preferences-panels" .}}
</dialog></template>
{{if .CanSetStatus}}<template data-dialog-template="status-dialog"><dialog class="shell-dialog status-dialog" id="status-dialog" aria-labelledby="status-dialog-title">
  {{template "status-form" .}}
</dialog></template>{{end}}
{{if .CanCreate}}<template data-dialog-template="create-channel"><dialog class="shell-dialog create-channel" id="create-channel" aria-labelledby="create-channel-title">
  {{template "create-channel-form" .}}
</dialog></template>{{end}}
<div class="visually-hidden" id="shell-status" aria-live="polite" aria-atomic="true"></div>{{end}}

{{define "invite-request-fields"}}<div class="dialog-body"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><p>Your workspace administrators review every invitation. Once one of them approves your request, the person you name is sent an invitation.</p><label for="invite-request-email">To</label><input id="invite-request-email" type="email" name="email" autocomplete="off" required placeholder="name@example.com"><label for="invite-request-reason">Reason for request <span class="optional">(optional)</span></label><textarea id="invite-request-reason" name="reason" maxlength="500" rows="3"></textarea></div>{{end}}
{{define "status-form"}}<form class="status-form" method="post" action="/app/status" data-status-form>
  <div class="dialog-head"><h2 id="status-dialog-title" tabindex="-1">Set a status</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close status">{{icon "close"}}</button></div>
  <div class="dialog-body">
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="return" value="{{.ReturnTo}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC">
    <div class="status-inputs">
      <label class="status-emoji-field"><span>Status emoji</span><input type="text" name="status_emoji" id="status-emoji" maxlength="64" value="{{.StatusEmoji}}" placeholder=":speech_balloon:" autocomplete="off"></label>
      <label class="status-text-field"><span>What’s your status?</span><input type="text" name="status_text" id="status-text" maxlength="100" value="{{.StatusText}}" placeholder="What’s your status?" autocomplete="off"></label>
    </div>
    <fieldset class="status-presets"><legend>Suggestions</legend>
      <button type="button" data-status-preset data-emoji=":spiral_calendar_pad:" data-text="In a meeting" data-clear="60"><span aria-hidden="true">🗓️</span> In a meeting <small>1 hour</small></button>
      <button type="button" data-status-preset data-emoji=":bus:" data-text="Commuting" data-clear="30"><span aria-hidden="true">🚌</span> Commuting <small>30 minutes</small></button>
      <button type="button" data-status-preset data-emoji=":face_with_thermometer:" data-text="Out sick" data-clear="today"><span aria-hidden="true">🤒</span> Out sick <small>Today</small></button>
      <button type="button" data-status-preset data-emoji=":palm_tree:" data-text="Vacationing" data-clear="never"><span aria-hidden="true">🌴</span> Vacationing <small>Don’t clear</small></button>
      <button type="button" data-status-preset data-emoji=":house_with_garden:" data-text="Working remotely" data-clear="today"><span aria-hidden="true">🏡</span> Working remotely <small>Today</small></button>
    </fieldset>
    <label class="status-clear"><span>Clear after</span><select name="clear_after" id="status-clear-after">
      <option value="never">Don’t clear</option><option value="30">30 minutes</option><option value="60">1 hour</option><option value="240">4 hours</option><option value="today">Today</option><option value="week">This week</option><option value="custom">Choose date and time</option>
    </select></label>
    <label class="status-custom" data-status-custom><span>Date and time</span><input type="datetime-local" name="clear_at" id="status-clear-at"></label>
    {{if .StatusExpiration}}<p class="dialog-note">Your current status clears <time datetime="{{.StatusExpiration}}" data-local-time>{{.StatusExpiration}}</time>.</p>{{end}}
  </div>
  <div class="dialog-foot">{{if or .StatusEmoji .StatusText}}<button class="button-quiet" type="submit" name="clear_status" value="1" formnovalidate>Clear status</button>{{end}}<button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Save</button></div>
</form>{{end}}

{{define "create-channel-form"}}<form class="create-channel-form" method="post" action="/app/conversation/create" data-stepped>
  <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
  <div class="dialog-head"><h2 id="create-channel-title" tabindex="-1">Create a channel</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close create a channel">{{icon "close"}}</button></div>
  <p class="form-error dialog-error" role="alert" tabindex="-1" data-step-error hidden></p>
  <section class="dialog-body" data-step="1" aria-label="Step 1 of 3: Name">
    <label for="new-channel-name">Name</label>
    <div class="prefixed-input"><span class="prefix" aria-hidden="true">{{icon "hash"}}</span><input id="new-channel-name" type="text" name="name" maxlength="80" required placeholder="e.g. plan-budget" autocomplete="off" aria-describedby="new-channel-count new-channel-hint"><span class="char-count" id="new-channel-count" aria-live="polite">80</span></div>
    <p class="dialog-note" id="new-channel-hint">Channels are where conversations happen around a topic. Use a name that is easy to find and understand.</p>
    <p class="dialog-step" aria-hidden="true">Step 1 of 3</p>
  </section>
  <section class="dialog-body" data-step="2" aria-label="Step 2 of 3: Visibility">
    <fieldset class="choice-list"><legend>Visibility</legend>
      <label><input type="radio" name="is_private" value="false" checked> <span><strong>Public</strong><small>Anyone in {{.WorkspaceName}}</small></span></label>
      <label><input type="radio" name="is_private" value="true"{{if not .CanCreatePrivate}} disabled aria-describedby="new-channel-private-restricted"{{end}}> <span><strong>Private</strong><small>Only specific people · Can only be viewed or joined by invitation</small></span></label>
    </fieldset>
    {{if not .CanCreatePrivate}}<p class="dialog-note" id="new-channel-private-restricted">Your workspace limits who can create private channels. Ask a workspace administrator if you need one.</p>{{end}}
    <p class="dialog-step" aria-hidden="true">Step 2 of 3</p>
  </section>
  <section class="dialog-body" data-step="3" aria-label="Step 3 of 3: Add people">
    <label for="new-channel-people">Add people</label>
    <input id="new-channel-people" type="search" autocomplete="off" placeholder="Enter a name" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="new-channel-people-results" data-people-picker>
    <ul class="people-results" id="new-channel-people-results" role="listbox" aria-label="People" hidden></ul>
    <ul class="people-chosen" aria-label="People to add" data-people-chosen></ul>
    <p class="dialog-note">People you add get access to the channel and its history.</p>
    <p class="dialog-step" aria-hidden="true">Step 3 of 3</p>
  </section>
  <div class="dialog-foot">
    <button class="button" type="button" data-step-back hidden>Back</button>
    <button class="button primary" type="button" data-step-next>Next</button>
    <button class="button" type="submit" data-step-skip hidden>Skip for now</button>
    <button class="button primary" type="submit" data-step-submit>Create</button>
  </div>
</form>{{end}}

{{define "preferences-panels"}}<div class="preferences-layout">
  <div class="preferences-tabs" role="tablist" data-shell-tabs aria-label="{{t "prefs.sections"}}" aria-orientation="vertical">
    <button type="button" role="tab" id="pref-tab-notifications" aria-controls="pref-notifications" aria-selected="true">{{icon "activity"}}<span>{{t "prefs.tab.notifications"}}</span></button>
    <button type="button" role="tab" id="pref-tab-vip" aria-controls="pref-vip" aria-selected="false" tabindex="-1">{{icon "star"}}<span>{{t "prefs.tab.vip"}}</span></button>
    <button type="button" role="tab" id="pref-tab-home" aria-controls="pref-home" aria-selected="false" tabindex="-1">{{icon "home"}}<span>{{t "prefs.tab.home"}}</span></button>
    <button type="button" role="tab" id="pref-tab-appearance" aria-controls="pref-appearance" aria-selected="false" tabindex="-1">{{icon "status"}}<span>{{t "prefs.tab.appearance"}}</span></button>
    <button type="button" role="tab" id="pref-tab-accessibility" aria-controls="pref-accessibility" aria-selected="false" tabindex="-1">{{icon "info"}}<span>{{t "prefs.tab.accessibility"}}</span></button>
    <button type="button" role="tab" id="pref-tab-navigation" aria-controls="pref-navigation" aria-selected="false" tabindex="-1">{{icon "sidebar"}}<span>{{t "prefs.tab.navigation"}}</span></button>
    <button type="button" role="tab" id="pref-tab-read" aria-controls="pref-read" aria-selected="false" tabindex="-1">{{icon "check"}}<span>{{t "prefs.tab.read"}}</span></button>
    <button type="button" role="tab" id="pref-tab-media" aria-controls="pref-media" aria-selected="false" tabindex="-1">{{icon "files"}}<span>{{t "prefs.tab.media"}}</span></button>
    <button type="button" role="tab" id="pref-tab-region" aria-controls="pref-region" aria-selected="false" tabindex="-1">{{icon "clock"}}<span>{{t "prefs.tab.region"}}</span></button>
    <button type="button" role="tab" id="pref-tab-av" aria-controls="pref-av" aria-selected="false" tabindex="-1">{{icon "huddle"}}<span>{{t "prefs.tab.av"}}</span></button>
    <button type="button" role="tab" id="pref-tab-privacy" aria-controls="pref-privacy" aria-selected="false" tabindex="-1">{{icon "lock"}}<span>{{t "prefs.tab.privacy"}}</span></button>
    <button type="button" role="tab" id="pref-tab-advanced" aria-controls="pref-advanced" aria-selected="false" tabindex="-1">{{icon "gear"}}<span>{{t "prefs.tab.advanced"}}</span></button>
  </div>
  <div class="preferences-panels">
    <section class="preferences-panel" role="tabpanel" id="pref-notifications" aria-labelledby="pref-tab-notifications" tabindex="0">
      <h3>Notifications</h3>
      <p>What you are notified about, your keywords, VIPs, browser notifications and your notification schedule are kept together on one page.</p>
      <p><a class="button" href="{{.With "/app/notifications"}}">Open notification preferences</a></p>
      <ul class="preferences-links">
        <li><a href="{{.With "/app/notifications"}}#workspace-notifications-heading">Notify me about…</a></li>
        <li><a href="{{.With "/app/notifications"}}#schedule-heading">Notification schedule</a></li>
        <li><a href="{{.With "/app/notifications"}}#notification-exceptions-heading">Exceptions</a></li>
      </ul>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-vip" aria-labelledby="pref-tab-vip" tabindex="0" hidden>
      <h3>VIP</h3>
      <p>Every message a VIP posts in your channels notifies you, even in a channel you have muted or set to mentions only. Mark someone from their profile or from People; your list, with a way to remove each one, is on your notification preferences.</p>
      <p><a class="button" href="{{.With "/app/notifications"}}#notification-vips-heading">Manage VIPs</a></p>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-home" aria-labelledby="pref-tab-home" tabindex="0" hidden>
      <h3>Home</h3>
      <fieldset><legend>Show in your sidebar</legend>
        <label><input type="checkbox" data-preference="sidebar-unreads"> Unreads</label>
        <label><input type="checkbox" data-preference="sidebar-threads" data-default="true"> Threads</label>
        <label><input type="checkbox" data-preference="sidebar-drafts" data-default="true"> Drafts &amp; sent</label>
        <label><input type="checkbox" data-preference="sidebar-saved" data-default="true"> Saved</label>
        <label><input type="checkbox" data-preference="sidebar-apps" data-default="true"> Apps</label>
      </fieldset>
      <fieldset><legend>Show</legend>
        <label><input type="radio" name="pref-sidebar-show" value="all" data-preference="sidebar-show" data-default="all"> All your conversations</label>
        <label><input type="radio" name="pref-sidebar-show" value="unreads" data-preference="sidebar-show"> Unreads only</label>
        <label><input type="radio" name="pref-sidebar-show" value="mentions" data-preference="sidebar-show"> Mentions only</label>
      </fieldset>
      <fieldset><legend>Sort</legend>
        <label><input type="radio" name="pref-sidebar-sort" value="alpha" data-preference="sidebar-sort" data-default="alpha"> Alphabetically</label>
        <label><input type="radio" name="pref-sidebar-sort" value="recent" data-preference="sidebar-sort"> By most recent activity</label>
        <label><input type="radio" name="pref-sidebar-sort" value="priority" data-preference="sidebar-sort"> Priority</label>
      </fieldset>
      <p class="dialog-note">A section’s own Sort and Show choices (in its ⋮ menu) override these.</p>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-appearance" aria-labelledby="pref-tab-appearance" tabindex="0" hidden>
      <h3>Appearance</h3>
      <fieldset><legend>Theme</legend>
        <label><input type="radio" name="pref-theme" value="light" data-preference="theme"> Light</label>
        <label><input type="radio" name="pref-theme" value="dark" data-preference="theme"> Dark</label>
        <label><input type="radio" name="pref-theme" value="system" data-preference="theme" data-default="system"> Sync with OS setting</label>
      </fieldset>
      <p class="dialog-note">The theme applies to every page in this browser as soon as you choose it.</p>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-accessibility" aria-labelledby="pref-tab-accessibility" tabindex="0" hidden>
      <h3>Accessibility</h3>
      <fieldset><legend>Zoom</legend>
        <label for="pref-zoom">Make everything larger or smaller</label>
        <select id="pref-zoom" data-preference="zoom" data-default="100">
          <option value="80">80%</option><option value="90">90%</option><option value="100">100%</option><option value="110">110%</option><option value="125">125%</option><option value="150">150%</option>
        </select>
      </fieldset>
      <fieldset><legend>Links</legend>
        <label><input type="checkbox" data-preference="underline-links"> Underline links in messages</label>
      </fieldset>
      <fieldset><legend>Animation</legend>
        <label><input type="checkbox" data-preference="reduce-motion"> Turn off interface animations and transitions</label>
      </fieldset>
      <fieldset><legend>Screen reader</legend>
        <label><input type="checkbox" data-preference="announce-messages" data-default="true"> Announce incoming messages in the conversation you are viewing</label>
      </fieldset>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-navigation" aria-labelledby="pref-tab-navigation" tabindex="0" hidden>
      <h3>Navigation</h3>
      <fieldset><legend>Show these tabs</legend>
        <label><input type="checkbox" checked disabled> Home</label>
        <label><input type="checkbox" data-preference="nav-dms" data-default="true"> DMs</label>
        <label><input type="checkbox" data-preference="nav-activity" data-default="true"> Activity</label>
        <label><input type="checkbox" data-preference="nav-todos" data-default="true"> To-dos</label>
        <label><input type="checkbox" data-preference="nav-files"> Files</label>
      </fieldset>
      <label><input type="checkbox" data-preference="nav-labels" data-default="true"> Show tab names</label>
      <p class="dialog-note">A tab you hide stays in More, and its keyboard shortcut still opens it.</p>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-read" aria-labelledby="pref-tab-read" tabindex="0" hidden>
      <h3>Mark as read</h3>
      <fieldset><legend>When I view a conversation</legend>
        <label><input type="radio" name="pref-read" value="newest" data-preference="mark-read" data-default="newest"> Start me at the newest message and mark the conversation read</label>
        <label><input type="radio" name="pref-read" value="newest-unread" data-preference="mark-read"> Start me at the newest message, but leave unseen messages unread</label>
        <label><input type="radio" name="pref-read" value="left-off" data-preference="mark-read"> Start me where I left off, and mark the conversation read</label>
      </fieldset>
      <label><input type="checkbox" data-preference="confirm-mark-all"> Prompt to confirm that I want to mark all messages as read (Shift+Esc)</label>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-media" aria-labelledby="pref-tab-media" tabindex="0" hidden>
      <h3>Messages &amp; media</h3>
      <fieldset><legend>Theme</legend>
        <label><input type="radio" name="pref-message-theme" value="clean" data-preference="message-theme" data-default="clean"> Clean: profile photos beside messages, with more space between them</label>
        <label><input type="radio" name="pref-message-theme" value="compact" data-preference="message-theme"> Compact: no profile photos and less space, to see more messages at once</label>
      </fieldset>
      <fieldset><legend>Names</legend>
        <label><input type="radio" name="pref-name-display" value="full" data-preference="name-display" data-default="full"> Full &amp; display names: display names in messages, full names beside them elsewhere</label>
        <label><input type="radio" name="pref-name-display" value="display" data-preference="name-display"> Just display names</label>
      </fieldset>
      <fieldset><legend>Inline media and links</legend>
        <label><input type="checkbox" data-preference="inline-media" data-default="true"> Show images uploaded to this workspace</label>
        <label><input type="checkbox" data-preference="link-previews" data-default="true"> Show previews of linked websites</label>
      </fieldset>
      <fieldset><legend>Emoji</legend>
        <label><input type="checkbox" data-preference="emoji-as-text"> Display emoji as plain text</label>
        <label><input type="checkbox" data-preference="jumbomoji" data-default="true"> Show large emoji in messages that contain only emoji</label>
      </fieldset>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-region" aria-labelledby="pref-tab-region" tabindex="0" hidden>
      <h3>{{t "prefs.region.title"}}</h3>
      <fieldset><legend>{{t "prefs.region.language"}}</legend>
        <label for="pref-language" class="visually-hidden">{{t "prefs.region.language"}}</label>
        <select id="pref-language" class="preference-select" data-preference="language" data-default="{{.Language}}">{{range .Languages}}<option value="{{.Locale}}" lang="{{.Locale}}"{{if eq .Locale $.Language}} selected{{end}}>{{.Name}}</option>{{end}}</select>
        <p class="dialog-note">{{t "prefs.region.language_note"}}</p>
      </fieldset>
      <fieldset><legend>{{t "prefs.region.timezone"}}</legend>
        <label><input type="checkbox" data-preference="timezone-auto" data-default="true"> {{t "prefs.region.timezone_auto"}}</label>
        <form class="timezone-form" method="post" action="/app/preferences/timezone?channel={{.Channel}}">
          <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
          <label for="pref-timezone">{{t "prefs.region.timezone"}}</label>
          <input id="pref-timezone" type="text" name="timezone" value="{{.Timezone}}" list="pref-timezone-options" autocomplete="off" required>
          <datalist id="pref-timezone-options" data-timezone-options></datalist>
          <button class="button" type="submit">{{t "prefs.region.timezone_set"}}</button>
        </form>
        <p class="dialog-note">{{t "prefs.region.timezone_note"}}</p>
      </fieldset>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-av" aria-labelledby="pref-tab-av" tabindex="0" hidden>
      <h3>Audio &amp; video</h3>
      <fieldset><legend>Devices</legend>
        <label for="pref-huddle-microphone">Microphone</label>
        <select id="pref-huddle-microphone" class="preference-select" data-preference="huddle-microphone" data-media-devices="audioinput"><option value="">System default</option></select>
        <label for="pref-huddle-camera">Camera</label>
        <select id="pref-huddle-camera" class="preference-select" data-preference="huddle-camera" data-media-devices="videoinput"><option value="">System default</option></select>
      </fieldset>
      <fieldset><legend>Joining a huddle</legend>
        <label><input type="checkbox" data-preference="huddle-join-muted"> Mute my microphone when I join a huddle</label>
      </fieldset>
      <p class="dialog-note">Devices are named once this browser has been allowed to use them; until then they are numbered. A device that is no longer connected falls back to the system default.</p>
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-privacy" aria-labelledby="pref-tab-privacy" tabindex="0" hidden>
      <h3>Privacy &amp; visibility</h3>
      <fieldset><legend>Slack Connect discoverability</legend>
        <label><input type="checkbox" data-preference="discoverable-by-email" data-default="true"> Let people in other organizations find me by my email address</label>
      </fieldset>
      <p class="dialog-note">This applies only while your workspace is discoverable; when it is not, nobody outside finds you by email either way.</p>
      <h4 id="pref-hidden-heading">People you've hidden</h4>
      <p class="dialog-note">Their messages are hidden behind a click-through and their names and photos are hidden in conversations. Nobody is told who you hide. Hide someone from their profile.</p>
      {{if .HiddenPeople}}<ul class="hidden-people" aria-labelledby="pref-hidden-heading">{{range .HiddenPeople}}<li><span>{{.Name}}</span><form method="post" action="/app/people/hidden"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="target" value="{{.ID}}"><input type="hidden" name="hidden" value="false"><input type="hidden" name="return" value="{{$.ReturnTo}}"><button type="submit" aria-label="Unhide {{.Name}}">Unhide</button></form></li>{{end}}</ul>{{else}}<p class="dialog-note">You haven't hidden anyone.</p>{{end}}
    </section>
    <section class="preferences-panel" role="tabpanel" id="pref-advanced" aria-labelledby="pref-tab-advanced" tabindex="0" hidden>
      <h3>Advanced</h3>
      {{template "composer-preferences"}}
      <p class="dialog-note">Your preferences are kept with your account, so they follow you to every browser you sign in from.</p>
    </section>
  </div>
</div>{{end}}`
