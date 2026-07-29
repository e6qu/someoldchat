# Product gap audit

This is the evidence-backed boundary between SameOldChat's current product and
the Slack behavior it is intended to support. A registered handler or a
server-rendered control is not counted as complete unless the surrounding user
journey is usable.

Measured on 2026-07-29:

- 215 of Slack's 310 current catalogued Web API methods are registered; the
  320-entry compatibility ledger separately retains ten legacy methods;
- 205 current methods are recorded as behavior-compatible, eight as
  SDK-compatible, two as schema-compatible, and none as live-differential
  `verified-against-slack`;
- the current official Node Web API SDK exercises the product end to end,
  including app manifests, OAuth, Socket Mode, interactions, message streaming,
  Block Kit validation, hosted-datastore CRUD/bulk operations, and an external
  file upload that becomes one idempotent `file_share` history message. Socket
  Mode now consumes a real posted message projected after installed-bot
  visibility checks rather than a hand-authored callback fixture;
- 27 Playwright scenarios cite 48 of the normative catalog's 101 stable journey
  IDs and run in Chromium, Firefox, and WebKit. A citation means the scenario
  exercises some part of that journey, not that the whole journey is complete.
  `make journey-check` now rejects unknown IDs and source-less journey files.
  Automated axe checks cover the desktop workspace, command discovery, and a
  320-pixel layout; visual-regression baselines, manual assistive-technology
  qualification, and comparison against a live Slack client remain absent.

The normative [Slack user-journey catalog](journeys/README.md) records the full
target separately from this coverage report. A journey omitted from the
working list below remains a gap if it is present in that catalog; current
implementation MUST NOT narrow the target.

## First-party UI journeys

### Working end to end

- authenticated workspace entry and terminal sign-out;
- Activity navigation with Slack web's `Control+3` on macOS and
  `Control+Shift+3` on Windows/Linux, joined-conversation unread aggregation,
  and explicit mention results;
- public channels, joining, DMs, channel creation, message send/edit/delete,
  threads, reactions, pins, read state, message search, hosted-file upload,
  first-class file messages, authenticated downloads, and live timeline delivery;
- channel details with named membership, real member selection, rename,
  topic/purpose editing, archive/unarchive, leave, and direct-conversation
  close behavior;
- member directory and profile/status/photo-URL editing;
- private save-for-later state, focused-message `A`, In progress, Archived,
  Completed, restore/removal, source navigation, and inaccessible-source
  redaction without conflating current Later with deprecated `stars.*`;
- desktop and narrow layouts, named mobile navigation, light/dark themes,
  message/composer keyboard navigation, safe Slack-markup formatting controls,
  member mention autocomplete, an emoji picker, per-conversation/thread draft
  recovery, and selected-file preview;
- current Block Kit block rendering, safe Slack markup, attachments, unfurls,
  message actions, dynamic options, modal interaction, app shortcuts, and App
  Home;
- developer app creation, current-manifest validation/edit/delete, one-time
  credentials, app-level tokens, OAuth consent, and installed-app discovery.

### Core Slack journeys still absent or incomplete

| Priority | Journey | Concrete gap |
| --- | --- | --- |
| P0 | Activity depth | Activity now covers joined unread conversations and explicit mentions, but it does not yet aggregate thread replies, reminders, notification-policy events, clear state, bulk read, layouts, filters, or custom tabs from Slack's current Activity model. |
| P0 | Composer depth | Formatting, standard emoji, member mention autocomplete, file preview, draft recovery, and browser-time-zone scheduled send work. Custom-emoji browsing, channel autocomplete, pasted-file staging, voice/video clips, and Slack's full shortcuts browser remain. |
| P1 | Saved and scheduled work | Scheduled-message APIs enforce exact-token ownership, ranges, threads, time/quota limits, durable failure state, and multi-workspace worker execution. The client can schedule from channel and thread composers, list pending work, and cancel it without posting early. Current Later has private save/unsave state and In progress/Archived/Completed organization, separate from deprecated app-facing stars. Edit/reschedule, send-now, failure history, Slack's combined Drafts & sent tabs, reminder dates/upcoming filters, channel `/remind`, and reminder delivery/UI remain. The five deprecated `reminders.*` methods are only SDK-compatible: natural-language parsing, recurrence, current targeting rules, delivery, and live outcomes are not falsely counted as behavior evidence. |
| P1 | Direct-message lifecycle | DMs can be opened from People, but there is no dedicated DM/group-DM section, participant management, or recent-DM navigation matching Slack. |
| P1 | Notifications and presence | Basic presence/status APIs exist, but the client has no per-conversation notification preferences, status expiry, snooze/DND controls, typing indicators, or presence-aware member affordances. |
| P1 | Calls and huddles | Calls APIs exist, but there is no first-party call/huddle experience. |
| P1 | App administration | Manifest JSON editing is real, but install-time incoming-webhook selection, event-delivery health/retries, scope explanation, token inventory/revocation, distribution, external-auth providers, and hosted-datastore browsing are absent. |
| P2 | Canvases, lists, and workflows | API slices exist, but these are not first-class workspace surfaces and workflow creation/execution is incomplete. |
| P2 | Client breadth | The semantic journeys now run in Chromium, Firefox, and WebKit, with automated WCAG checks for representative desktop, dialog, and narrow states. Performance budgets, screenshot differential, manual assistive-technology coverage, and live-Slack comparisons remain unqualified. |

## Web API and app-platform gaps

The 95 unimplemented current Web API methods are:

| Namespace | Missing | Boundary |
| --- | ---: | --- |
| `admin.*` | 50 | Enterprise analytics, policies, roles, bulk conversation operations, org sessions, and org-wide app/function/workflow administration |
| `apps.*` | 7 | Datastore query/count, external-auth get/delete, app activity history, app icons, and user connection state |
| `conversations.*` | 10 | Slack Connect invitation/approval policy and conversation canvases |
| `functions.*` / `workflows.*` | 14 | Function distribution, workflow-step discovery/export, featured workflows, and trigger permissions |
| `assistant.*` | 5 | Assistant search context and thread title/status/suggested-prompt presentation |
| `auth.*`, `rtm.*`, `search.*`, `team.*`, `users.*` | 9 | Authorized-team enumeration, legacy RTM bootstrap, all/file search, billing/external-team/preferences, and discoverable contacts |

App-platform work must remain dependency-ordered:

1. extend the new conversation-visibility-aware Events API hydration from
   installed bots to user-token HTTP/Socket subscriptions, message
   change/delete variants, and `file_shared`/`file_unshared`; message creation
   and hosted-file creation now hydrate only after installed-bot access is
   proved, and every currently translated event carrying `channel_id` is
   membership-filtered for app and RTM delivery;
2. implement workflow execution and durable activity records;
3. implement external OAuth provider configuration, browser consent, encrypted
   refresh/access tokens, and `apps.auth.external.*`/connection callbacks as one
   journey;
4. implement full datastore query/count semantics using Slack's documented
   DynamoDB expression contract rather than a partial expression parser;
5. add app icon storage/rendering and delivery-health/token administration;
6. then expand assistant, Slack Connect, and Enterprise administration.

## Qualification gaps

Official SDK qualification proves that genuine clients can serialize requests
and parse SameOldChat responses. It does not prove live Slack equivalence. The
remaining evidence layers are:

1. pin per-method current argument/response/error schemas, not only the current
   method index;
2. run response fixtures through current Node, Python, and Java SDK types where
   those SDKs expose the method;
3. add manual screen-reader, zoom, reduced-motion, and complete keyboard-only
   qualification to the existing three-engine automated accessibility gate;
4. maintain visual baselines for desktop and narrow layouts;
5. run opt-in differential tests against a dedicated live Slack sandbox and
   promote only observed methods to `verified-against-slack`.
