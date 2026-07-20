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
- dqlite as the production replicated SQLite implementation;
- stateless application processes that scale from zero;
- application hibernation in which the database is snapshotted and stopped;
- a minimal always-reachable activator that wakes the stack on demand;
- dependency admission that selects the newest eligible stable release only
  after a mandatory 24-hour publication quarantine;
- self-hosted deployment on ordinary Linux VMs in any cloud; and
- managed-container deployment on AWS ECS/Fargate, Google Cloud Run, and Azure
  Container Apps, subject to the persistence qualification rules.

The compatibility target is a pinned, reproducible contract. The archived
Slack specifications alone do not describe all Slack behavior,
so every inferred or observed behavior must retain its provenance.

## Governing specifications

- [Product specification](specs/product.md)
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
back-channel receiver revoked every correlated local session when the provider
initiated logout.

Remote chat services preserved canonical store and context errors across the
gRPC boundary, so first-login identity provisioning behaved the same in local
and split-process deployments. Concurrent first login was qualified through a
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
  VMs, AWS ECS/Fargate, Google Cloud Run plus companion database compute, and
  Azure Container Apps plus any required companion database compute.
- Publish deployment templates and qualification tests for each supported
  profile.

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
- dependency age, integrity, provenance, license, and vulnerability checks;
- migration forward and restore compatibility checks; and
- generated compatibility-ledger validation.

The dependency-admission gate verified exact direct npm lockfile versions and
Subresource Integrity checksums against the same aged evidence inventory used
for Go modules, GitHub Actions, and container inputs.

The container publication gate emitted immutable 12-character commit tags,
direct Linux amd64 and Linux arm64 image manifests, and a generic index made
from exactly those two manifests. It generated an SPDX SBOM from the exact
architecture image, attached signed provenance and SBOM attestations to the
architecture digest without changing the direct tag's media type, and read the
published references back from GitHub Container Registry. It retained at most
the newest 20 complete release groups and removed all other package versions.
BuildKit identified each provenance predicate with the originating GitHub
Actions run URL, and the release gate rejected provenance whose builder
identity or required SLSA v1 fields were absent before requesting a signature.

## Initial milestone

The first demonstrable milestone is a cold system receiving a request through
the activator, restoring its database, starting the Go application, allowing a
user to authenticate and post a threaded message through HTMX, and exposing the
same state through compatible Slack API calls from at least the Node, Python,
and Java official SDKs.
