package web

// conversationDetailsPartial is the details dialog: a real modal <dialog> with
// Slack's About, Members, Integrations and Settings tabs. The page renders it
// only for ?details=1; the header's name button fetches that rendering and
// opens it in place, so the dialog has one server-side definition whether it
// arrived by link, by keyboard shortcut or without script.
const conversationDetailsPartial = `{{define "conversation-details"}}<dialog class="shell-dialog conversation-details" id="conversation-details" aria-labelledby="conversation-details-title" data-close-url="{{.Details.CloseURL}}" data-initial-tab="{{.Details.InitialTab}}" data-autoopen open>
  <div class="dialog-head conversation-details-head">
    <h2 id="conversation-details-title" tabindex="-1"><span class="channel-kind-icon" aria-hidden="true">{{if eq .Details.Kind "private"}}{{icon "lock"}}{{else if eq .Details.Kind "dm"}}{{icon "user"}}{{else if eq .Details.Kind "group"}}{{icon "people"}}{{else}}{{icon "hash"}}{{end}}</span><span class="visually-hidden">{{.Details.Type}} </span>{{.Details.Name}}</h2>
    <a class="dialog-close conversation-details-close" href="{{.Details.CloseURL}}" data-dialog-close aria-label="Close conversation details">{{icon "close"}}</a>
  </div>
  <p class="dialog-note details-type">{{.Details.Type}}{{if .Details.Archived}} · Archived{{end}}</p>
  <div class="details-tabs" role="tablist" data-shell-tabs aria-label="Conversation details">
    <a role="tab" id="details-tab-about" data-tab="about" href="{{.Details.TabURL "about"}}" aria-controls="details-about" {{if eq .Details.InitialTab "about"}}aria-selected="true"{{else}}aria-selected="false" tabindex="-1"{{end}}>About</a>
    <a role="tab" id="details-tab-members" data-tab="members" href="{{.Details.TabURL "members"}}" aria-controls="details-members" {{if eq .Details.InitialTab "members"}}aria-selected="true"{{else}}aria-selected="false" tabindex="-1"{{end}}>Members {{len .Details.Members}}</a>
    {{if .Details.IsChannel}}<a role="tab" id="details-tab-integrations" data-tab="integrations" href="{{.Details.TabURL "integrations"}}" aria-controls="details-integrations" {{if eq .Details.InitialTab "integrations"}}aria-selected="true"{{else}}aria-selected="false" tabindex="-1"{{end}}>Integrations</a>{{end}}
    {{if .Details.HasSettings}}<a role="tab" id="details-tab-settings" data-tab="settings" href="{{.Details.TabURL "settings"}}" aria-controls="details-settings" {{if eq .Details.InitialTab "settings"}}aria-selected="true"{{else}}aria-selected="false" tabindex="-1"{{end}}>Settings</a>{{end}}
  </div>
  <section class="details-panel" role="tabpanel" id="details-about" aria-labelledby="details-tab-about"{{if ne .Details.InitialTab "about"}} hidden{{end}}>
    <div class="details-card">
      {{if .Details.IsChannel}}<div class="details-row"><div><h3>Channel name</h3><p>{{if .Details.IsPrivate}}{{icon "lock"}}{{else}}{{icon "hash"}}{{end}} {{.Details.Name}}</p></div>{{if .Details.CanEdit}}<button class="details-edit" type="button" data-dialog-open="details-edit-name" aria-label="Edit channel name">Edit</button>{{end}}</div>{{end}}
      <div class="details-row"><div><h3>Topic</h3><p>{{if .Details.Topic}}{{.Details.Topic}}{{else}}<span class="muted-text">No topic set</span>{{end}}</p></div>{{if .Details.CanEdit}}<button class="details-edit" type="button" data-dialog-open="details-edit-topic" aria-label="Edit topic">Edit</button>{{end}}</div>
      <div class="details-row"><div><h3>Description</h3><p>{{if .Details.Purpose}}{{.Details.Purpose}}{{else}}<span class="muted-text">No description set</span>{{end}}</p></div>{{if .Details.CanEdit}}<button class="details-edit" type="button" data-dialog-open="details-edit-purpose" aria-label="Edit description">Edit</button>{{end}}</div>
      {{if .Details.CreatedOn}}<div class="details-row"><div><h3>Created by</h3><p>{{if .Details.CreatedBy}}{{.Details.CreatedBy}} on {{end}}{{.Details.CreatedOn}}</p></div></div>{{end}}
      <div class="details-row"><div><h3>Pinned messages</h3><p><a href="/app?channel={{.Details.ID}}&amp;tab=pins">View pinned messages</a></p></div></div>
    </div>
    {{if .Details.CanNotify}}<form class="details-card details-notify" id="conversation-notifications" method="post" action="/app/conversation/notifications?channel={{.Details.ID}}">
      <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
      <fieldset class="choice-inline"><legend>Notify me about</legend>
        <label><input type="radio" name="level" value="inherit"{{if eq .Details.NotificationLevel "inherit"}} checked{{end}}> Workspace default</label>
        <label><input type="radio" name="level" value="all"{{if eq .Details.NotificationLevel "all"}} checked{{end}}> All new messages</label>
        <label><input type="radio" name="level" value="mentions"{{if eq .Details.NotificationLevel "mentions"}} checked{{end}}> @mentions</label>
        <label><input type="radio" name="level" value="mute"{{if eq .Details.NotificationLevel "mute"}} checked{{end}}> Nothing</label>
      </fieldset>
      <label class="toggle-row"><input type="checkbox" name="follow_every_thread" value="true"{{if .Details.FollowEveryThread}} checked{{end}}> Get notified about all replies in threads</label>
      <button class="button" type="submit">Save notifications</button>
    </form>{{end}}
    {{if or .Details.CanLeave .Details.CanClose}}<div class="details-card details-leave">
      {{if .Details.CanLeave}}<form method="post" action="/app/conversation/leave?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger-link" type="submit">Leave channel</button></form>{{end}}
      {{if .Details.CanClose}}<form method="post" action="/app/conversation/leave?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger-link" type="submit">Close conversation</button></form>{{end}}
    </div>{{end}}
  </section>
  <section class="details-panel" role="tabpanel" id="details-members" aria-labelledby="details-tab-members"{{if ne .Details.InitialTab "members"}} hidden{{end}}>
    <div class="details-members-tools">
      <label class="visually-hidden" for="details-member-search">Find members</label>
      <input id="details-member-search" type="search" placeholder="Find members" autocomplete="off" data-member-filter aria-controls="details-member-list" data-empty="details-member-empty">
    </div>
    <ul class="conversation-members" id="details-member-list">{{range .Details.Members}}<li class="conversation-member" data-member-name="{{.Name}}"><span class="conversation-member-avatar" aria-hidden="true">{{.AuthorInitial}}<span class="presence-dot {{.Presence}}"></span></span><span class="conversation-member-name">{{.Name}}{{if .IsSelf}} (you){{end}} <span class="visually-hidden">({{if eq .Presence "active"}}active{{else if eq .Presence "away"}}away{{else}}automatic; activity unavailable{{end}})</span></span>{{if .StatusDisplay}}<span class="conversation-member-status"{{if .Profile.StatusText}} title="{{.Profile.StatusText}}"{{end}}>{{.StatusDisplay}}</span>{{end}}
      <details class="menu member-menu" data-menu><summary role="button" aria-haspopup="menu" aria-expanded="false" aria-label="Options for {{.Name}}">{{icon "kebab"}}</summary>
        <div class="menu-list menu-end" role="menu" aria-label="{{.Name}}">
          <a role="menuitem" href="/app/members?user={{.ID}}" data-open-profile="{{.ID}}">{{icon "user"}}<span>View profile</span></a>
          {{if and (not .IsSelf) $.Details.CanMessage}}<form class="menu-form" method="post" action="/app/conversation/open" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="users" value="{{.ID}}"><button type="submit" role="menuitem">{{icon "dms"}}<span>Message</span></button></form>{{end}}
          <button type="button" role="menuitem" data-copy-text="{{.Name}}" data-copied="Name copied.">{{icon "copy"}}<span>Copy name</span></button>
          {{if and (not .IsSelf) $.Details.CanRemove}}<form class="menu-form" method="post" action="/app/conversation/remove?channel={{$.Details.ID}}" role="none"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="user" value="{{.ID}}"><button type="submit" role="menuitem">{{icon "signout"}}<span>Remove from channel</span></button></form>{{end}}
        </div>
      </details></li>{{end}}</ul>
    <p class="dialog-note" id="details-member-empty" role="status"></p>
    {{if .Details.Truncated}}<p class="conversation-details-note">This workspace has more than 1,000 members. Use the member directory to find people outside this list.</p>{{end}}
    {{if .Details.CanInvite}}
      {{if .Details.Invitees}}
      <form class="conversation-setting" method="post" action="/app/conversation/invite?channel={{.Details.ID}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <label for="conversation-invitee">Add people<select id="conversation-invitee" name="user" required><option value="" selected disabled>Choose a workspace member</option>{{range .Details.Invitees}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
        <button type="submit">Add</button>
      </form>
      {{else}}<p class="conversation-details-note">Every available workspace member is already in this channel.</p>{{end}}
    {{end}}
    {{if .Details.CanAddPeople}}
    <details class="conversation-setting">
      <summary>Add people</summary>
      <form method="post" action="/app/conversation/add-people?channel={{.Details.ID}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <fieldset>
          <legend>Choose people to add</legend>
          {{range .Details.Invitees}}<label class="privacy"><input type="checkbox" name="user_{{.ID}}" value="1"> {{.Name}}</label>{{end}}
        </fieldset>
        <p class="conversation-details-note">Slack creates a new group DM. This conversation and its members stay unchanged.</p>
        <button type="submit">Next</button>
      </form>
    </details>
    {{end}}
  </section>
  {{if .Details.IsChannel}}<section class="details-panel" role="tabpanel" id="details-integrations" aria-labelledby="details-tab-integrations"{{if ne .Details.InitialTab "integrations"}} hidden{{end}}>
    <div class="details-card">
      <div class="details-row"><div><h3>Apps</h3>{{if .Apps}}<ul class="details-apps">{{range .Apps}}<li>{{icon "apps"}} <a href="/app/apps/{{.ID}}?channel={{$.Details.ID}}">{{.Name}}</a></li>{{end}}</ul>{{else}}<p class="muted-text">No apps are installed in this workspace.</p>{{end}}</div><a class="button" href="/app/apps?channel={{.Details.ID}}">Add an app</a></div>
      <div class="details-row"><div><h3>Workflows</h3><p class="muted-text">Automate routine work in this channel with Workflow Builder.</p></div><a class="button" href="/app/workflows">Add a workflow</a></div>
    </div>
  </section>{{end}}
  {{if .Details.HasSettings}}<section class="details-panel" role="tabpanel" id="details-settings" aria-labelledby="details-tab-settings"{{if ne .Details.InitialTab "settings"}} hidden{{end}}>
    {{if .Details.CanEdit}}<div class="details-card"><div class="details-row"><div><h3>Channel name</h3><p>{{.Details.Name}}</p></div><button class="details-edit" type="button" data-dialog-open="details-edit-name" aria-label="Rename channel">Edit</button></div></div>{{end}}
    {{if .Details.CanConvert}}
    <details class="conversation-setting details-card">
      <summary>Change to a private channel</summary>
      <form method="post" action="/app/conversation/convert-to-private?channel={{.Details.ID}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <label for="converted-channel-name">Private channel name<input id="converted-channel-name" name="name" maxlength="80" required pattern="[A-Za-z0-9_-]+"></label>
        <p class="conversation-details-note">Messages and files from this group DM will stay in the new private channel and will be visible to members added later. Everyone in this group DM will be notified.</p>
        <button type="submit">Change to Private</button>
      </form>
    </details>
    {{end}}
    {{if .Details.CanChangeVisibility}}
    <details class="conversation-setting details-card">
      <summary>{{if .Details.IsPrivate}}Change to a public channel{{else}}Change to a private channel{{end}}</summary>
      <form method="post" action="/app/conversation/visibility?channel={{.Details.ID}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <input type="hidden" name="private" value="{{if .Details.IsPrivate}}false{{else}}true{{end}}">
        <p class="conversation-details-note">{{if .Details.IsPrivate}}Everyone in the workspace will be able to read everything already said in this channel. That cannot be undone by making it private again.{{else}}Only members will be able to read this channel. Everything already said stays, and stays visible to the people already in it.{{end}}</p>
        <button type="submit">{{if .Details.IsPrivate}}Make public{{else}}Make private{{end}}</button>
      </form>
    </details>
    {{end}}
    {{if .Details.CanSetRetention}}
    <section class="details-card" id="conversation-retention" aria-labelledby="conversation-retention-heading">
      <h3 id="conversation-retention-heading">How long messages are kept</h3>
      <p>{{.Details.RetentionSummary}}</p>
      <form class="connect-invite" method="post" action="/app/conversation/retention?channel={{.Details.ID}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <label>Keep for<input name="duration_days" type="number" min="1" max="36499" value="{{.Details.RetentionDays}}"><span class="read-only">days</span></label>
        <button type="submit">Use this limit here</button>
      </form>
      {{if .Details.RetentionCustom}}<form method="post" action="/app/conversation/retention/remove?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Follow the workspace default instead</button></form>{{end}}
      <p class="read-only">Deletion is permanent. A shorter limit here deletes sooner than the workspace default; it cannot keep messages for longer than a channel-specific limit would.</p>
    </section>
    {{end}}
    {{if .Details.IsChannel}}
    <section class="details-card" id="conversation-connect" aria-labelledby="conversation-connect-heading">
      <h3 id="conversation-connect-heading">Shared with other organizations</h3>
      {{if .Details.Connected}}<p>In this channel: {{range $index, $org := .Details.Connected}}{{if $index}}, {{end}}{{$org.Name}}{{end}}</p>{{else}}<p class="read-only">Only this workspace is in this channel.</p>{{end}}
      {{if .Details.Outstanding}}<ul class="connect-invites" aria-label="Outstanding invitations">{{range .Details.Outstanding}}<li>
        <span><strong>{{.Target}}</strong> <span class="status">{{.Status}}</span>{{if .Expired}}<br><span class="status">expired on {{.Expires}} and can no longer be approved</span>{{else if .Expires}}<br><span class="status">valid until {{.Expires}}</span>{{end}}</span>
        <span class="connect-actions">
        {{if .CanApprove}}<form method="post" action="{{.ApproveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_id" value="{{.ID}}"><button type="submit">Approve</button></form>{{end}}
        {{if .CanRevoke}}<form method="post" action="{{.DenyURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_id" value="{{.ID}}"><button type="submit">Withdraw</button></form>{{end}}
        </span>
      </li>{{end}}</ul>{{end}}
      {{if .Details.CanConnect}}
      <form class="connect-invite" method="post" action="{{.Details.ConnectURL}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <label>Invite an organization<select name="target">{{range .Details.ConnectHosts}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
        <button type="submit">Send invitation</button>
      </form>
      <p class="read-only">An invitation is recorded here and takes effect only when the other organization accepts it. Everyone there will be able to read this channel's history from the moment they join.</p>
      {{end}}
    </section>
    {{end}}
    {{if .Details.CanArchive}}<div class="details-card details-leave"><form method="post" action="/app/conversation/archive?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="archived" value="{{if .Details.Archived}}false{{else}}true{{end}}"><button class="danger-link" type="submit">{{.Details.ArchiveVerb}} channel</button></form></div>{{end}}
  </section>{{end}}
  {{if .Details.CanEdit}}
  <dialog class="shell-dialog details-edit" id="details-edit-name" aria-labelledby="details-edit-name-title"><form method="post" action="/app/conversation/rename?channel={{.Details.ID}}">
    <div class="dialog-head"><h2 id="details-edit-name-title" tabindex="-1">Rename this channel</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close rename">{{icon "close"}}</button></div>
    <div class="dialog-body"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label for="conversation-name">Channel name</label><div class="prefixed-input"><span class="prefix" aria-hidden="true">{{icon "hash"}}</span><input id="conversation-name" type="text" name="name" value="{{.Details.Name}}" maxlength="80" required></div><p class="dialog-note">Names must be lowercase, without spaces or periods, and can’t be longer than 80 characters.</p></div>
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Save changes</button></div>
  </form></dialog>
  <dialog class="shell-dialog details-edit" id="details-edit-topic" aria-labelledby="details-edit-topic-title"><form method="post" action="/app/conversation/topic?channel={{.Details.ID}}">
    <div class="dialog-head"><h2 id="details-edit-topic-title" tabindex="-1">Edit topic</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close edit topic">{{icon "close"}}</button></div>
    <div class="dialog-body"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label for="conversation-topic">Topic</label><textarea id="conversation-topic" name="topic" maxlength="250">{{.Details.Topic}}</textarea><p class="dialog-note">Let people know what {{.Details.Name}} is focused on right now (ex. a project milestone).</p></div>
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Save</button></div>
  </form></dialog>
  <dialog class="shell-dialog details-edit" id="details-edit-purpose" aria-labelledby="details-edit-purpose-title"><form method="post" action="/app/conversation/purpose?channel={{.Details.ID}}">
    <div class="dialog-head"><h2 id="details-edit-purpose-title" tabindex="-1">Edit description</h2><button class="dialog-close" type="button" data-dialog-close aria-label="Close edit description">{{icon "close"}}</button></div>
    <div class="dialog-body"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label for="conversation-purpose">Description</label><textarea id="conversation-purpose" name="purpose" maxlength="250">{{.Details.Purpose}}</textarea><p class="dialog-note">Let people know what this channel is for.</p></div>
    <div class="dialog-foot"><button class="button" type="button" data-dialog-close>Cancel</button><button class="button primary" type="submit">Save</button></div>
  </form></dialog>
  {{end}}
</dialog>{{end}}`
