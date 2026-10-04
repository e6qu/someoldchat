# Workspace navigation

## NAV-01 — Understand and traverse the workspace shell

**Preconditions:** The member is authenticated.

**Target behavior:** The shell exposes stable workspace, navigation, sidebar,
conversation, and secondary-detail regions. The active destination is both
visually and programmatically current. Home, DMs, Activity, Later, More, Apps,
channels, and direct messages appear according to Slack availability and
workspace policy. Collapsed sections retain their state without making the
active item unreachable.

At narrow widths, the same destinations remain available through named
navigation controls. Opening or closing navigation moves focus predictably,
prevents background interaction while modal, and does not reset the open
conversation.

Every destination is drawn inside one frame, as Slack's client is: a top bar
with history controls, search and Help; a left rail with the workspace menu,
Home, DMs, Activity, Later and More (Files, Canvases, Lists, Workflows, People,
Apps), a Create button (Message, Channel, Canvas, List, Huddle, Workflow) and
the member's avatar menu; and the destination beside it. Leaving the
conversation for a secondary destination keeps the conversation in the rail's
Home link, so returning never drops it. The workspace menu carries Tools &
settings (workspace settings for an administrator, developer apps) and sign
out; the avatar menu carries status, away, pausing notifications, profile,
Preferences and sign out.

## NAV-02 — Use Slack's global keyboard navigation

Outside conflicting text-entry contexts, the client MUST implement Slack's
documented platform and surface mapping:

| Action | macOS | Windows/Linux | Browser difference |
| --- | --- | --- | --- |
| Search Slack | `Command+G` | `Control+G` | none |
| Search current conversation | `Command+F` | `Control+F` | none |
| Jump to a conversation | `Command+K` | `Control+K` | none |
| Previous/next conversation | `Option+Up/Down` | `Alt+Up/Down` | none |
| Activity | `Command+Shift+M` | `Control+Shift+M` | the dedicated shortcut is desktop-only; web uses the assigned navigation-tab shortcut, currently `Control+3` on Mac and `Control+Shift+3` on Windows/Linux for the default Activity tab |
| Conversation details | `Command+Shift+I` | `Control+Shift+I` | none |
| Move among major sections | `F6` / `Shift+F6` | `F6` / `Shift+F6` | web uses `Command+F6` / `Command+Shift+F6` on Mac and `Control+F6` / `Control+Shift+F6` on Windows/Linux |
| Previous/next unread conversation | `Option+Shift+Up/Down` | `Alt+Shift+Up/Down` | none |
| Direct messages | `Command+Shift+K` | `Control+Shift+K` | none |
| Later | `Command+Shift+S` | `Control+Shift+S` | Slack's saved-items surface is named Later here |
| Mark this conversation read | `Escape` | `Escape` | applies outside a text field, so `Escape` still dismisses the composer's suggestions, a dialog, or the navigation drawer |
| Mark every conversation read | `Shift+Escape` | `Shift+Escape` | applies anywhere, including the composer: `Shift+Escape` means nothing else in a text field |
| Attach a file | `Command+U` | `Control+U` | none |
| Browse channels | `Command+Shift+L` | `Control+Shift+L` | none |
| People | `Command+Shift+E` | `Control+Shift+E` | none |
| New message | `Command+N` | `Control+N` | browsers reserve `Control+N` for a new window, so on the web the compose button beside the workspace name and the Create menu are the reliable entry points |
| Set your status | `Command+Shift+Y` | `Control+Shift+Y` | none |
| Show or hide the sidebar | `Command+Shift+D` | `Control+Shift+D` | none |
| Show or hide the right pane | `Command+.` | `Control+.` | none |
| Preferences | `Command+,` | `Control+,` | none |
| Keyboard shortcuts | `Command+/` | `Control+/` | none |

The client MUST also publish the layer it implements. Slack opens a shortcut
reference on `Command/Control+/`; a member who does not know that chord MUST be
able to reach the same reference from a visible control, or the reference is
only discoverable by already having the knowledge it exists to supply. The
reference MUST show the chords for the platform the member is on, and it MUST
NOT list a binding the client does not implement — an announced binding that
does nothing is worse than an absent one, because assistive technology reads it
out as available.

The shortcut target MUST receive visible focus and an announced name. Browser
or operating-system reserved behavior MUST be preserved where Slack preserves
it. `Command/Control+K` MUST NOT be mislabeled as global search, and a bare `/`
outside the composer MUST NOT be invented as a global-search shortcut.

## NAV-03 — Jump to a conversation

Slack's jump/quick-switcher entry point opens a searchable dialog over channels
and DMs the member may access. Results update as the member types, identify
conversation type and relevant context, support keyboard selection, and never
reveal a private conversation the member cannot discover. Choosing a result
navigates once and restores the conversation reading position where Slack does.

The query field is a combobox over a listbox: the highlighted result is its
active descendant, each result names its type (channel, private channel,
direct message, group DM, app), and a query that matches nothing says so in a
live region rather than showing an empty list. With nothing typed, the
conversations most recently opened lead, excluding the one being read.

## NAV-04 — Move through sidebar conversations

Previous/next-conversation shortcuts move through the same visible sidebar
ordering the member sees.

The sidebar lists the conversations the member belongs to: Starred first,
then custom sections, Channels, Direct messages and Apps. A public channel the
member has not joined is reached through Browse channels, not listed. Rows are
alphabetical by default, and each section's menu sorts it alphabetically, by
priority or (for direct messages) by most recent activity, shows all
conversations, unreads only or mentions only, and marks the section's unread
conversations read. An unread conversation is bold;
a count badge appears only for mentions in a channel and for every unread
message in a direct message; a muted conversation is greyed and never bold.
A private channel carries a lock rather than a `#`, and a self-DM is named
"<name> (you)". Each row's menu opens details, copies its name or link, stars,
mutes, changes notifications, moves it to a section and leaves it; dragging a
row onto a section moves it there as well. Muted, unread, closed-DM, custom-section, and
collapsed-section behavior MUST match a current Slack observation. The
navigation MUST not submit a composer, lose a draft, or select hidden DOM
leftovers.

Custom sidebar sections are implemented: a member creates named sections, moves
channels between them and reorders the sections (by dragging one onto another,
as in Slack, or with the section menu's Move up and Move down), and collapses a
section so its
channels are hidden — all durable, all the member's own. Each section is its own
named navigation region; a channel not assigned to any section falls to the
default Channels group, and a channel the member has left drops out of its
section at render time rather than lingering. A section also carries its own
notification level, layered in the per-recipient fanout between a channel's own
override and the workspace default: a muted section silences a channel the
workspace default would deliver, and a channel's own setting still wins over the
section's.

## NAV-05 — Use history and permalinks

Browser back/forward returns through meaningful destinations without replaying
mutations. A message or thread permalink opens the containing conversation,
loads a window containing the target, marks the target, and exposes enough
context to continue reading. A malformed permalink is refused
without a lookup. A removed target and one in a conversation the member may not
read MUST answer identically and MUST NOT disclose which applies, because the
difference is itself the disclosure — the answer names the message rather than
the conversation, since for a permalink the conversation is usually readable and
only the message is gone.

## NAV-06 — Change theme and density

Member-selected appearance applies across workspace, modal, app home, and
authentication surfaces; follows Slack's system-theme behavior where selected;
persists at the same scope as Slack; and preserves contrast, focus, charts,
syntax, emoji, files, and app-rendered content. Changing appearance MUST not
reload or discard in-progress work.

Appearance is chosen in Preferences (`Command/Control+,`, or the avatar menu):
Light, Dark, or the operating system's setting. The choice, like the other
Preferences (sidebar, sort and show, mark as read, what Enter does, markup,
skin tone), is kept for the member's account, as Slack keeps it: a browser the
member has never used starts with it, and a preference a browser kept before
its account did is kept for the account on that browser's next visit. Recent
conversations stay with each browser.

Accessibility underlines links in messages, turns off interface animation,
and stops announcing incoming messages (by default a screen reader hears each
arrival's sender and text, as Slack's "Announce incoming messages" does);
Messages & media switches the Clean theme to Compact (no profile photos
beside messages and less space between them), shows just display names (hiding
the full name the directory, people search, and mention suggestions show beside
one), hides uploaded images (leaving a link to each) or link previews, shows
emoji as their `:codes:`, and turns off large emoji. Each
applies at once to every message on the page and is kept with the account like
the rest.

A member hides a person from that person's profile, as Slack's "Hide a
person" does. The person's messages are still delivered and counted unread, but
each sits behind a "Show message" click-through that hides their name and
photo, and a screen reader announces an arrival from them without reading it.
Outside the conversation — thread cards, search results, Unreads, Activity, and
the direct-message list — a preview names neither them nor what they said.
Privacy & visibility lists the people hidden, each with Unhide. Hiding is the
member's own preference: nobody is told, and administrators cannot see it.

Language & region sets the member's time zone automatically from the browser,
as Slack does by default, or by hand: choosing a zone turns the automatic zone
off, so a browser in another zone no longer moves it, and turning it back on
lets the browser's zone in again.

Navigation chooses which of DMs, Activity, Later and Files the rail shows
(Home always shows) and whether tabs show their names. A tab taken off the
rail moves into More and keeps its keyboard shortcut; a name taken off the
rail stays the tab's accessible name.

## NAV-07 — Review the threads you follow

Slack's Threads view lists the threads a member follows, most recently replied
first, with the containing conversation, the root message, the reply count and
how many replies the member has not read. A thread whose root has been deleted
MUST leave the view rather than appear as a row that opens onto nothing.

Each card ends in a reply field, as in Slack. A reply sent from it MUST land in
that thread and return the member to the same card; Enter follows the
member's composer preference. A refused reply MUST open the thread with the
draft kept and the reason shown, rather than lose the text.

Unread MUST be derived from the member's read position in the containing
conversation. A second, thread-only read position would let the Threads view
and the conversation disagree about the same replies.

## NAV-08 — Triage unread conversations

Slack's Unreads view groups every unread message by conversation so a member
can clear a backlog without opening each conversation in turn. It MUST offer
marking one conversation read and marking every conversation read, and where it
bounds what it shows it MUST say what it has not shown rather than present a
truncated list as complete.

## Evidence

- Execute every documented shortcut in Chromium, Firefox, and WebKit on the
  applicable operating-system mapping and in/out of editable controls.
- Test full-page and enhanced navigation, browser history, deep links, narrow
  navigation, section collapse, unread ordering, and focus announcements.
- Keep desktop and narrow visual baselines for every shell region and theme.
## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| NAV-01 | [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard) | Slack exposes named regions and predictable focus movement through its workspace. |
| NAV-02 | [Slack keyboard shortcuts](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands) | Slack publishes platform-specific global navigation shortcuts. |
| NAV-03 | [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard) | Command or Control K opens a searchable channel and person switcher. |
| NAV-04 | [Slack keyboard shortcuts](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands) | Option or Alt with arrow keys moves among conversations. |
| NAV-05 | [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard) | Slack preserves message navigation and focused reading position. |
| NAV-06 | [Change your Slack theme](https://slack.com/help/articles/205166337-Change-your-Slack-theme) | Slack persists member-selected appearance across the client. |
| NAV-07 | [Manage threads in Slack](https://slack.com/help/articles/115000769927-Use-threads-to-organize-discussions) | Slack collects the threads a member follows into a dedicated view. |
| NAV-08 | [Manage your unread messages](https://slack.com/help/articles/360043207674-Manage-your-unread-messages) | Slack collects unread messages into a dedicated view with per-conversation and workspace-wide mark-as-read. |

Sources checked 2026-07-29:

- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard)
- [Change your Slack theme](https://slack.com/help/articles/205166337-Change-your-Slack-theme)
- [Use threads to organize discussions](https://slack.com/help/articles/115000769927-Use-threads-to-organize-discussions)
- [Manage your unread messages](https://slack.com/help/articles/360043207674-Manage-your-unread-messages)
