package web

// The conversation page's own parts of the frame: the Home and DMs panes
// beside the rail, the channel header, the conversation details dialog and the
// channel notification dialog. They render with the page's pageData.

const homePanePartial = `{{define "home-pane"}}<div class="sidebar-head">
  <details class="menu" data-menu><summary class="workspace-button" role="button" aria-haspopup="menu" aria-expanded="false"><span>{{.Shell.WorkspaceName}}</span>{{icon "chevron"}}</summary>{{template "workspace-menu" .Shell}}</details>
  {{if .Shell.CanMessage}}<a class="compose-button" href="{{.Shell.With "/app/dms"}}#new-dm" aria-label="New message" {{ariaKeyshortcuts "New message"}}>{{icon "compose"}}</a>{{end}}
</div>
<div class="sidebar-scroll">
  <nav class="sidebar-top" aria-label="Views">
    <a class="side-link" href="{{.Shell.With "/app/unreads"}}" data-pref-row="sidebar-unreads" aria-label="Unreads" {{ariaKeyshortcuts "Unreads"}}><span class="side-icon" aria-hidden="true">{{icon "unreads"}}</span><span class="side-text">Unreads</span></a>
    <a class="side-link" href="{{.Shell.With "/app/threads"}}" data-pref-row="sidebar-threads" data-default="true" aria-label="Threads" {{ariaKeyshortcuts "Threads"}}><span class="side-icon" aria-hidden="true">{{icon "threads"}}</span><span class="side-text">Threads</span></a>
    {{if .CanSchedule}}<a class="side-link" href="{{.Shell.With "/app/drafts"}}" data-pref-row="sidebar-drafts" data-default="true" aria-label="Drafts and sent"><span class="side-icon" aria-hidden="true">{{icon "send"}}</span><span class="side-text">Drafts &amp; sent</span></a>{{end}}
  </nav>
  {{range $section := .HomeSections}}
  <nav class="side-section{{if $section.Custom}} side-section-custom{{end}}" aria-label="{{$section.Label}}" data-section-key="{{$section.Key}}"{{if $section.DropTarget}} data-drop-section="{{$section.DropTarget}}"{{end}}>
    <div class="side-section-head"{{if $section.Custom}} draggable="true" data-section-drag="{{$section.ID}}"{{end}}>
      {{if $section.Custom}}<form class="section-collapse" method="post" action="/app/sidebar/sections/collapse?channel={{$.Channel}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{$section.ID}}"><input type="hidden" name="collapsed" value="{{if $section.Collapsed}}false{{else}}true{{end}}"><button class="section-toggle" type="submit" aria-expanded="{{if $section.Collapsed}}false{{else}}true{{end}}" aria-label="{{if $section.Collapsed}}Expand{{else}}Collapse{{end}} {{$section.Label}}">{{icon "caret"}}<span>{{$section.Label}}</span></button></form>
      {{else}}<button class="section-toggle" type="button" data-section-toggle aria-expanded="true">{{icon "caret"}}<span>{{$section.Label}}</span></button>{{end}}
      <details class="menu section-menu" data-menu>
        <summary role="button" aria-haspopup="menu" aria-expanded="false" aria-label="Options for {{$section.Label}}">{{icon "kebab"}}</summary>
        <div class="menu-list" role="menu" aria-label="{{$section.Label}} options">
          <a role="menuitem" href="{{$.Shell.With "/app/sidebar/sections/new"}}" data-dialog-open="section-dialog" data-section-dialog="create">{{icon "plus"}}<span>Create new section</span></a>
          {{if $section.Custom}}<a role="menuitem" href="{{$.Shell.With "/app/sidebar/sections/new"}}" data-dialog-open="section-rename-{{$section.ID}}">{{icon "compose"}}<span>Rename…</span></a>{{end}}
          {{with $section.UnreadIDs}}<form class="menu-form" method="post" action="/app/read/section?channel={{$.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}">{{range .}}<input type="hidden" name="conversation" value="{{.}}">{{end}}<button type="submit" role="menuitem">{{icon "check"}}<span>Mark all as read</span></button></form>{{end}}
          <hr role="separator">
          <div role="group" aria-label="Sort">
            <p class="menu-heading" role="none">Sort</p>
            <button type="button" role="menuitemradio" aria-checked="false" data-section-sort="alpha"><span>Alphabetically</span></button>
            {{if not $section.Apps}}<button type="button" role="menuitemradio" aria-checked="false" data-section-sort="recent"><span>By most recent activity</span></button>{{end}}
            <button type="button" role="menuitemradio" aria-checked="false" data-section-sort="priority"><span>Priority</span></button>
          </div>
          <div role="group" aria-label="Show">
            <p class="menu-heading" role="none">Show</p>
            <button type="button" role="menuitemradio" aria-checked="false" data-section-show="all"><span>All conversations</span></button>
            <button type="button" role="menuitemradio" aria-checked="false" data-section-show="unreads"><span>Unreads only</span></button>
            <button type="button" role="menuitemradio" aria-checked="false" data-section-show="mentions"><span>Mentions only</span></button>
          </div>
          {{if $section.Custom}}<hr role="separator">
          <details class="submenu" data-menu><summary role="menuitem" aria-haspopup="menu" aria-expanded="false">{{icon "activity"}}<span>Notifications</span></summary>
            <div class="menu-list" role="menu" aria-label="Notifications for {{$section.Label}}">
              <form class="menu-form" method="post" action="/app/sidebar/sections/notify?channel={{$.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{$section.ID}}">
                <button type="submit" role="menuitemradio" name="level" value="inherit" aria-checked="{{if or (eq $section.NotificationLevel "inherit") (eq $section.NotificationLevel "")}}true{{else}}false{{end}}"><span>Workspace default</span></button>
                <button type="submit" role="menuitemradio" name="level" value="all" aria-checked="{{if eq $section.NotificationLevel "all"}}true{{else}}false{{end}}"><span>All new messages</span></button>
                <button type="submit" role="menuitemradio" name="level" value="mentions" aria-checked="{{if eq $section.NotificationLevel "mentions"}}true{{else}}false{{end}}"><span>@mentions</span></button>
                <button type="submit" role="menuitemradio" name="level" value="mute" aria-checked="{{if eq $section.NotificationLevel "mute"}}true{{else}}false{{end}}"><span>Nothing</span></button>
              </form>
            </div>
          </details>
          {{if $section.CanUp}}<form class="menu-form" method="post" action="/app/sidebar/sections/move?channel={{$.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{$section.ID}}"><input type="hidden" name="direction" value="up"><button type="submit" role="menuitem"><span>Move section up</span></button></form>{{end}}
          {{if $section.CanDown}}<form class="menu-form" method="post" action="/app/sidebar/sections/move?channel={{$.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{$section.ID}}"><input type="hidden" name="direction" value="down"><button type="submit" role="menuitem"><span>Move section down</span></button></form>{{end}}
          <form class="menu-form" method="post" action="/app/sidebar/sections/delete?channel={{$.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{$section.ID}}"><button type="submit" role="menuitem"><span>Delete section</span></button></form>{{end}}
        </div>
      </details>
    </div>
    <div class="side-section-list" data-section-list>
      {{if not $section.Collapsed}}{{range $row := $section.Rows}}
      <div class="side-row" data-conversation="{{$row.ID}}" data-section="{{$row.SectionID}}" data-name="{{$row.SortName}}" data-recent="{{$row.RecentUnix}}" data-priority="{{$row.Priority}}"{{if $row.IsUnread}} data-unread{{end}}{{if $row.MentionCount}} data-mention{{end}}{{if $section.Draggable}} draggable="true"{{end}}>
        <a class="side-link{{if and $row.IsUnread (not $row.Muted)}} is-unread{{end}}{{if $row.Muted}} is-muted{{end}}" href="/app?channel={{$row.ID}}"{{if $row.Current}} aria-current="page"{{end}} aria-label="{{$row.AccessibleName}}">
          {{if eq $row.Kind "dm"}}<span class="side-avatar" aria-hidden="true">{{if $row.AvatarURL}}<img src="{{$row.AvatarURL}}" alt="">{{else}}{{$row.Initial}}{{end}}<span class="presence-dot {{$row.Presence}}"></span></span>
          {{else if eq $row.Kind "group"}}<span class="side-avatar" aria-hidden="true">{{$row.OthersCount}}</span>
          {{else}}<span class="side-icon" aria-hidden="true">{{if $row.IsPrivate}}{{icon "lock"}}{{else}}{{icon "hash"}}{{end}}</span>{{end}}
          <span class="side-text">{{$row.Name}}</span>
          {{if $row.HasDraft}}<span class="draft-badge" aria-hidden="true">{{icon "compose"}}</span>{{end}}{{if $row.MentionCount}}<span class="badge" aria-hidden="true">{{$row.MentionCount}}</span>{{else if $row.Muted}}<span class="side-icon" aria-hidden="true">{{icon "mute"}}</span>{{end}}
        </a>
        <details class="menu row-menu channel-menu" data-menu>
          <summary role="button" aria-haspopup="menu" aria-expanded="false" aria-label="Options for {{$row.Name}}">{{icon "kebab"}}</summary>
          <div class="menu-list" role="menu" aria-label="{{$row.Name}}">
            <a role="menuitem" href="/app?channel={{$row.ID}}&amp;details=1" data-details-trigger>{{icon "info"}}<span>{{if $row.IsChannelKind}}Open channel details{{else}}Open conversation details{{end}}</span></a>
            <button type="button" role="menuitem" data-copy-text="{{$row.Name}}" data-copied="Name copied.">{{icon "copy"}}<span>Copy name</span></button>
            <button type="button" role="menuitem" data-copy-text="/app?channel={{$row.ID}}" data-copy-url data-copied="Link copied.">{{icon "link"}}<span>Copy link</span></button>
            <form class="menu-form" method="post" action="/app/conversation/star?channel={{$row.ID}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="starred" value="{{if $row.IsStarred}}false{{else}}true{{end}}"><input type="hidden" name="return" value="{{$.Shell.ReturnTo}}"><button type="submit" role="menuitem">{{icon "star"}}<span>{{if $row.IsStarred}}Unstar{{else}}Star{{end}} {{$row.KindNoun}}</span></button></form>
            <form class="menu-form" method="post" action="/app/conversation/notifications?channel={{$row.ID}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="level" value="{{if $row.Muted}}inherit{{else}}mute{{end}}"><input type="hidden" name="follow_every_thread" value="{{if $row.FollowEveryThread}}true{{else}}false{{end}}"><input type="hidden" name="return" value="{{$.Shell.ReturnTo}}"><button type="submit" role="menuitem">{{icon "mute"}}<span>{{if $row.Muted}}Unmute{{else}}Mute{{end}} {{$row.KindNoun}}</span></button></form>
            <a role="menuitem" href="/app?channel={{$row.ID}}&amp;details=1&amp;tab=notifications" data-details-trigger>{{icon "activity"}}<span>Change notifications</span></a>
            {{if and $row.IsChannelKind $.SectionOptions}}<details class="submenu" data-menu><summary role="menuitem" aria-haspopup="menu" aria-expanded="false">{{icon "list"}}<span>Move to…</span></summary>
              <div class="menu-list" role="menu" aria-label="Move {{$row.Name}} to">
                <form class="menu-form" method="post" action="/app/sidebar/sections/assign?channel={{$.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="conversation" value="{{$row.ID}}">
                  {{range $.SectionOptions}}{{if ne .ID $row.SectionID}}<button type="submit" role="menuitem" name="section" value="{{.ID}}"><span>{{.Name}}</span></button>{{end}}{{end}}
                  {{if $section.Custom}}<button type="submit" role="menuitem" name="section" value=""><span>Channels</span></button>{{end}}
                </form>
              </div>
            </details>{{end}}
            {{if $row.CanLeave}}<hr role="separator"><form class="menu-form" method="post" action="/app/conversation/leave?channel={{$row.ID}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit" role="menuitem">{{icon "signout"}}<span>{{if $row.IsChannelKind}}Leave channel{{else}}Close conversation{{end}}</span></button></form>{{end}}
          </div>
        </details>
      </div>
      {{else}}<p class="side-empty">{{$section.Empty}}</p>{{end}}{{end}}
      {{if $section.AddChannels}}<details class="menu add-channels" data-menu>
        <summary class="side-add" role="button" aria-haspopup="menu" aria-expanded="false"><span class="side-icon" aria-hidden="true">{{icon "plus"}}</span><span class="side-text">Add channels</span></summary>
        <div class="menu-list" role="menu" aria-label="Add channels">
          {{if $.CanCreate}}<a role="menuitem" href="{{$.Shell.With "/app/channels/new"}}" data-dialog-open="create-channel"><span>Create a new channel</span></a>{{end}}
          <a role="menuitem" href="{{$.Shell.With "/app/channels"}}" {{ariaKeyshortcuts "Browse channels"}}><span>Browse channels</span></a>
        </div>
      </details>{{end}}
      {{if $section.AddCoworkers}}{{if $.Shell.ShowAuthAdmin}}<a class="side-add" href="/app/admin/auth">{{else}}<a class="side-add" href="{{$.Shell.With "/app/invitations/request"}}" data-dialog-open="invite-request-dialog">{{end}}<span class="side-icon" aria-hidden="true">{{icon "plus"}}</span><span class="side-text">Add coworkers</span></a>{{end}}
      {{if $section.Apps}}{{range $.Apps}}<div class="side-row"><a class="side-link" href="/app/apps/{{.ID}}?channel={{$.Channel}}" aria-label="{{.Name}}, app"><span class="side-icon" aria-hidden="true">{{icon "apps"}}</span><span class="side-text">{{.Name}}</span></a></div>{{end}}{{end}}
    </div>
  </nav>
  {{end}}
  {{if .SidebarTruncated}}<p class="side-note">You belong to more conversations than the sidebar shows. Use Jump to a conversation (Ctrl/⌘K) to reach the rest.</p>{{end}}
  <form id="sidebar-section-move-form" method="post" action="/app/sidebar/sections/move?channel={{.Channel}}" hidden><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="section_id" value=""><input type="hidden" name="before" value=""></form>
  <form id="sidebar-move-form" method="post" action="/app/sidebar/sections/assign?channel={{.Channel}}" hidden><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="conversation" value=""><input type="hidden" name="section" value=""></form>
</div>
{{if .Shell.CanRequestInvite}}<template data-dialog-template="invite-request-dialog"><dialog class="shell-dialog" id="invite-request-dialog" aria-labelledby="invite-request-dialog-title">
  <form method="post" action="/app/invitations/request?channel={{.Channel}}">
    <div class="dialog-head"><h2 id="invite-request-dialog-title" tabindex="-1">Invite people to {{.Shell.WorkspaceName}}</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close invite people">{{icon "close"}}</button></div>
    {{template "invite-request-fields" .Shell}}
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Send request</button></div>
  </form>
</dialog></template>{{end}}
<template data-dialog-template="section-dialog"><dialog class="shell-dialog" id="section-dialog" aria-labelledby="section-dialog-title">
  <form method="post" action="/app/sidebar/sections/create?channel={{.Channel}}">
    <div class="dialog-head"><h2 id="section-dialog-title" tabindex="-1">Create a section</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close create a section">{{icon "close"}}</button></div>
    <div class="dialog-body"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label for="new-section-name">Section name</label><input id="new-section-name" type="text" name="name" maxlength="80" placeholder="e.g. Priorities" required autocomplete="off"><p class="dialog-note">Organise conversations into sections. Only you see your sections.</p></div>
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Create section</button></div>
  </form>
</dialog></template>
{{range .HomeSections}}{{if .Custom}}<template data-dialog-template="section-rename-{{.ID}}"><dialog class="shell-dialog" id="section-rename-{{.ID}}" aria-labelledby="section-rename-{{.ID}}-title">
  <form method="post" action="/app/sidebar/sections/rename?channel={{$.Channel}}">
    <div class="dialog-head"><h2 id="section-rename-{{.ID}}-title" tabindex="-1">Rename section</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close rename section">{{icon "close"}}</button></div>
    <div class="dialog-body"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{.ID}}"><label for="section-name-{{.ID}}">Section name</label><input id="section-name-{{.ID}}" type="text" name="name" maxlength="80" value="{{.Label}}" required autocomplete="off"></div>
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Save</button></div>
  </form>
</dialog></template>{{end}}{{end}}{{end}}

{{define "channel-header"}}<div class="channel-header">
  <div class="channel-header-row">
    <div class="channel-identity">
      <h1 class="channel-title"><a class="channel-name-button" id="conversation-name-button" href="/app?channel={{.Channel}}&amp;details=1" data-details-trigger aria-haspopup="dialog" {{ariaKeyshortcuts "Conversation details"}}><span class="channel-kind-icon" aria-hidden="true">{{if eq .Kind "private"}}{{icon "lock"}}{{else if eq .Kind "dm"}}{{icon "user"}}{{else if eq .Kind "group"}}{{icon "people"}}{{else}}{{icon "hash"}}{{end}}</span><span class="visually-hidden">{{.KindText}} </span><span class="channel-name-text">{{.ChannelName}}</span>{{if .ChannelStatusDisplay}}<span class="channel-title-status"{{if .ChannelStatusText}} title="{{.ChannelStatusText}}"{{end}}>{{.ChannelStatusDisplay}}</span>{{end}}{{icon "chevron"}}</a></h1>
      {{if .ChannelTopic}}<p class="channel-meta" title="{{.ChannelTopic}}">{{.ChannelTopic}}</p>{{end}}
    </div>
    <div class="channel-actions">
      {{if .MemberCount}}<a class="member-count facepile" href="/app?channel={{.Channel}}&amp;details=1&amp;tab=members" data-details-trigger aria-label="{{.MemberCount}} member{{if ne .MemberCount 1}}s{{end}} — open the member list"><span class="faces" aria-hidden="true">{{range .FaceInitials}}<span>{{.}}</span>{{end}}</span><span aria-hidden="true">{{.MemberCount}}</span></a>{{end}}
      {{if .Huddle.Visible}}<details class="menu huddle-menu" data-menu>
        <summary class="header-button{{if .Huddle.Active}} is-live{{end}}" role="button" aria-haspopup="menu" aria-expanded="false" aria-label="Huddle{{if .Huddle.Active}}, in progress{{end}}">{{icon "huddle"}}{{icon "chevron"}}</summary>
        <div class="menu-list menu-end" role="menu" aria-label="Huddle">
          {{if .Huddle.Active}}{{if not .Huddle.Joined}}<form class="menu-form" method="post" action="{{.Huddle.JoinURL}}" hx-post="{{.Huddle.JoinURL}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem">{{icon "huddle"}}<span>Join huddle</span></button></form>{{else}}<p class="menu-note" role="none">You are in this huddle.</p>{{end}}
          {{else}}<form class="menu-form" method="post" action="{{.Huddle.StartURL}}" hx-post="{{.Huddle.StartURL}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem" aria-describedby="huddle-start-note">{{icon "huddle"}}<span>Start a huddle</span></button></form><p class="menu-note" id="huddle-start-note" role="none">Starting a huddle opens a call: your browser connects to each person who joins.</p>{{end}}
          <button type="button" role="menuitem" data-copy-text="/app?channel={{.Channel}}" data-copy-url data-copied="Huddle link copied.">{{icon "link"}}<span>Copy huddle link</span></button>
        </div>
      </details>{{end}}
      <details class="menu channel-overflow" data-menu>
        <summary class="header-button" role="button" aria-haspopup="menu" aria-expanded="false" aria-label="More actions for this conversation">{{icon "kebab"}}</summary>
        <div class="menu-list menu-end channel-overflow-menu" role="menu" aria-label="Conversation actions">
          <a role="menuitem" href="/app?channel={{.Channel}}&amp;details=1" data-details-trigger>{{icon "info"}}<span>{{if .IsChannelKind}}Open channel details{{else}}Open conversation details{{end}}</span></a>
          <button type="button" role="menuitem" data-copy-text="/app?channel={{.Channel}}" data-copy-url data-copied="Link copied.">{{icon "link"}}<span>Copy link</span></button>
          <button type="button" role="menuitem" data-copy-text="{{.ChannelName}}" data-copied="Name copied.">{{icon "copy"}}<span>Copy name</span></button>
          {{if .IsMember}}<hr role="separator">
          <form class="menu-form" method="post" action="/app/conversation/notifications?channel={{.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="level" value="{{if .Current.Muted}}inherit{{else}}mute{{end}}"><input type="hidden" name="follow_every_thread" value="{{if .Current.FollowEveryThread}}true{{else}}false{{end}}"><input type="hidden" name="return" value="{{.Shell.ReturnTo}}"><button type="submit" role="menuitem">{{icon "mute"}}<span>{{if .Current.Muted}}Unmute{{else}}Mute{{end}} {{.KindNoun}}</span></button></form>
          <button type="button" role="menuitem" data-dialog-open="channel-notifications">{{icon "activity"}}<span>Edit notifications</span></button>
          <form class="menu-form" method="post" action="/app/conversation/star?channel={{.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="starred" value="{{if .Current.IsStarred}}false{{else}}true{{end}}"><input type="hidden" name="return" value="{{.Shell.ReturnTo}}"><button type="submit" role="menuitem">{{icon "star"}}<span>{{if .Current.IsStarred}}Unstar{{else}}Star{{end}} {{.KindNoun}}</span></button></form>{{end}}
          <hr role="separator">
          <a role="menuitem" href="/app?channel={{.Channel}}&amp;tab=pins">{{icon "pin"}}<span>Pinned messages</span></a>
          {{if .CanvasURL}}<a role="menuitem" href="{{.CanvasURL}}">{{icon "canvas"}}<span>Open canvas</span></a>{{end}}
          <a role="menuitem" href="/app/apps?channel={{.Channel}}">{{icon "apps"}}<span>Add an app</span></a>
          <a role="menuitem" href="/app/workflows">{{icon "workflow"}}<span>Add a workflow</span></a>
          <hr role="separator">
          {{if .MarkReadURL}}<form class="menu-form" id="mark-read" method="post" action="{{.MarkReadURL}}" hx-post="{{.MarkReadURL}}" data-quiet="true" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="ts" value="{{.MarkReadTimestamp}}"><button type="submit" role="menuitem">{{icon "check"}}<span>Mark as read</span></button></form>{{end}}
          <form class="menu-form" id="mark-all-read" method="post" action="/app/read/all?channel={{.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem" {{ariaKeyshortcuts "Mark every conversation read"}}>{{icon "check"}}<span>Mark all as read</span></button></form>
          {{if .Current.CanLeave}}<hr role="separator"><form class="menu-form" method="post" action="/app/conversation/leave?channel={{.Channel}}" role="none"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit" role="menuitem">{{icon "signout"}}<span>{{if .IsChannelKind}}Leave channel{{else}}Close conversation{{end}}</span></button></form>{{end}}
        </div>
      </details>
    </div>
  </div>
  <nav class="channel-tabs" aria-label="Conversation tabs">
    <a href="/app?channel={{.Channel}}"{{if eq .Tab ""}} aria-current="page"{{end}}>{{icon "dms"}}<span>Messages</span></a>
    {{if .CanvasURL}}<a href="{{.CanvasURL}}" aria-label="Canvas — open the canvas for this conversation">{{icon "canvas"}}<span>Canvas</span></a>{{end}}
    <a href="/app?channel={{.Channel}}&amp;tab=pins"{{if eq .Tab "pins"}} aria-current="page"{{end}}>{{icon "pin"}}<span>Pins</span></a>
    {{range .CodeChannel.Tabs}}<a href="{{.URL}}"{{if .Current}} aria-current="page"{{end}}>{{icon "files"}}<span>{{.Label}}</span></a>{{end}}
    {{if .CanBookmark}}<details class="bookmark-add"><summary aria-label="Add a bookmark">{{icon "plus"}}</summary>
      <form class="bookmark-form" method="post" action="/app/bookmarks/add?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <label for="bookmark-link">Link</label><input id="bookmark-link" name="link" type="url" required placeholder="https://" maxlength="2000">
        <label for="bookmark-title">Name</label><input id="bookmark-title" name="title" type="text" required maxlength="255">
        <button class="button primary" type="submit">Add bookmark</button>
      </form>
    </details>{{end}}
  </nav>
  {{if or .CodeChannel.ContextBar .CodeChannel.Resource.URL}}<ul class="code-context-bar" aria-label="Agent context">{{with .CodeChannel.Resource}}{{if .URL}}<li><a href="{{.URL}}" rel="noopener noreferrer" target="_blank">{{icon "link"}}<span>{{if .Title}}{{.Title}}{{else}}{{.URL}}{{end}}</span></a></li>{{end}}{{end}}{{range .CodeChannel.ContextBar}}<li>{{if .URL}}<a href="{{.URL}}" rel="noopener noreferrer" target="_blank">{{.Label}}</a>{{else}}<span>{{.Label}}</span>{{end}}</li>{{end}}</ul>{{end}}
  {{if .Bookmarks}}<ul class="bookmarks-bar" aria-label="Bookmarks">{{range .Bookmarks}}<li><a href="{{.Link}}" rel="noopener noreferrer" target="_blank">{{icon "link"}}<span>{{.Title}}</span></a>{{if $.CanBookmark}}<form method="post" action="/app/bookmarks/remove?channel={{$.Channel}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="bookmark" value="{{.ID}}"><button type="submit" aria-label="Remove bookmark {{.Title}}">{{icon "close"}}</button></form>{{end}}</li>{{end}}</ul>{{end}}
  <div class="channel-notices">
    <p class="action-feedback" id="action-feedback" role="alert" tabindex="-1" hidden></p>
    {{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
  </div>
</div>{{end}}

{{define "channel-notifications"}}<template data-dialog-template="channel-notifications"><dialog class="shell-dialog" id="channel-notifications" aria-labelledby="channel-notifications-title">
  <form method="post" action="/app/conversation/notifications?channel={{.Channel}}">
    <div class="dialog-head"><h2 id="channel-notifications-title" tabindex="-1">Notifications for {{.ChannelPrefix}}{{.ChannelName}}</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close notifications">{{icon "close"}}</button></div>
    <div class="dialog-body">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="return" value="{{.Shell.ReturnTo}}">
      <fieldset class="choice-list"><legend>Notify me about</legend>
        <label><input type="radio" name="level" value="inherit"{{if or (eq .Current.NotificationLevel "inherit") (eq .Current.NotificationLevel "")}} checked{{end}}> <span><strong>Workspace default</strong><small>Follow your notification preferences</small></span></label>
        <label><input type="radio" name="level" value="all"{{if eq .Current.NotificationLevel "all"}} checked{{end}}> <span><strong>All new messages</strong></span></label>
        <label><input type="radio" name="level" value="mentions"{{if eq .Current.NotificationLevel "mentions"}} checked{{end}}> <span><strong>@mentions</strong></span></label>
        <label><input type="radio" name="level" value="mute"{{if eq .Current.NotificationLevel "mute"}} checked{{end}}> <span><strong>Nothing</strong><small>The conversation is muted: it is greyed in your sidebar and never shows as unread in bold.</small></span></label>
      </fieldset>
      <label class="toggle-row"><input type="checkbox" name="follow_every_thread" value="true"{{if .Current.FollowEveryThread}} checked{{end}}> Get notified about all replies in threads</label>
    </div>
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Save changes</button></div>
  </form>
</dialog></template>{{end}}`

// dmPanePartial is the DMs pane beside the rail. The DMs page and a conversation
// opened from it (?pane=dms) both draw it, so it is parsed into every page with
// the rest of the frame (mustPage).
const dmPanePartial = `{{define "dm-pane"}}<div class="sidebar-head">
  <h2 class="pane-title">Direct messages</h2>
  {{if .Shell.CanMessage}}<a class="compose-button" href="{{.Shell.With "/app/dms"}}#new-dm" aria-label="New message" {{ariaKeyshortcuts "New message"}}>{{icon "compose"}}</a>{{end}}
</div>
<div class="sidebar-scroll">
  <nav class="side-section dm-list" aria-label="Direct messages">
    {{range .Shell.Directs}}<div class="side-row dm-row">
      <a class="side-link{{if .IsUnread}} is-unread{{end}}{{if .Muted}} is-muted{{end}}" href="/app?channel={{.ID}}&amp;pane=dms"{{if .Current}} aria-current="page"{{end}} aria-label="{{.AccessibleName}}">
        {{if eq .Kind "dm"}}<span class="side-avatar" aria-hidden="true">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else}}{{.Initial}}{{end}}<span class="presence-dot {{.Presence}}"></span></span>{{else}}<span class="side-avatar" aria-hidden="true">{{.OthersCount}}</span>{{end}}
        <span class="dm-copy"><span class="side-text">{{.Name}}</span>{{if .Topic}}<small>{{.Topic}}</small>{{end}}</span>
        {{if not .RecentAt.IsZero}}<time class="dm-time" datetime="{{.RecentISO}}" data-local-time>{{.RecentISO}}</time>{{end}}
        {{if .MentionCount}}<span class="badge" aria-hidden="true">{{.MentionCount}}</span>{{end}}
      </a>
    </div>{{else}}<p class="side-empty">No direct messages yet. Start one with New message.</p>{{end}}
  </nav>
</div>{{end}}`
