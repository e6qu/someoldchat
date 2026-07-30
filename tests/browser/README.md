# Browser qualification

This suite runs 34 seeded journeys in Chromium, Firefox, and WebKit and
exercises behavior that server-side tests cannot observe: session-authenticated
workspace entry, public-channel preview and joining, message posting with the
advertised Enter and Shift+Enter behavior, Slack-style search shortcuts,
Slack-style message focus, chronological arrow/Home/End navigation, and
keyboard thread, edit, delete, pin, and reaction actions,
typed workspace/current-conversation message, file, people, and channel search,
theme switching, reactions, pins, and navigation to workspace
members. It also exercises message editing and deletion, private channel
creation and duplicate-name errors, named mobile navigation, thread reflow,
drawer focus containment, contextual mutation failures, unread bookkeeping,
live delivery, history pagination, search-result positioning, JSON-authored
blocks, attachments, link previews, draft preservation, reviewed DM
participant expansion with selected history, and in-place group-DM conversion
to a private channel. The current
Slack Later journey is exercised through focused-message `A`, private
save/unsave state, In progress, Completed, Archived, restore, source navigation,
and removal. It also
exercises message-reminder `M`, preset and custom local times, personal
reminder editing/completion/deletion, `/remind` channel creation, and the
private `/remind list` projection. It also
schedules a message in the browser's local time zone, verifies that the
pending item does not appear in channel history, reviews it on the Scheduled
surface, and cancels it. It signs out through the application UI,
asserts the application-owned signed-out destination remains terminal across a
reload, does not invent a sign-in route when the local fixture has no provider,
and verifies the revoked session cannot reopen a protected page. Provider-backed
qualification separately verifies the configured sign-in destination.

Every test title carries one or more stable IDs from the normative
[Slack user-journey catalog](../../specs/journeys/README.md). The suite also
runs `@axe-core/playwright` 4.12.1 against the desktop workspace, the
conversation-switcher dialog, and a 320-pixel narrow viewport. Those automated
WCAG 2.0/2.1 A/AA and WCAG 2.2 AA checks complement, but do not replace, manual
screen-reader, keyboard, zoom, and live-Slack comparison evidence.

Run it from the repository root:

```sh
make browser-qualification
```

`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` may point at an already-installed
Chromium-compatible executable. This is an explicit browser coordinate for
developer workstations; CI otherwise installs and runs the lockfile-matched
Playwright browser builds.

The suite uses the pinned Playwright version in `package.json` and the lock
file. The explicit Chromium executable override does not weaken Firefox or
WebKit qualification; those engines always use Playwright's lockfile-matched
builds. It starts `cmd/server` with the local in-memory store and a disposable
browser session. It does not test a production deployment or use a remote
authorization provider.

The separate `make shauth-sso-qualification` gate requires
`SHAUTH_SOURCE_DIR` to point at Shauth commit
`0fda680cba964e5768ed75a9c3e5b7230c418ca6`. It uses the same pinned Playwright
installation to exercise two real SameOldChat relying parties against real
Shauth, Ory Hydra, and PostgreSQL services. The two applications use distinct
databases and dynamically allocated loopback ports, while `.localhost` origins
preserve secure relying-party origin behavior without fixed host-port
collisions.

The browser qualification is separate from the official Slack SDK suites in
[`../official-sdk-qualification`](../official-sdk-qualification/README.md).
The repository's build and release checks are documented in
[`../../README.md`](../../README.md).
