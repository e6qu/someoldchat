# Browser qualification

This suite runs the seeded journeys in Chromium, Firefox, and WebKit.
`npx playwright test --list` (from this directory) gives the current test count.

## Running

From the repository root:

```sh
make browser-qualification        # npm ci, install browsers, run the suite
make browser-qualification-run    # run again without reinstalling
```

The suite uses the Playwright and `@axe-core/playwright` versions pinned in
`package.json` and the lock file. For each engine, and once more for
administration, it starts `cmd/server` in local composition with the in-memory
store and two disposable browser sessions: the member every test signs in as,
and a second plain member (`-peer-session-token`) that a test opens in a
browser context of its own when a journey needs two people. It does not test a production
deployment or use a remote authorization provider.

`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` may point at an already-installed
Chromium-compatible executable on a developer workstation. It does not affect
Firefox or WebKit, which always use Playwright's lockfile-matched builds, and
CI otherwise installs and runs the lockfile-matched browsers.

## What it covers

The suite exercises behavior that server-side tests cannot observe:

- **Workspace and messaging:** session-authenticated workspace entry,
  public-channel preview and joining, posting with Enter and Shift+Enter,
  editing and deletion, JSON-authored blocks, attachments, link previews, draft
  preservation, live delivery, history pagination, and unread bookkeeping.
- **Keyboard and focus:** Slack-style message focus, chronological
  arrow/Home/End navigation, keyboard thread, edit, delete, pin, and
  searchable reaction-picker actions, named mobile navigation, thread reflow,
  and drawer focus containment.
- **Search and typeahead:** Slack-style search shortcuts; typed workspace and
  current-conversation message, file, people, and channel search;
  search-result positioning; durable recent-search selection; and
  visibility-aware people, user-group, channel, file, and emoji typeahead with
  keyboard navigation.
- **Conversations:** reactions, pins, standard and custom emoji, channel
  autocomplete, private channel creation and duplicate-name errors, reviewed
  DM participant expansion with selected history, in-place group-DM conversion
  to a private channel, and navigation to workspace members.
- **Activity, Saved, To-dos, and reminders:** a private channel created
  through the Slack-compatible API produces a durable, source-linked
  Invitations item; focused-message `A` adds to Saved in Home, with Open in
  Home, the clear icon, Move to To-dos, and clean-up (move a selection, remove
  all); message-reminder `M` creates a to-do with a reminder; Add To-do, the
  reminder filter and sort, Edit reminder with a custom local time, Clear due
  date, renaming, Done, and deletion; `/app/later` redirecting to To-dos;
  `/remind` channel creation and the private `/remind list` projection.
- **Scheduled messages:** scheduling in the browser's local time zone, absence
  from channel history while pending, review on the Scheduled surface, and
  cancellation.
- **Workflows:** creating and installing a remote-function app, building and
  publishing a two-step workflow, a link trigger that starts one durable
  execution whose run state survives reload, and a webhook trigger invoked
  over HTTP through its owner-revealed secret URL, with the indistinguishable
  404 for a wrong secret.
- **App surfaces:** the suite plays a Socket Mode app itself. It installs an
  app, issues its app-level token in the developer console, holds the app's
  socket, and acknowledges envelopes as Bolt does, so a global shortcut opening
  a modal (validation errors, submission, close), a legacy dialog with a
  `dialog_suggestion`-loaded select, and an App Home published on
  `app_home_opened` and re-rendered after a button are exercised end to end.
- **Huddles:** starting, the huddle thread, minimising, microphone, camera and
  screen-share controls with synthetic devices, and, as two signed-in members
  in two browsers, an invitation found in the invitee's Activity, joining,
  leaving while the huddle goes on, and ending for everyone, followed live by
  the other member's header and window.
- **Sign-out:** signing out through the UI, a signed-out destination that stays
  terminal across reload, no invented sign-in route when the fixture has no
  provider, and a revoked session that cannot reopen a protected page.
  Provider-backed qualification separately verifies the configured sign-in
  destination.
- **Theme switching.**

Every test title carries one or more stable IDs from the normative
[Slack user-journey catalog](../../specs/journeys/README.md).

Accessibility scans run `@axe-core/playwright` with the WCAG 2.0/2.1 A/AA and
WCAG 2.2 AA tags across many surfaces, including the desktop workspace, the
conversation switcher, Browse channels, the status dialog, bookmarks and Pins,
and the workflow builder and run views, and fail on serious or critical
violations. The suite also checks that the shell reflows without sideways
scrolling at 320 CSS pixels and at 200% zoom. These automated checks
complement, but do not replace, manual screen-reader, keyboard, and zoom
evidence.

The [`probes/`](probes/README.md) directory holds standalone reproductions
that the suite never runs.

## Shauth SSO qualification

`make shauth-sso-qualification` requires `SHAUTH_SOURCE_DIR` to point at a
Shauth checkout of commit `226ffffb9a046378334098c9bf34cc31776c34d4`. It uses
the same pinned Playwright installation to exercise two real SameOldChat
relying parties against real Shauth, Ory Hydra, and PostgreSQL services. The
two applications use distinct databases and dynamically allocated loopback
ports, while `.localhost` origins preserve secure relying-party origin behavior
without fixed host-port collisions. Registering the two applications makes
Shauth queue its own browser validation of each one, from the application and
from Shauth's catalog, with the other application as the global-logout witness;
the script drains that queue the way Shauth's validator worker does and fails
unless exactly those four runs pass.

The official Slack SDK suites are separate; see
[`../official-sdk-qualification`](../official-sdk-qualification/README.md).
Repository build and release checks are listed in the
[repository overview](../../README.md#development-commands).
