# SameOldChat architecture

## Context

The system presents two application adapters over one domain:

- a Slack-compatible HTTP and event surface for external clients; and
- an HTML/HTMX product interface for people.

Both adapters invoke application services directly. The HTMX interface does
not call the Slack-compatible interface over HTTP.

## Implementation principles

The engineering rules are in [`AGENTS.md`](../AGENTS.md). Two of them shape
this document: the program fails fast and loudly (missing, invalid, or
contradictory configuration is an error, never a silent default), and where
several implementations are supported the caller selects one explicitly
(`memory`, `sqlite`, `postgresql`, `dqlite`). Those are operating modes, not
fallbacks, and an unavailable selected mode fails at startup.

## Crash-only operation

SameOldChat follows the crash-only design described by the
[Crash-Only Software](https://lwn.net/Articles/191059/) principle. A process,
worker, database node, or active deployment may be terminated abruptly at any
point; recovery uses the same validated startup path as ordinary activation. There is no correctness-critical graceful
shutdown protocol and no user action required to repair a crashed component.

This requires that:

- every committed mutation is transactionally durable before acknowledgement;
- domain events and outbox records are committed with their mutation;
- leases, cursors, sessions, idempotency records, and lifecycle generations are
  durable or safely reconstructible;
- recovery is idempotent and replays unfinished work from durable state;
- stale processes are fenced before they can write after recovery;
- partial snapshots are rejected and the last verified snapshot remains usable;
- replicas can be replaced while the service remains available through the
  remaining replicas; and
- crash/restart tests run during normal verification, including process,
  database-node, quorum, network, and snapshot interruption faults.

The activator and service drivers may use bounded transparent retries where the
operation is idempotent or durably keyed. That is recovery behavior, not a
fallback to a different implementation. A handled request or dependency error
must not be reported as HTTP 500; 500 is reserved for an unhandled exception.
If the pinned Slack contract intentionally specifies a 500 response for a
particular condition, that contract is authoritative and the exception must be
recorded in the compatibility ledger.

Distributed module links use mutual TLS. A module server requires a configured
client CA and a caller presents a client certificate; incomplete TLS material
fails startup rather than opening an unauthenticated internal port. The client
and server authorities must differ; see
[separable module architecture](modules.md).

## Stateless replicas and state stores

Application replicas are stateless workers over authoritative state stores.
They may hold request-local data and disposable caches, but correctness MUST
NOT depend on process memory, replica affinity, local queues, local sessions,
or local event subscriptions. Durable state belongs in the selected SQL store
(SQLite, PostgreSQL, or dqlite), object storage, or the explicitly designated
lifecycle control store. Browser session
validity and revocation are read and written through that durable store; a
cookie is only a bearer reference and never local authority. Workspace
membership and role are also durable records; a user’s workspace identifier
alone is not authorization.

Mutations may carry an explicit idempotency key. The key, committed result, and
outbox event share one transaction; delivery retries use durable leases and
explicit retry times. Long-running deliveries renew their lease while active,
so lease expiry represents a crashed or fenced worker rather than a slow
healthy worker; this is never an alternate implementation path.

Both compositions may run multiple replicas. In local composition
(`-chat-mode local`, the `monolith` targets in `modules.json`) each replica
contains the direct-call composition and all replicas share one qualified
durable store. In distributed composition (`-chat-mode grpc`, the
`separate-chat` targets) module processes have independent replica counts and
communicate over gRPC; each module's replicas share that module's store and
must be replaceable without data migration or user repair. A process that owns
a module may run more than one replica only on PostgreSQL or dqlite (dqlite
needs at least three); `memory` and `sqlite` are single-replica.

Direct and multi-person conversations use durable participant sets and a
unique participant-set key, so concurrent `conversations.open` calls from
different replicas converge on one conversation rather than creating
replica-local duplicates. A participant set of one is the caller's self-DM.
New one-to-one conversations receive Slack's D-prefixed identifiers; group
DMs and channels use the C prefix. Existing one-to-one conversations with a C
identifier keep it, because stored references already name it.

Named channels likewise use a durable workspace-and-name unique index. The
service normalizes the human input once and every storage profile enforces the
same address invariant for creation and rename, so concurrent requests cannot
create two destinations with one channel name. When the index is created on an
existing database, the first holder of a name keeps it, later duplicates are
renamed deterministically, and each rename is recorded in
`schema_migration_notices`.

## Runtime topology

```text
                         durable control metadata
                                   │
Internet ──> activator/ingress ─────┼──────────────┐
                 │                 │              │
                 │ wake/forward    │              │ snapshot manifest
                 ▼                 ▼              ▼
          web/API replicas     worker + blobgc replicas  object storage
              (0..N)              (0..N)        snapshots/blobs
                 │                   │
                 └─────────┬─────────┘
                           ▼
                    persistence port
                      ├─ SQLite 0..1
                      ├─ PostgreSQL (external)
                      └─ dqlite 0 or 3..N
```

The activator is deliberately small and separately deployable. It contains no
chat domain logic and holds no authoritative chat state. Its durable state is
limited to lifecycle generation, stack state, snapshot manifest reference,
next scheduled wake, and bounded activation bookkeeping.

The runnable `cmd/activator` binds that role to the standalone lifecycle SQLite
control store, verified snapshot manager, explicit command driver, and bounded
reverse proxy. It requires its declared configuration and never becomes a no-op
activator when lifecycle commands or snapshot credentials are absent.

The runnable `cmd/worker` is a stateless outbox, scheduled-message, and
reminder replica. It requires an explicit state backend, a unique `-owner`,
and a `-delivery-format`. `record` sends the internal event record for one
`-workspace` to one `-delivery-url`. `slack-events` is the multi-workspace
production mode: it delivers each event to the request URL its installed app's
manifest declares, signed with that app's stored signing secret, so it requires
`-app-credential-key-hex` and `-auth-public-url` and rejects `-workspace`,
`-delivery-url`, `-app-id`, and `-signing-secret`. Both formats use durable
leases and the event ID as their idempotency key. Due
scheduled messages, including normalized Block Kit and attachment payloads, are
claimed with a separate durable lease and posted with the scheduled-message ID
as their idempotency key before the scheduled record is acknowledged. A worker
crash therefore leaves committed events and scheduled records claimable after
lease expiry rather than losing a process-local queue. With
`-wake-deadline-url` and `-wake-deadline-token` the worker also publishes the
earliest scheduled wake to the activator.

The runnable `cmd/socketmode-worker` is a stateless Socket Mode response
replica. It requires `-store` (with that backend's storage settings), `-app-id`,
a unique `-owner`, and `-response-url`. It claims responses through the
process-independent chat boundary and posts each payload to the response URL
with the application identifier, envelope identifier, and idempotency key. A
2xx response acknowledges the durable record. A failed delivery releases it at
the configured retry time, and a process crash leaves the lease available to
another replica after expiry.

Both workers tolerate transient store, claim, or delivery failures up to
`-max-consecutive-failures` (default 20) poll cycles without progress, then
exit so the orchestrator restarts them and an alert fires, rather than hiding a
broken storage path.

The runnable `cmd/blobgc` is a separate stateless blob-cleanup replica. It
claims only the durable `file.blob_delete` topic, uses the same lease/retry
rules, and treats an already-missing object as an idempotent completed delete.
Its `-audit` mode is described in [blob lifecycle](blob-lifecycle.md).

## Go package boundaries

```text
cmd/
  server/         web and Slack-compatible API process
  chatd/          separate chat gRPC process
  worker/         asynchronous work process
  socketmode-worker/  Socket Mode response process
  blobgc/         blob cleanup process
  activator/      wake coordinator and reverse proxy
  ecs-ws-activator/  WebSocket edge for deploy/ecs-scale-zero
  modulegen/, contractcheck/, journeycheck/, sdkcheck/, sdkcoverage/,
  rebaseaudit/    build and qualification tools run by make targets
internal/
  activator/      standalone wake coordinator handler and request spool
  api/slack/      Slack wire decoding and response mapping
  app/localchat/  explicit local storage and blob composition
  web/            page and HTMX fragment handlers
  auth/           browser sessions, bearer tokens, scopes
  bearer/         the shared case-insensitive Authorization: Bearer parser
  domain/         entities, invariants, error vocabulary, and request limits
  service/        transactions and application use cases
  store/          persistence ports
    memory/       single-replica development store
    sqlstore/     shared SQL repositories (SQLite, PostgreSQL, dqlite) and lifecycle state
    postgres/     PostgreSQL driver and dialect translation over sqlstore
    dqlite/       dqlite node and cluster over sqlstore (`dqlite` build tag)
    dqlitetest/   dqlite cluster harness
    storetest/    behavioral checks every repository must pass
  events/         event journal, outbox, webhook delivery
  outbox/         outbox delivery worker
  scheduler/      scheduled-message and reminder workers
  socketmode/     Socket Mode connection, envelope, and response handling
  observability/  bounded Prometheus-compatible aggregates
  realtime/       SSE registration and replay
  blob/           external file objects and storage port
  lifecycle/      state machine, fencing, snapshots
  modules/        stable module APIs and transport implementations
  generated/      generated composition bindings
  ...             leaf helpers (blockkit, slackobject, slackemoji, secretbox,
                  lease, thumbnail, huddlesfu, clientaddr, appmanifest, slackapp)
proto/            gRPC service schemas
specs/            project requirements and pinned contract sources
deploy/           request-triggered activation infrastructure modules
terraform/        durable application infrastructure modules
tests/            application and official SDK qualification tests
docs/             architecture, operations, and deployment guidance
```

Module API packages are the separable seams. `cmd/server` chooses the generated
local or remote chat provider at startup from `-chat-mode`, so today its binary
links both; business packages do not inspect topology or choose a transport.
Choosing the composition at build time instead is planned in
[PLAN.md Phase 7](../PLAN.md#phase-7-compile-time-module-composition). See
[separable module architecture](modules.md).

Imports point inward. Wire adapters (`api/slack`, `web`, `realtime`, `auth`)
depend on module APIs, `domain`, and the store port's sentinels, never on
`service` or a storage backend, so a process that reaches chat over gRPC does
not link the implementation it calls; `internal/modules` asserts this for every
adapter's import graph. Storage adapters implement the `store` ports, and
domain packages know nothing about HTTP, HTMX, SQLite, dqlite, or a particular
deployment platform.

## State model

Authoritative state is restricted to:

- the active SQLite, PostgreSQL, or dqlite database;
- immutable file objects;
- verified database snapshots and manifests; and
- minimal lifecycle metadata used while the database is absent.

Caches and in-process broadcasts are disposable. Sessions, idempotency keys,
read cursors, event offsets, job leases, scheduled work, and call lifecycles
are durable.

## Transaction and event model

A command executes in one database transaction:

1. Validate identity, scope, membership, and command preconditions.
2. Apply the domain mutation.
3. Append domain/API events to the event journal and outbox.
4. Commit.

Workers claim outbox records with expiring leases. Delivery is at least once;
receivers and worker handlers use stable event/idempotency IDs to produce
exactly-once effects where the application controls the destination.

## Real-time model

SSE is the default browser transport. Each event has a durable ordered ID.
Browsers reconnect with `Last-Event-ID`, and a replica reads missed events from
the journal before subscribing to best-effort live notification. A stream opened
with no cursor starts at the journal head (`LatestEventSequence`), as an RTM
ticket does at `rtm.connect`, rather than replaying the reader's history.
Because that head is taken when the stream connects, not when the page was
drawn, every web page that opens `/events` reads the member's head before it
reads the state it renders, carries it as `<body data-event-head>`, and opens
its first connection with `last_event_id` set to it; an event committed between
the render and the connection is therefore delivered rather than lost.
Replica-local fan-out is an optimization only.

Both live streams — SSE and RTM — read the journal only through the per-user
projection (`ListUserEventsAfter`); the unfiltered workspace journal is not
part of the chat service surface or its gRPC boundary. Records about
conversations the reader is not a member of are withheld, and content-bearing
records are hydrated only after membership is proven. `realtime.NewHandler` and `NewRTMHandler` accept
nothing else, so a stream cannot be wired to the raw workspace journal. Each
projected page reports the last sequence it examined, visible or not, and a
stream resumes after that sequence, so withheld records are read once rather
than on every poll.

Journal records without a typed payload cannot be delivered or repaired.
Migration quarantines them once: each is marked `undeliverable`, excluded from
every consumer read, and recorded in `schema_migration_notices`, so new
streams do not re-scan and re-log them. Consumers also skip and log any
undecodable record they meet at runtime.

Long-lived SSE connections are activity and intentionally prevent the web tier
from scaling to zero. Once clients disconnect and the idle policy is satisfied,
web replicas may stop.

## Distributed identifiers and ordering

- Public IDs are generated in Go using type-specific Slack-compatible formats.
- Internal keys may use integers for compact indexing.
- Message timestamps are stored in an exact sortable representation, never as
  floating point.
- Ordering that must be global is allocated within the database transaction.
- The journal (outbox) sequence is allocated in commit order on every profile,
  because every reader resumes with `sequence > cursor` and a record that
  became visible below a cursor would never be delivered. SQLite allocates it
  under its database write lock and dqlite applies one transaction at a time.
  PostgreSQL allocates an identity at insert time, so there a row is inserted
  with a provisional negative sequence and a deferred constraint trigger gives
  it its final sequence at commit, under a transaction-scoped advisory lock
  held until the commit is visible. Event-producing commits on PostgreSQL are
  therefore serialized and forgo group commit (measured on local PostgreSQL 16
  with sixteen append-only writers: roughly 1,600–2,500 instead of
  4,300–6,500 appends a second). Writes that produce no event, and everything
  a transaction does before its commit, are unaffected.
- Every lifecycle and writer lease includes a fencing generation so a process
  from a previous activation cannot write after hibernation begins.

## Route inventory outside `/api`

Every Slack Web API method is `/api/{method}` and is enumerated in
[`specs/compatibility.yaml`](../specs/compatibility.yaml). The table below
lists the other route groups `cmd/server` serves, for operators configuring a
CDN, WAF, or access-log redaction policy. The browser surface under `/app` is
large and changes often, so it is listed by prefix; `internal/web` registers
the individual routes.

| Route | Purpose |
|---|---|
| `GET /healthz`, `GET /readyz` | liveness and end-to-end readiness |
| `GET /monitoring/observation` | bearer-authenticated monitoring document; see [operations](operations.md#health-and-monitoring-endpoints) |
| `GET /{$}` | redirect to `/app` |
| `GET`/`POST /app`, `/app/...` | session-authenticated HTMX pages, fragments, and browser mutations, including `/app/admin/...` |
| `GET /archives/{channel}/{timestamp}` | Slack-style message permalink |
| `GET /favicon.ico`, `GET /favicon.svg` | site icon |
| `GET /login`, `GET /auth/{provider}`, `GET /auth/{provider}/callback`, `POST /logout`, `GET /signed-out`, `GET /me`, `GET /auth/validation` | browser authorization; see [authentication](authentication.md) |
| `POST /auth/oidc/backchannel-logout`, `GET /auth/shauth/logout/complete` | provider-initiated logout |
| `GET`/`POST /oauth/authorize`, `/oauth/v2/authorize` | app installation consent |
| `GET /events` | server-sent event stream |
| `GET /rtm` | Real Time Messaging WebSocket; 16 KiB inbound message limit |
| `/socket-mode` | Socket Mode WebSocket, mounted with the Web API and RTM by `slack.Mount` in every composition |
| `POST /internal/admin/incoming-webhooks/create`, `/enable` | webhook administration |
| `GET /internal/slack-lists/download.csv`, `GET /internal/exports/workflow-step-responses.csv` | token-authenticated CSV exports |
| `GET /avatars/{workspace}/{user}/{size}.png`, `GET /team-icons/{workspace}/{size}.png` | generated default images; unauthenticated, and they disclose only the color every user or team object already carries |

`/metrics` is not on this listener; it is served only on the operator-only
`-metrics-listen` address.

These routes are **unauthenticated token-bearing capability URLs**: possession
of the path is the authorization.

| Route | Purpose |
|---|---|
| `GET /files/public/{token}` | public file download |
| `GET /users/{workspace}/{user}/photo/{token}` | user avatar |
| `POST /internal/files/external/{upload}` | server-minted external upload target |
| `POST /services/{workspace}/{app}/{secret}` | incoming webhook delivery; see [incoming webhooks](incoming-webhooks.md) |
| `POST /services/triggers/{workspace}/{trigger}/{secret}` | workflow webhook trigger |
| `POST /app-response/{token}` | app `response_url` |

Treat the whole path of each as a secret: do not log it, do not place it in a
referrer-leaking context, and do not cache it under a shared key. See
[files](files.md).

## Deployment profiles

### Local

One `cmd/server` process, a SQLite file, and a local blob directory. The server
runs no background work: outbox delivery, scheduled messages, and blob cleanup
require the separate `cmd/worker` and `cmd/blobgc` processes, and lifecycle
testing requires `cmd/activator`. They stay separate because `cmd/worker`
requires an explicit `-delivery-format` and a per-replica owner identity, and
`cmd/blobgc` its own lease owner; folding them into the server would mean
inferring configuration the program is required to reject.

### Small scale-to-zero

Activator plus a 0..1 server, SQLite on persistent storage, and object storage
for blobs/snapshots. The volume or verified snapshot survives shutdown.

### Production

Activator, independent 0..N web/API and worker deployments, object storage,
and either a three-or-more-node dqlite deployment while active or an external
[PostgreSQL](postgresql.md) server. During application hibernation dqlite nodes
stop after a verified snapshot is published.

Managed-container platforms MAY place the stateless tiers directly on their
serverless container service while using lifecycle-controlled companion compute
for dqlite when the managed service cannot provide stable raw-TCP peer identity.
This remains a qualification target until the required provider tests pass.

## Scalability rules

- Processes are stateless and horizontally replaceable.
- Work is bounded and backpressured.
- All list operations are paginated.
- Hot paths avoid workspace-wide locks and scans.
- Search has a portable baseline; optional SQLite extensions cannot be required
  for correctness.
- File bytes do not live in the relational database.
- Migrations run as a fenced singleton before general traffic is admitted.
- Startup and shutdown are observable state transitions, not shell-script
  timing assumptions.

Related documents: [module boundaries](modules.md), [operations](operations.md),
[deployment](deployment.md), [persistence specification](../specs/persistence.md),
and [scale-to-zero specification](../specs/scale-to-zero.md).
