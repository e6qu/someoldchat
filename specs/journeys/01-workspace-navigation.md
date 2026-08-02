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

## NAV-04 — Move through sidebar conversations

Previous/next-conversation shortcuts move through the same visible sidebar
ordering the member sees. Muted, unread, closed-DM, custom-section, and
collapsed-section behavior MUST match a current Slack observation. The
navigation MUST not submit a composer, lose a draft, or select hidden DOM
leftovers.

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

## NAV-07 — Review the threads you follow

Slack's Threads view lists the threads a member follows, most recently replied
first, with the containing conversation, the root message, the reply count and
how many replies the member has not read. A thread whose root has been deleted
MUST leave the view rather than appear as a row that opens onto nothing.

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
