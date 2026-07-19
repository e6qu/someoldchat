# SameOldChat architecture

## Context

The system presents two application adapters over one domain:

- a Slack-compatible HTTP and event surface for external clients; and
- an HTML/HTMX product interface for people.

Both adapters invoke application services directly. The HTMX interface does
not call the Slack-compatible interface over HTTP.

## Implementation principles

The program fails fast and fails loudly. Missing, invalid, or contradictory
configuration is an error; it is never silently replaced with a default
backend, empty value, alternate algorithm, or no-op implementation.

When multiple implementations are genuinely supported, the caller selects one
explicitly (for example `memory`, `sqlite`, or `dqlite`). Those are distinct
operating modes, not fallbacks. An unavailable selected mode fails at startup.
Retries, bounded backoff, and protocol negotiation are explicit protocol
behaviors and are not fallback mechanisms.

Required values are represented as explicit parameters or configuration fields.
Optional parameters are not used to smuggle mode selection or behavior changes
through zero values. Nil is avoided where a concrete value or a required
interface can be used; where Go APIs require a nil-capable value, nil has one
documented meaning and is rejected or handled at the boundary rather than
propagated inward.

Inputs containing lists or repeated alternatives are normalized once at the
boundary into one canonical representation. Downstream logic consumes that
representation through one path. Invalid input is rejected during
normalization. Duplicate logic is removed when it is truly the same behavior;
small independent duplication is retained when it keeps a module deletable and
avoids coupling. Every abstraction must have a seam that makes its removal
safe, and dead or functionally dead code is deleted rather than preserved for
possible future use.

The type system is load-bearing. Domain identifiers are distinct types rather
than interchangeable strings, and adapters perform the unavoidable conversion
from wire values exactly once. Functions accept the narrowest domain type they
need. This makes invalid combinations a compile-time error and keeps parsing,
authorization, and persistence boundaries visible in code review.

Collections are bounded or streamed. A list endpoint must normalize its page
parameters once, read at most the requested page plus the continuation marker,
and expose continuation explicitly. It must not load an unbounded history into
memory merely to render or serialize it. Where the protocol permits streaming,
the implementation should consume and emit records incrementally. File
uploads are streamed through the module boundary in bounded chunks; metadata
is committed only after the blob is durably published.

Blob storage is an explicit capability. A configured absolute directory selects
the filesystem store; a configured Amazon Simple Storage Service bucket selects
the provider store; no storage configuration selects the typed `blob.Disabled` store,
whose operations fail with an explicit unavailable error. The disabled store is
not an empty-store fallback, and filesystem and Amazon Simple Storage Service
configuration cannot be selected together. Production composition does not use a nil blob
store. Directly constructed test services still reject a nil capability at the
boundary so an invalid composition cannot panic or silently skip file behavior.

## Crash-only operation

SameOldChat follows the crash-only design described by the
[Crash-Only Software](https://lwn.net/Articles/191059/) principle. A process,
worker, database node, or active deployment may be terminated abruptly at any
point; recovery uses the same validated
startup path as ordinary activation. There is no correctness-critical graceful
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
fails startup rather than opening an unauthenticated internal port.

## Stateless replicas and state stores

Application replicas are stateless workers over authoritative state stores.
They may hold request-local data and disposable caches, but correctness MUST
NOT depend on process memory, replica affinity, local queues, local sessions,
or local event subscriptions. Durable state belongs in SQLite/dqlite, object
storage, or the explicitly designated lifecycle control store. Browser session
validity and revocation are read and written through that durable store; a
cookie is only a bearer reference and never local authority. Workspace
membership and role are also durable records; a user’s workspace identifier
alone is not authorization.

Mutations may carry an explicit idempotency key. The key, committed result, and
outbox event share one transaction; delivery retries use durable leases and
explicit retry times. Long-running deliveries renew their lease while active,
so lease expiry represents a crashed or fenced worker rather than a slow
healthy worker; this is never an alternate implementation path.

Both deployment modes may run multiple replicas. In monolith mode each replica
contains the direct-call composition and all replicas use the same
qualified durable stores. In separate mode module processes have independent
replica counts and communicate through the selected transport; each module's
replicas still use the module's state store and must be replaceable without
data migration or user repair. The in-memory store is a single-replica test
backend only; selecting it for a multi-replica deployment is invalid.

Direct and multi-person conversations use durable participant sets and a
unique participant-set key, so concurrent `conversations.open` calls from
different replicas converge on one conversation rather than creating
replica-local duplicates.

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

The runnable `cmd/worker` is a stateless outbox and scheduled-message replica.
It requires an explicit state backend, workspace, unique owner, and HTTP
delivery target; event delivery uses durable leases and the event ID as its
idempotency key. Due scheduled messages are claimed with a separate durable
lease and posted with the scheduled-message ID as their idempotency key before
the scheduled record is acknowledged. A worker crash therefore leaves both
committed events and scheduled records claimable after lease expiry rather
than losing a process-local queue.

The runnable `cmd/socketmode-worker` is a stateless Socket Mode response
replica. It claims responses through the process-independent chat boundary and
posts each response payload to an explicitly configured HTTP destination with
the application identifier, envelope identifier, and idempotency key. A
successful destination response acknowledges the durable record. A failed
delivery releases it at the configured retry time, and a process crash leaves
the lease available to another replica after expiry.

The runnable `cmd/blobgc` is a separate stateless blob-cleanup replica. It
claims only the durable `file.blob_delete` topic, uses the same lease/retry
rules, and treats an already-missing object as an idempotent completed delete.

## Go package boundaries

```text
cmd/
  server/         web and Slack-compatible API process
  chatd/          separate chat gRPC process
  worker/         asynchronous work process
  socketmode-worker/  Socket Mode response process
  blobgc/         blob cleanup process
  activator/      wake coordinator and reverse proxy
internal/
  api/slack/      Slack wire decoding and response mapping
  web/            page and HTMX fragment handlers
  auth/           browser sessions, bearer tokens, scopes
  domain/         entities and domain invariants
  service/        transactions and application use cases
  store/          persistence ports
    sqlstore/     portable SQLite repositories and lifecycle state
    dqlite/       clustered lifecycle adapter
  events/         event journal, outbox, webhook delivery
  realtime/       SSE registration and replay
  blob/           external file objects and storage port
  lifecycle/      state machine, fencing, snapshots
  modules/        stable module APIs and transport implementations
  generated/      generated composition bindings
proto/            gRPC service schemas
specs/            project requirements and pinned contract sources
deploy/           provider-specific infrastructure modules
tests/            application and official SDK qualification tests
docs/             architecture, operations, and deployment guidance
```

Module API packages are the separable seams. The generated composition root
chooses local bindings for a static monolith or generated transport bindings
for a split deployment. Business packages do not inspect topology or choose a
transport. See [separable module architecture](modules.md).

Imports point inward: wire and storage adapters depend on service/domain
packages, while domain packages know nothing about HTTP, HTMX, SQLite, dqlite,
or a particular deployment platform.

## State model

Authoritative state is restricted to:

- the active SQLite/dqlite database;
- immutable file objects;
- verified database snapshots and manifests; and
- minimal lifecycle metadata used while the database is absent.

Caches and in-process broadcasts are disposable. Sessions, idempotency keys,
read cursors, event offsets, job leases, scheduled work, and call lifecycles are durable.

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
the journal before subscribing to best-effort live notification. Replica-local
fan-out is an optimization only.

Long-lived SSE connections are activity and intentionally prevent the web tier
from scaling to zero. Once clients disconnect and the idle policy is satisfied,
web replicas may stop.

## Distributed identifiers and ordering

- Public IDs are generated in Go using type-specific Slack-compatible formats.
- Internal keys may use integers for compact indexing.
- Message timestamps are stored in an exact sortable representation, never as
  floating point.
- Ordering that must be global is allocated within the database transaction.
- Every lifecycle and writer lease includes a fencing generation so a process
  from a previous activation cannot write after hibernation begins.

## Deployment profiles

### Local

One combined server/worker process, SQLite file, local blob directory, and an
optional in-process activator for lifecycle testing.

### Small scale-to-zero

Activator plus a 0..1 server, SQLite on persistent storage, and object storage
for blobs/snapshots. The volume or verified snapshot survives shutdown.

### Production

Activator, independent 0..N web/API and worker deployments, object storage,
and a three-or-more-node dqlite stateful deployment while active. During
application hibernation those nodes stop after a verified snapshot is
published.

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
