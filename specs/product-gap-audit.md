# Product gap audit

This is the evidence-backed boundary between SameOldChat's current product and
the Slack behavior it is intended to support. A registered handler or a
server-rendered control is not counted as complete unless the surrounding user
journey is usable.

Measured on 2026-07-31:

- 223 of Slack's 310 current catalogued Web API methods are registered; the
  320-entry compatibility ledger separately retains ten legacy methods;
- 200 current methods are recorded as behavior-compatible, 21 as
  SDK-compatible, two as schema-compatible, and none as live-differential
  `verified-against-slack`;
- the current official Node Web API SDK exercises the product end to end,
  including app manifests, OAuth, Socket Mode, interactions, message streaming,
  Block Kit validation, hosted-datastore CRUD/bulk operations, and an external
  file upload that becomes one idempotent `file_share` history message. Socket
  Mode now consumes a real posted message projected after installed-bot
  visibility checks rather than a hand-authored callback fixture;
- the official SDK qualification fixture records the exact Web API method paths
  emitted by the pinned Node, Python, and Java clients, while the Deno runtime
  suite records its separately verified completion requests. The fail-closed
  comparison currently observes all 231 methods claimed at `sdk-compatible` or
  above (221 current and ten retained legacy methods);
- all 102 stable journey IDs have an individually checked source-map row. The
  live official-source gate currently makes 131 representative assertions
  explicitly citing 50 of those IDs across authentication, navigation,
  conversations, messaging, search, files, apps, OAuth, presence, huddles,
  canvases, lists, workflows, administration, Slack Connect, accessibility,
  Activity, and reminders before local evidence runs. The remaining 52 IDs are
  printed as upstream-text evidence gaps rather than inheriting coverage;
- 36 Playwright scenarios cite 75 of the normative catalog's 102 stable journey
  IDs and run in Chromium, Firefox, and WebKit. A citation means the scenario
  exercises some part of that journey, not that the whole journey is complete.
  `make journey-check` rejects unknown IDs, missing or duplicate per-journey
  source rows, non-official source URLs, and empty behavioral assertions.
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
- public channels, joining, channel creation, message send/edit/delete,
  threads, reactions, pins, read state, typed workspace/current-conversation
  message and hosted-file search, private durable recent searches,
  visibility-aware people/channel/file typeahead, hosted-file upload,
  first-class file messages, authenticated downloads, and live timeline delivery;
- a dedicated searchable DMs surface, one-to-one and group recipient
  selection, Slack's nine-person total, human-readable group-DM names,
  per-member close without membership/history loss, idempotent API close,
  exact-participant canonical reopen, participant-message reopen, reviewed
  add-people with selectable history and participant notices, and atomic
  group-DM-to-private-channel conversion preserving identity/history/files;
- channel details with named membership, real member selection, rename,
  topic/purpose editing, archive/unarchive, leave, and direct-conversation
  close behavior;
- member directory, profile/status/photo-URL editing, durable status expiry,
  up-to-five future statuses with edit/cancel and atomic timed activation,
  manual active/away selection, and presence-aware member affordances;
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
| P0 | Search depth | Messages and hosted files now use typed modifier plans, visibility-safe totals/pagination, persistent Unicode-folded file search, private durable recent history, visibility-aware people/channel/file typeahead, generated gRPC parity, user-token Web API methods, pinned Node/Python/Java calls, and real Messages/Files/People/Channels UI tabs with `Command/Control+F`. Remaining depth is Canvases, semantic ranking/highlighting, every modifier variant, full People/Channels pagination, exact thread-entry scope and hit marking, visual baselines, and live-Slack differential outcomes. |
| P0 | Activity depth | Activity now uses durable per-recipient records for DMs/MPIMs, mentions, authored-thread and followed-thread replies, all-new-post channel preferences, reactions, applicable app messages, and delivered reminders, with shared memory/SQL/gRPC pagination, typed filters, dense/detailed layout, read/clear/restore, source authorization, bulk actions, keyboard triage, inline reactions, and focus/selection-preserving live refresh. Remaining depth is invitation/VIP/section notifications, custom views, and pre-v107 history backfill. |
| P0 | Composer depth | Formatting, member/user-group/channel autocomplete, a searchable standard/custom emoji picker backed by Slack's pinned catalog, durable recent/category/skin-tone choices, custom alias rendering, selected/pasted/dropped multi-file staging, permission-aware five-minute audio/video recording, atomic attachment-plus-text send, draft recovery, browser-time-zone scheduled send, and a full searchable built-in/installed-app shortcuts browser work. User-group selection uses Slack's subteam transport syntax and drives visibility-safe Activity. Slack video source/device controls, thumbnails, transcripts, the exact emoji ranking differential, and durable staged-attachment drafts remain. |
| P1 | Saved and scheduled work | Scheduled-message APIs enforce exact-token ownership, ranges, threads, time/quota limits, durable failure state, and multi-workspace worker execution. The client can schedule from channel and thread composers; list pending, failed, and sent work in Drafts & sent; edit/reschedule; send now; and cancel without posting early. Current Later has private save/unsave state and In progress/Archived/Completed organization, separate from deprecated app-facing stars. First-party reminders have a separate durable model, message-action presets/custom time, Later CRUD/filtering, named-weekday `/remind`, private channel-reminder listing, guest enforcement, worker recurrence/retry/failure fencing, Activity/source projection, and wake publication. Broader natural-language reminder parsing, month-end recurrence, deterministic deployed-worker browser delivery, and live-Slack differential outcomes remain. The five deprecated app-facing `reminders.*` methods remain only SDK-compatible and are not evidence for the first-party Later model. |
| P1 | Direct-message lifecycle | Dedicated DMs navigation/search, one-to-one/group creation, naming, Slack's nine-person limit, durable per-member close, exact API no-op fields, canonical reopen, reviewed add-people with no/all-history choices and notices, and in-place private-channel conversion now work across memory/SQL/gRPC/browser paths. Current official SDKs also prove canonical exact-member group opening. Slack's help page does not enumerate its complete history-option list, so that exact live option inventory, Slack Connect/external conversion policy, and workspace-configurable restrictions remain differential gaps rather than inferred compatibility. |
| P1 | Notifications and presence | Workspace and per-conversation notification preferences plus snooze/DND controls exist in the client and API. Status expiry and up-to-five future statuses with edit/cancel and atomic worker activation are durable and restart-safe; manual `auto`/`away` selection and truthful member affordances exist. Live activity-derived automatic presence and Slack's ten-minute idle transition, full status projection outside People/profile, workspace emoji validation, typing indicators, and deeper Activity/VIP/invitation policy remain. |
| P1 | Calls and huddles | Calls APIs exist, but there is no first-party call/huddle experience. |
| P1 | App administration | Manifest JSON editing and OAuth installation are real. A developer-owned hosted app can now browse its manifest-declared datastore schema and durable items, execute Slack-compatible expression/cursor queries, count matches, replace/update JSON items, and delete them through the same local/gRPC/storage boundary as `apps.datastore.*`; ownership, installation, hosted-runtime, schema, and query failures remain explicit. Install-time incoming-webhook selection, event-delivery health/retries, scope explanation, token inventory/revocation, distribution, and external-auth providers remain absent. |
| P2 | Canvases, lists, and workflows | Canvases and lists are now first-class persisted workspace surfaces: access-filtered directories, read/write-grant-aware detail controls, atomic canvas title/content revision, list creation, item creation and complete/restore run through memory/SQL and the generated gRPC seam. Full structured canvas blocks/comments/history, sharing management, list schemas/views/assignments/comments/files, deletion, and workflow creation/execution remain incomplete. |
| P2 | Client breadth | The semantic journeys now run in Chromium, Firefox, and WebKit, with automated WCAG checks for representative desktop, dialog, and narrow states. Performance budgets, screenshot differential, manual assistive-technology coverage, and live-Slack comparisons remain unqualified. |

## Web API and app-platform gaps

The 87 unimplemented current Web API methods are:

| Namespace | Missing | Boundary |
| --- | ---: | --- |
| `admin.*` | 50 | Enterprise analytics, policies, roles, bulk conversation operations, org sessions, and org-wide app/function/workflow administration |
| `apps.*` | 5 | External-auth get/delete, app activity history, app icons, and user connection state |
| `conversations.*` | 9 | Slack Connect invitation/approval and external-invite policy |
| `functions.*` / `workflows.*` | 14 | Function distribution, workflow-step discovery/export, featured workflows, and trigger permissions |
| `assistant.*` | 5 | Assistant search context and thread title/status/suggested-prompt presentation |
| `team.*`, `users.*` | 4 | Billing/external-team administration and discoverable contacts |

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
4. extend the implemented datastore query/count expression evaluator and
   documented scan semantics only when a current Slack contract or controlled
   differential establishes additional operators;
5. add app icon storage/rendering and delivery-health/token administration;
6. then expand assistant, Slack Connect, and Enterprise administration.

## Qualification gaps

Official SDK qualification now proves, method by method, that genuine clients
issued a request and parsed SameOldChat's response; a large neighboring test
can no longer lend SDK evidence to an uncalled method. It still does not prove
live Slack equivalence. The compatibility report also exposes the distinction:
only 25 of 221 current claims at `sdk-compatible` or above presently carry
method-level evidence in the ledger; the remaining 196 must not inherit that
evidence from the aggregate green suite. The remaining evidence layers are:

1. pin per-method current argument/response/error schemas, not only the current
   method index;
2. run response fixtures through current Node, Python, and Java SDK types where
   those SDKs expose the method;
3. add manual screen-reader, zoom, reduced-motion, and complete keyboard-only
   qualification to the existing three-engine automated accessibility gate;
4. maintain visual baselines for desktop and narrow layouts;
5. run opt-in differential tests against a dedicated live Slack sandbox and
   promote only observed methods to `verified-against-slack`.
