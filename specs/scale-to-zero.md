# SameOldChat application scale-to-zero specification

## Goal

When idle, all web, API, worker, SQLite/dqlite, and application lifecycle
processes MUST be stopped. Only a low-cost logical activator endpoint, durable
object storage, and the deployment control plane remain available. The
activator MAY itself use request-triggered serverless compute, but it MUST
remain reachable and MUST wake and restore the stack on demand.

## Always-on activator

The activator MUST be independently deployable and intentionally small. It MAY
store only:

- lifecycle state and monotonically increasing generation;
- the reference and digest for the current snapshot manifest;
- references to older known-good manifests;
- the next scheduled wake deadline;
- activation lease and bounded request-spool metadata; and
- non-sensitive status/observability data.

It MUST NOT store workspace, user, conversation, message, token, or file
metadata. Lifecycle metadata MUST use a durable compare-and-swap mechanism or a
single-writer service with equivalent fencing guarantees.

## State machine

```text
ACTIVE ──idle──> QUIESCING ──drained──> SNAPSHOTTING ──verified──> HIBERNATED
  ▲                   │                       │                         │
  │                   └────cancel/fail────────┘                         │ request/timer
  │                                                                    ▼
  └────────────────────────────ready───────────────────────────────── WAKING

Any unrecoverable transition ──> FAILED
```

Every transition MUST be conditional on the expected state and generation.
Processes holding an older generation MUST be fenced from writes.

## Idle eligibility

The stack MAY hibernate only when:

- the configured idle interval has elapsed;
- there are no live browser streams or in-flight commands;
- no required worker lease is active;
- required outbox records are delivered or explicitly safe to defer;
- no scheduled deadline falls within the wake safety window;
- no migration, backup, restore, or operator hold is active; and
- snapshot storage and lifecycle metadata are healthy.

## Hibernate protocol

1. The activator changes `ACTIVE` to `QUIESCING` and increments the fencing
   generation.
2. New requests are held, spooled, or rejected according to buffer policy.
3. Web/API instances reject new mutations and drain accepted commands.
4. Workers finish or release claims; deferred work remains durable.
5. The application exports the next scheduled deadline to lifecycle metadata.
6. The persistence adapter establishes a consistent snapshot boundary.
7. It creates and locally verifies the snapshot.
8. The snapshot is encrypted, uploaded, and verified through an independent
   read/digest check.
9. A signed immutable manifest is atomically selected as current.
10. Database nodes, workers, and web/API nodes stop.
11. Active database volumes MAY be released after publication when the
    deployment profile treats the verified object-store snapshot as the sole
    hibernated copy; no volume may be released earlier.
12. The activator changes `SNAPSHOTTING` to `HIBERNATED`.

No database process may stop before a restorable snapshot is verified and
published. Snapshot failure MUST leave the previously selected manifest intact.

## Wake protocol

1. A request or scheduled timer reaches the activator.
2. Exactly one caller changes `HIBERNATED` to `WAKING` for a new generation.
3. The activator validates the selected manifest and starts persistence
   resources.
4. The lifecycle job downloads, authenticates, decrypts, and restores the
   snapshot.
5. SQLite runs integrity checks, or dqlite is bootstrapped into its configured
   active cluster size and reaches quorum.
6. A fenced singleton applies compatible pending migrations.
7. Workers and web/API replicas start and pass end-to-end readiness.
8. The activator changes `WAKING` to `ACTIVE`.
9. Buffered requests are forwarded once, preserving their original order where
   the protocol requires it.

Concurrent wake triggers MUST join the same wake generation. A timed-out client
MUST NOT cancel the shared wake operation.

## Request handling while cold

Browser navigation SHOULD receive a lightweight activator-owned startup page
that polls or streams lifecycle status and then redirects.

API and webhook requests MUST follow bounded policy:

- safe/idempotent requests MAY be held in memory within strict limits;
- bodies that must survive activator restart MUST be encrypted and spooled to
  durable object storage;
- non-idempotent requests MUST have a stable spool/idempotency ID before replay;
- the activator MUST cap body bytes, total queued bytes, request count, and wait
  duration; and
- overflow MUST return 503 with `Retry-After` rather than accept data it might
  lose.

Authentication normally remains inside the restored stack. The activator MUST
not require access to general Slack bearer-token or user-session secrets merely
to wake it.

## Scheduled activation

The active stack MUST publish its earliest necessary wake time before shutdown.
The activator or control plane MUST trigger wake early enough to meet the job's
deadline, including measured restore time and safety margin. The authoritative
job remains in the snapshot; the external deadline is only a wake hint.

## Snapshot manifest

A manifest MUST include:

- format and manifest version;
- lifecycle generation;
- backend and database format versions;
- schema version;
- creating application version;
- minimum/maximum compatible restorer version;
- creation and verification timestamps;
- plaintext and ciphertext digests where safe and applicable;
- encrypted artifact location and byte length;
- encryption key identifier, not key material; and
- prior known-good generation reference.

## Failure behavior

- Restore failure MUST select older compatible known-good generations in order
  as an explicit recovery policy.
- A failed generation MUST be quarantined without deletion.
- Partial dqlite bootstrap MUST be destroyed or fenced before another attempt.
- The activator MUST never route traffic to a partly restored stack.
- After bounded recovery attempts, state MUST become `FAILED` and require an
  operator decision.
- Lifecycle status exposed publicly MUST reveal no snapshot paths, topology,
  credentials, or tenant data.

## Security

- Snapshots and spooled requests MUST be encrypted in transit and at rest.
- Snapshot manifests MUST be authenticated.
- Restore code MUST defend against path traversal, decompression bombs, size
  mismatch, and unsupported schema versions.
- Activation APIs MUST be authenticated to the deployment control plane.
- Lifecycle generations MUST fence stale application and database writers.

## Service-level measurements

The implementation MUST measure and publish:

- idle-to-hibernated duration;
- request-to-active cold-wake duration by stage;
- snapshot size, duration, and verification duration;
- restoration and dqlite quorum time;
- buffered/rejected request counts and bytes; and
- failed restore counts and operator-selected restore generation counts.

Cold-start targets MUST be selected after a working prototype measures snapshot
size and infrastructure startup. They MUST NOT be invented before measurement.

## Required tests

- Hibernate and wake with SQLite.
- Hibernate and wake a three-node dqlite deployment from snapshot.
- Concurrent first requests cause one restore.
- Activator restart during wake resumes safely.
- Client timeout does not cancel wake.
- Mutation arriving during quiescence is processed once or explicitly rejected.
- Snapshot upload interruption preserves the prior manifest.
- Corrupt newest snapshot selects an older compatible generation according to
  the recovery policy.
- Restore under a newer compatible schema runs migration once.
- Scheduled work wakes early and executes once.
- Stale-generation processes cannot write after hibernation begins.

Related documents: [architecture](../docs/architecture.md),
[operations](../docs/operations.md), [deployment](../docs/deployment.md), and
[hosting specification](hosting.md).
