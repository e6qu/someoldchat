# Slack user-journey contract

These files are the normative behavioral target for SameOldChat's first-party
web client. They describe Slack's current desktop and web journeys, not the
subset SameOldChat happens to implement. A missing control, API, state, or test
is an implementation gap; it MUST NOT be removed from this contract to make a
test pass.

The reference snapshot date is **2026-07-29**. Slack can change independently
of this repository, so every product audit MUST recheck the linked Slack help
and developer documentation and record the observation date. Where Slack
desktop and Slack in a browser differ, the files call that out explicitly.

## How to read a journey

Every journey has a stable identifier and specifies:

- the user and workspace state required before entry;
- every supported entry point, including keyboard and assistive-technology
  paths;
- the visible sequence and focus behavior;
- the durable state and externally observable API effects;
- empty, loading, permission, validation, conflict, connectivity, and recovery
  behavior; and
- the executable evidence required before the journey can be called
  compatible.

`Target` means observed Slack behavior. `Coverage` belongs in
[`../product-gap-audit.md`](../product-gap-audit.md), the compatibility ledgers,
and test reports; it does not belong in these target definitions.

## Universal interaction contract

The following requirements apply to every journey unless a more specific
requirement overrides them:

1. A control MUST perform the action its accessible name promises. Placeholder
   controls and success notices for uncommitted actions are forbidden.
2. Authorization MUST be checked by the same backend contract used by Slack
   API clients. Hiding a control is not authorization.
3. A handled validation, permission, conflict, or upstream failure MUST retain
   the user's input, explain the failed action in context, and MUST NOT become
   an HTTP 500 response.
4. A mutation MUST have one durable outcome under retry, double activation,
   reconnect, refresh, and browser-history navigation. Optimistic UI MUST
   reconcile with the committed result or visibly roll back.
5. Loading and reconnect states MUST preserve the last usable content. Empty
   state, no-results state, access-denied state, and transient failure are
   distinct states with distinct accessible text.
6. A full-page navigation and its enhanced HTMX/SSE path MUST reach the same
   URL, state, focus destination, authorization result, and browser-history
   behavior.
7. All functionality MUST be usable at 200% zoom, at a 320 CSS-pixel viewport,
   with keyboard only, and with current screen-reader semantics. Focus MUST
   never disappear behind a menu, dialog, or layout transition.
8. Native controls, landmarks, headings, names, descriptions, live regions,
   menu/dialog relationships, selected/current state, errors, and focus order
   MUST expose the same information as the visual UI. Color, position, hover,
   and animation MUST NOT be the only signal.
9. Slack keyboard shortcuts MUST be suppressed while they would corrupt text
   entry, except for shortcuts Slack intentionally handles in the composer.
   Platform-specific `Command` and `Control` variants MUST follow Slack rather
   than a guessed cross-platform mapping.
10. Dates and times MUST respect the member's locale and time zone while
    machine-readable values, scheduling, reminders, cursors, and API timestamps
    remain unambiguous.
11. Destructive and security-sensitive actions MUST identify their target and
    consequence. Slack-confirmed immediate actions MUST not gain an invented
    confirmation; Slack-confirmed confirmations MUST not be omitted.
12. Refreshing, opening a permalink in a new tab, or signing in on another
    client MUST reveal the same committed workspace state.

## Evidence required for compatibility

A journey is compatible only when all applicable layers pass:

| Layer | Required evidence |
| --- | --- |
| Contract | Current Slack help/developer source is linked and its observation date is recorded. |
| Domain | State transitions, permissions, idempotency, and failure classes have deterministic tests. |
| Transport | Browser, Web API, Events API, Socket Mode, webhook, and gRPC projections agree where applicable. |
| SDK | A current official Slack SDK performs or consumes the journey's external contract. |
| Browser | Chromium, Firefox, and WebKit execute the journey at desktop and narrow widths. |
| Accessibility | Keyboard-only and automated accessibility checks pass; screen-reader-critical journeys have a manual transcript. |
| Visual | Stable desktop and narrow screenshots are compared for layout-bearing states. |
| Differential | An opt-in dedicated Slack sandbox records and normalizes the equivalent live behavior. |

Each automated test SHOULD include the relevant journey ID in its title or
metadata. A test that only asserts HTML text, a route status, or a mocked
callback does not qualify an end-to-end journey.

`make journey-check` treats the catalog and browser citations as data: stable
IDs must be unique, each numbered journey file must retain a dated official
Slack source, and a browser test cannot cite an ID the normative catalog does
not define. Its browser coverage report is intentionally a gap report, not a
compatibility score; citing an ID does not by itself satisfy the evidence table
above.

## Catalog

- [Authentication and workspace entry](00-authentication-and-workspaces.md)
- [Workspace navigation](01-workspace-navigation.md)
- [Channels and direct messages](02-conversations-and-direct-messages.md)
- [Composer, drafts, and scheduled messages](03-composer-drafts-and-scheduled.md)
- [Messages, threads, and actions](04-messages-threads-and-actions.md)
- [Search and Activity](05-search-and-activity.md)
- [Later and reminders](06-later-and-reminders.md)
- [Files and media](07-files-and-media.md)
- [Apps, bots, commands, and interactions](08-apps-bots-and-interactions.md)
- [Profile, presence, and notifications](09-profile-presence-and-notifications.md)
- [Calls and huddles](10-calls-and-huddles.md)
- [Canvases, lists, and workflows](11-canvases-lists-and-workflows.md)
- [Administration and Slack Connect](12-administration-and-slack-connect.md)
- [Accessibility, responsive layout, and resilience](13-accessibility-responsive-and-resilience.md)

## Primary external references

- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Use shortcuts to take actions in Slack](https://slack.com/help/articles/360057554553-Use-shortcuts-to-take-actions-in-Slack)
- [Slack accessibility](https://slack.com/help/articles/4455747966739-Accessibility-in-Slack)
- [Use Slack with a screen reader](https://slack.com/help/articles/360000411963-Use-Slack-with-a-screen-reader)
- [Slack developer documentation](https://docs.slack.dev/)

Journey files link their domain-specific sources. Live observations MUST use a
dedicated test workspace and synthetic data; personal or production workspace
content MUST NOT become a fixture.
