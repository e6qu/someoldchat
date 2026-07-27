# SameOldChat operations

## Service states

Operators and automation observe the same lifecycle states defined in the
scale-to-zero specification:

`ACTIVE`, `QUIESCING`, `SNAPSHOTTING`, `STOPPING`, `HIBERNATED`, `WAKING`, and
`FAILED`.

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
failure. On restart, a persisted `QUIESCING` or `SNAPSHOTTING` phase re-enters
`WAKING` and starts persistence **from the live volume, restoring nothing**: no
manifest was published for that fencing generation, so every retained snapshot
is strictly older than the data still on the active storage, and restoring one
would destroy everything written since the last successful hibernation. "No
snapshot exists yet" is deliberately not fatal there. An interrupted `STOPPING`
is the single restoring case, and it restores only the manifest published for
its own fencing generation, never an older one — `STOPPING` is the one phase
that has already published and verified a manifest for that fence. Handled
restore and integrity failures remain `FAILED` and require explicit operator
recovery. See
[scale-to-zero](../specs/scale-to-zero.md#snapshot-boundary-by-profile).

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
be held and replayed only within configured body, count, and deadline limits. A
request whose body exceeds the configured maximum is rejected with HTTP 413
before it is held. A request that cannot be held or replayed within the queue and
deadline limits receives HTTP 503 and `Retry-After`. Both the provider-neutral
`sameoldchat-activator` and the AWS Lambda activator in `deploy/ecs-scale-zero`
enforce the same body and deadline limits.

The refusal body is where the two currently differ, and the difference is
recorded rather than glossed: the Lambda answers with the Slack error envelope
`{"ok":false,"error":"service_unavailable"}` and `application/json`, so an
official SDK surfaces a Slack error code. `sameoldchat-activator` still answers
`text/plain` through `http.Error`, which an SDK reports as a JSON decode
failure. Giving `internal/activator` the same envelope is a pending change to
that package.

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
processor claims one response at a time and renews its lease while the handler
runs. It does not guess application-specific response semantics or run an
unbounded retry loop. See this section and the compatibility ledger for the
supported wire contract.

The scale-to-zero activator applies the same lease rule to buffered HTTP
requests. It claims one request at a time and renews that request's lease while
the selected application handler is running. A slow handler therefore does not
look like a crashed replica, while a process crash leaves the request
reclaimable after the last durable lease expires. The activator does not retain
an unbounded in-memory batch.

The `cmd/socketmode-worker` process supplies the explicit HTTP handler for
deployments that forward Socket Mode responses to another application. Run one
or more replicas with the same application identifier and different owner
identifiers against shared durable storage. Each replica claims a disjoint
lease set, so a crash does not require a process-local queue or a coordinated
shutdown.
The worker continues after a handler delivery failure because it has released
the records at an explicit retry time. It exits on claim, release, or
acknowledgement failure so the deployment platform can restart it.

The outbox worker requires `-delivery-format`. Use `record` only for an
integration that explicitly accepts the internal `events.Record` JSON shape.
Use `slack-events` with `-app-id` and `-signing-secret` for a Slack Events
API request URL. That mode translates the durable topic through the shared
Slack event table, sends only events whose inner shape is complete and allowed
on the Events API surface, and signs each resulting `event_callback` body.
Topics with no safe Slack representation are acknowledged without being sent;
malformed or incomplete typed payloads are permanent producer failures rather
than retry loops. A record may fan out into several deliveries (for example,
one `member_joined_channel` event per invited user), each with a distinct
idempotency key. The request includes `X-Slack-Request-Timestamp`,
`X-Slack-Signature`, and that key as `Idempotency-Key`.

The implementation follows [Slack's Socket Mode guide](https://docs.slack.dev/apis/events-api/using-socket-mode/),
[Slack's request-signing guide](https://docs.slack.dev/authentication/verifying-requests-from-slack/),
and [the `apps.connections.open` method reference](https://docs.slack.dev/reference/methods/apps.connections.open/).
Socket Mode is available in both local composition and distributed composition:
the HTTP process calls the repository directly in local composition and uses
the generated gRPC boundary in distributed composition.
Malformed Socket Mode event payloads are closed as protocol errors; the server
does not synthesize a replacement payload from an internal topic and string.
The Real Time Messaging event stream applies the same rule and rejects invalid
or type-less JSON event payloads.

## Snapshot retention and verification

- Manifests are immutable and monotonically generated.
- A manifest includes schema version, backend, application compatibility range,
  byte length, cryptographic digest, encryption metadata, creation time, and
  fencing generation.
- A snapshot is not considered valid merely because upload succeeded.

Snapshot retention is a stated target, not current behaviour. `internal/lifecycle`
exposes snapshot creation, exact-generation selection, restore, and quarantine
records, and no delete, prune, or retain operation at all; every published
generation is retained forever, `retained_snapshots` in the deployment guide's
configuration schema is read by no code, and no automated restore drill exists
in `.github/workflows` or `scripts`. The target is:

- retain the newest verified generation and at least two older verified
  generations by default;
- perform snapshot deletion as a separate garbage-collection operation that is
  never part of publication;
- run restore drills automatically on disposable infrastructure.

Until those are implemented, generations accumulate without bound and restore
drills are a manual operator step in the release procedure below.

## Disaster recovery

If the current snapshot fails verification or restoration, the activator marks
that generation unusable, writes a durable
`quarantine/<generation>.json` record for deterministic integrity failures,
and the stack enters `FAILED`, preserving evidence and exposing an
operator-safe status endpoint without leaking internal details publicly.
Provider availability failures are not quarantined. Restoring an older retained
generation is an explicit, authenticated operator action with its own
generation and compatibility checks
(`POST /restore` with the selected generation, guarded by `-control-token`). It
is a recovery selection, not an implicit implementation fallback:
`specs/scale-to-zero.md` states that restore failure MUST NOT be converted into
an implicit fallback, and the coordinator's automatic walk-back through older
generations has been removed to match: `POST /restore` with an explicit
generation is now the only way an older generation is selected, and it refuses
any generation but the one the operator named. This paragraph said the removal
"is being removed" for a release after it had happened, so a reader of the
shipped documentation was told the implicit fallback was still live.

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

Three `sameoldchat-activator` settings bound those operations and have defaults,
so they are easy to miss:

| Flag | Default | Effect |
|---|---|---|
| `-wake-deadline` | `2m` | How long a caller waits for a cold start before the activator gives up on that request. |
| `-wake-safety-margin` | `5m` | Measured restore time plus margin reserved before a scheduled wake deadline. Hibernation is refused with 409 and `Retry-After` when a published deadline falls inside it, and the scheduled-wake loop polls at a tenth of it. |
| `-request-max-bytes` | `4194304` | One cap for both the spooled request body and the captured response. They must agree, so there is one flag rather than two that can diverge. |

Every other activator setting is required, with one deliberate exception: the
snapshot-store settings are *conditionally* required and mutually exclusive.
`-snapshot-root` is required for `-snapshot-store=filesystem` and refused for
`s3`; `-snapshot-s3-bucket` is required for `-snapshot-store=s3` and refused for
`filesystem`, and `-snapshot-s3-prefix` applies only to `s3`. Otherwise the
process exits 2 rather than choosing a snapshot store, key, or command for the
operator. The
`-control-token` value is what a WebSocket edge must be given as
`-activator-token`, and what a `/metrics` scraper must present.

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

`GET /metrics` requires the control-plane bearer token, like `POST /activate`,
`POST /hibernate`, and `POST /recover`. A scraper must be configured with the
token; an unauthenticated scrape receives 401. The listener is shared with
forwarded application traffic, so authorization is an allow-list of exactly two
open routes — the forwarded catch-all, which the active stack authenticates
itself, and `GET /healthz`, which a load balancer polls before any token exists.
Every other route the activator registers requires the token by default, so a
newly added operator endpoint cannot be unauthenticated by omission.

Outbox replicas run `sameoldchat-worker` with distinct owner IDs and the same
authoritative backend. Blob cleanup replicas run `sameoldchat-blobgc` with
distinct owner IDs and the same backend/blob store. `sameoldchat-blobgc` also
takes `-min-orphan-age` (default `1h`), the grace period an unreferenced object
must survive before an audit may classify it as an orphan; see
[blob lifecycle](blob-lifecycle.md).

The published container image at `ghcr.io/e6qu/someoldchat` contains only
`cmd/server`. `sameoldchat-worker`, `sameoldchat-blobgc`,
`sameoldchat-socketmode-worker`, `sameoldchat-chatd`, `sameoldchat-activator`,
and `sameoldchat-ecs-ws-activator` have no published image, so on a container
platform they must be built from this repository — the root `Dockerfile` for the
first five by changing the built package, and
`deploy/ecs-scale-zero/Dockerfile.websocket-edge` for the WebSocket activator.
`make build` and `make build-static` produce all seven for the systemd profile in
the deployment guide. Neither worker persists
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
