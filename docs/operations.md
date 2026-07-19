# SameOldChat operations

## Service states

Operators and automation observe the same lifecycle states defined in the
scale-to-zero specification:

`ACTIVE`, `QUIESCING`, `SNAPSHOTTING`, `HIBERNATED`, `WAKING`, and `FAILED`.

Only the activator accepts public traffic in every state. When the application
is active it reverse-proxies traffic; otherwise it coordinates wake-up.
Forwarding uses bounded request bodies and a configured wake deadline. Requests
arriving during an in-progress wake wait for that same fenced generation rather
than starting a second restoration.

The HTTP server exposes `/healthz` for process liveness and `/readyz` for
end-to-end readiness. Readiness performs a bounded chat-store operation through
the selected composition, so a separate HTTP replica is not admitted while its
TLS gRPC chat dependency is unavailable.

## Replica termination

`SIGTERM` and `SIGINT` are explicit drain signals. HTTP replicas stop admitting
new work and allow in-flight requests up to a bounded ten-second shutdown
deadline; chat gRPC replicas use `GracefulStop` for the same deadline and then
force-stop. A process crash does not rely on either path for correctness:
durable leases, idempotency records, outbox events, and state-store recovery
remain the authoritative crash-recovery mechanisms.

## Normal hibernation

Hibernation begins only after the configured idle window and after checking the
next scheduled deadline. The controller:

1. Advances the fencing generation and rejects new mutating traffic.
2. Drains in-flight commands and required outbox work.
3. Records the next scheduled wake deadline outside the database.
4. Stops general workers and leaves only the lifecycle path active.
5. Stops the database and remaining application processes.
6. Creates a consistent snapshot using the selected backend provider.
7. Encrypts, uploads, re-downloads or independently reads, and verifies it.
8. Atomically publishes a signed manifest while retaining older generations.
9. Optionally releases active database volumes after snapshot publication.
10. Marks the stack `HIBERNATED`.

Any failure during hibernation is recorded as `FAILED`; it is never silently
treated as a successful active state. Fencing prevents old-generation writers
from re-entering service while an operator or recovery controller resolves the
failure.

An abrupt process or host crash is different from a handled hibernation
failure. On restart, a persisted `QUIESCING`, `SNAPSHOTTING`, or `STOPPING`
phase automatically re-enters `WAKING` and restores the newest verified
retained snapshot at or before that fencing generation. If no verified
snapshot can be authenticated, the stack fails closed instead of guessing;
handled restore and integrity failures remain `FAILED` and require explicit
operator recovery.

## Wake path

The activator deduplicates concurrent wake requests using one lifecycle
generation. It then:

1. Moves to `WAKING` and fetches the authoritatively published current snapshot
   manifest.
2. Verifies the snapshot manifest and restores the selected snapshot format
   before starting persistence. SQLite restores one database file; dqlite
   restores its stopped state directory according to Canonical's documented
   filesystem procedure.
3. Starts the persistence resources.
4. Runs integrity checks.
5. Runs a fenced migration job if the binary requires a newer schema.
6. Starts workers and web/API replicas.
7. Waits for end-to-end readiness, not merely process readiness.
8. Moves to `ACTIVE` and forwards buffered requests.

The activator returns a lightweight startup page to browsers. API requests may
be held and replayed only within configured body, count, and deadline limits.
Requests beyond those limits receive HTTP 503, `Retry-After`, and the closest
compatible Slack error envelope recorded in the compatibility ledger.

## Scheduled work while hibernated

Before shutdown, the application exports the earliest required wake deadline
to lifecycle metadata. The activator/control plane schedules a wake before that
deadline. This metadata is a hint to start the authoritative database; it does
not contain the scheduled job payload.

An external webhook or API call also wakes the stack. The activator must spool
an accepted request body durably before acknowledging it if the sender cannot
be expected to retry.
Spool rows are claimed with durable per-replica leases; only the lease owner
may delete a delivered row, and lease expiry is the crash-recovery path for a
replica that dies during replay.

The shared SQLite, dqlite, and PostgreSQL qualification contract also verifies
event replay order, topic-specific claims, lease renewal, delayed release, and
acknowledgement ownership for durable outbox records.

The standalone activator receives an explicit process context. Shutdown
cancels wake and replay work owned by that process, while accepted spool rows
remain durable for a replacement replica to reclaim after lease expiry. A
request context controls only that request's enqueue and response wait; it
does not cancel the shared wake operation.

The WebSocket activator uses the same termination rule. Signal handling
cancels active request contexts, closes both sides of each proxied connection,
and allows a bounded server drain. Lease release and scale-down cleanup use a
separate short-lived cleanup context so a disconnected client cannot leave a
live lease indefinitely. The proxy also applies a four-megabyte per-message
read limit to bound memory use at the transport edge. Endpoint discovery reads
all paginated Amazon Elastic Container Service task results and batches task
description requests at the service limit, so replica counts do not silently
truncate the active endpoint set.

The RTM WebSocket endpoint follows Slack's published legacy RTM protocol:
successful ping messages return a `pong`, preserve scalar fields, and copy a
positive client `id` into `reply_to`; nested ping fields fail as invalid input.
The endpoint also rejects messages larger than 16 kilobytes at the WebSocket
boundary. See [Slack's RTM protocol](https://api.slack.com/legacy/rtm) for the
upstream wire contract.

### Socket Mode

Socket Mode uses an app-level token with the `connections:write` scope. The
`apps.connections.open` method creates a short-lived, single-use connection
lease and returns a WebSocket URL. The WebSocket consumes that lease, sends a
`hello` message, and acknowledges each valid received envelope by returning
its `envelope_id`. A missing envelope identifier closes the connection with a
protocol error. Approved app installations identify the workspaces whose
durable outbox events can be delivered. The last acknowledged event sequence
is stored per app, so a replacement process resumes after the last confirmed
event instead of depending on process memory. The implementation allows up to
ten active connections per app. Each active connection renews its durable
lease and releases it when the WebSocket closes. Each connection uses a
bounded one-event-at-a-time delivery loop; the client must acknowledge an
event before the next event is sent.

Response payloads are accepted only for known event envelopes and must be
valid JSON. The HTTP process records each response durably by app identifier
and envelope identifier before it advances the event cursor. Replaying the
same response is idempotent; replaying the envelope with different payload
bytes fails with a state conflict. The response record is the explicit handoff
to the application response processor, so a process crash after the WebSocket
ack does not erase the response input. The local and distributed compositions
use the same generated chat boundary for this write.

The response record is an input journal, not an implicit retry or a hidden
fallback. The reusable response processor claims records with an owner and a
lease, invokes an explicitly supplied handler, acknowledges each successful
record, and releases failed records at an explicit retry time. A crash before
acknowledgement leaves the record reclaimable after the lease expires. The
processor does not guess application-specific response semantics or run an
unbounded retry loop. See this section and the compatibility ledger for the
supported wire contract.

The `cmd/socketmode-worker` process supplies the explicit HTTP handler for
deployments that forward Socket Mode responses to another application. Run one
or more replicas with the same application identifier and different owner
identifiers against shared durable storage. Each replica claims a disjoint
lease set, so a crash does not require a process-local queue or a coordinated
shutdown.

The implementation follows [Slack's Socket Mode guide](https://docs.slack.dev/apis/events-api/using-socket-mode/)
and [the `apps.connections.open` method reference](https://docs.slack.dev/reference/methods/apps.connections.open/).
Socket Mode is available in both local composition and distributed composition:
the HTTP process calls the repository directly in local composition and uses
the generated gRPC boundary in distributed composition.

## Snapshot retention and verification

- Manifests are immutable and monotonically generated.
- A manifest includes schema version, backend, application compatibility range,
  byte length, cryptographic digest, encryption metadata, creation time, and
  fencing generation.
- The newest verified generation and at least two older verified generations
  are retained by default.
- Snapshot deletion is a separate garbage-collection operation and never part
  of publication.
- Restore drills run automatically on disposable infrastructure.
- A snapshot is not considered valid merely because upload succeeded.

## Disaster recovery

If the current snapshot fails verification or restoration, the activator marks
that generation unusable and the stack enters `FAILED`, preserving evidence and
exposing an operator-safe status endpoint without leaking internal details
publicly. Restoring an older retained generation is an explicit, authenticated
operator action with its own generation and compatibility checks. It is a
recovery selection, not an implicit implementation fallback.

The lifecycle controller rejects wake attempts while `FAILED`. An operator must
explicitly acknowledge the failure, which advances the fencing generation and
returns the stack to `HIBERNATED`, before a new wake can begin. A failed wake is
therefore never converted into an implicit retry by an ingress replica.
The standalone activator remains available in this state for authenticated
operator inspection and exposes `POST /recover` for that acknowledgement; it
does not accept ordinary activation until the acknowledgement succeeds.

Linux/OCI deployments may bind the provider-neutral coordinator to the explicit
command driver. Every command is required at construction time and receives
`SAMEOLDCHAT_LIFECYCLE_GENERATION`; persistence start additionally receives the
selected backend, snapshot artifact, and schema version. Missing commands fail
startup rather than selecting an alternate command.

The authenticated activator exposes `POST /hibernate` for the deployment
control plane. Hibernation runs with an operation context independent of the
request context, so a control-plane client timeout cannot cancel fencing,
snapshot verification, or storage release. `POST /activate` and public wake
forwarding use the same property for shared recovery.

SQLite startup migrations acquire an immediate transaction on a pinned database
connection. Concurrent replicas therefore serialize schema changes, and a
process crash rolls back the in-flight migration instead of exposing a partial
schema.

## Observability

Record metrics and structured events for:

- lifecycle state and generation;
- last successful snapshot and restore;
- wake duration by stage;
- buffered request count/bytes and rejection count;
- active SSE connections;
- outbox depth and oldest age;
- database leader, quorum, and transaction latency;
- migration version;
- dependency policy report age; and
- Slack compatibility suite status.

The standalone activator publishes bounded Prometheus-compatible aggregates at
`GET /metrics`. The endpoint contains lifecycle state and generation, wake and
snapshot durations, snapshot sizes, restore failures, and buffered or rejected
request counts and bytes. It does not expose request identifiers, tenant data,
credentials, or snapshot locations.

Outbox replicas run `sameoldchat-worker` with distinct owner IDs and the same
authoritative backend. Blob cleanup replicas run `sameoldchat-blobgc` with
distinct owner IDs and the same backend/blob store. Neither worker persists
queue state locally; a failure releases the durable lease with its retry time,
and a process crash is recovered by lease expiry.

Logs and traces must never contain bearer tokens, signing secrets, session
cookies, raw private messages, or unredacted file contents.

## Release procedure

1. Resolve only dependencies admitted by the dependency policy.
2. Run all contract, SDK, persistence, lifecycle, browser, and security tests.
3. Generate the compatibility report and SBOM.
4. Build reproducibly where supported.
5. Sign binaries, images, manifests, and provenance attestations.
6. Restore the prior release's snapshot into the candidate version and test it.
7. Roll out the activator compatibly before workloads that require a new wake
   protocol.
8. Retain a rollback binary compatible with retained snapshot generations.
