# SameOldChat product specification

## Scope

SameOldChat MUST provide a multi-workspace Slack-compatible chat experience and a
Slack-compatible platform API implemented in Go. The human interface MUST use
server-rendered HTML enhanced with HTMX. SQLite MUST be the default persistence
implementation and dqlite MUST be selectable without changing domain logic.

## Functional requirements

The product MUST support:

- users, workspace membership, roles, profiles, sessions, and API tokens;
- public/private channels, direct messages, and multi-person direct messages;
- messages, edits, deletion, threads, reactions, pins, mentions, and unread
  cursors;
- files through an external blob-storage abstraction;
- search with a backend-portable correctness baseline;
- Slack-compatible methods, objects, errors, pagination, events, OAuth,
  interactivity, and protocols selected in the compatibility ledger;
- durable event delivery and scheduled work; and
- application hibernation and request-triggered restoration.

## HTMX requirements

- Entry points MUST render complete HTML documents.
- Mutations and incremental navigation SHOULD return focused fragments.
- Basic navigation and message submission MUST remain usable without client
  JavaScript, excluding live-update behavior.
- Custom JavaScript MUST be limited to behavior not reasonably supplied by
  HTML and HTMX, such as keyboard shortcuts, focus, composer state, and
  bounded reconnection.
- Live updates MUST be replayable after disconnection.
- Browser surfaces MUST satisfy keyboard and accessibility tests.

## Isolation and authorization

- Every tenant-owned record MUST be scoped to a workspace.
- Private-conversation access MUST be checked on every read and mutation.
- API tokens MUST carry explicit token type, workspace, actor, and scopes.
- Browser sessions MUST be revocable.
- Cross-workspace identifiers MUST NOT grant access by possession alone.

## Performance and scale

- Every unbounded collection MUST be paginated.
- Application replicas MUST be horizontally replaceable.
- No correctness property MAY rely on process memory.
- File bodies MUST NOT be stored as large relational blobs.
- A hot conversation MUST NOT require locking an entire workspace.
- The system MUST apply bounded concurrency, timeouts, and backpressure.
- The default deployment MUST be capable of application scale-to-zero.

## Hosting

- SameOldChat MUST support self-hosting on ordinary Linux virtual machines.
- It MUST provide qualified deployment profiles for AWS ECS on Fargate, Google
  Cloud Run, and Azure Container Apps, subject to the qualification levels in
  the hosting specification.
- Hosting adapters MUST preserve the same snapshot, fencing, wake, and
  compatibility semantics.
- A profile MUST NOT claim pure managed-container dqlite support unless stable
  peer networking, lifecycle control, and restoration pass qualification tests.

## Compatibility reporting

Each published operation MUST be labeled as one of:

- `unimplemented`;
- `schema-compatible`;
- `sdk-compatible`;
- `behavior-compatible`; or
- `verified-against-slack`.

No operation MAY be advertised above the level demonstrated by automated tests
and retained evidence.

## Non-goals of the initial milestone

The initial milestone does not promise visual pixel equivalence, voice/video
huddles, every enterprise administration workflow, or behavior that exists
only in Slack's private clients. Those may be added through explicit,
source-backed compatibility entries.

Related documents: [terminology](../docs/terminology.md),
[architecture](../docs/architecture.md), and [compatibility specification](api-compatibility.md).
