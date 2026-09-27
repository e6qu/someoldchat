package web

// This file holds the timeline's message row — its markup, the page-level
// dialogs its actions open, its styles and its script — in one place, so the
// message surface (MSG-*, THREAD-*, ACT-* in specs/journeys/04) reads as one
// component rather than as runs spread through the workspace page.
//
// Script contract for other parts of the page:
//
//   - window.sameoldchatEditMessage(target) opens the in-place editor of a
//     message. target is a message element, its data-message-id, or its ts.
//     It returns false when the message is not the reader's or not editable.
//   - window.sameoldchatEditLastMessage(region) opens the editor on the
//     reader's newest editable message in region (the timeline when omitted):
//     the composer's Up-arrow-in-an-empty-composer binding.
//   - window.sameoldchatEmojiPicker.open(trigger, onSelect) opens the one
//     shared emoji picker anchored to trigger; onSelect(name) receives the
//     chosen colon-code name (with any skin tone). The composer's picker
//     button uses it through data-open-emoji-picker data-emoji-target="composer".
//   - A 204 mutation response may carry X-SameOldChat-Notice; the page script
//     raises it as the sameoldchat:notice event, which shows a toast.
//   - Opening a thread in place replaces aside.thread and dispatches
//     sameoldchat:thread-pane on document with {detail: {pane}} so a composer
//     inside the pane can bind itself. A pane without its own reply form falls
//     back to a full navigation.

// messageIcons are the toolbar and menu glyphs. They are this product's own
// drawings, sized for a 20px box.
const messageIcons = `{{define "icon-emoji"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><circle cx="9.5" cy="10.5" r="6.75" fill="none" stroke="currentColor" stroke-width="1.5"/><circle cx="7.2" cy="9.2" r="1" fill="currentColor"/><circle cx="11.8" cy="9.2" r="1" fill="currentColor"/><path d="M6.8 12.6a3.6 3.6 0 0 0 5.4 0" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/><path d="M16.5 1.8v4.4M14.3 4h4.4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>{{end}}
{{define "icon-thread"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M3.2 6.2A2 2 0 0 1 5.2 4.2h9.6a2 2 0 0 1 2 2v5.2a2 2 0 0 1-2 2H8.4L5 16.2v-2.8a2 2 0 0 1-1.8-2Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-forward"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M11 4.5 17 10l-6 5.5V12C7.6 12 5.2 13.1 3.5 15.5c.3-4.6 3-7.4 7.5-7.9Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-bookmark"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M5.5 3.8h9v12.4L10 12.6l-4.5 3.6Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-bookmark-filled"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M5.5 3.8h9v12.4L10 12.6l-4.5 3.6Z" fill="currentColor" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-more"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><circle cx="10" cy="4.6" r="1.4" fill="currentColor"/><circle cx="10" cy="10" r="1.4" fill="currentColor"/><circle cx="10" cy="15.4" r="1.4" fill="currentColor"/></svg>{{end}}
{{define "icon-pin"}}<svg class="context-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M7 3h6l-1 5 3 3H5l3-3Zm3 8v6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/></svg>{{end}}
{{define "icon-download"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M10 3.5v9m-4-4 4 4 4-4M4 16h12" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>{{end}}
`

// messagesPartial is the message list: the timeline, the thread pane, and
// every fragment the live refresh swaps in.
const messagesPartial = messageIcons + `{{define "messages"}}
{{range $message := .Messages}}
{{if $message.DaySeparator}}<div class="day-separator" role="separator" aria-label="{{$message.DaySeparator}}"><time class="day-pill" datetime="{{$message.DaySeparatorMachine}}" data-format="day">{{$message.DaySeparator}}</time></div>{{end}}
{{if $message.FirstUnread}}<div class="unread-divider" role="separator" aria-label="New messages"><span>New</span></div>{{end}}
{{if $message.System}}<article class="message system-message{{if $message.Continuation}} is-continuation{{end}}" id="{{$message.Anchor}}" data-message-id="{{$message.ID}}" data-ts="{{$message.Timestamp}}" data-subtype="{{$message.Subtype}}" tabindex="-1" aria-label="{{if $message.SystemSentence}}{{$message.AuthorName}} {{$message.SystemSentence}}{{else}}{{$message.Preview}}{{end}}">
  <div class="message-gutter"><span class="avatar avatar-small" aria-hidden="true">{{$message.AuthorInitial}}</span></div>
  <div class="message-body"><p class="system-text">{{if $message.SystemSentence}}<span class="author">{{$message.AuthorName}}</span> {{$message.SystemSentence}}{{else}}{{$message.DisplayText}}{{end}} <time class="time" datetime="{{$message.MachineTime}}" title="{{$message.FullTime}}" data-format="time">{{$message.ClockTime}}</time></p></div>
</article>
{{else}}
<article class="message{{if $message.Continuation}} is-continuation{{end}}{{if $message.MentionsMe}} mentions-me{{end}}{{if $message.Pinned}} is-pinned{{end}}{{if $message.Editing}} is-editing{{end}}" id="{{$message.Anchor}}" data-message-id="{{$message.ID}}" data-ts="{{$message.Timestamp}}"{{if $message.ThreadRoot}} data-thread-root{{end}} tabindex="-1" aria-label="{{if $message.Ephemeral}}Private message only visible to you{{else}}Message{{end}} from {{$message.AuthorName}} at {{$message.ClockTime}}" aria-keyshortcuts="ArrowUp ArrowDown Home End{{if not $message.InThread}} ArrowRight T{{end}}{{if not $message.Ephemeral}} A M F{{end}}{{if $message.MarkUnreadURL}} U{{end}}{{if $message.CanEdit}} E{{end}}{{if $.CanPin}} P{{end}}{{if $.CanReact}} R{{end}}{{if $message.CanDelete}} Delete{{end}}">
  {{if $message.Pinned}}<p class="message-context pinned-label">{{template "icon-pin"}}<span>Pinned{{if $message.PinnedBy}} by {{$message.PinnedBy}}{{end}}</span></p>{{end}}
  {{if $message.BroadcastThreadURL}}<p class="message-context broadcast-context">replied to a thread: <a href="{{$message.BroadcastThreadURL}}" data-thread-link>{{if $message.BroadcastRootPreview}}{{$message.BroadcastRootPreview}}{{else}}View thread{{end}}</a></p>{{end}}
  <div class="message-gutter">
    <div class="avatar{{if $message.AvatarEmoji}} avatar-emoji{{end}}" aria-hidden="true"{{if $message.ProfileID}} data-profile-user="{{$message.ProfileID}}"{{end}}>{{if $message.AvatarURL}}<img src="{{$message.AvatarURL}}" alt="">{{else if $message.AvatarEmoji}}{{$message.AvatarEmoji}}{{else}}{{$message.AuthorInitial}}{{end}}</div>
    {{if $message.Continuation}}<span class="gutter-time" aria-hidden="true"><time datetime="{{$message.MachineTime}}" title="{{$message.FullTime}}" data-format="clock">{{$message.ClockTime}}</time></span>{{end}}
  </div>
  <div class="message-body">
    <div class="message-head">
      {{if $message.ProfileID}}<a class="author" href="/app/members?user={{$message.ProfileID}}" data-profile-user="{{$message.ProfileID}}">{{$message.AuthorName}}</a>{{else}}<span class="author">{{$message.AuthorName}}</span>{{end}}{{if $message.AuthorStatus}}<span class="author-status"{{if $message.AuthorStatusText}} title="{{$message.AuthorStatusText}}"{{end}}>{{$message.AuthorStatus}}</span>{{end}}{{if $message.IsApp}}<span class="app-label">APP</span>{{end}}
      {{if $message.Permalink}}<a class="time" href="{{$message.Permalink}}"><time datetime="{{$message.MachineTime}}" title="{{$message.FullTime}}" data-format="time">{{$message.ClockTime}}</time></a>{{else}}<time class="time" datetime="{{$message.MachineTime}}" title="{{$message.FullTime}}" data-format="time">{{$message.ClockTime}}</time>{{end}}{{if $message.Streaming}}<span class="streaming-label" role="status">Responding…</span>{{end}}{{if $message.Ephemeral}}<span class="ephemeral-label">Only visible to you</span>{{end}}
    </div>
    {{if and $message.InThread $message.Broadcast}}<p class="broadcast-label">Also sent to #{{$message.ChannelName}}</p>{{end}}
    <div class="message-content"{{if $message.Editing}} hidden{{end}}>
    {{if $message.DisplayText}}<div class="message-text{{if $message.Jumbo}} jumbo{{end}}">{{$message.DisplayText}}{{if $message.Edited}}<span class="edited-label" title="{{$message.EditedTime}}"> (edited)</span>{{end}}</div>{{else if $message.Edited}}<span class="edited-label" title="{{$message.EditedTime}}">(edited)</span>{{end}}
    {{if $message.Files}}<div class="message-files" aria-label="Shared files">{{range $file := $message.Files}}
      <div class="message-file{{if $file.IsImage}} is-image{{end}}{{if $file.IsSnippet}} is-snippet{{end}}">
        {{if $file.IsImage}}<span class="file-image-name">{{$file.Title}}</span><a class="message-image-link" href="{{$file.DownloadURL}}" data-lightbox data-file-title="{{$file.Title}}" data-file-meta="{{$file.Name}} · {{$file.Size}}"><img class="message-image" src="{{$file.ThumbnailURL}}" alt="{{$file.AccessibleName}}" loading="lazy"></a>{{if not $file.AccessibleName}}<span class="message-file-meta undescribed">No description yet</span>{{end}}
        {{else}}<span class="message-file-icon" aria-hidden="true">{{if $file.IsSnippet}}{{if $file.FileType}}{{$file.FileType}}{{else}}snippet{{end}}{{else}}FILE{{end}}</span>
        <span class="message-file-copy"><span class="message-file-title">{{$file.Title}}</span><span class="message-file-meta">{{$file.Name}} · {{$file.MIMEType}} · {{$file.Size}}</span></span>{{end}}
        {{if $file.IsSnippet}}<pre class="message-snippet"{{if $file.FileType}} data-filetype="{{$file.FileType}}"{{end}}><code>{{$file.SnippetContent}}</code></pre>{{if $file.SnippetTruncated}}<span class="message-file-meta">Truncated — download for the full snippet.</span>{{end}}{{end}}
        {{if $file.Deleted}}<span class="message-file-meta">This file was deleted.</span>{{else}}<div class="file-actions" role="group" aria-label="Actions for {{$file.Title}}"><a class="message-action" href="{{$file.DownloadURL}}" download aria-label="Download {{$file.Title}}" title="Download">{{template "icon-download"}}</a>{{if or $file.DeleteURL $file.DescribeURL}}<details class="file-more"><summary class="message-action" aria-label="More actions for {{$file.Title}}" title="More actions">{{template "icon-more"}}</summary><div class="file-menu">
          {{if $file.DescribeURL}}<details class="file-describe"><summary>{{if $file.Description}}Edit description{{else}}Add a description{{end}}</summary>
          <form method="post" action="{{$file.DescribeURL}}" hx-post="{{$file.DescribeURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="describe-{{$file.ID}}-{{$message.ID}}">Describe this image for people who cannot see it</label>
            <textarea id="describe-{{$file.ID}}-{{$message.ID}}" name="description" maxlength="1000" rows="2">{{$file.Description}}</textarea>
            <button type="submit">Save description</button>
          </form></details>{{end}}
          {{if $file.DeleteURL}}<details class="file-delete"><summary>Delete file…</summary>
          <form method="post" action="{{$file.DeleteURL}}" hx-post="{{$file.DeleteURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <p>Deleting {{$file.Title}} removes it from every message and search result in this workspace. This cannot be undone.</p>
            <button class="danger" type="submit">Delete this file</button>
          </form></details>{{end}}
        </div></details>{{end}}</div>{{end}}
      </div>{{end}}</div>{{end}}
    {{if $message.Blocks}}<div class="message-blocks" aria-label="Structured message">{{range $block := $message.Blocks}}
      {{if eq $block.Kind "divider"}}<hr class="message-block divider">
      {{else}}<div class="message-block {{$block.Kind}}">
        {{if $block.HTML}}<div class="formatted-text">{{$block.HTML}}</div>{{else if $block.Text}}<div class="block-text">{{$block.Text}}</div>{{end}}
        {{if $block.Fields}}<ul class="message-block-fields">{{range $index, $field := $block.Fields}}<li>{{with index $block.FieldHTML $index}}<div class="formatted-text">{{.}}</div>{{else}}<div class="block-text">{{$field}}</div>{{end}}</li>{{end}}</ul>{{end}}
        {{if $block.Table}}<div class="block-table-wrap"><table class="block-table">{{if $block.Caption}}<caption>{{$block.Caption}}</caption>{{end}}<tbody>{{range $rowIndex, $row := $block.Table}}<tr>{{range $cell := $row}}{{if and $block.HeaderRow (eq $rowIndex 0)}}<th scope="col">{{$cell}}</th>{{else}}<td>{{$cell}}</td>{{end}}{{end}}</tr>{{end}}</tbody></table></div>{{end}}
        {{if and $message.CanInteract $block.Actions}}<div class="message-block-actions" aria-label="Message actions">{{range $action := $block.Actions}}
          {{if $action.Dispatch}}<form method="post" action="/app/interaction" hx-post="/app/interaction">{{else}}<div>{{end}}
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <input type="hidden" name="message_id" value="{{$message.MessageID}}">
            <input type="hidden" name="app_id" value="{{$message.AppID}}">
            <input type="hidden" name="block_id" value="{{$action.BlockID}}">
            <input type="hidden" name="action_id" value="{{$action.ActionID}}">
            <input type="hidden" name="action_type" value="{{$action.Type}}">
            <input type="hidden" name="channel" value="{{$message.Channel}}">
            {{if eq $action.Control "button"}}<input type="hidden" name="value" value="{{$action.Value}}">{{if $action.Dispatch}}<button class="block-action{{if $action.Style}} style-{{$action.Style}}{{end}}{{if $action.Tone}} feedback-{{$action.Tone}}{{end}}" type="submit"{{if $action.AccessibilityLabel}} aria-label="{{$action.AccessibilityLabel}}"{{end}}>{{$action.Text}}</button>{{end}}
            {{else if eq $action.Control "date"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="date" name="value" value="{{$action.Value}}" required></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
            {{else if eq $action.Control "time"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="time" name="value" value="{{$action.Value}}" required></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
            {{else if eq $action.Control "datetime"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="datetime-local" name="value" value="{{$action.Value}}"{{if $action.DateTimeUnix}} data-unix="{{$action.DateTimeUnix}}"{{end}} data-unix-seconds="true" required><input type="hidden" name="timezone" data-browser-timezone value="UTC"></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
            {{else if eq $action.Control "radio"}}<fieldset class="block-action-options"><legend class="sr-only">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="radio" name="value" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}} required> {{$option.Text}}</label>{{end}}</fieldset>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
            {{else if eq $action.Control "checkbox"}}<fieldset class="block-action-options"><legend class="sr-only">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="checkbox" name="value" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}> {{$option.Text}}</label>{{end}}</fieldset>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
            {{else if eq $action.Control "external"}}<div class="external-select" data-app-options data-app-id="{{$message.AppID}}" data-message-id="{{$message.MessageID}}" data-block-id="{{$action.BlockID}}" data-action-id="{{$action.ActionID}}" data-channel="{{$message.Channel}}" data-min-query="{{$action.MinQueryLength}}"><label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="search" data-options-query placeholder="{{$action.Text}}" minlength="{{$action.MinQueryLength}}"></label><button class="block-action" type="button" data-options-load>Search</button><label><span class="sr-only">Results</span><select class="block-action block-action-select" name="value" data-options-results{{if $action.Multiple}} multiple{{end}}{{if not $action.Options}} disabled{{end}}>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label>{{if $action.Dispatch}}<button class="block-action" type="submit" data-options-choose{{if not $action.Options}} disabled{{end}}>Choose</button>{{end}}<p class="external-select-status" data-options-status role="status"></p><noscript>Dynamic options require JavaScript in this client.</noscript></div>
            {{else if eq $action.Control "file"}}<p class="modal-hint modal-unsupported" role="note">{{$action.Text}} Files are attached in an app's modal form, not here.</p>
            {{else if or (eq $action.Control "text") (eq $action.Control "textarea") (eq $action.Control "richtext") (eq $action.Control "email") (eq $action.Control "url") (eq $action.Control "number")}}{{if or (eq $action.Control "textarea") (eq $action.Control "richtext")}}<textarea class="block-action" name="value" placeholder="{{$action.Text}}">{{$action.Value}}</textarea>{{else}}<input class="block-action" type="{{$action.Control}}" name="value" value="{{$action.Value}}" placeholder="{{$action.Text}}">{{end}}{{if $action.Dispatch}}<button class="block-action" type="submit">Send</button>{{end}}
            {{else}}<label><span class="sr-only">{{$action.Text}}</span><select class="block-action block-action-select" name="value"{{if $action.Multiple}} multiple{{end}} required>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}{{end}}
          {{if $action.Dispatch}}</form>{{else}}</div>{{end}}
        {{end}}</div>{{end}}
        {{if $block.Call}}<div class="call-card">
          {{if $block.Call.Unavailable}}<p class="call-unavailable">This call is no longer available.</p>
          {{else}}<p class="call-title">{{if $block.Call.Title}}{{$block.Call.Title}}{{else}}Call{{end}}</p>
          <p class="call-state">{{if $block.Call.Active}}In progress{{else}}Ended{{end}}{{if $block.Call.Participants}} · {{len $block.Call.Participants}} in the call{{end}}</p>
          {{if $block.Call.Participants}}<ul class="call-participants">{{range $block.Call.Participants}}<li>{{.}}</li>{{end}}</ul>{{end}}
          {{if and $block.Call.Active $block.Call.JoinURL}}<a class="call-join" href="{{$block.Call.JoinURL}}" rel="noreferrer noopener">Join call</a>
          {{else if $block.Call.JoinURL}}<p class="call-state">This call has ended. The link it was created with is no longer offered.</p>{{end}}{{end}}
        </div>{{end}}
        {{if $block.ImageURL}}<img class="message-media" src="{{$block.ImageURL}}" alt="{{$block.ImageAlt}}" loading="lazy">{{end}}
        {{if $block.LinkURL}}<a href="{{$block.LinkURL}}" rel="noreferrer noopener">{{$block.LinkLabel}}</a>{{end}}
      </div>{{end}}
    {{end}}</div>{{end}}
    {{if $message.Attachments}}<div class="message-attachments" aria-label="Attachments">{{range $attachment := $message.Attachments}}{{template "attachment" $attachment}}{{end}}</div>{{end}}
    {{if $message.Unfurls}}<div class="message-unfurls" aria-label="Link previews">{{range $attachment := $message.Unfurls}}{{template "attachment" $attachment}}{{end}}</div>{{end}}
    </div>
    {{if $message.CanEdit}}<form class="message-editor" method="post" action="{{$message.UpdateURL}}" hx-post="{{$message.UpdateURL}}" data-message-editor aria-label="Edit message"{{if not $message.Editing}} hidden{{end}}>
      <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
      <label class="visually-hidden" for="edit-{{$message.ID}}">Edit message</label>
      <textarea id="edit-{{$message.ID}}" name="text" maxlength="40000" rows="2" required aria-describedby="edit-hint-{{$message.ID}}"{{if $message.Editing}} autofocus{{end}}>{{$message.Text}}</textarea>
      <div class="editor-foot"><span class="editor-hint" id="edit-hint-{{$message.ID}}"><kbd>Esc</kbd> to cancel · <kbd>Enter</kbd> to save</span><a class="editor-button" href="{{$message.Permalink}}" data-edit-cancel role="button">Cancel</a><button class="editor-button primary" type="submit">Save</button></div>
    </form>{{end}}
    {{if $.CanReact}}<form id="reaction-form-{{$message.ID}}" class="reaction-picker-form" method="post" action="{{$message.ReactionURL}}" hx-post="{{$message.ReactionURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"></form>{{end}}
    {{if $message.Reactions}}<div class="reactions" role="group" aria-label="Reactions">
      {{range $reaction := $message.Reactions}}{{if $.CanReact}}<form class="reaction-pill" method="post" action="{{if $reaction.Mine}}{{$message.UnreactURL}}{{else}}{{$message.ReactionURL}}{{end}}" hx-post="{{if $reaction.Mine}}{{$message.UnreactURL}}{{else}}{{$message.ReactionURL}}{{end}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="name" value="{{$reaction.Name}}"><button class="chip" type="submit" aria-pressed="{{if $reaction.Mine}}true{{else}}false{{end}}"{{if $reaction.Tooltip}} title="{{$reaction.Tooltip}}"{{end}} aria-label="{{if $reaction.Tooltip}}{{$reaction.Tooltip}}{{else}}{{$reaction.Count}} reacted with :{{$reaction.Name}}:{{end}}. {{if $reaction.Mine}}Remove your reaction{{else}}Add this reaction{{end}}"><span class="reaction-emoji">{{$reaction.Display}}</span><span class="chip-count">{{$reaction.Count}}</span></button></form>
      {{else}}<span class="chip" role="img"{{if $reaction.Tooltip}} title="{{$reaction.Tooltip}}"{{end}} aria-label="{{if $reaction.Tooltip}}{{$reaction.Tooltip}}{{else}}{{$reaction.Count}} reacted with :{{$reaction.Name}}:{{end}}"><span class="reaction-emoji">{{$reaction.Display}}</span><span class="chip-count">{{$reaction.Count}}</span></span>{{end}}{{end}}
      {{if $.CanReact}}<button class="chip add-reaction-chip" type="button" data-open-emoji-picker data-emoji-target="reaction" data-reaction-form="reaction-form-{{$message.ID}}" aria-haspopup="dialog" aria-label="Add reaction" title="Add reaction">{{template "icon-emoji"}}</button>{{end}}
    </div>{{end}}
    {{if $message.ReplyCount}}<a class="thread-summary" href="{{$message.ReplyURL}}" data-thread-link aria-label="{{$message.ReplySummary}}{{if $message.LastReplyRelative}}, last reply {{$message.LastReplyRelative}}{{end}}. View thread">
      <span class="thread-avatars" aria-hidden="true">{{range $message.ThreadRepliers}}<span class="avatar avatar-tiny" title="{{.Name}}">{{.Initial}}</span>{{end}}</span>
      <span class="thread-count">{{$message.ReplyCountLabel}}</span>
      {{if $message.LastReplyRelative}}<span class="thread-last-reply">Last reply <time datetime="{{$message.LastReplyMachine}}" data-format="relative">{{$message.LastReplyRelative}}</time></span>{{end}}
      <span class="thread-view" aria-hidden="true">View thread</span>
    </a>{{end}}
  </div>
  {{if not $message.Ephemeral}}<div class="message-actions" role="toolbar" aria-label="Actions for the message from {{$message.AuthorName}}">
    {{if $.CanReact}}{{range $.QuickReactions}}<button class="message-action quick-reaction" type="submit" form="reaction-form-{{$message.ID}}" name="name" value="{{.Name}}" data-quick-reaction aria-label="React with :{{.Label}}:" title=":{{.Label}}:">{{.Display}}</button>{{end}}
    <button class="message-action" type="button" data-open-emoji-picker data-emoji-target="reaction" data-reaction-form="reaction-form-{{$message.ID}}" aria-haspopup="dialog" aria-label="Add reaction" title="Add reaction">{{template "icon-emoji"}}</button>{{end}}
    {{if not $message.InThread}}<a class="message-action" href="{{$message.ReplyURL}}" data-thread-link aria-label="{{if $.CanReply}}Reply in thread{{else}}View thread{{end}}" title="{{if $.CanReply}}Reply in thread{{else}}View thread{{end}}">{{template "icon-thread"}}</a>{{end}}
    {{if and $message.ForwardURL $.CanReply}}<a class="message-action" href="{{$message.ForwardLinkURL}}" data-forward-message="{{$message.ForwardURL}}" aria-haspopup="dialog" aria-label="Forward message" title="Forward message">{{template "icon-forward"}}</a>{{end}}
    <form method="post" action="{{if $message.Saved}}{{$message.UnsaveURL}}{{else}}{{$message.SaveURL}}{{end}}" hx-post="{{if $message.Saved}}{{$message.UnsaveURL}}{{else}}{{$message.SaveURL}}{{end}}" data-message-save>
      <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
      <button class="message-action" type="submit" aria-pressed="{{if $message.Saved}}true{{else}}false{{end}}" aria-label="{{if $message.Saved}}Remove from Later{{else}}Save for later{{end}}" title="{{if $message.Saved}}Remove from Later{{else}}Save for later{{end}}">{{if $message.Saved}}{{template "icon-bookmark-filled"}}{{else}}{{template "icon-bookmark"}}{{end}}</button>
    </form>
    <details class="message-more" data-message-menu><summary class="message-action" aria-label="More actions" title="More actions" aria-haspopup="menu">{{template "icon-more"}}</summary>
    <div class="message-menu" role="menu" aria-label="More actions">
      {{if $message.FollowURL}}<form method="post" action="{{$message.FollowURL}}" hx-post="{{$message.FollowURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="followed" value="{{if $message.Following}}false{{else}}true{{end}}"><button type="submit" role="menuitem"><span>{{if $message.Following}}Turn off notifications for replies{{else}}Get notified about new replies{{end}}</span></button></form>
      <div class="menu-separator" role="separator"></div>{{end}}
      {{if $message.MarkUnreadURL}}<form method="post" action="{{$message.MarkUnreadURL}}" hx-post="{{$message.MarkUnreadURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit" role="menuitem" data-menu-key="u"><span>Mark unread</span><kbd>U</kbd></button></form>{{end}}
      <details class="menu-submenu" data-reminder-menu><summary role="menuitem" aria-haspopup="menu"><span>Remind me about this</span><span class="submenu-arrow" aria-hidden="true">›</span></summary>
        <form class="message-submenu reminder-form" role="menu" aria-label="Remind me about this" method="post" action="{{$message.RemindURL}}" hx-post="{{$message.RemindURL}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <input type="hidden" name="timezone" data-browser-timezone value="UTC">
          <button type="submit" name="preset" value="20m" role="menuitem">In 20 minutes</button>
          <button type="submit" name="preset" value="1h" role="menuitem">In 1 hour</button>
          <button type="submit" name="preset" value="3h" role="menuitem">In 3 hours</button>
          <button type="submit" name="preset" value="tomorrow" role="menuitem">Tomorrow</button>
          <button type="submit" name="preset" value="nextweek" role="menuitem">Next week</button>
          <div class="menu-separator" role="separator"></div>
          <details class="reminder-custom"><summary role="menuitem">Custom…</summary><div class="reminder-custom-fields"><label>Date<input type="date" name="date"></label><label>Time<input type="time" name="time" value="09:00"></label><button type="submit" name="preset" value="custom">Set reminder</button></div></details>
        </form>
      </details>
      <div class="menu-separator" role="separator"></div>
      {{if $message.CopyLinkURL}}<a class="copy-link" href="{{$message.CopyLinkURL}}" data-copy-link="{{$message.CopyLinkURL}}" role="menuitem"><span>Copy link</span></a>{{end}}
      {{if $.CanPin}}<form method="post" action="{{if $message.Pinned}}{{$message.UnpinURL}}{{else}}{{$message.PinURL}}{{end}}" hx-post="{{if $message.Pinned}}{{$message.UnpinURL}}{{else}}{{$message.PinURL}}{{end}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit" role="menuitem" data-menu-key="p"><span>{{if $message.Pinned}}Un-pin from channel{{else}}Pin to channel{{end}}</span><kbd>P</kbd></button></form>{{end}}
      {{if or $message.CanEdit $message.CanDelete}}<div class="menu-separator" role="separator"></div>{{end}}
      {{if $message.CanEdit}}<a href="{{$message.EditLinkURL}}" data-edit-message role="menuitem" data-menu-key="e"><span>Edit message</span><kbd>E</kbd></a>{{end}}
      {{if $message.CanDelete}}<a class="danger" href="{{$message.DeleteLinkURL}}" data-delete-message="{{$message.DeleteURL}}" data-delete-files="{{if $message.Files}}true{{end}}" role="menuitem" aria-haspopup="dialog" data-menu-key="delete"><span>Delete message…</span><kbd>delete</kbd></a>{{end}}
      {{if $message.Shortcuts}}<div class="menu-separator" role="separator"></div>
      {{range $shortcut := $message.Shortcuts}}<form method="post" action="/app/shortcut" hx-post="/app/shortcut"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="channel" value="{{$message.Channel}}"><input type="hidden" name="message_id" value="{{$message.MessageID}}"><input type="hidden" name="app_id" value="{{$shortcut.AppID}}"><input type="hidden" name="callback_id" value="{{$shortcut.CallbackID}}"><button type="submit" role="menuitem"{{if $shortcut.Description}} title="{{$shortcut.Description}}"{{end}}><span>{{$shortcut.Name}}<small>{{$shortcut.AppName}}</small></span></button></form>{{end}}{{end}}
    </div>
    </details>
  </div>{{end}}
</article>
{{if and $message.ThreadRoot $.ThreadReplyLabel}}<div class="thread-replies-divider" role="separator" aria-label="{{$.ThreadReplyLabel}}"><span>{{$.ThreadReplyLabel}}</span></div>{{end}}
{{end}}
{{else}}
<p class="empty">{{if .IsMember}}No messages yet. Start the conversation.{{else}}No messages have been posted in this channel yet.{{end}}</p>
{{end}}
{{end}}`

// messageDialogsPartial renders the page-level surfaces the message actions
// open: the shared emoji picker, the Delete message and Forward message
// dialogs, the image viewer and the toast. They are rendered once, not per
// message; the page script points them at the message that opened them. A
// no-script link renders the requested dialog open for its message instead.
const messageDialogsPartial = `{{define "message-dialogs"}}
{{if or .CanPost .Timeline.CanReact}}<div class="emoji-picker" id="emoji-picker" role="dialog" aria-modal="false" aria-labelledby="emoji-picker-title" hidden>
  <h2 class="visually-hidden" id="emoji-picker-title">Emoji picker</h2>
  <div class="emoji-picker-head">
    <label class="emoji-search"><span class="visually-hidden">Search all emoji</span><input id="emoji-picker-query" type="search" autocomplete="off" maxlength="100" placeholder="Search all emoji" role="combobox" aria-expanded="true" aria-controls="emoji-picker-grid" aria-autocomplete="list"></label>
    <button class="emoji-tone-button" id="emoji-tone-button" type="button" aria-haspopup="true" aria-expanded="false" aria-controls="emoji-tone-options" aria-label="Skin tone" title="Skin tone"><span aria-hidden="true">✋</span></button>
  </div>
  <div class="emoji-tone-options" id="emoji-tone-options" role="radiogroup" aria-label="Skin tone" hidden>{{range emojiSkinTones}}<button type="button" role="radio" data-tone="{{.Value}}" aria-checked="false" aria-label="{{.Label}}" title="{{.Label}}">{{.Glyph}}</button>{{end}}</div>
  <div class="emoji-categories" role="tablist" aria-label="Emoji categories">{{range emojiCategoryTabs}}<button type="button" role="tab" data-emoji-category="{{.Name}}" aria-selected="false" aria-controls="emoji-picker-grid" title="{{.Label}}" aria-label="{{.Label}}"><span aria-hidden="true">{{.Glyph}}</span></button>{{end}}</div>
  <p class="emoji-section-title" id="emoji-section-title">Frequently used</p>
  <div class="emoji-grid" id="emoji-picker-grid" role="listbox" aria-labelledby="emoji-section-title" tabindex="-1"></div>
  <p class="emoji-picker-status" id="emoji-picker-status" role="status" aria-live="polite"></p>
  <div class="emoji-picker-foot"><span class="emoji-preview" id="emoji-preview" aria-hidden="true"><span class="emoji-preview-glyph"></span><span class="emoji-preview-name"></span></span>{{if .CanAddEmoji}}<a class="emoji-add" href="/app/customize/emoji?channel={{.Channel}}">Add emoji</a>{{end}}</div>
</div>{{end}}
<dialog class="message-dialog" id="delete-message-dialog" aria-labelledby="delete-message-title" aria-describedby="delete-message-body"{{if and .MessageDialog (eq .MessageDialog.Kind "delete")}} open{{end}}>
  <form method="post" action="{{if and .MessageDialog (eq .MessageDialog.Kind "delete")}}{{.MessageDialog.Message.DeleteURL}}{{end}}" hx-post="{{if and .MessageDialog (eq .MessageDialog.Kind "delete")}}{{.MessageDialog.Message.DeleteURL}}{{end}}" data-delete-form>
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <header class="dialog-head"><h2 id="delete-message-title">Delete message</h2><a class="dialog-close" href="{{if .MessageDialog}}{{.MessageDialog.CloseURL}}{{else}}#{{end}}" data-dialog-cancel role="button" aria-label="Close">×</a></header>
    <p id="delete-message-body">Are you sure you want to delete this message? This cannot be undone.</p>
    <div class="dialog-preview" data-dialog-preview>{{if and .MessageDialog (eq .MessageDialog.Kind "delete")}}<p class="dialog-preview-author">{{.MessageDialog.Message.AuthorName}} <span class="time">{{.MessageDialog.Message.ClockTime}}</span></p><div class="message-text">{{.MessageDialog.Message.DisplayText}}</div>{{end}}</div>
    <p class="dialog-note" data-delete-files-note{{if not (and .MessageDialog .MessageDialog.Message.Files)}} hidden{{end}}>This message shares a file. Deleting it also removes the file from this conversation, unless another message here shares it too. The file itself is kept.</p>
    <footer class="dialog-actions"><a class="dialog-button" href="{{if .MessageDialog}}{{.MessageDialog.CloseURL}}{{else}}#{{end}}" data-dialog-cancel role="button">Cancel</a><button class="dialog-button danger" type="submit">Delete</button></footer>
  </form>
</dialog>
{{if and .CanPost .ForwardDestinations}}<dialog class="message-dialog forward-dialog" id="forward-message-dialog" aria-labelledby="forward-message-title"{{if and .MessageDialog (eq .MessageDialog.Kind "forward")}} open{{end}}>
  <form method="post" action="{{if and .MessageDialog (eq .MessageDialog.Kind "forward")}}{{.MessageDialog.Message.ForwardURL}}{{end}}" hx-post="{{if and .MessageDialog (eq .MessageDialog.Kind "forward")}}{{.MessageDialog.Message.ForwardURL}}{{end}}" data-forward-form>
    <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
    <header class="dialog-head"><h2 id="forward-message-title">Forward message</h2><a class="dialog-close" href="{{if .MessageDialog}}{{.MessageDialog.CloseURL}}{{else}}#{{end}}" data-dialog-cancel role="button" aria-label="Close">×</a></header>
    <label class="dialog-label" for="forward-destination-query">Add to</label>
    <input class="dialog-field" id="forward-destination-query" type="search" autocomplete="off" placeholder="Search for channel or person" data-forward-filter aria-controls="forward-destination" aria-describedby="forward-destination-help">
    <p class="visually-hidden" id="forward-destination-help">Type to narrow the list, then choose where to forward the message.</p>
    <select class="dialog-field forward-destinations" id="forward-destination" name="destination" size="6" required aria-label="Destination">
      <optgroup label="Channels">{{range .ForwardDestinations}}<option value="{{.ID}}">#{{.Name}}</option>{{end}}</optgroup>
      {{if .Directs}}<optgroup label="Direct messages">{{range .Directs}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</optgroup>{{end}}
      {{with .ComposerDialogs.Directory.People}}<optgroup label="People">{{range .}}{{if not .Self}}<option value="user:{{.ID}}">{{.Name}}</option>{{end}}{{end}}</optgroup>{{end}}
    </select>
    <label class="dialog-label" for="forward-comment">Add a message <span class="optional">(optional)</span></label>
    <textarea class="dialog-field" id="forward-comment" name="comment" maxlength="2000" rows="2"></textarea>
    <div class="dialog-preview forward-preview" data-dialog-preview>{{if and .MessageDialog (eq .MessageDialog.Kind "forward")}}<p class="dialog-preview-author">{{.MessageDialog.Message.AuthorName}} <span class="time">{{.MessageDialog.Message.ClockTime}}</span></p><div class="message-text">{{.MessageDialog.Message.DisplayText}}</div>{{end}}</div>
    <footer class="dialog-actions"><button class="dialog-button copy-link-button" type="button" data-forward-copy-link{{if and .MessageDialog (eq .MessageDialog.Kind "forward")}} data-copy-link="{{.MessageDialog.Message.CopyLinkURL}}"{{end}}>Copy link</button><span class="dialog-spacer"></span><button class="dialog-button primary" type="submit">Forward</button></footer>
  </form>
</dialog>{{end}}
<dialog class="lightbox" id="image-lightbox" aria-labelledby="lightbox-title">
  <header class="lightbox-head"><div class="lightbox-copy"><h2 id="lightbox-title" data-lightbox-title></h2><p data-lightbox-meta></p></div><div class="lightbox-actions"><a class="lightbox-button" href="#" download data-lightbox-download aria-label="Download" title="Download">{{template "icon-download"}}</a><a class="lightbox-button" href="#" target="_blank" rel="noopener" data-lightbox-open aria-label="Open in a new tab" title="Open in a new tab"><span aria-hidden="true">↗</span></a><button class="lightbox-button" type="button" data-lightbox-close aria-label="Close" title="Close">×</button></div></header>
  <div class="lightbox-stage"><img alt="" data-lightbox-image></div>
</dialog>
<div class="toast" id="message-toast" role="status" aria-live="polite" hidden></div>
{{end}}`
