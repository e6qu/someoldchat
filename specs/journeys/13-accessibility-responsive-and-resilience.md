# Accessibility, responsive layout, and resilience

This file makes the universal interaction contract executable across all
journeys. Passing an automated scanner alone is not accessibility
qualification.

## A11Y-01 — Navigate with keyboard only

Every interactive action in the catalog is reachable and operable without a
pointer. Focus order follows reading/task order; visible focus has sufficient
contrast; skipped/hidden regions are not focusable; menus use menu semantics;
dialogs trap and restore focus; escape behavior is predictable; and opening a
timeline update does not steal focus.

Slack's documented shortcuts coexist with browser, operating-system, and
assistive-technology commands. The shortcut help surface reports the actual
platform mapping implemented.

## A11Y-02 — Navigate with a screen reader

Workspace navigation, channel/DM lists, conversation, thread, Activity, Later,
composer, search, modal, and app surfaces have stable landmarks/headings and
concise names. Conversation messages expose author, time, content, edited/
thread/reaction/file state, and actions without reading decorative duplication.
Dynamic additions and mutation results use appropriately scoped live
announcements.

The member can use Slack-compatible screen-reader preferences, message
navigation mode, and `F6` region traversal without entering a focus trap.
Virtualized/paginated history announces boundaries and preserves the current
message.

## A11Y-03 — Perceive visual and media content

Text and controls meet current WCAG AA contrast and non-text contrast;
information is not color-only; zoom/reflow does not clip essential content;
user text can enlarge without fixed-height loss; reduced-motion and
high-contrast/forced-colors preferences are respected. User/app images accept
useful alt descriptions, decorative images are hidden, and audio/video/huddle
features expose captions/transcripts where Slack does.

## RESPONSIVE-01 — Complete every journey on a narrow viewport

At 320 CSS pixels and 200% zoom, the active task remains usable. Navigation,
conversation, thread/details, composer, message actions, tables, app blocks,
files, modals, toasts, and virtual keyboard never create inaccessible
off-screen controls. Secondary panes use explicit open/back/close navigation
and restore the originating position/focus.

Desktop-only hover actions have focus and touch equivalents. Device rotation
and viewport resize retain unsent input, open object, and committed state.

## RESILIENCE-01 — Disconnect and reconnect

When SSE/network connectivity is lost, the shell states that updates are
paused while preserving readable content and local work. Mutations are not
reported successful until acknowledged. Reconnect resumes from the last
durable event ID, replays each missed event once, reconciles optimistic state,
and refreshes authorization. An expired replay window performs a bounded
resynchronization without duplicating messages or Activity.

## RESILIENCE-02 — Cold wake, slow operation, and retry

Cold activation has an explicit bounded progress state and never invites
duplicate activation by repeated controls. Slow searches, uploads, apps, and
mutations remain cancellable where Slack permits. Retry reuses idempotency for
the same intended action and a new key for a deliberate new action.

## RESILIENCE-03 — Process loss and multi-client convergence

Terminating a web/worker/database node after any commit boundary yields either
one complete action or no action, never partial UI success. A second browser or
SDK observes the same durable result. Concurrent edit/delete/react/read/save/
schedule/app actions converge using the journey's defined conflict semantics.

## RESILIENCE-04 — Safe error and recovery presentation

Errors identify the attempted action and affected object without exposing
credentials, internal stack traces, SQL, tenant data, or app secrets. Inline
errors are associated with their field; global results use a focusable heading
or live region; recovery does not erase input. Handled domain, validation,
authorization, rate-limit, conflict, timeout, and dependency failures MUST NOT
be HTTP 500.

## Qualification matrix

Every P0/P1 journey MUST run at least these configurations:

| Dimension | Required values |
| --- | --- |
| Engine | Chromium, Firefox, WebKit |
| View | desktop, 320 CSS-pixel narrow, 200% zoom |
| Input | pointer, keyboard only, touch-equivalent |
| Theme | light, dark, forced colors where supported |
| Motion | default, reduced motion |
| Connectivity | online, offline during read, offline during mutation, replay |
| Composition | local, distributed gRPC, cold wake where applicable |

Screen-reader-critical paths require maintained manual transcripts for current
VoiceOver/Safari and NVDA/Firefox or documented equivalent coverage. Automated
checks MUST include current axe rules, HTML/name validation, focus assertions,
and no horizontal overflow at target widths.

Sources checked 2026-07-29:

- [Accessibility in Slack](https://slack.com/help/articles/4455747966739-Accessibility-in-Slack)
- [Use Slack with a screen reader](https://slack.com/help/articles/360000411963-Use-Slack-with-a-screen-reader)
- [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard)
- [WCAG 2.2](https://www.w3.org/TR/WCAG22/)
