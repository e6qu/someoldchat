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
context to continue reading. Removed, inaccessible, malformed, and moved
targets have distinct safe outcomes.

## NAV-06 — Change theme and density

Member-selected appearance applies across workspace, modal, app home, and
authentication surfaces; follows Slack's system-theme behavior where selected;
persists at the same scope as Slack; and preserves contrast, focus, charts,
syntax, emoji, files, and app-rendered content. Changing appearance MUST not
reload or discard in-progress work.

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

Sources checked 2026-07-29:

- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard)
- [Change your Slack theme](https://slack.com/help/articles/205166337-Change-your-Slack-theme)
