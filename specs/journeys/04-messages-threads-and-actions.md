# Messages, threads, and actions

## MSG-01 — Read a conversation

The timeline presents messages in durable order with author/bot/app identity,
timestamp, edited state, thread summary, reactions, attachments/blocks/files,
delivery or failure state, and Slack-compatible formatting. Date separators,
unread marker, new-message affordance, loading windows, and pagination preserve
the reader's position. New SSE events do not yank a reader away from older
history.

Deleted, redacted, retained, imported, system, bot, app, ephemeral, scheduled,
thread-broadcast, and file-share messages MUST be distinguishable where Slack
distinguishes them. Message markup and app-provided content are safe.

## MSG-02 — Navigate and mark unread/read

Opening a conversation positions the first unread message according to Slack's
read cursor. The member can jump to newest, jump to first unread, mark the
conversation read, or mark unread from a selected message. These actions update
only the member's cursor and synchronize with Activity/sidebar counts. Loading
a permalink or preview MUST not accidentally advance a cursor beyond Slack's
behavior.

## MSG-03 — Edit a message

An eligible author opens Edit from the message menu or Slack's `E` shortcut,
changes the content in place, and saves or cancels. Validation preserves the
edit. A successful edit keeps message identity/thread/reactions, shows Slack's
edited marker, updates API history, and emits the correct change event.
Concurrent edit/delete, retention locks, time/role policy, app-authored
messages, and lost access have explicit outcomes.

## MSG-04 — Delete a message

An eligible author opens Delete from the menu or Slack's documented shortcut
and receives Slack's applicable confirmation. Success removes or tombstones the
message consistently across timeline, thread, search, Activity, Later, pins,
files, API history, and events. It does not delete an entire thread or shared
file unless Slack does. Already-deleted, retained, legal-hold, unauthorized,
and concurrent cases are handled.

## THREAD-01 — Open and read a thread

Reply in thread opens Slack's secondary thread view, identifies the root and
conversation, loads replies in order, and moves focus to a useful thread
heading or composer. The thread can be deep-linked and closed without losing
the parent reading position. Deleted/inaccessible roots and paginated replies
remain intelligible.

## THREAD-02 — Reply and broadcast

The thread composer follows `COMP-01`. Sending commits one reply with the root
timestamp, increments thread summary, emits the correct event, and notifies
eligible participants. “Also send to channel” creates Slack's broadcast
projection without duplicating the underlying reply. App slash commands are
not accepted in threads; Slack built-in commands that Slack permits remain
available.

## ACT-01 — Use message actions

Hover/focus reveals a keyboard-accessible action toolbar and overflow menu
without shifting content. Slack's current actions include:

- add reaction;
- reply in thread;
- forward/share;
- save for later;
- mark unread from here;
- set a reminder;
- copy link and copy text where available;
- pin/unpin where authorized;
- edit/delete where authorized; and
- app-provided message shortcuts.

The corresponding documented one-key shortcuts (`E`, `Delete`/`Backspace`,
`T`, `F`, `P`, `A`, `U`, `M`, `R`) apply only when a message has keyboard
focus. They MUST not fire while editing text.

## ACT-02 — React to a message

The emoji reaction picker exposes standard and custom emoji, recent choices,
search, skin tone where applicable, and keyboard operation. Toggling a reaction
updates the exact member set/count once and synchronizes with API/events.
Removing the last reaction removes its chip. Deleted custom emoji,
authorization changes, and concurrent toggles reconcile correctly.

## ACT-03 — Pin, forward, copy, and share

Pinning follows conversation permissions and produces Slack's channel-visible
effect and system/event projection. Forward/share identifies destination and
optional message, prevents unauthorized destination disclosure, and preserves
the original attribution/link. Copy-link yields a durable authorized permalink;
copy-text contains the message content Slack exposes rather than hidden HTML.

## Evidence

- Browser tests cover every action from mouse, keyboard, and touch-sized narrow
  layout, including focus return after menus/dialogs.
- API/event/SDK tests prove edit/delete/reply/reaction/pin/share projections and
  permission failures.
- Differential fixtures record Slack's shortcut context, confirmation,
  tombstone, broadcast, and cursor behavior.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| MSG-01 | [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages) | Slack renders conversation history, unread state, and rich message content. |
| MSG-02 | [Slack keyboard shortcuts](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands) | Escape and modifier-click provide Slack's read and unread actions. |
| MSG-03 | [Edit or delete messages](https://slack.com/help/articles/202395258-Edit-or-delete-messages) | Eligible authors can edit messages and Slack marks the result edited. |
| MSG-04 | [Edit or delete messages](https://slack.com/help/articles/202395258-Edit-or-delete-messages) | Eligible authors can delete messages under workspace policy. |
| THREAD-01 | [Use threads](https://slack.com/help/articles/115000769927-Use-threads-to-organize-discussions) | Thread replies open with their parent conversation context. |
| THREAD-02 | [Use threads](https://slack.com/help/articles/115000769927-Use-threads-to-organize-discussions) | A thread reply may also be sent to the channel. |
| ACT-01 | [Understand your actions](https://slack.com/help/articles/360002063088-Understand-your-actions-in-Slack) | Focused messages expose Slack's action menu and one-key actions. |
| ACT-02 | [Use emoji and reactions](https://slack.com/help/articles/202931348-Use-emoji-and-reactions) | Members add and remove emoji reactions from messages. |
| ACT-03 | [Understand your actions](https://slack.com/help/articles/360002063088-Understand-your-actions-in-Slack) | Slack message actions include pinning, forwarding, sharing, and copying. |

Sources checked 2026-07-29:

- [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages)
- [Edit or delete messages](https://slack.com/help/articles/202395258-Edit-or-delete-messages)
- [Use threads to organize discussions](https://slack.com/help/articles/115000769927-Use-threads-to-organize-discussions)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Understand your actions in Slack](https://slack.com/help/articles/360002063088-Understand-your-actions-in-Slack)
