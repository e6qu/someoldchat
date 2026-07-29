# SameOldChat project status and planned work

This document records current implementation status and work that remains
before a deployment profile or compatibility claim can be treated as
qualified. It is not a description of behavior that the repository does not
implement yet.

## Objective

Build SameOldChat, a multi-workspace chat application with:

- a Go backend implementing the Slack platform contracts described by pinned
  published OpenAPI/AsyncAPI specifications and official open-source SDKs;
- a server-rendered HTMX web application;
- SQLite for local and small deployments;
- PostgreSQL as the qualified external durable SQL profile, gated in CI against
  a pinned PostgreSQL 18.1 service;
- dqlite as the production replicated SQLite implementation;
- stateless application processes that scale from zero;
- application hibernation in which the database is snapshotted and stopped;
- a minimal always-reachable activator that wakes the stack on demand;
- dependency admission that selects the newest eligible stable release only
  after a mandatory 24-hour publication quarantine;
- self-hosted deployment on ordinary Linux VMs in any cloud; and
- managed-container deployment on Amazon Elastic Container Service (ECS) on
  AWS Fargate, Google Cloud Run, and Azure Container Apps, subject to the
  persistence qualification rules.

The compatibility target is a pinned, reproducible contract. The archived
Slack specifications alone do not describe all Slack behavior,
so every inferred or observed behavior must retain its provenance.

## Governing specifications

- [Product specification](specs/product.md)
- [Slack user-journey contract](specs/journeys/README.md)
- [API compatibility specification](specs/api-compatibility.md)
- [Persistence specification](specs/persistence.md)
- [Scale-to-zero specification](specs/scale-to-zero.md)
- [Dependency policy](specs/dependency-policy.md)
- [Hosting specification](specs/hosting.md)
- [Architecture](docs/architecture.md)
- [Operations](docs/operations.md)
- [Deployment guide](docs/deployment.md)

## Delivery principles

1. Contract behavior is generated or tested from pinned sources; it is not
   reconstructed from memory.
2. The web, API, worker, activator, and persistence concerns are separate.
3. No application process owns irreplaceable state.
4. Domain changes and their emitted events commit atomically.
5. SQLite and dqlite run the same schema and portable query suite.
6. Hibernation is a state machine with fencing, verification, and recovery;
   it is never a blind process shutdown.
7. Security checks are release gates, including for tools and test-only code.

## Phases

### Phase 0: Repository and contract foundation

- Establish the Go module, build commands, CI, and document checks.
- Vendor the Slack OpenAPI 2.0 and Events AsyncAPI sources at exact commits.
- Pin exact releases of official Node, Python, Java, and Deno Slack SDKs.
- Pin applicable Bolt SDK releases for event, OAuth, interactivity, and Socket
  Mode behavior.
- Record source URL, revision, checksum, license, and retrieval timestamp.
- Build a normalized operation and schema catalog.
- Create `specs/compatibility.yaml` as the machine-readable compatibility
  ledger.
- Add CI checks for source drift and generated-file drift.

Exit criteria:

- A clean checkout reproduces the same catalog without unpinned network input.
- Every source conflict is visible and no upstream schema is silently patched.

### Phase 1: Portable persistence and platform kernel

- Define application-facing transaction and query interfaces.
- Implement shared SQL repositories over `database/sql`.
- Implement the SQLite lifecycle adapter.
- Implement the dqlite lifecycle adapter and a three-node integration fixture.
- Add identical schema, migration, and repository tests for both adapters.
- Implement workspaces, users, memberships, roles, sessions, tokens, scopes,
  conversations, and Slack-style public identifiers.
- Implement a transactional outbox and durable idempotency records.

Exit criteria:

- Both backends pass the same functional suite.
- A dqlite leader failure produces either one committed command or no committed
  command, never a duplicate or partial command.

PostgreSQL schema migration acquired a database-scoped transaction advisory
lock before touching the catalog, so concurrent application replicas started
against a fresh database without racing on schema creation. Its qualification
used isolated durable identifiers and passed repeatedly against both fresh and
already-populated databases.

### Phase 2: Core Slack API vertical slice

Implement the first usable slice:

- authentication and scope enforcement;
- `auth.*`;
- core `users.*` and `users.profile.*`;
- core `conversations.*`;
- `chat.postMessage`, `chat.update`, and `chat.delete`;
- threads, reactions, pins, and read cursors;
- Slack-compatible query, form, JSON, and error decoding; and
- cursor pagination.

Run the slice through all applicable pinned official SDKs using a configurable
API base URL or an SDK-specific test proxy.

Exit criteria:

- An SDK can authenticate, create/join a conversation, post/update/delete a
  message, reply in a thread, paginate results, and decode errors.

### Phase 3: HTMX application and real-time delivery

- Build the workspace shell, channel/DM sidebar, timeline, composer, thread
  pane, member/profile views, reactions, unread state, and dialogs.
- Use full-page server rendering for entry points and HTMX fragments for
  mutations and incremental navigation.
- Use SSE for live delivery with durable event IDs and replay.
- Add minimal JavaScript only for focus, keyboard, composer, and reconnect
  behavior that HTMX cannot provide.
- Add accessibility, browser, and screenshot regression tests.

The web identity flow bound each authorization response to a per-request nonce
and persisted its verified OIDC issuer, subject, session ID, ID-token metadata,
and provider-bounded expiry with each durable application session. RP-initiated
logout revoked the local session before redirecting through the provider's
discovered end-session endpoint with an ID-token hint, client ID, and an exact
return to SameOldChat's terminal signed-out page. Provider logout failures left
the application signed out and were reported on that page. The signed
back-channel receiver accepted the standard `sid` or `sub` correlation forms,
rejected non-canonical token delivery and replayed `jti` values, and revoked
only the correlated provider sessions. Logout-token replay state remained
durable until expiry through the same SQLite/PostgreSQL store and generated
gRPC boundary used by distributed composition.

Shauth qualification used the provider's exact pinned browser contract against
real PostgreSQL, Ory Hydra, and two SameOldChat relying parties with distinct
databases and origins. It covered direct and catalog entry, silent SSO,
application-initiated and provider-initiated global logout, witness-session
revocation, the one-time logout completion bridge, fail-closed anonymous
access, verified identity and role display, immutable release identity, and
validator credential isolation.

Remote chat services preserved canonical store and context errors across the
gRPC boundary, so first-login identity provisioning behaved the same in local
and distributed composition. Concurrent first login was qualified through a
real gRPC server and PostgreSQL repository and converged on one durable user and
external identity.

Exit criteria:

- A user can complete the core chat workflow after a cold wake.
- Terminating a web replica during live delivery loses no committed event.

### Phase 4: Application hibernation and activation

- Implement the always-on activator as a separately deployable small service.
- Implement the lifecycle state machine and fencing epochs.
- Quiesce ingress, drain writes and required outbox work, checkpoint state,
  create a snapshot, verify it, publish a manifest, and stop all app, worker,
  and database nodes.
- On a request, elect one wake attempt, restore and validate the database,
  start dqlite, run a migration job if needed, start workers/web, and forward
  the triggering request.
- Preserve scheduled wake deadlines outside the hibernated database.
- Add bounded request buffering and explicit overload behavior.
- Implement the provider-neutral lifecycle driver and hosting drivers for Linux
  VMs, Amazon ECS on AWS Fargate, Google Cloud Run plus companion database
  compute, and
  Azure Container Apps plus any required companion database compute.
- Publish deployment templates and qualification tests for each supported
  profile.

SQLite activator request spool leases used parsed timestamps for expiry,
renewal, and deletion decisions, so fractional RFC 3339 timestamps could not
be misordered lexicographically. Deterministic clock-driven race tests covered
exclusive leases, renewal during slow delivery, expired-owner rejection, and
crash recovery without depending on scheduler timing.

The dqlite qualification gate ran package suites serially because its real
clusters bind ephemeral loopback ports; this prevented independent package
processes from racing between port discovery and dqlite listener creation.

Exit criteria:

- Only the activator, durable object storage, and control-plane facilities
  remain active while hibernated.
- Repeated and concurrent wake requests cause one restoration.
- A failed or corrupt snapshot never replaces the last known-good snapshot.

### Phase 5: Remaining published Slack surface

Implement methods in domain waves:

1. Files, remote files, search, stars, reminders, and bookmarks.
2. User groups, DND, presence, team information, and scheduled messages.
3. OAuth, app installations, webhooks, slash commands, and interactivity.
4. Views, dialogs, Block Kit models, and event subscriptions.
5. Calls, admin/enterprise families, legacy aliases, and deprecated methods
   represented by the pinned contract.
6. Socket Mode and other SDK-exposed protocols selected in the compatibility
   ledger.

An operation is complete only after input, authorization, success, warning,
error, pagination, SDK, SQLite, and dqlite tests pass as applicable.

In progress. The 2026-07-29 post-merge audit reconciled the ledger with Slack's
current method catalog rather than treating every historical ledger entry as a
current method:

- Slack's current reference contains 310 methods. SameOldChat registers 215 of
  them and leaves 95 unimplemented.
- The 320-entry ledger also retains ten legacy methods for clients that still
  call them. Those methods are useful compatibility inventory, but they are not
  part of the current Slack denominator.
- After correcting the overstated `chat.postMessage` and `files.upload`
  claims, the ledger records 210 current methods as `behavior-compatible`,
  three as `sdk-compatible`, two as `schema-compatible`, and none as
  `verified-against-slack`. A passing route, local test, or SDK parse is not
  itself live Slack equivalence.
- Several recorded evidence levels are too high. The ratchet must permit an
  explicit, reviewed downgrade backed by a concrete deviation; otherwise a
  false compatibility claim becomes permanent.

The remaining 95 current methods break down as 50 `admin.*`, seven `apps.*`,
five `assistant.*`, one `auth.*`, ten `conversations.*`, six `functions.*`, one
`rtm.*`, two `search.*`, four `team.*`, one `users.*`, and eight
`workflows.*`.

Before another breadth wave, complete the following evidence-backed work in
order:

1. maintain the normative Slack-matching journey catalog under
   `specs/journeys/`, map every browser/API/SDK/differential test to stable
   journey IDs, and keep implementation coverage in the separate gap audit;
2. report current and retained-legacy coverage separately, attach executable
   method-level evidence to compatibility claims, allow audited corrections,
   and repair stale SDK/event-platform documentation;
3. make scheduled messages token-isolated and app-attributed, implement Slack's
   time-window, quota, pagination, thread, and error contracts, run scheduling
   in every worker delivery mode across all workspaces, persist terminal
   failures, and deploy a real worker process;
4. correct Slack keyboard mappings and slash-command semantics, including
   `response_url` authorization, `should_escape`, command discovery, built-in
   commands, and realistic human/bot qualification identities;
5. add scheduled-send, reminder execution, Drafts & sent, Later, and full
   Activity journeys to the first-party client;
6. audit every claimed Web API method's current arguments, authorization,
   success and error schemas, pagination, rate limits, and official SDK
   behavior, beginning with `chat.postMessage` and file uploads;
7. qualify Chromium, Firefox, and WebKit with automated accessibility and visual
   comparison, then add opt-in differential runs against a dedicated Slack
   developer workspace.

Current sweep status (2026-07-29): the normative catalog and stable-ID mapping
for all 27 first-party browser journeys are in place; Slack web keyboard and
slash-command discovery/escaping semantics are corrected; and the journeys now
qualify in Chromium, Firefox, and WebKit with representative automated WCAG
checks. Visual baselines, manual assistive-technology evidence, complete
API/SDK-to-journey mapping, and live-Slack differential runs remain explicit
work rather than inferred compatibility. Scheduled send now preserves
channel/thread context and the browser's local time zone, stays out of history
until delivery, and has a real pending
list and cancellation journey. Current Later is now a private first-party
saved-item model—not an alias over deprecated `stars.*`—with save/unsave,
focused-message `A`, In progress, Archived, Completed, restore/removal,
inaccessible-source redaction, live reconciliation, portable persistence, and
local/distributed composition parity. Slack exposes no current Later Web API,
so official SDK qualification remains evidence for the deliberately separate
legacy `stars.*` and `reminders.*` contracts rather than being mislabeled as
Later evidence. Drafts/Sent aggregation, scheduled edit, reschedule/send-now
and failure history, reminder dates/delivery, and Later reminder filtering
remain the next stateful client slice.

The reminder contract audit also corrected a false evidence claim. Current
Slack Help puts personal reminder creation and management in Later and message
actions; current `/remind` documentation covers channel reminders and a private
channel-reminder list, with channel edits performed by delete-and-recreate.
Slack's five deprecated `reminders.*` app methods remain a separate legacy
surface. They are now recorded as `sdk-compatible`, not
`behavior-compatible`, until natural-language parsing, recurrence, targeting,
delivery, retirement behavior, and controlled live outcomes have their own
evidence.

UI evidence is now measured against the normative catalog rather than counted
from test files: 28 Playwright scenarios cite 51 of 101 stable journey IDs.
`make journey-check` rejects duplicate/unknown IDs and any journey file that
loses its observation date or official Slack source. The remaining IDs are
printed as an explicit browser gap list; a citation is not promoted
to full compatibility without the domain, transport, accessibility, visual,
and differential layers required by the catalog.

SDK evidence is now measured at the HTTP boundary too. The pinned official
Node, Python, and Java clients record every `/api/{method}` path they actually
request, and the Deno runtime records its two verified function-completion
requests at its own receiver. `make sdk-qualification` fails if any operation
claimed at `sdk-compatible` or above is absent. The current result is 223 of
223 claimed methods observed (213 current plus ten retained legacy methods);
this proves SDK serialization/decoding only, not live Slack equivalence.

The reminder/Later contract is also checked upstream on every SDK CI run.
`make external-contract-qualification` fetches the current official Slack Help
and developer pages and fails if their text no longer supports the journey's
entry points, organization, privacy, local-time default, recurrence, guest,
editability, retirement, or Later-versus-app-API separation. This detects
documentation drift; controlled live-workspace behavior and visual comparison
remain distinct evidence layers.

The first-party reminder slice now has a durable model separate from deprecated
`reminders.*`, message `M` presets/custom time, Later CRUD and filtering,
reserved `/remind` parsing including named weekdays, private channel-reminder
listing, guest enforcement, worker delivery/recurrence/retry/failure fencing,
Activity/source projection, durable Later/Activity badge acknowledgement, and
combined scheduled/reminder lifecycle wake publication. The real browser
journey runs in all three engines; deterministic memory, SQL, service, web, and
local-versus-gRPC evidence covers delivery state that cannot safely wait for a
wall clock in CI. Live-workspace parsing/presentation comparison,
deterministic deployed-worker browser delivery, and undocumented month-end
recurrence remain explicit gaps.

The same source refresh exposed that the 2026 Activity journey is much broader
than the current client. SameOldChat currently projects unread conversations,
mentions, and reminders, but does not yet implement the durable notification
classes, layouts, filters/custom views, clear/restore, per-item actions, or
Activity-local keyboard triage required by `ACTIVITY-01` through `ACTIVITY-03`.
Those omissions remain named gaps rather than being hidden behind a passing
reminder test.

Phase 5 exits only when each method counted as complete names its current
official sources, executable evidence, known deviations, and live-comparison
state. An aggregate green suite is supporting evidence, not a substitute for
that per-method record.

### Phase 6: Differential verification and production hardening

- Run controlled differential requests against a disposable Slack developer
  workspace and normalize volatile fields before comparison.
- Fuzz request decoding, cursor handling, event envelopes, and restore manifests.
- Load-test hot channels, reconnect storms, file upload, search, and cold wake.
- Exercise node loss, quorum loss, failed snapshot upload, corrupt snapshot,
  interrupted restoration, and rollback.
- Produce an SBOM, signed artifacts, compatibility report, and operational
  recovery guide for each release.

## Cross-cutting release gates

Every change must pass:

- formatting, linting, unit, integration, race, and browser tests;
- the relevant official Slack SDK suites;
- SQLite and dqlite persistence suites;
- hibernation/wake tests when lifecycle code or schema changes;
- dependency age, integrity, provenance, and license checks
  (`make dependency-check`);
- vulnerability scanning. `govulncheck` runs in the pull-request workflow over
  the module source; OSV/advisory and container-image scanning required by the
  [dependency policy](specs/dependency-policy.md) are not yet wired into CI;
- migration forward and restore compatibility checks; and
- generated compatibility-ledger validation.

Fuzz smoke gates requested a fixed 25,000-execution budget per target under an
explicit two-minute process timeout, so successful completion did not depend
on a wall-clock fuzz deadline while hung inputs still failed the gate.

Dqlite qualification clusters retained their kernel-assigned TCP listeners for
the complete test lifetime and passed those real connections through
Canonical's external-connection interface. The adapter used the same dialer
for cluster health probes. Accepted upgrades remained queued until each
Canonical dqlite application had constructed its local engine, and restarted
applications received a distinct transport session on the same retained
listener. Stores deactivated and drained their external accept loop before
closing the local engine; the drain barrier completed Canonical's real dqlite
wire handshake after all routed connections. Peer dials therefore could not
reach an obsolete session during node loss or restart. Cluster creation and
restart tests had neither a released-port bind window nor an external-accept
lifecycle race and required neither retries nor sleeps.

The dependency-admission gate verified exact direct npm lockfile versions and
Subresource Integrity checksums against the same aged evidence inventory used
for Go modules, GitHub Actions, and container inputs.

The official SDK qualification script cleared `CDPATH` with a portable empty
assignment, so its repository-root discovery passed ShellCheck on Linux and
macOS shells without inheriting caller-specific directory search behavior.

The container publication gate emitted immutable 12-character commit tags,
direct Linux amd64 and Linux arm64 image manifests, and a generic index made
from exactly those two manifests. It generated an SPDX SBOM from the exact
architecture image, attached GitHub's native signed SLSA provenance and a
signed SBOM attestation to the architecture digest without changing the direct
tag's media type, and read the published references back from GitHub Container
Registry. It retained at most
the newest 20 complete three-version release groups and removed incomplete,
mixed-tag, untagged, and older package versions. Every remaining root was
verified to have exactly one direct amd64 and one direct arm64 sibling, while
signed attestation records remained outside the container package versions.
The release gate used the official GitHub attestation action's native SLSA
generator instead of submitting BuildKit extension fields to GitHub's stricter
SLSA decoder, and it rejected malformed BuildKit SPDX documents before
requesting an SBOM signature.

## Initial milestone

The first demonstrable milestone is a cold system receiving a request through
the activator, restoring its database, starting the Go application, allowing a
user to authenticate and post a threaded message through HTMX, and exposing the
same state through compatible Slack API calls from at least the Node, Python,
and Java official SDKs.
