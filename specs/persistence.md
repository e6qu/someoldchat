# SameOldChat persistence specification

## Portability boundary

Domain and application packages MUST depend on a persistence port and MUST NOT
import a SQLite, PostgreSQL, or dqlite driver. Shared repositories SHOULD use
`database/sql` and a deliberately portable SQL dialect.

The port MUST support transactional commands, consistent reads, migrations,
health checks, and lifecycle closure. Backend lifecycle and cluster management
MUST remain adapter-specific.

## Crash-only requirement

Application and database components MUST be safe to terminate abruptly and
restart through the normal startup/recovery path. Correctness MUST NOT depend
on a graceful shutdown sequence. Committed state MUST survive a process or
node crash, unfinished outbox work MUST be replayable, and stale writers MUST
be fenced after recovery. Crash and restart tests are release gates for
persistence and lifecycle changes.

## Core schema

The initial schema SHOULD include:

- workspaces, users, workspace members, roles, and profiles;
- sessions, applications, installations, tokens, and scopes;
- conversations and conversation members;
- messages and message revisions;
- reactions, pins, bookmarks, and read cursors;
- file metadata and object references;
- reminders, scheduled messages, user groups, and calls;
- event subscriptions, event journal, and outbox;
- webhook deliveries, job leases, idempotency records, and audit log; and
- schema and lifecycle metadata.

Tenant-owned tables MUST include or unambiguously derive workspace scope.
Foreign keys MUST be enabled where the backend supports their enforcement.

## SQL rules

- Message timestamps and ordered IDs MUST NOT use floating point.
- IDs and wall-clock timestamps SHOULD be generated in Go, except transactional
  ordering values that require database serialization.
- Queries MUST be parameterized.
- Transactions MUST be short and MUST NOT perform network I/O.
- Migrations MUST be forward-only, repeatably tested, and run by one fenced
  migration job.
- Core correctness MUST NOT depend on FTS5 or another optional extension.
- `VACUUM` MUST NOT be required because supported dqlite versions may not
  support it.
- Backend-specific pragmas MUST stay inside the relevant adapter.

## SQLite adapter

The SQLite adapter MUST:

- support a durable file and disposable test databases;
- enable foreign-key enforcement;
- configure WAL and a bounded busy timeout when appropriate;
- reject unsafe multi-writer deployment configuration;
- expose integrity checking and consistent snapshot creation; and
- pass the shared repository and migration suite.

## PostgreSQL adapter

The PostgreSQL adapter MUST:

- use the pinned official `github.com/jackc/pgx/v5` driver through
  `database/sql`;
- require an explicit PostgreSQL connection string and fail at startup when it
  is absent or invalid;
- preserve the shared repository transaction, constraint, migration, and
  outbox semantics without changing domain code;
- use PostgreSQL-native identity columns, conflict handling, metadata queries,
  and transaction boundaries at the adapter seam; and
- pass the shared repository and migration suite against a real PostgreSQL
  server.

PostgreSQL is a separately selectable storage profile. It is not a fallback for
SQLite or dqlite, and the application never changes storage profiles after
startup.

## dqlite adapter

The dqlite adapter MUST:

- use pinned Canonical dqlite and Go binding releases;
- manage node identity, address, certificates, bootstrap, join, and readiness;
- expose leader and quorum health;
- preserve transaction semantics during leader changes;
- refuse writes without the required quorum;
- pass the shared repository and migration suite; and
- implement a tested snapshot-to-new-cluster restoration procedure.

The application MUST NOT assume that stopping and restarting arbitrary dqlite
processes is equivalent to restoring a verified application snapshot.

Lifecycle state and fencing generations MUST be stored durably. A controller
restart MUST reload the state and reject a stale generation through an atomic
compare-and-swap operation.

## Transactional outbox

Every externally observable event caused by a domain mutation MUST be inserted
in the same transaction as that mutation. Workers MUST claim records using a
lease and fencing token. Delivery attempts, results, and retry schedule MUST be
durable. Claim operations are bounded; an explicit released retry schedule (or
an expired lease) is eligible for a later claim, while acknowledgement or
release by a different or expired owner fails.

Mutating API requests with an idempotency key MUST record that key in the same
transaction as the mutation and its outbox event. A committed duplicate MUST
return the original result; a losing concurrent transaction MUST roll back
without retaining a database lock.

## Snapshot provider

Each backend MUST provide:

```text
Quiesce(generation)
CreateSnapshot(generation) -> local artifact
VerifySnapshot(artifact)
RestoreSnapshot(manifest)
VerifyRestoredDatabase()
```

Snapshot artifacts MUST be encrypted before leaving the trusted runtime,
content-addressed, and accompanied by a signed immutable manifest. Publication
MUST be atomic from the activator's perspective.

## Compatibility testing

Every migration and repository test MUST run against SQLite and a real dqlite
cluster in CI or a required integration environment. Driver mocks do not count
as dqlite compatibility evidence.
