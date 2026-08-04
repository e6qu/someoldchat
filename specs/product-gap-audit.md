# Product gap audit

This is the evidence-backed boundary between SameOldChat's current product and
the Slack behavior it is intended to support. A registered handler or a
server-rendered control is not counted as complete unless the surrounding user
journey is usable.

Measured on 2026-08-02:

- 236 of Slack's 310 current catalogued Web API methods are registered; the
  320-entry compatibility ledger separately retains ten legacy methods;
- 211 current methods are recorded as behavior-compatible, 35 as
  SDK-compatible, two as schema-compatible, and none as live-differential
  `verified-against-slack`. The three
  `admin.conversations.*CustomRetention` methods moved from unimplemented to
  behavior-compatible with this change, bounded to messages and files and to
  the workspace administrator role rather than Slack's Enterprise plan gate;
- the Events API surface emits 40 event names: the message family (with the
  message_changed/message_deleted subtypes and the projection-derived
  app_mention), reactions, pins, stars, membership, app_home_opened,
  function_executed, app_installed, the file events including file_public,
  the conversation lifecycle (channel_created, im_created, and the
  channel_*/group_* rename/archive/unarchive/delete pairs), emoji_changed,
  the subteam family, team_join, user_change with its user_profile_changed
  and user_status_changed companions, dnd_updated, team_rename, and the
  RTM-only presence_change, plus tokens_revoked (minted inside the store's
  revoking mutation, on both sides of the auth seam) and app_uninstalled
  (whose announcement outlives the installation it announces). Recorded
  event gaps: dnd_updated_user, link_shared, message metadata, typing, and
  the Slack Connect and Grid families — see the events-api decision in
  compatibility.yaml;
- the Web API enforces its rate-limiting contract: 429 with Retry-After and
  the pinned rate_limited code, a uniform per-method budget at Tier 4's
  documented floor per credential, and chat.postMessage's documented
  one-per-second-per-channel allowance. Recorded boundaries: budgets are
  replica-local, and per-method tier assignments below the Tier 4 floor are
  not modelled;
- the current official Node Web API SDK exercises the product end to end,
  including app manifests, OAuth, Socket Mode, interactions, message streaming,
  Block Kit validation, hosted-datastore CRUD/bulk operations, and an external
  file upload that becomes one idempotent `file_share` history message. Socket
  Mode now consumes a real posted message projected after installed-bot
  visibility checks rather than a hand-authored callback fixture;
- the official SDK qualification fixture records the exact Web API method paths
  emitted by the pinned Node, Python, and Java clients, while the Deno runtime
  suite records its separately verified completion requests. The fail-closed
  comparison currently observes all 244 methods claimed at `sdk-compatible` or
  above (234 current and ten retained legacy methods);
- all 104 stable journey IDs have an individually checked source-map row. The
  live official-source gate currently makes 136 representative assertions
  explicitly citing 51 of those IDs across authentication, navigation,
  conversations, messaging, search, files, apps, OAuth, presence, huddles,
  canvases, lists, workflows, administration, Slack Connect, accessibility,
  Activity, and reminders before local evidence runs. The remaining 51 IDs are
  printed as upstream-text evidence gaps rather than inheriting coverage;
- 40 Playwright scenarios cite 80 of the normative catalog's 104 stable journey
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

### Closed by this change

| Journey | What now exists |
| --- | --- |
| Retention (ADMIN-05) | Workspace and per-conversation retention with a real sweep. Deletion is permanent and cascades through everything referencing a message; a thread is retained until its newest reply expires; files expire on their own date and only when no other conversation still shares them. `admin.conversations.{get,set,remove}CustomRetention` are behavior-compatible. Canvases and lists remain a bounded gap. |
| Message chrome | Copy link and a resolvable `/archives` permalink, forward with a destination picker, an edited marker from a durable fact, thread reply summaries read in one batched call, date separators and the unread divider, mark-unread-from-here, broadcast-to-channel, and system join/topic/name notices committed with the change they describe. |
| Files | The uploader's delete control, a remote-file surface that never claims to host the bytes, and `file.unshared` produced when deleting the last message that shared a file — which also retracts the share the file's visibility is derived from. |
| Invitations (AUTH-05) | Recording, issuing and accepting are three distinct transitions. Acceptance creates the user, the membership at the recorded guest tier and every channel join in one transaction, matched against a provider-verified address, and `GET /app/invite/{id}` answers every terminal state distinctly to a signed-out visitor. |
| Administration (ADMIN-02, ADMIN-03) | Invitation and app-request queues, workspace policy settings with a durable backend, analytics counted from the durable rows, and an audit view whose export comes from the same query as the page. Browser sign-ins now reach the access log at all. |
| Huddles (HUDDLE-01, HUDDLE-03, HUDDLE-04) | The whole lifecycle, minus media, which is named rather than faked. |
| Workspace switcher (AUTH-02) | A switch that mints an isolated session for the target workspace, and event streams that follow the credential that opened them rather than a construction-time workspace. |
| Slack Connect (CONNECT-01..03) | The invitation lifecycle the post-connection half was missing, with the 250-organization capacity claimed transactionally. |

### Core Slack journeys still absent or incomplete

| Priority | Journey | Concrete gap |
| --- | --- | --- |
| P0 | Search depth | Messages and hosted files now use typed modifier plans, visibility-safe totals/pagination, persistent Unicode-folded file search, private durable recent history, visibility-aware people/channel/file typeahead, generated gRPC parity, user-token Web API methods, pinned Node/Python/Java calls, and real Messages/Files/People/Channels UI tabs with `Command/Control+F`. Remaining depth is Canvases, semantic ranking/highlighting, every modifier variant, full People/Channels pagination, exact thread-entry scope and hit marking, visual baselines, and live-Slack differential outcomes. |
| P0 | Activity depth | Activity now uses durable per-recipient records for DMs/MPIMs, mentions, authored-thread and followed-thread replies, all-new-post channel preferences, reactions, applicable app messages, delivered reminders, and public/private channel-addition invitations, with shared memory/SQL/gRPC pagination, typed filters, dense/detailed layout, read/clear/restore, source authorization, bulk actions, keyboard triage, inline reactions, and focus/selection-preserving live refresh. Remaining depth is Slack Connect/canvas-share invitations, VIP/section notifications, custom views, and pre-v107 history backfill. |
| P0 | Composer depth | Formatting, member/user-group/channel autocomplete, a searchable standard/custom emoji picker backed by Slack's pinned catalog, durable recent/category/skin-tone choices, custom alias rendering, selected/pasted/dropped multi-file staging, permission-aware five-minute audio/video recording, atomic attachment-plus-text send, private durable conversation/thread attachment drafts, reload/restart recovery, Drafts & sent counts, sidebar indicators, browser-time-zone scheduled send with durable file-only/text-plus-file delivery, and a full searchable built-in/installed-app shortcuts browser work. Scheduled files retain private ownership beyond upload-ticket/draft expiry and promote through one idempotent file-share/message mutation in every storage and service composition. User-group selection uses Slack's subteam transport syntax and drives visibility-safe Activity. Slack video source/device controls, thumbnails, transcripts, exact emoji ranking, attachment reordering/image descriptions, and Slack's 1 GB per-file allowance versus the explicit 100 MiB deployment request limit remain. |
| P1 | Saved and scheduled work | Scheduled-message APIs enforce exact-token ownership, ranges, threads, time/quota limits, durable failure state, and multi-workspace worker execution. The client can schedule from channel and thread composers; list pending, failed, and sent work in Drafts & sent; edit/reschedule; send now; and cancel without posting early. Current Later has private save/unsave state and In progress/Archived/Completed organization, separate from deprecated app-facing stars. First-party reminders have a separate durable model, message-action presets/custom time, Later CRUD/filtering, named-weekday `/remind`, private channel-reminder listing, guest enforcement, worker recurrence/retry/failure fencing, Activity/source projection, and wake publication. Broader natural-language reminder parsing, month-end recurrence, deterministic deployed-worker browser delivery, and live-Slack differential outcomes remain. The five deprecated app-facing `reminders.*` methods remain only SDK-compatible and are not evidence for the first-party Later model. |
| P1 | Direct-message lifecycle | Dedicated DMs navigation/search, one-to-one/group creation, naming, Slack's nine-person limit, durable per-member close, exact API no-op fields, canonical reopen, reviewed add-people with no/all-history choices and notices, and in-place private-channel conversion now work across memory/SQL/gRPC/browser paths. Current official SDKs also prove canonical exact-member group opening. Slack's help page does not enumerate its complete history-option list, so that exact live option inventory, Slack Connect/external conversion policy, and workspace-configurable restrictions remain differential gaps rather than inferred compatibility. |
| P1 | Notifications and presence | Desktop notifications are delivered under all three permissions they need — the stored preference, the browser's own grant, and Do Not Disturb not being active — with the page reporting which one is missing rather than one silent "off"; the text comes from an authorized fetch, never from the content-free event frame. Push, e-mail, sounds and schedules are absent and named as absent on the page. Workspace and per-conversation notification preferences plus snooze/DND controls exist in the client and API. Status expiry and up-to-five future statuses with edit/cancel and atomic worker activation are durable and restart-safe; manual `auto`/`away` selection and truthful member affordances exist. Live activity-derived automatic presence and Slack's ten-minute idle transition, full status projection outside People/profile, workspace emoji validation, typing indicators, and deeper Activity/VIP/invitation policy remain. |
| P1 | Calls and huddles | The huddle lifecycle is first-class and durable: one active huddle per conversation with concurrent starts converging through an atomic upsert, single-participant join and leave, the last participant to leave ending it in the same transaction, and ending-for-everyone reserved to the starter or an administrator. Conversation membership is the authority, checked independently of posting permission. **There is no media transport of any kind** — no WebRTC, audio, video or screen sharing — and the surface says so wherever it offers a control. HUDDLE-02's media and CALL-01's rendering of app-created call objects remain. |
| P1 | App administration | Manifest JSON editing and OAuth installation are real. A developer-owned hosted app can browse its manifest-declared datastore schema and durable items, execute Slack-compatible expression/cursor queries, count matches, replace/update JSON items, and delete them through the same local/gRPC/storage boundary as `apps.datastore.*`; ownership, installation, hosted-runtime, schema, and query failures remain explicit. App event administration now projects the real durable HTTP/Socket transport cursor—configuration, install state, acknowledged/in-flight sequence, queued evaluation, and retry time/count/reason—without exposing payloads or inventing history. Install-time incoming-webhook selection, retained delivery-attempt history/metrics, scope explanation, token inventory/revocation, distribution, and external-auth providers remain absent. |
| P2 | Canvases and lists | Canvases and lists are first-class persisted workspace surfaces: access-filtered directories, read/write-grant-aware detail controls, atomic canvas title/content revision, list creation, item creation and complete/restore, all through memory/SQL and the generated gRPC seam. A canvas is now rendered and edited **section by section**; it used to be flattened into joined text and marked read-only whenever it had more than one section, so a document an app had built through `canvases.edit` could be read here and never edited. Sections this client has no editor for keep their content and say so rather than offering a control that would flatten them. Canvas comments and revision history, sharing management, and list schemas/views/assignments/comments/files remain absent. |
| — | Workflows (this row was wrong) | The audit claimed "workflow creation/execution remain incomplete" for four releases after both were built. Creation is the Workflow Builder (PRs #123–#127: steps, branches, variables, interactive steps, schedules, copy/delete, activity, run visibility). Execution is `service.advanceStep` with function, form and button step kinds, conditional branching resolved against completed step outputs, and `scheduler.WorkflowScheduleWorker` starting scheduled runs. Three built-in steps now exist — send a message, add people to a channel, create a canvas — which makes a workflow that needs no app and no person expressible. Waiting for a set time now exists too, suspending a run on a durable instant that a worker resumes, so the wait survives a restart rather than starting again. What remains of Slack's catalogue is waiting until a named date, and the connector steps that call third-party services. Like the method count before it, this row was hand-maintained and went stale silently. |
| P1 | Typing indicators | Slack shows who is composing in a conversation. Nothing here produces or renders one, and the reason it is not simply built is a design decision rather than effort: a typing signal must not be journalled — the outbox is durable, replayable and delivered to apps, and none of that is true of "someone is typing" — so it needs a non-durable broadcast the product does not have. In a single replica that is a map; across the replicas the Terraform module deploys it is a bus. Choosing which is the work. |
| P1 | Media transport | Huddles have no WebRTC, audio, video or screen sharing of any kind. The huddle lifecycle is durable and first-class and every surface that offers a control says the media is absent rather than faking it. HUDDLE-02 cannot be closed without building the transport, and a faithful one is Slack's: clients meet at a media server, not each other, so this needs an SFU in the deployment and the dependency admission that implies — not a peer-to-peer shortcut, which would be a deviation dressed as a simplification. **CALL-01 is no longer part of this**: Slack's `calls.*` are for third-party call providers and Slack supplies no media for them either, so rendering the call block is the whole journey and it is now implemented. |

| — | Message-chrome accessibility (fixed) | The action toolbar was absolutely positioned at `top:-15px`, permanently visible, so it overlaid the message above and axe reported its links as `partiallyObscured` under WCAG 2.2 AA target-size. Two earlier attempts failed and are worth recording: adding top padding to the message destabilised the timeline enough that three journeys could no longer click an action, and making the toolbar hover-only removed it from the accessibility tree entirely, which is worse than the violation. The fix keeps the toolbar always present and always in the tree, moves it inside its own message, and clears it horizontally so no message grows and nothing reflows. The keyboard-shortcuts journey now runs an **unscoped** accessibility scan over a populated timeline. |
| P1 | `[ACT-02 ACT-03]` is intermittently red | The reactions-and-pins journey fails perhaps one full run in four, in WebKit and occasionally Firefox, asserting that a pin or reaction did not clear. It passes in isolation every time and passes on a re-run of the same suite, so it is a race and not a broken feature. What is known: the timeline replaces itself when a live event arrives, so a control can be swapped between resolving it and dispatching the click, and the client's guard against that was focus-based — which does not cover WebKit, where clicking a button does not focus it. PR #145 added a press-scoped hold on the refresh, which covers a person whose pointerdown precedes their click but cannot cover a synthetic click, and added `toPass` retries to the journey. Both reduced the failure rate without eliminating it. What has not been tried is making the mutation's own response authoritative for the region rather than re-fetching it, which would remove the second replacement entirely. Until then a red run here should be re-run before it is believed. |
| P2 | Browser-citation ceiling | Ten journeys have no browser citation, and each reason is structural rather than an unwritten test. ADMIN-01..03 and ADMIN-05 are now covered by a fourth Playwright project against a `-session-admin` server. Four cannot take a citation as the harness is built, and the reason is structural rather than unwritten tests. APP-04 and APP-06 are observable only at an app endpoint the browser harness does not host, and adding one to `cmd/server` would put test-only surface in the production binary. REMIND-04 needs `cmd/worker` beside a server sharing a durable store; the browser servers use `-store memory`, so a worker process would share nothing. REMIND-API-01 restricts itself to SDK evidence. Each is recorded in its own journey's Evidence section. The last six — AUTH-02, AUTH-05, CONNECT-02, CALL-01, HUDDLE-02, HUDDLE-04 — need an identity provider or media, both recorded separately above. |
| P2 | Client breadth | The semantic journeys now run in Chromium, Firefox, and WebKit, with automated WCAG checks for representative desktop, dialog, and narrow states. Performance budgets, screenshot differential, manual assistive-technology coverage, and live-Slack comparisons remain unqualified. |

## Web API and app-platform gaps

The 62 unimplemented current Web API methods are, as reported by
`make compatibility-report` — which is the authority, not this table:

| Namespace | Missing | Boundary |
| --- | ---: | --- |
| `admin.*` | 47 | Enterprise analytics, policies, roles, bulk conversation operations, org sessions, and org-wide app/function/workflow administration |
| `apps.*` | 5 | External-auth get/delete, app activity history, app icons, and user connection state |
| `assistant.*` | 5 | Assistant search context and thread title/status/suggested-prompt presentation |
| `team.*` | 3 | Billing and external-team administration |
| `functions.*` | 1 | Workflow-step response export |
| `users.*` | 1 | Discoverable contacts |

This table said 74 across seven namespaces, including nine `conversations.*`,
for two releases after those nine were implemented. It is generated from
nothing, so it goes stale silently; the report command above is the number to
trust.

App-platform work must remain dependency-ordered:

1. finish retained authorization and attempt history on top of the bot/user
   HTTP/Socket subscription, immutable message create/change/delete, and file
   create/share projections now in place. `file_unshared` production is done:
   deleting the last live message carrying a file retracts the file's share
   into that conversation and journals the event, in one transaction;
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
only 41 of 234 current claims at `sdk-compatible` or above presently carry
method-level evidence in the ledger; the remaining 193 must not inherit that
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
