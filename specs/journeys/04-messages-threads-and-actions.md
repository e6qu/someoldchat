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

Slack's timeline presentation that the target includes:

- consecutive messages from one author within a few minutes form one group:
  the name and avatar are shown once, and each later message shows its time
  in the avatar gutter on hover. A day divider, the unread line, a workspace
  notice, a thread broadcast, or a different app name or icon starts a group;
- the header time is the clock time ("10:19 AM") in the member's time zone and
  locale, with the full date and time ("Saturday, September 27th at 10:19:05
  AM") as its tooltip; day dividers read "Today", "Yesterday" or "Friday,
  September 26th" for the member's calendar day and stay pinned at the top of
  the list while their day scrolls;
- mrkdwn renders one line break per newline, `>` and `>>>` quotes with a left
  bar, fenced code blocks as their own block, inline code, and the bullet and
  numbered lines Slack writes for lists; a message that is only a few emoji is
  shown enlarged;
- mentions of the member (and `@here`, `@channel`, `@everyone`) are
  highlighted, as is the message row; `@person` and `#channel` references
  open the person or channel;
- a workspace notice is one compact line beside a small avatar ("Ada joined
  #general."); a pinned message carries "Pinned by …" and a tint; a thread
  reply also sent to the channel reads "replied to a thread: …" with the root
  linking to the thread;
- a message with replies shows the repliers' avatars, "N replies" and "Last
  reply … ago", becoming "View thread" on hover;
- an image opens in a viewer (Escape closes it) with download; a file card
  offers its actions on hover.

## MSG-02 — Navigate and mark unread/read

Opening a conversation positions the first unread message according to Slack's
read cursor. The member can jump to newest, jump to first unread, mark the
conversation read, or mark unread from a selected message. These actions update
only the member's cursor and synchronize with Activity/sidebar counts. Loading
a permalink or preview MUST not accidentally advance a cursor beyond Slack's
behavior.

## MSG-03 — Edit a message

An eligible author opens Edit from the message menu or Slack's `E` shortcut,
changes the content in place, and saves or cancels. The editor replaces the
message body with Cancel and Save; Enter saves, Shift+Enter adds a line,
Escape cancels, and focus returns to the message. Saving an emptied message
offers deletion instead. Validation preserves the
edit. A successful edit keeps message identity/thread/reactions, shows Slack's
edited marker, updates API history, and emits the correct change event.
Concurrent edit/delete, retention locks, time/role policy, app-authored
messages, and lost access have explicit outcomes.

## MSG-04 — Delete a message

An eligible author opens Delete from the menu or Slack's documented shortcut
and receives Slack's applicable confirmation: a "Delete message" dialog
reading "Are you sure you want to delete this message? This cannot be
undone.", quoting the message, with Cancel and Delete. Success removes or tombstones the
message consistently across timeline, thread, search, Activity, Later, pins,
files, API history, and events. It does not delete an entire thread or shared
file unless Slack does. Already-deleted, retained, legal-hold, unauthorized,
and concurrent cases are handled.

## THREAD-01 — Open and read a thread

Reply in thread opens Slack's secondary thread view, identifies the root and
conversation, loads replies in order, and moves focus to a useful thread
heading or composer. The pane is headed "Thread" with the conversation name
and a close control (Escape also closes it), divides the root from its
replies with "N replies", and keeps "Get notified about new replies" /
"Turn off notifications for replies" in the root's menu. The Threads view
lists the member's threads — messages with replies — with the conversation,
the root, its latest replies and a place to reply. The thread can be deep-linked and closed without losing
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

The hover toolbar leads with three one-click reactions: the emoji the member
most recently reacted with, from their account so they follow the member to
any client, then the emoji this browser used, then Slack's defaults (white
check mark, eyes, raised hands), none twice. Then Add reaction, Reply in thread, Forward message, Save for later and
More actions. More actions lists, with separators and key hints: the thread
notification toggle; Mark unread (`U`); Remind me about this, a submenu of In
20 minutes, In 1 hour, In 3 hours, Tomorrow, Next week and Custom…; Copy link;
Pin to channel / Un-pin from channel (`P`); Edit message (`E`); Delete
message… (`delete`); then app message shortcuts. Right-clicking a message
opens the same menu at the pointer. Menus close on Escape or an outside click,
move with the arrow keys, and stay on screen at phone width. A reminder,
forward, mark-unread or follow change completes where the member is.

## ACT-02 — React to a message

The emoji reaction picker exposes standard and custom emoji, recent choices,
search, skin tone where applicable, and keyboard operation. Toggling a reaction
updates the exact member set/count once and synchronizes with API/events.
Removing the last reaction removes its chip. Deleted custom emoji,
authorization changes, and concurrent toggles reconcile correctly.

The picker opens from the message action or focused-message `R`, places focus
in search, supports arrow/Enter selection, and returns focus to the originating
message action after dismissal. It is a popover anchored to its trigger with a
category row (Frequently used first), a skin-tone control, the whole catalog
browsable by category, a hover preview, and "Add emoji" for members allowed to
add one. Reaction pills keep the order emoji were first used, name who reacted
("Ada, Grace and you reacted with :eyes:"), and are followed by an add-reaction
control. Selection submits the canonical colon-code
name, never arbitrary free text. Standard aliases and outer colons normalize
before storage; a new reaction MUST match Slack's standard catalog or a durable
workspace custom emoji/alias. Existing reactions remain removable after a
custom emoji is deleted. Custom emoji render as their validated HTTP(S) image,
or the image an administrator uploaded (a PNG, GIF, or JPEG of at most 128 KB,
served from a public URL and reclaimed when the emoji is removed), with
`:name:` alternative text; standard reactions render the corresponding
Unicode sequence; counts and `aria-pressed` identify the current member's
membership independently of the visual.

## ACT-03 — Pin, forward, copy, and share

Pinning follows conversation permissions and produces Slack's channel-visible
effect and system/event projection. Forward/share identifies destination and
optional message, prevents unauthorized destination disclosure, and preserves
the original attribution/link. Slack's "Forward message" dialog searches
channels, direct messages and people, previews the message, and offers Copy
link beside Forward. Copy-link yields a durable authorized permalink;
copy-text contains the message content Slack exposes rather than hidden HTML.

## Evidence

- Browser tests cover every action from mouse, keyboard, and touch-sized
  narrow layout, including reaction picker search, standard/custom rendering,
  and focus return after menus/dialogs.
- API/event/SDK tests prove edit/delete/reply/reaction/pin/share projections
  and permission failures.
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
| ACT-02 | [Use emoji and reactions](https://slack.com/help/articles/202931348-Use-emoji-and-reactions) | Members search emoji and add/remove reactions; the checked developer sources below establish the shared data/API representation. |
| ACT-03 | [Understand your actions](https://slack.com/help/articles/360002063088-Understand-your-actions-in-Slack) | Slack message actions include pinning, forwarding, sharing, and copying. |

Sources checked 2026-07-30:

- [Send and read messages](https://slack.com/help/articles/201457107-Send-and-read-messages)
- [Edit or delete messages](https://slack.com/help/articles/202395258-Edit-or-delete-messages)
- [Use threads to organize discussions](https://slack.com/help/articles/115000769927-Use-threads-to-organize-discussions)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Understand your actions in Slack](https://slack.com/help/articles/360002063088-Understand-your-actions-in-Slack)
- [Use emoji and reactions](https://slack.com/help/articles/202931348-Use-emoji-and-reactions)
- [Formatting message text](https://docs.slack.dev/messaging/formatting-message-text/)
- [`emoji.list`](https://docs.slack.dev/reference/methods/emoji.list/)
