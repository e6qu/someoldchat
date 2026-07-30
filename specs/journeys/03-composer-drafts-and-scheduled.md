# Composer, drafts, and scheduled messages

## COMP-01 — Compose and send a message

**Preconditions:** The member can view and post to the selected conversation.

**Target behavior:**

1. The composer names its destination and thread context. Disabled posting
   states explain why the member cannot send.
2. The member can enter multi-line text; apply Slack formatting; insert
   standard/custom emoji; mention eligible people, user groups, and channels;
   attach files; record supported audio/video clips; and open Slack's shortcuts
   browser without losing text.
3. `Enter` sends and `Shift+Enter` inserts a line break under Slack's default
   preference. Any configurable send preference MUST change both behavior and
   help text.
4. A send with content valid under Slack's rules commits exactly once, appears
   in the timeline, clears only the committed draft/attachments, emits the
   corresponding event, and is returned by Web API history.
5. Empty, over-limit, disconnected, permission-changed, archived, rate-limited,
   duplicate, and server-rejected sends retain recoverable input and present a
   specific non-500 result.

Audio and video clip controls request only the required browser devices, show
recording and permission-failure state, stop at Slack's five-minute limit, and
stage the resulting media without sending it. The member may add text, remove
the clip, or send it through the same exactly-once file-share transaction as
other staged attachments. Cancelling or closing the recorder stops every media
track and adds no file. Slack's video source selection, camera/microphone
controls, thumbnail selection, and optional transcript are part of this journey
and remain explicit differential requirements until implemented and captured.

## COMP-02 — Format with keyboard and controls

Formatting controls and keyboard shortcuts MUST produce Slack-compatible
message markup and selection behavior. At minimum the current Slack mappings
for bold (`Command/Control+B`), italic (`Command/Control+I`), strikethrough
(`Command/Control+Shift+X`), link, ordered/bulleted list, block quote, and code
must work where Slack supports them. The visible pressed/active state and
accessible description follow the selection.

Formatting MUST not inject unsafe HTML, corrupt Slack entity syntax, or move
focus unexpectedly. Pasted rich text and plain text follow the member's Slack
composer mode and preserve an undo path.

## COMP-03 — Resolve mentions and emoji

Typing Slack's trigger characters opens a contextual, keyboard-operable
suggestion list anchored to the caret:

- `@` resolves visible members and user groups with identity-disambiguating
  details;
- `#` resolves channels the member may reference without leaking private
  channels;
- `:` resolves standard and workspace custom emoji;
- `/` at the start of an otherwise empty composer opens the shortcuts/command
  browser described by `APP-05`.

Arrow keys move the active option, `Enter`/`Tab` accepts according to Slack,
`Escape` closes, and continued typing/filtering is announced without stealing
the composer. Literal trigger characters remain possible.

**Observable states and boundaries:**

1. Before a trigger, no listbox is exposed and the composer reports
   `aria-expanded=false`.
2. A recognized trigger filters only objects the member may reference. The
   first match is active; an empty result closes the list without modifying
   text. A private channel lookup MUST resolve to an unavailable/private label
   rather than reveal its name.
3. Accepting a person stores `<@USER>`, accepting a channel stores
   `<#CHANNEL>`, and accepting emoji stores `:name:`. The first-party renderer
   resolves these to the current visible label or image while history and API
   responses retain Slack's transport representation.
4. The emoji toolbar opens a modal picker with search over Slack's standard
   catalog plus durable workspace custom emoji and aliases. Choosing from the
   picker inserts at the current selection and restores focus to the composer.
   Custom image URLs are restricted to HTTP(S) and rendered with the emoji name
   as alternative text.
5. Standard codes, aliases, custom emoji, and custom aliases share one lookup
   model across composer suggestions, message rendering, reactions, and
   `emoji.list`; unknown codes remain literal message text and cannot be added
   as a reaction.

Recent emoji ordering, skin-tone preference, and Slack's exact ranking algorithm
remain differential requirements until captured against a dedicated Slack
workspace. They MUST NOT be inferred from the alphabetical or upstream dataset
order.

## DRAFT-01 — Preserve a conversation or thread draft

Leaving a non-empty composer preserves text, formatting, and staged attachments
as a draft associated with the exact conversation/thread. The sidebar and
Drafts & sent surface expose the draft indicator. Returning restores the draft
without mixing main-conversation and thread content. Sending or explicitly
deleting the draft removes it across clients according to Slack's sync
behavior.

Refresh, session expiry, reconnect, navigation race, and a failed send MUST NOT
silently discard the draft. Sensitive drafts MUST remain scoped to the signed-in
member and workspace.

## DRAFT-02 — Manage Drafts & sent

**Preconditions:** The member is signed in. Drafts and scheduled work are
private to that member and workspace; Sent contains messages authored by that
member.

**Target sequence and states:**

1. `Drafts & sent` at the top of the workspace navigation opens one surface
   with Slack's current Drafts, Scheduled, and Sent tabs. The selected tab is
   named and exposed as current to assistive technology, survives pagination,
   and has a distinct meaningful empty state.
2. Drafts show their exact channel/DM/thread destination, text preview, and last
   update. Continue returns to that destination with the draft restored; delete
   removes only the selected draft. A destination the member can no longer
   access is not reopened or leaked.
3. Scheduled items show the destination, exact local delivery time, text and
   delivery/failure state. Edit preserves the original until valid replacement
   text and time commit. Reschedule, send now, cancel and delete address the
   exact item and reconcile a concurrent worker outcome instead of reporting a
   second success.
4. Sent items are newest first and open the exact authorized conversation,
   thread and message. Deletion or later permission loss produces an
   unavailable/redacted result rather than exposing retained content.
5. Every action returns focus to the affected list/tab or navigated composer,
   announces its committed outcome, and is usable at narrow width and with a
   keyboard. Refresh and another signed-in client see the same committed
   drafts, pending work, failures and sent messages.

The public Slack Web API does not provide a scheduled-message update or
send-now method. App-authored updates are the documented
`chat.deleteScheduledMessage` plus `chat.scheduleMessage` sequence. A
first-party atomic edit/send-now operation MUST remain an internal typed
service and MUST NOT be advertised as a Slack Web API method.

## SCHED-01 — Schedule a message

The send-arrow menu exposes Slack's suggested times and a custom date/time
picker in the member's time zone. The confirmation identifies the destination
and exact local time. Slack's supported time window, per-channel quota,
content/attachment restrictions, thread rules, permissions, and invalid-time
errors are enforced by the backend and match `chat.scheduleMessage`.

Scheduling creates a durable pending message, clears only the scheduled draft,
shows it in Drafts & sent, and does not insert an ordinary history message or
`message` event before delivery.

## SCHED-02 — Deliver, reschedule, send now, or cancel

At the due instant, one worker delivers the message once with the scheduling
member/app attribution and thread context. Delivery survives process restart,
workspace hibernation, clock boundaries, and multi-worker races. Terminal
permission/content failures remain visible with a recoverable explanation;
they do not become successful history.

Before delivery, authorized members can reschedule or cancel the exact pending
item, and Slack-supported clients can send it now. A cancellation racing
delivery has one observable outcome. Pagination and ownership are exact-token
and workspace isolated.

Editing a failed item clears its terminal failure only when the replacement
commits. Send now uses the scheduled item ID as the post idempotency key: a
process failure after posting and before acknowledgement cannot create a
duplicate on retry. A failed edit retains the previous item; a failed send-now
retains a recoverable scheduled/failed record. Handled permission, archive,
invalid-thread, quota, past-time, too-far, lease-conflict and already-delivered
outcomes are not HTTP 500 responses.

## Evidence

- Browser: rich/plain composition, all suggestion types, keyboard formatting,
  pasted/dropped/selected file staging, permission-denied/cancelled/completed
  audio and video clip recording, draft switching/reload, all Drafts & sent
  tabs, schedule picker, edit/cancel/send-now, and delivery/failure updates.
- Backend: current official SDK qualification for `chat.postMessage`,
  `chat.scheduleMessage`, `chat.scheduledMessages.list`, and
  `chat.deleteScheduledMessage`; Node, Python, and Java also request
  `emoji.list(include_categories=true)` through their typed current SDK
  surfaces. Scheduler tests use deterministic clocks and real persistence in
  every worker composition.
- Catalog: standard emoji come from the exact iamcal/emoji-data revision named
  by Slack's formatting guide, with source and license checksums enforced by
  the repository updater. Workspace custom emoji remain durable store data and
  are tested through browser, memory, SQL, and Slack API paths.
- User groups: the composer combines enabled workspace user groups with visible
  people under the `@` trigger, filters by handle/name/description, exposes
  identity-disambiguating type and member-count detail, and accepts the same
  keyboard controls as person mentions. Selection stores Slack's
  `<!subteam^ID>` transport form; timelines and Activity resolve the durable ID
  back to the current handle without rewriting message history. Shared
  memory/SQLite/dqlite/PostgreSQL qualification proves enabled-group expansion,
  disabled-group suppression, public-channel non-member delivery, and private
  conversation fencing. Current official Node, Python, and Java SDK
  qualification continues to create/list/update group membership through
  Slack's published user-group methods.
- Boundary: the first-party Drafts & sent RPCs are covered by
  local-versus-gRPC differential and converter-property tests, while the
  official Node, Python, and Java SDKs continue to exercise only Slack's
  published schedule/list/delete Web API surface.
- Differential: capture exact suggestion, draft, schedule-window, quota,
  thread, and failure behavior in a dedicated Slack workspace.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| COMP-01 | [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages) | Slack's composer sends text, formatting, files, emoji, mentions, and clips; recordings are at most five minutes and may carry an optional message. |
| COMP-02 | [Format your messages](https://slack.com/help/articles/202288908-Format-your-messages) | Slack publishes formatting controls, markup, and keyboard behavior. |
| COMP-03 | [Create and edit user groups](https://slack.com/help/articles/212906697-Create-and-edit-user-groups) | A user group's unique handle notifies its members; the emoji and developer transport sources checked below establish the other completion representations. |
| DRAFT-01 | [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages) | Slack preserves and exposes drafts associated with their destination. |
| DRAFT-02 | [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages) | Drafts and sent contains Drafts, Scheduled, and Sent tabs with item actions. |
| SCHED-01 | [Send or schedule messages](https://slack.com/help/articles/1500012915082-Send-or-schedule-messages) | Slack schedules from the send control using suggested or custom local times. |
| SCHED-02 | [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages) | First-party scheduled items can be edited, rescheduled, sent, cancelled, or deleted; Slack's developer scheduling guide separately establishes the app-facing delete-plus-schedule update boundary below. |

Sources checked 2026-07-30:

- [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages)
- [Record audio and video clips](https://slack.com/help/articles/4406235165587-Record-audio-and-video-clips-in-Slack)
- [Format your messages](https://slack.com/help/articles/202288908-Format-your-messages)
- [Use emoji and reactions](https://slack.com/help/articles/202931348-Use-emoji-and-reactions)
- [Use mentions in Slack](https://slack.com/help/articles/205240127-Use-mentions-in-Slack)
- [Create and edit user groups](https://slack.com/help/articles/212906697-Create-and-edit-user-groups)
- [Usergroup object](https://docs.slack.dev/reference/objects/usergroup-object)
- [Formatting message text](https://docs.slack.dev/messaging/formatting-message-text/)
- [Send or schedule messages](https://slack.com/help/articles/1500012915082-Send-or-schedule-messages)
- [Send and read messages: manage draft, scheduled, and sent messages](https://slack.com/help/articles/201457107-Send-and-read-messages)
- [Slack developer guide: sending and scheduling messages](https://docs.slack.dev/messaging/sending-and-scheduling-messages/)
