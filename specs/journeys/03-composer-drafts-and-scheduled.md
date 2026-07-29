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

The Drafts & sent surface has Slack's current Drafts, Scheduled, and Sent tabs.
It exposes meaningful empty states and lets the member open, continue, send, or
delete a draft; open, edit/reschedule, send now, or cancel a scheduled message;
and inspect recently sent items. Actions update the list and destination
conversation atomically.

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

## Evidence

- Browser: rich/plain composition, all suggestion types, keyboard formatting,
  file staging, draft switching/reload, all Drafts & sent tabs, schedule
  picker, edit/cancel/send-now, and delivery/failure updates.
- Backend: current official SDK qualification for `chat.postMessage`,
  `chat.scheduleMessage`, `chat.scheduledMessages.list`, and
  `chat.deleteScheduledMessage`; scheduler tests use deterministic clocks and
  real persistence in every worker composition.
- Differential: capture exact suggestion, draft, schedule-window, quota,
  thread, and failure behavior in a dedicated Slack workspace.

Sources checked 2026-07-29:

- [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages)
- [Format your messages](https://slack.com/help/articles/202288908-Format-your-messages)
- [Use emoji and reactions](https://slack.com/help/articles/202931348-Use-emoji-and-reactions)
- [Send or schedule messages](https://slack.com/help/articles/1500012915082-Send-or-schedule-messages)
- [Send and read messages: manage draft, scheduled, and sent messages](https://slack.com/help/articles/201457107-Send-and-read-messages)
