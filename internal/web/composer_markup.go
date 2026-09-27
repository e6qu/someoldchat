package web

// composerIcons are SameOldChat's own monochrome line icons for the composer.
// They inherit currentColor, so they follow the theme and the pressed state,
// and they are decorative: every control that carries one also carries an
// accessible name.
const (
	composerIconBold      = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M6 4h5a3 3 0 0 1 0 6H6zm0 6h6a3 3 0 0 1 0 6H6z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/></svg>`
	composerIconItalic    = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M9 4h6M5 16h6M12 4 8 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`
	composerIconStrike    = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M4 10h12M13.5 6.5C13 5 11.7 4 10 4 7.8 4 6.5 5.2 6.5 6.7M6.5 13.5C7 15 8.3 16 10 16c2.2 0 3.5-1.2 3.5-2.7" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`
	composerIconLink      = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M8.5 11.5a3 3 0 0 0 4.2 0l2.6-2.6a3 3 0 0 0-4.2-4.2l-1 1M11.5 8.5a3 3 0 0 0-4.2 0l-2.6 2.6a3 3 0 0 0 4.2 4.2l1-1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`
	composerIconOrdered   = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M8 5h9M8 10h9M8 15h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><path d="M3 4h1.5v3M3 7h3M3 12.5c.3-.6 2.7-.8 2.5.5-.1.7-2.5 1.5-2.5 2.5h2.6" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/></svg>`
	composerIconBulleted  = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M8 5h9M8 10h9M8 15h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><circle cx="4" cy="5" r="1.4" fill="currentColor"/><circle cx="4" cy="10" r="1.4" fill="currentColor"/><circle cx="4" cy="15" r="1.4" fill="currentColor"/></svg>`
	composerIconQuote     = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M4 4v12M8 6h8M8 10h8M8 14h5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`
	composerIconCode      = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m7 6-4 4 4 4M13 6l4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`
	composerIconCodeBlock = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect x="2.5" y="3.5" width="15" height="13" rx="2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="m8 8-2 2 2 2M12 8l2 2-2 2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`
	composerIconPlus      = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M10 4v12M4 10h12" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>`
	composerIconFormat    = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m2.5 15 4-10 4 10M4 11.5h5M16.5 9.5v5.5M16.5 12c0-1.4-1-2.5-2.3-2.5S12 10.6 12 12.2s1 2.8 2.2 2.8 2.3-1.3 2.3-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>`
	composerIconEmoji     = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><circle cx="10" cy="10" r="7" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M7 12c.7 1 1.8 1.6 3 1.6s2.3-.6 3-1.6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/><circle cx="7.6" cy="8.2" r="1" fill="currentColor"/><circle cx="12.4" cy="8.2" r="1" fill="currentColor"/></svg>`
	composerIconMention   = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><circle cx="10" cy="10" r="3" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M13 7v4c0 1.3 1 2 2 2s2-.9 2-3a7 7 0 1 0-3 5.7" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`
	composerIconVideo     = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect x="2.5" y="5" width="10.5" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="m13 8.5 4.5-2.5v8L13 11.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/></svg>`
	composerIconAudio     = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect x="7.5" y="2.5" width="5" height="9" rx="2.5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M4.5 9.5a5.5 5.5 0 0 0 11 0M10 15v2.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>`
	composerIconSlash     = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><rect x="2.5" y="2.5" width="15" height="15" rx="3" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="m12.5 6-5 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`
	composerIconSend      = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M3 10 17 3.5 13 17l-3.2-5.3zM9.8 11.7 17 3.5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/></svg>`
	composerIconChevron   = `<svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m6 8 4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`
)

// composerPartial is the composer's markup. Without JavaScript it is an
// ordinary form: the textarea is the field, Enter adds a line and Send
// posts, the schedule menu's suggested and custom times submit to the
// schedule route, and the upload form stages files with a full reload. The
// script replaces the textarea with the rich editor, serialising back into
// the same field, so the server contract is the textarea's in both cases.
const composerPartial = `{{define "composer"}}  {{if .CanUpload}}<form class="upload-form" id="{{.IDPrefix}}upload-form" method="post" action="{{.StageUploadURL}}" enctype="multipart/form-data" hidden>
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <input type="hidden" id="{{.IDPrefix}}upload-comment" name="text" value="{{.Draft}}">
    <input type="hidden" id="{{.IDPrefix}}upload-draft-attachments" name="draft_attachments" value="{{.DraftJSON}}">
    <input id="{{.IDPrefix}}upload-file" type="file" name="file" multiple aria-label="Files to attach">
  </form>{{end}}
  <div class="composer-outbox" id="{{.IDPrefix}}composer-outbox" aria-live="polite"></div>
  <form class="composer{{if .Error}} is-error{{end}}" id="{{.IDPrefix}}composer" data-composer="{{if .Thread}}thread{{else}}channel{{end}}" method="post" action="{{.ComposeURL}}" hx-post="{{.ComposeURL}}" hx-target="{{.HXTarget}}" data-newest="{{.Newest}}" data-draft-url="{{.DraftURL}}" data-channel="{{.Channel}}" data-channel-label="{{.ChannelLabel}}" data-member-count="{{.MemberCount}}"{{if .CanInvite}} data-invite-url="{{.InviteURL}}"{{end}}{{if .IsDirect}} data-direct{{end}}>
    <p class="form-error" id="{{.IDPrefix}}composer-error" role="alert" tabindex="-1"{{if .Error}} autofocus{{else}} hidden{{end}}>{{.Error}}</p>
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <input type="hidden" name="timezone" data-browser-timezone value="UTC">
    <input type="hidden" id="{{.IDPrefix}}draft-attachments" name="draft_attachments" value="{{.DraftJSON}}">
    <input type="hidden" name="client_msg_id" value="">
    {{if .Thread}}<input type="hidden" name="thread_ts" value="{{.ThreadTimestamp}}">{{else if .ViewThread}}<input type="hidden" name="view_thread" value="{{.ViewThread}}">{{end}}
    <div class="composer-format" id="{{.IDPrefix}}composer-format" role="toolbar" aria-label="Formatting" hidden>
      <button class="composer-tool" type="button" data-format="bold" aria-pressed="false" aria-label="Bold" data-tip="Bold" data-tip-key="B" {{ariaKeyshortcuts "Bold"}}>` + composerIconBold + `</button>
      <button class="composer-tool" type="button" data-format="italic" aria-pressed="false" aria-label="Italic" data-tip="Italic" data-tip-key="I" {{ariaKeyshortcuts "Italic"}}>` + composerIconItalic + `</button>
      <button class="composer-tool" type="button" data-format="strike" aria-pressed="false" aria-label="Strikethrough" data-tip="Strikethrough" data-tip-key="Shift+X" {{ariaKeyshortcuts "Strikethrough"}}>` + composerIconStrike + `</button>
      <span class="composer-separator" aria-hidden="true"></span>
      <button class="composer-tool" type="button" data-format="link" aria-label="Link" aria-haspopup="dialog" data-tip="Link" data-tip-key="Shift+U" {{ariaKeyshortcuts "Link"}}>` + composerIconLink + `</button>
      <span class="composer-separator" aria-hidden="true"></span>
      <button class="composer-tool" type="button" data-format="ordered" aria-pressed="false" aria-label="Ordered list" data-tip="Ordered list" data-tip-key="Shift+7" {{ariaKeyshortcuts "Ordered list"}}>` + composerIconOrdered + `</button>
      <button class="composer-tool" type="button" data-format="bulleted" aria-pressed="false" aria-label="Bulleted list" data-tip="Bulleted list" data-tip-key="Shift+8" {{ariaKeyshortcuts "Bulleted list"}}>` + composerIconBulleted + `</button>
      <span class="composer-separator" aria-hidden="true"></span>
      <button class="composer-tool" type="button" data-format="quote" aria-pressed="false" aria-label="Blockquote" data-tip="Blockquote" data-tip-key="Shift+9" {{ariaKeyshortcuts "Blockquote"}}>` + composerIconQuote + `</button>
      <span class="composer-separator" aria-hidden="true"></span>
      <button class="composer-tool" type="button" data-format="code" aria-pressed="false" aria-label="Code" data-tip="Code" data-tip-key="Shift+C" {{ariaKeyshortcuts "Code"}}>` + composerIconCode + `</button>
      <button class="composer-tool" type="button" data-format="codeblock" aria-pressed="false" aria-label="Code block" data-tip="Code block" data-tip-key="Alt+Shift+C" {{ariaKeyshortcuts "Code block"}}>` + composerIconCodeBlock + `</button>
    </div>
    <label class="visually-hidden" for="{{.IDPrefix}}text" id="{{.IDPrefix}}text-label">{{.Label}}</label>
    <textarea id="{{.IDPrefix}}text" class="composer-input" name="text" rows="2"{{if .Autofocus}} autofocus{{end}} role="combobox" aria-describedby="{{.IDPrefix}}composer-hint {{.IDPrefix}}composer-count" aria-autocomplete="list" aria-haspopup="listbox" aria-controls="{{.IDPrefix}}composer-suggestions" aria-expanded="false" {{ariaKeyshortcuts "Send the message"}} placeholder="{{.Label}}">{{.Draft}}</textarea>
    <div class="composer-suggestions" id="{{.IDPrefix}}composer-suggestions" role="listbox" aria-label="Suggestions" hidden></div>
    <div class="composer-attachments" id="{{.IDPrefix}}upload-preview" role="group" aria-label="Attachments">{{range .DraftAttachments}}<span class="staged-file">{{.Name}}</span>{{end}}</div>
    {{if .Thread}}<label class="composer-broadcast"><input type="checkbox" name="reply_broadcast" value="true"{{if .Broadcast}} checked{{end}}> {{.BroadcastLabel}}</label>{{end}}
    <div class="composer-footer">
      <div class="composer-toolbar" role="toolbar" aria-label="Composer actions">
        <details class="composer-menu composer-plus"><summary class="composer-tool composer-plus-button" role="button" aria-expanded="false" aria-label="Attach" aria-haspopup="menu" data-tip="Attach">` + composerIconPlus + `</summary>
          <div class="composer-popover" role="menu" aria-label="Attach">
            {{if .CanUpload}}<button type="button" role="menuitem" data-composer-action="upload" {{ariaKeyshortcuts "Attach a file"}}>Upload from your computer</button>{{end}}
            <a role="menuitem" href="/app/canvases?channel={{.Channel}}">Canvas</a>
            <a role="menuitem" href="/app/lists?channel={{.Channel}}">List</a>
            <a role="menuitem" href="/app/workflows?channel={{.Channel}}">Workflow</a>
            {{if .CanUpload}}<button type="button" role="menuitem" data-composer-action="snippet" aria-haspopup="dialog">Text snippet</button>{{end}}
            {{if .HasShortcuts}}<button type="button" role="menuitem" data-composer-action="shortcuts" aria-haspopup="dialog">Shortcuts</button>{{end}}
          </div>
        </details>
        <button class="composer-tool" type="button" data-format-toggle aria-pressed="false" aria-controls="{{.IDPrefix}}composer-format" aria-label="Show formatting" data-tip="Show formatting" hidden>` + composerIconFormat + `</button>
        <button class="composer-tool" type="button" data-open-emoji-picker data-emoji-target="composer" aria-label="Emoji" aria-haspopup="dialog" aria-controls="emoji-picker-dialog" data-tip="Emoji">` + composerIconEmoji + `</button>
        <button class="composer-tool" type="button" data-composer-action="mention" aria-label="Mention someone" data-tip="Mention someone" hidden>` + composerIconMention + `</button>
        {{if .CanUpload}}<span class="composer-separator" aria-hidden="true"></span>
        <button class="composer-tool" type="button" data-record-clip="video" aria-label="Record video clip" aria-haspopup="dialog" aria-controls="clip-recorder" data-tip="Record video clip">` + composerIconVideo + `</button>
        <button class="composer-tool" type="button" data-record-clip="audio" aria-label="Record audio clip" aria-haspopup="dialog" aria-controls="clip-recorder" data-tip="Record audio clip">` + composerIconAudio + `</button>{{end}}
        {{if .HasShortcuts}}<span class="composer-separator" aria-hidden="true"></span>
        <button class="composer-tool" type="button" data-composer-action="slash" aria-label="Shortcuts" aria-haspopup="dialog" aria-controls="shortcut-browser" data-tip="Shortcuts">` + composerIconSlash + `</button>{{end}}
      </div>
      <div class="send-actions">
        <button class="send" type="submit" aria-label="Send now" data-tip="Send now">` + composerIconSend + `<span class="send-label">Send</span></button>
        {{if .CanSchedule}}<details class="schedule-menu"><summary class="send schedule-toggle" role="button" aria-expanded="false" aria-label="Schedule for later" aria-haspopup="menu" data-tip="Schedule for later">` + composerIconChevron + `</summary>
          <div class="schedule-popover" role="menu" aria-labelledby="{{.IDPrefix}}schedule-heading">
            <h3 id="{{.IDPrefix}}schedule-heading">Schedule message</h3>
            <button type="submit" role="menuitem" formaction="{{.ScheduleURL}}" name="schedule_preset" value="tomorrow" data-schedule-preset="tomorrow">Tomorrow at 9:00 AM</button>
            <button type="submit" role="menuitem" formaction="{{.ScheduleURL}}" name="schedule_preset" value="monday" data-schedule-preset="monday">Monday at 9:00 AM</button>
            <details class="schedule-custom" data-schedule-custom><summary role="menuitem">Custom time</summary>
              <label for="{{.IDPrefix}}schedule-at">Date and time<input id="{{.IDPrefix}}schedule-at" type="datetime-local" name="schedule_at" value="{{.ScheduleAt}}" data-schedule-at></label>
              <button type="submit" formaction="{{.ScheduleURL}}" name="schedule_preset" value="custom">Schedule message</button>
            </details>
            <input type="hidden" name="post_at">
            <p class="schedule-zone" data-schedule-zone>Times are in your time zone.</p>
            <a href="{{.ScheduledURL}}">View scheduled messages</a>
          </div>
        </details>{{end}}
      </div>
    </div>
  </form>
  <div class="composer-below">
    {{if not .Thread}}<div id="typing" class="typing-region" data-typing="/app/typing?channel={{.Channel}}" data-channel="{{.Channel}}">{{template "typing" .Typing}}</div>{{end}}
    <p class="composer-hint" id="{{.IDPrefix}}composer-hint" data-composer-hint><span data-hint-send><kbd>Shift</kbd> + <kbd>Return</kbd> to add a new line</span></p>
    <p class="composer-count" id="{{.IDPrefix}}composer-count" role="status" hidden></p>
  </div>{{end}}
{{define "composer-directory"}}<template id="composer-directory">{{range .People}}<i data-kind="person" data-id="{{.ID}}" data-name="{{.Name}}" data-real="{{.RealName}}" data-display="{{.DisplayName}}" data-avatar="{{.AvatarURL}}" data-initial="{{.Initial}}"{{if .Member}} data-member{{end}}{{if .Bot}} data-bot{{end}}{{if .Self}} data-self{{end}}></i>{{end}}{{range .Groups}}<i data-kind="group" data-id="{{.ID}}" data-name="{{.Handle}}" data-real="{{.Name}}" data-description="{{.Description}}" data-count="{{.MemberCount}}"></i>{{end}}{{range .Specials}}<i data-kind="special" data-id="{{.Entity}}" data-name="{{.Name}}" data-description="{{.Description}}"></i>{{end}}{{range .Channels}}<i data-kind="channel" data-id="{{.ID}}" data-name="{{.Name}}"{{if .Private}} data-private{{end}}></i>{{end}}{{range .Commands}}<i data-kind="command" data-name="{{.Command}}" data-description="{{.Description}}" data-hint="{{.UsageHint}}" data-app="{{.AppName}}"></i>{{end}}</template>{{end}}
{{define "composer-dialogs"}}{{template "composer-directory" .Directory}}
  {{if or .Directory.Commands .Directory.Shortcuts}}<dialog class="shortcut-browser" id="shortcut-browser" aria-labelledby="shortcut-browser-title">
    <div class="shortcut-browser-head"><label><span id="shortcut-browser-title">Shortcuts</span><input id="shortcut-browser-query" type="search" autocomplete="off" placeholder="Search shortcuts and commands"></label><button id="shortcut-browser-close" type="button" aria-label="Close shortcuts">×</button></div>
    <div class="shortcut-browser-results" id="shortcut-browser-results">{{range .Directory.Commands}}<button type="button" data-browser-command="{{.Command}}" data-shortcut-search="{{.Command}} {{.Description}} {{.UsageHint}} {{.AppName}}"><strong>{{.Command}}</strong><span>{{.Description}}{{if .UsageHint}}<small>{{.UsageHint}}</small>{{end}}{{if .AppName}}<small>{{.AppName}}</small>{{end}}</span></button>{{end}}{{range $shortcut := .Directory.Shortcuts}}
      <form method="post" action="/app/shortcut" hx-post="/app/shortcut" data-shortcut-search="{{$shortcut.Name}} {{$shortcut.Description}} {{$shortcut.AppName}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="channel" value="{{$.Channel}}"><input type="hidden" name="app_id" value="{{$shortcut.AppID}}"><input type="hidden" name="callback_id" value="{{$shortcut.CallbackID}}">
        <button type="submit"><strong>{{$shortcut.Name}}</strong><span>{{$shortcut.Description}}<small>{{$shortcut.AppName}}</small></span></button>
      </form>{{end}}</div>
    <p class="shortcut-browser-empty" id="shortcut-browser-empty" role="status" hidden>No matching shortcuts.</p>
  </dialog>{{end}}
  {{if .CanUpload}}<dialog class="clip-recorder" id="clip-recorder" aria-labelledby="clip-recorder-title" aria-describedby="clip-recorder-status">
    <h2 id="clip-recorder-title">Record a clip</h2>
    <p id="clip-recorder-status" role="status" aria-live="polite">Choose an audio or video clip from the composer.</p>
    <video id="clip-recorder-preview" autoplay muted playsinline hidden></video>
    <div class="clip-recorder-actions"><button type="button" id="clip-recorder-cancel">Cancel</button><button class="clip-stop" type="button" id="clip-recorder-stop" disabled>Stop recording</button></div>
  </dialog>
  <dialog class="composer-dialog" id="composer-snippet-dialog" aria-labelledby="composer-snippet-title">
    <form method="dialog" class="composer-dialog-form" data-snippet-form>
      <h2 id="composer-snippet-title">Create a text snippet</h2>
      <label for="composer-snippet-name">Title (optional)<input id="composer-snippet-name" type="text" maxlength="200" autocomplete="off"></label>
      <label for="composer-snippet-body">Content<textarea id="composer-snippet-body" rows="8" required spellcheck="false"></textarea></label>
      <div class="composer-dialog-actions"><button type="button" value="cancel" data-dialog-cancel>Cancel</button><button type="submit" value="save" class="composer-dialog-primary">Create snippet</button></div>
    </form>
  </dialog>{{end}}
  <dialog class="composer-dialog" id="composer-link-dialog" aria-labelledby="composer-link-title">
    <form method="dialog" class="composer-dialog-form" data-link-form>
      <h2 id="composer-link-title">Add link</h2>
      <label for="composer-link-text">Text<input id="composer-link-text" type="text" autocomplete="off" maxlength="1000"></label>
      <label for="composer-link-url">Link<input id="composer-link-url" type="text" inputmode="url" autocomplete="off" maxlength="3000" required aria-describedby="composer-link-error"></label>
      <p class="composer-dialog-error" id="composer-link-error" role="alert" hidden></p>
      <div class="composer-dialog-actions"><button type="button" value="cancel" data-dialog-cancel>Cancel</button><button type="submit" value="save" class="composer-dialog-primary">Save</button></div>
    </form>
  </dialog>
  <dialog class="composer-dialog" id="composer-schedule-dialog" aria-labelledby="composer-schedule-title" aria-describedby="composer-schedule-zone">
    <form method="dialog" class="composer-dialog-form" data-schedule-form>
      <h2 id="composer-schedule-title">Schedule message</h2>
      <div class="composer-dialog-row"><label for="composer-schedule-date">Date<input id="composer-schedule-date" type="date" required></label><label for="composer-schedule-time">Time<input id="composer-schedule-time" type="time" required></label></div>
      <p class="composer-dialog-note" id="composer-schedule-zone">Times are in your time zone.</p>
      <p class="composer-dialog-error" id="composer-schedule-error" role="alert" hidden></p>
      <div class="composer-dialog-actions"><button type="button" value="cancel" data-dialog-cancel>Cancel</button><button type="submit" value="save" class="composer-dialog-primary">Schedule message</button></div>
    </form>
  </dialog>
  <dialog class="composer-dialog" id="composer-broadcast-dialog" aria-labelledby="composer-broadcast-title" aria-describedby="composer-broadcast-body">
    <form method="dialog" class="composer-dialog-form">
      <h2 id="composer-broadcast-title">Send a notification to everyone?</h2>
      <p id="composer-broadcast-body"></p>
      <div class="composer-dialog-actions"><button type="submit" value="cancel">Cancel</button><button type="submit" value="send" class="composer-dialog-primary">Send now</button></div>
    </form>
  </dialog>{{end}}
`

// composerPreferencesPartial is the composer's preferences: what Enter does and
// whether markup is formatted. They are rendered in the shell's Preferences >
// Advanced section on every page (mustPage parses this with the shell), and
// written to the same per-browser keys whether composerScript or the shell's
// script handles the change.
const composerPreferencesPartial = `{{define "composer-preferences"}}<fieldset class="composer-preferences" data-composer-preferences>
  <legend>When writing a message, press <kbd>Enter</kbd> to…</legend>
  <label><input type="radio" name="composer-enter" value="send" data-composer-preference="enter" checked> Send the message</label>
  <label><input type="radio" name="composer-enter" value="newline" data-composer-preference="enter"> Start a new line (use <kbd data-keyboard-apple>⌘</kbd><kbd data-keyboard-other>Ctrl</kbd> + <kbd>Enter</kbd> to send)</label>
  <label class="composer-preferences-markup"><input type="checkbox" data-composer-preference="markup"> Format messages with markup</label>
  <p class="muted" data-composer-preference-status aria-live="polite">These preferences are saved in this browser and apply as soon as you choose them.</p>
</fieldset>{{end}}`

// composerStyle is the composer's presentation. The popovers are anchored to
// the composer itself: they used to be positioned against the page, so the
// mention list floated at the bottom-left of the window whichever composer
// asked for it.
const composerStyle = `<style>
.composer-wrap{grid-area:composer;padding:6px 20px 4px;background:var(--panel-strong);min-width:0}
.thread:has(.thread-composer-wrap){display:flex;flex-direction:column;overflow:hidden}
.thread:has(.thread-composer-wrap)>#thread-messages{flex:1 1 auto;min-height:0;overflow:auto}
.thread-composer-wrap{grid-area:auto;flex:0 0 auto;padding:8px 0 0;background:var(--panel)}
.composer{position:relative;border:1px solid var(--field-line);border-radius:9px;background:var(--panel-strong);padding:0;min-width:0}
.composer:focus-within{border-color:var(--focus);box-shadow:0 0 0 1px var(--focus)}
.composer.is-error{border-color:var(--danger)}
.composer.is-dragging{border-color:var(--action);box-shadow:0 0 0 3px color-mix(in srgb,var(--action) 25%,transparent)}
.composer>.form-error{margin:8px 8px 0}
.composer-format{display:flex;align-items:center;flex-wrap:wrap;gap:2px;padding:4px 6px;border-bottom:1px solid var(--line);border-radius:9px 9px 0 0;background:color-mix(in srgb,var(--panel) 60%,transparent)}
.composer-input{display:block;width:100%;min-height:42px;max-height:40vh;resize:none;overflow-y:auto;border:0;outline:0;background:transparent;color:var(--text);padding:10px 12px 4px;font:inherit;line-height:1.45}
.composer-editor{min-height:42px;max-height:40vh;overflow-y:auto;padding:10px 12px 4px;outline:0;white-space:pre-wrap;overflow-wrap:anywhere;color:var(--text);line-height:1.45;cursor:text}
.composer-editor>*{margin:0}
.composer-editor.is-empty::before{content:attr(data-placeholder);color:var(--muted);pointer-events:none;position:absolute}
.composer-editor p{margin:0;min-height:1.45em}
.composer-editor blockquote{margin:2px 0;padding-left:10px;border-left:4px solid var(--line)}
.composer-editor ul,.composer-editor ol{margin:2px 0;padding-left:24px}
.composer-editor pre{margin:4px 0;padding:8px 10px;border:1px solid var(--line);border-radius:6px;background:var(--hover);white-space:pre-wrap;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace}
.composer-editor code{padding:1px 4px;border:1px solid var(--line);border-radius:4px;background:var(--hover);color:var(--danger);font:.9em ui-monospace,SFMono-Regular,Consolas,monospace}
.composer-editor pre code{padding:0;border:0;background:transparent;color:inherit}
.composer-editor a{color:var(--action)}
.composer-pill{display:inline-block;padding:0 3px;border-radius:4px;background:color-mix(in srgb,var(--action) 16%,transparent);color:var(--action);font-weight:600;white-space:nowrap;user-select:all}
.composer-pill.is-special{background:color-mix(in srgb,#d29b05 22%,transparent);color:var(--text)}
.composer-footer{display:flex;justify-content:space-between;align-items:center;gap:8px;padding:4px 6px 6px;flex-wrap:wrap}
.composer-toolbar{display:flex;align-items:center;flex:1 1 auto;min-width:0;flex-wrap:wrap;gap:2px;border:0;padding:0;margin:0}
.composer-tool{position:relative;display:inline-flex;align-items:center;justify-content:center;width:30px;height:30px;border:0;border-radius:5px;background:transparent;color:var(--muted);cursor:pointer;padding:0;list-style:none}
.composer-tool::-webkit-details-marker{display:none}
.composer-tool svg{width:18px;height:18px}
.composer-tool:hover,.composer-tool:focus-visible,.composer-menu[open]>.composer-tool{background:var(--hover);color:var(--text)}
.composer-tool[aria-pressed=true]{background:color-mix(in srgb,var(--action) 18%,transparent);color:var(--text)}
.composer-plus-button{border-radius:50%;background:var(--hover)}
.composer-separator{width:1px;height:18px;margin:0 4px;background:var(--line)}
.send-actions [data-tip]:hover::after,.send-actions [data-tip]:focus-visible::after{left:auto;right:0;transform:none}
.thread-composer-wrap .composer-tool{width:28px}.thread-composer-wrap .composer-separator{margin:0 2px}
[data-tip]:hover::after,[data-tip]:focus-visible::after{content:attr(data-tip);position:absolute;z-index:20;bottom:calc(100% + 6px);left:50%;transform:translateX(-50%);padding:4px 8px;border-radius:5px;background:var(--text);color:var(--bg);font-size:12px;font-weight:700;white-space:nowrap;pointer-events:none}
.composer-menu{position:relative}
.composer-menu>summary{list-style:none}
.composer-popover{position:absolute;z-index:12;left:0;bottom:calc(100% + 6px);display:grid;min-width:230px;max-width:min(320px,calc(100vw - 32px));border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:6px}
.composer-popover button,.composer-popover a{display:flex;width:100%;min-height:32px;align-items:center;border:0;border-radius:5px;background:transparent;color:var(--text);padding:6px 10px;text-align:left;text-decoration:none;font:inherit;cursor:pointer}
.composer-popover button:hover,.composer-popover button:focus-visible,.composer-popover a:hover,.composer-popover a:focus-visible{background:var(--action);color:var(--on-strong)}
.composer-suggestions{position:absolute;z-index:14;left:0;right:0;bottom:calc(100% + 6px);max-height:min(300px,45vh);overflow:auto;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:6px}
.composer-suggestions [role=option]{display:flex;gap:10px;align-items:center;min-height:34px;border-radius:5px;padding:5px 9px;cursor:pointer;color:var(--text)}
.composer-suggestions [role=option][aria-selected=true]{background:var(--action);color:var(--on-strong)}
.composer-suggestions [role=option][aria-selected=true] .suggestion-meta{color:inherit}
.suggestion-avatar{flex:0 0 auto;display:grid;place-items:center;width:22px;height:22px;border-radius:5px;background:var(--hover);color:var(--text);font-size:11px;font-weight:800;overflow:hidden}.suggestion-avatar img{width:100%;height:100%;object-fit:cover}
.suggestion-name{font-weight:700;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.suggestion-meta{color:var(--muted);font-size:13px;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.suggestion-badge{margin-left:auto;flex:0 0 auto;color:var(--muted);font-size:12px}
.composer-suggestions [aria-selected=true] .suggestion-badge{color:inherit}
.suggestion-heading{margin:4px 9px;color:var(--muted);font-size:12px;font-weight:700}
.composer-attachments{display:flex;flex-wrap:wrap;gap:8px;padding:0 10px}
.composer-attachments:not(:empty){padding:6px 10px 2px}
.staged-file{position:relative;display:flex;align-items:center;gap:8px;max-width:260px;min-width:0;padding:6px 30px 6px 6px;border:1px solid var(--line);border-radius:8px;background:var(--panel);font-size:13px}
.staged-file-thumb{flex:0 0 auto;display:grid;place-items:center;width:40px;height:40px;border-radius:6px;background:var(--accent);color:var(--on-accent);font-size:10px;font-weight:800;overflow:hidden;text-transform:uppercase}.staged-file-thumb img{width:100%;height:100%;object-fit:cover}
.staged-file-copy{display:grid;min-width:0}.staged-file-name{font-weight:700;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.staged-file-meta{color:var(--muted);font-size:12px}
.staged-file-remove{position:absolute;top:3px;right:3px;display:grid;place-items:center;width:24px;height:24px;border:0;border-radius:50%;background:var(--text);color:var(--bg);font-size:14px;line-height:1;cursor:pointer}
.staged-file-move{border:0;background:transparent;color:var(--muted);padding:0 3px;cursor:pointer}
.staged-file progress{width:100%;height:4px}
.composer-broadcast{display:flex;align-items:center;gap:6px;padding:4px 12px 0;color:var(--muted);font-size:13px}
.send-actions{display:flex;align-items:center;margin-left:auto;border-radius:6px;overflow:visible}
.send{display:inline-flex;align-items:center;justify-content:center;gap:6px;height:30px;min-width:34px;border:0;border-radius:6px 0 0 6px;background:var(--ok);color:var(--on-strong);font-weight:700;padding:0 10px;cursor:pointer}
.send:only-child{border-radius:6px}
.send svg{width:17px;height:17px}
.send-label{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
.composer.is-empty .send,.composer.is-empty .schedule-toggle{background:transparent;color:var(--muted)}
.send:disabled{opacity:.6;cursor:progress}
.schedule-menu{position:relative}
.schedule-toggle{border-radius:0 6px 6px 0;border-left:1px solid color-mix(in srgb,var(--on-strong) 35%,transparent);min-width:28px;padding:0 4px;list-style:none}
.schedule-toggle::-webkit-details-marker{display:none}
.schedule-popover{position:absolute;z-index:12;right:0;bottom:calc(100% + 6px);display:grid;gap:2px;width:min(280px,calc(100vw - 32px));padding:8px;border:1px solid var(--line);border-radius:9px;background:var(--panel-strong);box-shadow:var(--shadow)}
.schedule-popover h3{margin:2px 8px 6px;font-size:14px}
.schedule-popover>button,.schedule-custom>summary{display:flex;align-items:center;width:100%;min-height:32px;border:0;border-radius:5px;background:transparent;color:var(--text);padding:6px 8px;text-align:left;font:inherit;cursor:pointer;list-style:none}
.schedule-custom>summary::-webkit-details-marker{display:none}
.schedule-popover>button:hover,.schedule-popover>button:focus-visible,.schedule-custom>summary:hover,.schedule-custom>summary:focus-visible{background:var(--action);color:var(--on-strong)}
.schedule-custom label{display:grid;gap:4px;margin:4px 8px;font-size:12px;font-weight:700}
.schedule-custom input{border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);padding:6px 8px;font:inherit}
.schedule-custom button{margin:4px 8px;border:0;border-radius:6px;background:var(--ok);color:var(--on-strong);padding:7px 10px;font-weight:800}
.schedule-zone{margin:6px 8px 2px;color:var(--muted);font-size:12px}
.schedule-popover a{margin:2px 8px;color:var(--action);font-size:12px;font-weight:700}
.composer-below{display:flex;align-items:flex-start;gap:12px;min-height:20px;padding:2px 2px 0;font-size:12px;color:var(--muted)}
.typing-region{flex:1 1 auto;min-width:0}
.composer-hint{margin:0 0 0 auto;visibility:hidden;white-space:nowrap}
.composer-hint.is-visible{visibility:visible}
.composer-hint kbd{font:inherit;font-weight:700}
.composer-count{margin:0;color:var(--danger);font-weight:700}
.composer-outbox:empty{display:none}
.composer-outbox{display:grid;gap:6px;margin:0 0 6px}
.composer-outbox-item{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:4px 10px;align-items:center;padding:8px 10px;border:1px dashed var(--danger);border-radius:8px;background:var(--panel)}
.composer-outbox-item.is-prompt{border:1px solid var(--line);border-left:4px solid var(--action)}
.composer-outbox-text{margin:0;color:var(--muted);white-space:pre-wrap;overflow-wrap:anywhere}
.composer-outbox-state{margin:0;color:var(--danger);font-size:12px;font-weight:700}
.composer-outbox-item.is-prompt .composer-outbox-state{color:var(--muted);font-weight:400}
.composer-outbox-actions{grid-column:2;grid-row:1/span 2;display:flex;gap:6px}
.composer-outbox-actions button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:5px 10px;font-weight:700;cursor:pointer}
.composer-dialog{width:min(460px,calc(100vw - 28px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);padding:0}
.composer-dialog::backdrop{background:#0008}
.composer-dialog-form{display:grid;gap:12px;padding:18px}
.composer-dialog h2{margin:0;font-size:18px}
.composer-dialog label{display:grid;gap:5px;font-weight:700}
.composer-dialog input,.composer-dialog textarea{width:100%;box-sizing:border-box;border:1px solid var(--field-line);border-radius:7px;background:var(--panel);color:var(--text);padding:8px 10px;font:inherit}
.composer-dialog textarea{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
.composer-dialog-row{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px}
.composer-dialog-note{margin:0;color:var(--muted);font-size:13px}
.composer-dialog-error{margin:0;color:var(--danger);font-weight:700}
.composer-dialog-actions{display:flex;justify-content:flex-end;gap:8px;flex-wrap:wrap}
.composer-dialog-actions button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel);color:var(--text);padding:8px 14px;font-weight:800;cursor:pointer}
.composer-dialog-actions .composer-dialog-primary{border-color:var(--ok);background:var(--ok);color:var(--on-strong)}
.composer-preferences{display:grid;gap:8px;border:0;padding:0;margin:0}
.composer-preferences legend{font-weight:800;margin-bottom:6px}
.composer-preferences label{display:flex;align-items:center;gap:8px}
.upload-form{display:none}
.message-quote{margin:2px 0;padding-left:10px;border-left:4px solid var(--line);color:inherit}
@media(max-width:800px){
.composer-wrap{padding:4px 8px 2px}
.thread-composer-wrap{padding:8px 0 0}
.composer-hint{display:none}
.composer-toolbar .composer-separator{display:none}
.composer-tool{width:32px;height:32px}
}
</style>`
