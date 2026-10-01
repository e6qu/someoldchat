# SameOldChat operations

For the process topology and the crash-only rules these procedures rely on, see
[architecture](architecture.md#runtime-topology). For the binaries' startup
configuration, see the [deployment guide](deployment.md).

## Service states

Operators and automation observe the lifecycle states defined in the
[scale-to-zero specification](../specs/scale-to-zero.md):

`ACTIVE`, `QUIESCING`, `SNAPSHOTTING`, `STOPPING`, `HIBERNATED`, `WAKING`, and
`FAILED`.

Only the activator accepts public traffic in every state. When the application
is active it reverse-proxies traffic; otherwise it coordinates wake-up.
Forwarding uses bounded request bodies and a configured wake deadline. Requests
arriving during an in-progress wake wait for that same fenced generation rather
than starting a second restoration.

## Health and monitoring endpoints

The HTTP server exposes `/healthz` for process liveness and `/readyz` for
end-to-end readiness. Readiness performs a bounded chat-store operation through
the selected composition, so a separate HTTP replica is not admitted while its
TLS gRPC chat dependency is unavailable.

Set `SAMEOLDCHAT_MONITORING_TOKEN` to an independent bearer credential of at
least 32 non-whitespace characters to publish `GET /monitoring/observation`.
The authenticated `e6qu.monitoring/v2` document reports the same end-to-end
readiness check plus fixed-cardinality operation, duration, and process
evidence. An unset token leaves the endpoint fail-closed.

`-metrics-listen` / `SAMEOLDCHAT_METRICS_LISTEN` on `sameoldchat-server` and
`sameoldchat-chatd` publishes `GET /metrics` on a separate operator-only
address; empty serves no metrics endpoint. The activator's `/metrics` is
described under [Observability](#observability).

## Replica termination

`SIGTERM` and `SIGINT` are explicit drain signals. HTTP replicas stop admitting
new work and allow in-flight requests up to a bounded ten-second shutdown
deadline; chat gRPC replicas use `GracefulStop` for the same deadline and then
force-stop. A process crash does not rely on either path for correctness:
durable leases, idempotency records, outbox events, and state-store recovery
remain the authoritative crash-recovery mechanisms.

SQLite startup migrations acquire an immediate transaction on a pinned database
connection. Concurrent replicas therefore serialize schema changes, and a
process crash rolls back the in-flight migration instead of exposing a partial
schema.

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

Requests may be held and replayed only within configured body, count, and
deadline limits. A request whose body exceeds the configured maximum is
rejected with HTTP 413 before it is held. A request that cannot be held or
replayed within the queue and deadline limits receives HTTP 503 and
`Retry-After`. Both the provider-neutral `sameoldchat-activator` and the AWS
Lambda activator in `deploy/ecs-scale-zero` enforce the same body and deadline
limits.

Both answer a refusal with Slack's error envelope and `application/json`, so
an official SDK surfaces a Slack error code rather than a decode failure:
`service_unavailable` (503, with `Retry-After`) while the stack wakes, the
spool is full or unavailable, or the application cannot be reached;
`request_entity_too_large` (413) for a body over the limit, whether declared
or discovered mid-stream; and, from `sameoldchat-activator` only,
`invalid_form_data` (400) for a body that cannot be read. Spool overflow and
spool failure stay distinguishable through
`sameoldchat_activator_spool_overflow_total` and
`sameoldchat_activator_spool_failures_total`.

## Scheduled work while hibernated

Before shutdown, the application exports the earliest required wake deadline
to lifecycle metadata. The activator/control plane schedules a wake before that
deadline. This metadata is a hint to start the authoritative database; it does
not contain the scheduled job payload.

An external webhook or API call also wakes the stack. The activator must spool
an accepted request body durably before acknowledging it if the sender cannot
be expected to retry. Spool rows are claimed with durable per-replica leases;
only the lease owner may delete a delivered row, and lease expiry is the
crash-recovery path for a replica that dies during replay. The activator claims
one buffered request at a time and renews that request's lease while the
selected application handler runs, so a slow handler does not look like a
crashed replica, and it retains no unbounded in-memory batch.

The standalone activator receives an explicit process context. Shutdown
cancels wake and replay work owned by that process, while accepted spool rows
remain durable for a replacement replica to reclaim after lease expiry. A
request context controls only that request's enqueue and response wait; it
does not cancel the shared wake operation.

The shared SQLite, dqlite, and PostgreSQL qualification contract also verifies
event replay order, topic-specific claims, lease renewal, delayed release, and
acknowledgement ownership for durable outbox records.

## WebSocket edge and RTM

The WebSocket activator (`sameoldchat-ecs-ws-activator`) uses the same
termination rule as the standalone activator. Signal handling cancels active
request contexts, closes both sides of each proxied connection, and allows a
bounded server drain. Lease release and scale-down cleanup use a separate
short-lived cleanup context so a disconnected client cannot leave a live lease
indefinitely. The proxy applies a four-megabyte per-message read limit to bound
memory use at the transport edge. Endpoint discovery reads all paginated Amazon
Elastic Container Service task results and batches task description requests
at the service limit, so replica counts do not silently truncate the active
endpoint set.

The RTM WebSocket endpoint follows Slack's published legacy RTM protocol:
successful ping messages return a `pong`, preserve scalar fields, and copy a
positive client `id` into `reply_to`; nested ping fields fail as invalid input.
The endpoint rejects messages larger than 16 kilobytes at the WebSocket
boundary. The server pings every RTM socket every ten seconds and closes one
that answers neither that ping nor the next, and every write is bounded by a
deadline, so a vanished or stalled client does not hold its stream. The URL
`rtm.connect` returns follows the origin the client called it on: `wss://`
when the request arrived over TLS or carries `X-Forwarded-Proto: https`. The
event stream rejects invalid or type-less JSON event payloads. See
[Slack's RTM protocol](https://api.slack.com/legacy/rtm) for the upstream wire
contract.

## Socket Mode

Socket Mode follows [Slack's Socket Mode guide](https://docs.slack.dev/apis/events-api/using-socket-mode/)
and [the `apps.connections.open` method reference](https://docs.slack.dev/reference/methods/apps.connections.open/),
and is available in both compositions: the HTTP process calls the repository
directly in local composition and uses the generated gRPC boundary in
distributed composition.

### Connections

Socket Mode uses an app-level token with the `connections:write` scope. The
`apps.connections.open` method creates a short-lived, single-use connection
lease and returns a WebSocket URL. The WebSocket consumes that lease, sends a
`hello` message carrying `num_connections`, `connection_info.app_id` and
`debug_info`, and acknowledges each valid received envelope by returning its
`envelope_id`. A missing envelope identifier closes the connection with a
protocol error; an acknowledgement for an envelope the connection no longer
holds (a late or duplicate one) is ignored. Malformed event payloads are closed
as protocol errors; the server does not synthesize a replacement payload from
an internal topic and string.

Approved app installations identify the workspaces whose durable outbox events
can be delivered. The last acknowledged event sequence is stored per app, so a
replacement process resumes after the last confirmed event instead of depending
on process memory. Up to ten connections per app may be active; an eleventh
`apps.connections.open` is answered with HTTP 429, `Retry-After`, and
`ratelimited`, which official clients retry. Each active connection renews its
durable lease and releases it when the WebSocket closes.

The connection URL follows the origin the client called
`apps.connections.open` on, so no configuration is needed behind a
TLS-terminating proxy that sets `X-Forwarded-Proto`. Two settings override it:

- `-socket-host` / `SAMEOLDCHAT_SOCKET_HOST` names the public `host:port`
  every connection URL uses, for deployments that serve `/socket-mode` on a
  different host from the Web API.
- `-socket-tls` / `SAMEOLDCHAT_SOCKET_TLS=1` selects `wss://`. With
  `-socket-host` it chooses the scheme; without it, it forces `wss://` behind a
  proxy that terminates TLS without sending `X-Forwarded-Proto`.

[`terraform/ecs-runtime`](../terraform/ecs-runtime/README.md) exports
`SAMEOLDCHAT_SOCKET_TLS=1`. The WebSocket upgrade accepts any `Origin` (the
single-use ticket is the credential), and a request that cannot be upgraded
does not spend the ticket.

### Delivery and retries

Delivery is claimed, not polled: after every acknowledgement the connection
claims the next envelope at once. Interaction envelopes (slash commands,
shortcuts, block actions, view submissions, options) are delivered
concurrently, up to ten unacknowledged per connection. Event delivery state is
kept per record — lease, attempt count, retry reason, and the envelopes already
acknowledged — so a record waiting for its retry does not hold back the records
after it, and an app's connections lease different records at once. Each
connection holds one event record in flight at a time; this is a recorded
deviation from Slack, which delivers events concurrently on a connection. A
record that fans out into several envelopes is retried only for the envelopes
the app did not acknowledge.

An envelope that is not acknowledged within thirty seconds goes back to the
queue without closing the connection and is re-sent with `retry_attempt` and
`retry_reason` (`timeout`); a first delivery carries `retry_attempt: 0` and an
empty `retry_reason`. Retries follow Slack's Events API schedule — immediately,
after one minute, after five minutes — and after the third retry the envelope
is dropped and logged at error level. A dropped event is recorded in the app's
delivery-attempt history as delivered, because the store has no separate
outcome for it.

### Responses

Response payloads are accepted only for envelopes the connection holds and
must be a JSON object. An absent or `null` payload is a plain acknowledgement,
and so is `{}` on an event envelope, which several official SDKs attach to
every acknowledgement; on an interaction `{}` is still a response (an empty
option list, or a modal closed). The HTTP process records each response durably
by app identifier and envelope identifier before it advances the event cursor,
through the same generated chat boundary in both compositions. Replaying the
same response is idempotent; replaying the envelope with different payload
bytes fails with a state conflict. The response record is the explicit handoff
to the application response processor, so a process crash after the WebSocket
ack does not erase the response input.

The response record is an input journal, not an implicit retry or a hidden
fallback. The reusable response processor claims records with an owner and a
lease, invokes an explicitly supplied handler, acknowledges each successful
record, and releases failed records at an explicit retry time. It claims one
response at a time and renews its lease while the handler runs; a crash before
acknowledgement leaves the record reclaimable after the lease expires. It does
not guess application-specific response semantics or run an unbounded retry
loop. Acknowledged response and interaction rows are kept for 24 hours, so a
replayed acknowledgement stays idempotent, and are then pruned on the write
path in every storage profile. The supported wire contract is recorded in the
[compatibility ledger](../specs/compatibility.yaml).

## Workers

### Socket Mode response worker

`sameoldchat-socketmode-worker` supplies the explicit HTTP handler for
deployments that forward Socket Mode responses to another application. Run one
or more replicas with the same `-app-id` and different `-owner` values against
shared durable storage. Each replica claims a disjoint lease set, so a crash
does not require a process-local queue or a coordinated shutdown. The worker
continues after a handler delivery failure because it has released the records
at an explicit retry time. It exits on claim, release, or acknowledgement
failure so the deployment platform can restart it.

### Event and scheduled-message worker

`sameoldchat-worker` requires `-delivery-format`:

- `record` sends the internal `events.Record` JSON shape, with the event ID as
  `Idempotency-Key`. Use it only for an integration that explicitly accepts
  that shape.
- `slack-events` uses the manifest-owned encrypted application credentials and
  `SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX`; the manual `-app-id` and
  `-signing-secret` flags are rejected. It translates the durable topic through
  the shared Slack event table, sends only events whose inner shape is complete
  and allowed on the Events API surface, and signs each resulting
  `event_callback` body as described in
  [Slack's request-signing guide](https://docs.slack.dev/authentication/verifying-requests-from-slack/).

In `slack-events` mode, topics with no safe Slack representation are
acknowledged without being sent; malformed or incomplete typed payloads are
permanent producer failures rather than retry loops. A record may fan out into
several callbacks (for example, one `member_joined_channel` event per invited
user), each with its own stable `event_id`, which is what Slack apps
deduplicate on. Each request carries `X-Slack-Request-Timestamp` and
`X-Slack-Signature`; a retry adds `X-Slack-Retry-Num` and
`X-Slack-Retry-Reason`.

Delivery state is kept per record, not per app. A callback an app fails is
retried on Slack's schedule — immediately, after one minute, after five
minutes — and then dropped, while the app's later events keep being delivered.
A record that fans out is retried only for the callbacks the app did not
accept. A failure on this side of the delivery, such as a storage read that
could not project the event, is retried after a few seconds without spending
one of the app's retries, and its reason is recorded in the app's delivery
history but never sent to the app. Each cycle delivers up to 32 records per
app, stops taking more for an app after about a second, and serves up to eight
apps concurrently, so one slow endpoint does not delay another app. Every
claimed record carries its own `-lease`, so several workers can share an app.

The same process executes due scheduled messages and first-party Later/channel
reminders in both delivery formats. `record` is explicitly workspace-scoped;
`slack-events` claims due schedules and reminders across every workspace.
Scheduled records retain their owner, app/bot attribution, thread parent, and
terminal delivered or failed state. The owner is the identity that scheduled
the message — the app's bot for a bot token, the member and app for a user
token, the member alone for the first-party client — not the bytes of one
token, so `chat.scheduledMessages.list` and `chat.deleteScheduledMessage`
keep working after a token is rotated or reissued, while another app's token
still sees nothing. Schedules recorded without app or bot attribution belong to
their author with no app: they execute under that author and list and cancel
through the author's first-party session, not through an app's token.
Permanent posting failures are recorded once rather than retried forever;
transient failures retain their fenced lease and retry path.

When workers are stopped as part of a lifecycle profile, configure both
`-wake-deadline-url`/`SAMEOLDCHAT_WAKE_DEADLINE_URL` and
`-wake-deadline-token`/`SAMEOLDCHAT_WAKE_DEADLINE_TOKEN`. After each cycle the
worker publishes the fenced minimum of scheduled-message and reminder due
times. Supplying only one coordinate is a configuration error.
`deploy/ecs-scale-zero` keeps its worker always on, so it does not need this
publication.

### Replicas and images

Outbox replicas run `sameoldchat-worker` with distinct `-owner` values and the
same authoritative backend. Blob cleanup replicas run `sameoldchat-blobgc` with
distinct owners and the same backend and blob store; its audit mode also takes
`-min-orphan-age` (default `1h`), the grace period an unreferenced object must
survive before it may be classified as an orphan; see
[blob lifecycle](blob-lifecycle.md). Neither worker persists queue state
locally; a failure releases the durable lease with its retry time, and a
process crash is recovered by lease expiry.

The published container image `ghcr.io/e6qu/someoldchat` contains only
`cmd/server`. `sameoldchat-chatd`, `sameoldchat-worker`, `sameoldchat-blobgc`,
`sameoldchat-socketmode-worker`, `sameoldchat-activator`, and
`sameoldchat-ecs-ws-activator` have no published image: `make build` (or
`make build-static`) builds all seven binaries, and
`deploy/ecs-scale-zero/Dockerfile.worker` and
`deploy/ecs-scale-zero/Dockerfile.websocket-edge` build images for the worker
and the WebSocket edge. Publishing the other binaries is planned in
[Phase 7](../PLAN.md#phase-7-compile-time-module-composition).

## Client addresses behind a reverse proxy

The Web API rate limiter keys a request with no bearer token by its client
address, and the access log records that address for every sign-in and API
call. Behind a reverse proxy every request arrives from the proxy, so without
configuration every such client shares one rate-limit bucket and the access
log names the proxy.

Set `-trusted-proxies` / `SAMEOLDCHAT_TRUSTED_PROXIES` to the proxy's address
or range (comma-separated addresses and CIDR ranges). A request from a trusted
peer is keyed by the rightmost `X-Forwarded-For` entry that is not itself a
trusted proxy; a request from any other peer is keyed by the peer, and its
`X-Forwarded-For` is ignored because the peer wrote it. A trusted proxy that
forwards an entry that is not an address is answered with HTTP 400. A value
that is neither an address nor a CIDR range refuses startup, and
`-check-config` reports it.

## Public URL

`-auth-public-url` / `SAMEOLDCHAT_AUTH_PUBLIC_URL` is the one statement of
where clients reach a deployment, and every process that builds a URL a client
follows reads it:

- `sameoldchat-server` builds the Web API's absolute URLs and the web
  client's links on it, and in local composition the chat service it hosts
  builds event payloads on it too.
- `sameoldchat-chatd` builds the Events API, Socket Mode and RTM payloads in
  distributed composition, so it takes the same flag.
- `sameoldchat-worker -delivery-format slack-events` builds the HTTP Events API
  callbacks, so it takes the same flag; record delivery refuses it.

The URLs an event carries — a shared file's `url_private`,
`url_private_download` and `permalink`, and the `image_*` of the user object in
`team_join`, `user_change`, `user_profile_changed` and `user_status_changed` —
are built on it, exactly as `files.info` and `users.info` build theirs. Journal
records store those URLs origin-relative and are resolved when an event is
delivered, so a changed public URL applies to every later delivery.

Without a public URL the Web API builds its URLs on the origin of each request
(see [Files](files.md#absolute-urls)), but an event has no request to take an
origin from, so its URLs stay origin-relative and each process that builds
events logs a warning at startup. A listen address is deliberately not used as
a fallback: it names a local interface or an internal port behind a proxy, not
an address a client can reach. Set the public URL on every deployment an app
connects to.

## Snapshot retention and verification

- Manifests are immutable and monotonically generated.
- A manifest includes schema version, backend, application compatibility range,
  byte length, cryptographic digest, encryption metadata, creation time, and
  fencing generation.
- A snapshot is not considered valid merely because upload succeeded.

Snapshot retention is not implemented. `internal/lifecycle` exposes snapshot
creation, exact-generation selection, restore, and quarantine records, and no
delete, prune, or retain operation; every published generation is retained, so
generations accumulate without bound, and no automated restore drill exists in
`.github/workflows` or `scripts`. The target is:

- retain the newest verified generation and at least two older verified
  generations by default;
- perform snapshot deletion as a separate garbage-collection operation that is
  never part of publication;
- run restore drills automatically on disposable infrastructure.

Until then, restore drills are a manual operator step in the
[release procedure](#release-procedure).

## Disaster recovery

If the current snapshot fails verification or restoration, the activator marks
that generation unusable, writes a durable `quarantine/<generation>.json`
record for deterministic integrity failures, and the stack enters `FAILED`,
preserving evidence. Lifecycle status is available to the operator through the
token-guarded `GET /lifecycle`; the public `GET /healthz` answers
`{"ok":true}` and nothing else. Provider availability failures are not
quarantined.

Restoring an older retained generation is an explicit, authenticated operator
action with its own generation and compatibility checks:
`POST /restore?generation=<n>`, guarded by `-control-token` and accepted only
while the stack is `FAILED` or `HIBERNATED`. It restores exactly the generation
named and refuses any generation that is not a verified known-good snapshot.
There is no automatic walk-back through older generations:
`specs/scale-to-zero.md` states that restore failure MUST NOT be converted into
an implicit fallback.

The lifecycle controller rejects wake attempts while `FAILED`. An operator must
explicitly acknowledge the failure with `POST /recover`, which advances the
fencing generation and returns the stack to `HIBERNATED`, before a new wake can
begin. A failed wake is therefore never converted into an implicit retry by an
ingress replica. The standalone activator remains available in this state for
authenticated operator inspection; it does not accept ordinary activation until
the acknowledgement succeeds.

Linux/OCI deployments may bind the provider-neutral coordinator to the explicit
command driver. Every command is required at construction time and receives
`SAMEOLDCHAT_LIFECYCLE_GENERATION`; persistence start additionally receives the
selected backend, snapshot artifact, and schema version. Missing commands fail
startup rather than selecting an alternate command.

The authenticated activator exposes `POST /hibernate` for the deployment
control plane. Hibernation runs with an operation context independent of the
request context, so a control-plane client timeout cannot cancel fencing,
snapshot verification, or storage release. `POST /activate`, `POST /restore`,
and public wake forwarding use the same property for shared recovery.

The activator's startup flags, including the three with defaults
(`-wake-deadline`, `-wake-safety-margin`, `-request-max-bytes`), are listed in
the [deployment guide](deployment.md#lifecycle-activator). Its `-control-token`
is also the value a WebSocket edge must be given as `-activator-token`, and the
token a `/metrics` scraper must present.

## Observability

The standalone activator publishes bounded Prometheus-compatible aggregates at
`GET /metrics`: lifecycle state and generation, wake duration by stage,
snapshot durations and sizes, last successful snapshot and restore, restore
failures, migration schema version, and buffered or rejected request counts and
bytes. It does not expose request identifiers, tenant data, credentials, or
snapshot locations.

`GET /metrics` requires the control-plane bearer token, like every other
control route (`/activate`, `/hibernate`, `/recover`, `/restore`,
`/wake-deadline`, `/lifecycle`); an unauthenticated scrape receives 401. The
listener is shared with forwarded application traffic, so authorization is an
allow-list of exactly two open routes — the forwarded catch-all, which the
active stack authenticates itself, and `GET /healthz`, which a load balancer
polls before any token exists. Every other route the activator registers
requires the token by default, so a newly added operator endpoint cannot be
unauthenticated by omission.

No process exports these yet; they remain observability targets:

- active SSE connections;
- outbox depth and oldest age;
- database leader, quorum, and transaction latency;
- dependency policy report age; and
- Slack compatibility suite status.

Logs and traces must never contain bearer tokens, signing secrets, session
cookies, raw private messages, or unredacted file contents.

## Release procedure

The [publish workflow](deployment.md#published-container-verification) builds
the server image and attests its provenance and SBOM; CI runs the test suites.
Every other step below is an operator responsibility.

1. Resolve only dependencies admitted by the dependency policy.
2. Run all contract, SDK, persistence, lifecycle, browser, and security tests.
3. Generate the compatibility report and SBOM.
4. Build reproducibly where supported.
5. Sign binaries, images, manifests, and provenance attestations.
6. Restore the prior release's snapshot into the candidate version and test it.
7. Roll out the activator compatibly before workloads that require a new wake
   protocol.
8. Retain a rollback binary compatible with retained snapshot generations.
