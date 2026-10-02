# SameOldChat project status and planned work

This document records where the project stands and the work that remains
before a deployment profile or compatibility claim can be treated as
qualified. Completed work is described by the code, its tests, and
[the architecture documentation](docs/README.md), not here; the git history
holds how it got there.

## Objective

Build SameOldChat, a multi-workspace chat application with:

- a Go backend implementing the Slack platform contracts described by pinned
  published OpenAPI/AsyncAPI specifications and official open-source SDKs;
- a server-rendered HTMX web application;
- SQLite for local and small deployments, PostgreSQL as the qualified external
  SQL profile, and dqlite as the replicated SQLite profile;
- stateless application processes that scale from zero, with the database
  snapshotted and stopped during hibernation and a minimal always-reachable
  activator that wakes the stack on demand;
- modules that call one another as Go interfaces, composed at build time into
  either one monolith binary or separately scalable binaries;
- dependency admission that selects the newest eligible stable release only
  after a mandatory 24-hour publication quarantine; and
- self-hosted deployment on Linux VMs and managed-container deployment on
  Amazon ECS on AWS Fargate, Google Cloud Run, and Azure Container Apps,
  subject to the persistence qualification rules.

The compatibility target is a pinned, reproducible contract. The archived
Slack specifications alone do not describe all Slack behavior, so every
inferred or observed behavior must retain its provenance.

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
5. Every SQL profile runs the same schema and portable query suite.
6. Hibernation is a state machine with fencing, verification, and recovery;
   it is never a blind process shutdown.
7. Security checks are release gates, including for tools and test-only code.

## Status

The figures below come from `make compatibility-report` and
`make journey-check`; rerun them rather than editing numbers by hand.

| Measure | Value |
|---|---|
| Current Slack Web API methods implemented | 331 of 331 |
| …with method-level evidence | 331 of 331 |
| …`behavior-compatible` or better | 269 of 331 |
| …`verified-against-slack` | 0 of 331 |
| Recorded known deviations | 92 |
| Retained legacy methods implemented | 10 of 10 |
| User journeys in the normative catalog | 108 |
| …cited by a browser scenario | 100 of 108 |
| …cited by a current official Slack source assertion | 53 of 108 |

Delivered and gated in CI: the pinned contract catalog and compatibility
ledger (Phase 0); portable persistence on memory, SQLite, PostgreSQL, and
dqlite with a transactional outbox and idempotency records (Phase 1); the
Slack Web API, Events API, RTM, and Socket Mode surfaces exercised by the
pinned Node, Python, and Java SDKs (Phases 2 and 5); the HTMX application with
SSE delivery, OIDC sign-in and logout, qualified in Chromium, Firefox, and
WebKit with automated accessibility checks (Phase 3); and the activator,
lifecycle state machine, verified snapshots, and Amazon ECS scale-to-zero
modules (Phase 4).

No compatibility claim is live Slack equivalence: a passing route, local
test, or SDK parse is evidence for its own layer only. The per-method record
lives in [specs/compatibility.yaml](specs/compatibility.yaml), product gaps in
[specs/product-gap-audit.md](specs/product-gap-audit.md), and journey gaps in
the `make journey-check` report.

## Remaining work

### Phase 4: Hosting profiles

- Linux VM, Google Cloud Run, and Azure Container Apps profiles have guidance
  but no templates or qualification; each needs its lifecycle driver,
  deployment templates, and the hibernation and wake tests the
  [hosting specification](specs/hosting.md) requires before it is supported.

Exit criteria for every supported profile:

- Only the activator, durable object storage, and control-plane facilities
  remain active while hibernated.
- Repeated and concurrent wake requests cause one restoration.
- A failed or corrupt snapshot never replaces the last known-good snapshot.
- A user can complete the core chat workflow after a cold wake, and
  terminating a web replica during live delivery loses no committed event.

### Phase 5: Compatibility evidence

The surface is implemented; what remains is evidence and the recorded
deviations.

- Work down the 92 known deviations in the ledger, and keep each claim at the
  level its evidence supports; the contract ratchet permits an audited
  downgrade when a claim is found to be overstated.
- Refuse bot tokens on every `admin.*` method with `not_allowed_token_type`,
  as Slack does: admin scopes exist only on user tokens. Only the
  `admin.apps.permissions.*`, `admin.apps.mcp.servers.*`, the new
  `admin.usergroups.*` methods and `admin.conversations.bulkSetProperties`
  enforce it today. The other admin methods
  accept the deployment's `-api-token`, a bot token, and the handler tests and
  official SDK qualification call them with bot tokens, so the change needs an
  admin user-token fixture and an operator path to an admin user token first.
- Close the journey gaps `make journey-check` prints: eight journeys without a
  browser scenario and 55 without a current official-source assertion.
- Add visual baselines and manual assistive-technology evidence to the
  browser qualification.
- Sign in with Slack: serve an OpenID discovery document and key set at this
  deployment's own URLs, sign ID tokens RS256 with a durable key every
  replica shares, and serve `/openid/connect/authorize`, so a relying party
  that only changes Slack's endpoints can discover and verify tokens the way
  it does Slack's.
- Phase 5 exits only when each method names its current official sources,
  executable evidence, known deviations, and live-comparison state; an
  aggregate green suite supports that record but does not replace it.

### Phase 6: Differential verification and production hardening

- Run controlled differential requests against a disposable Slack developer
  workspace, normalizing volatile fields, so claims can reach
  `verified-against-slack`. No live-Slack runner exists yet; the existing
  differential suites compare local and gRPC composition only.
- Exercise node loss, quorum loss, failed snapshot upload, corrupt snapshot,
  interrupted restoration, and rollback against a deployed profile; the
  lifecycle and dqlite qualification suites cover them in process today.
- Authenticate and encrypt dqlite node-to-node traffic with per-node
  certificates, as the [persistence specification](specs/persistence.md#dqlite-adapter)
  requires; nodes replicate over plain TCP today, so the cluster network must
  be private.
- Wire OSV/advisory and container-image scanning, which the
  [dependency policy](specs/dependency-policy.md) requires, into CI;
  `govulncheck` already runs over the module source.
- Produce a compatibility report and operational recovery guide with each
  release, alongside the SBOM and signed provenance the container workflow
  already attaches.

### Phase 7: Compile-time module composition

The architecture's distinguishing mechanic, which this phase finishes, is that
modules call one another as ordinary Go interfaces and the build decides how a
call travels. `modulegen` reads [modules.json](modules.json) and, for each
target, generates the composition: in the monolith every module dependency is a
direct function call into the implementation; in a split target a dependency on
a module that lives in another binary is satisfied by a generated gRPC client,
and the owning binary registers the generated server. Business code never
names a transport, and the choice is made when the binary is compiled, not by a
runtime flag.

Current state, measured rather than intended:

- There is one module. `chat` owns a 518-method `chatapi.Service`, the
  502-method `store.Store` port, and `internal/service`, an implementation of
  23,500 lines outside its tests; identity, files, apps, real-time delivery, and collaboration
  features all sit behind it.
- Transport selection is a runtime decision. `sameoldchat -chat-mode
  local|grpc` links the implementation, every storage backend, and the gRPC
  client into one binary, and `internal/generated/bindings.go` exports both the
  local and the remote provider from a single package, so every binary that
  imports it links both. The target profiles in `modules.json` are runtime data;
  no build output corresponds to a target.
- Configuration is declared per binary. `sameoldchat` parses 49 flags and
  `sameoldchat-chatd` 21, of which 17 (`-store`, `-db`, the `-dqlite-*` and
  `-blob-*` families, `-auth-public-url`, `-app-credential-key-hex`,
  `-metrics-listen`, the app and session tokens) are declared twice; the workers each declare their own copy of the
  store settings.
- Publication covers one shape. The container workflow publishes only
  `ghcr.io/e6qu/someoldchat`, built from `cmd/server`. `sameoldchat-chatd` and
  the workers are built by `make build` but never published, so a split
  deployment cannot be assembled from released artifacts.

Done: the HTTP and HTMX adapters link only module APIs and `internal/domain`,
which now holds the error sentinels, request limits, and pure parsers they
used to take from `internal/service`; `internal/modules/boundary_test.go`
enforces it.

Remaining work, in order:

1. **Classify packages as modules or libraries.** A module owns durable state
   and its transactions and is reachable through an API that can cross a
   process boundary. A library is pure code linked into whichever binary
   imports it and is never called over gRPC. The proposed modules are
   identity (users, profiles, groups, sessions, tokens, external identity,
   workspace membership and roles), messaging (conversations, membership,
   messages, threads, reactions, pins, bookmarks, drafts, scheduled messages,
   the journal and outbox), files (metadata, blob streaming, thumbnails, remote
   files, blob deletion), apps platform (installations, manifests, datastores,
   Events API delivery, interactivity, views, Socket Mode, workflows and
   functions), real-time delivery (SSE and RTM fan-out, typing, presence; it
   only reads the journal), and collaboration (canvases, lists, huddles,
   reminders, saved items). Libraries are `domain`, `blockkit`, `slackobject`,
   `slackemoji`, `appmanifest`, `bearer`, `secretbox`, `lease`,
   `clientaddr`, `observability`, `thumbnail`, and `huddlesfu`; `outbox`,
   `socketmode`, `realtime`, and `scheduler` are shared runtime libraries,
   and `scheduler` must stop importing `internal/service`.
2. **Split the chat API in process first.** Divide `chatapi.Service` and
   `store.Store` into per-module interfaces served by the existing monolith and
   database, so dependencies become visible before any process boundary moves.
   Each module's sentinels move to its API package. The store port's
   sentinels move out of `internal/store` with them; five names
   (`ErrInvalidAppApproval`, `ErrInvalidInviteRequest`,
   `ErrScheduledStatusLimit`, `ErrTriggerExchanged`, `ErrTriggerExpired`) are
   declared in both `store` and the former service set with different
   meanings and must be renamed apart while keeping their wire keys.
3. **Make dependency direction a build error.** `modules.json` names each
   module's dependencies on other module APIs; `modulegen` rejects cycles and
   generates the boundary rules the adapter test now hard-codes, so a module
   importing another module's implementation fails `make check`. Feature
   modules depend on messaging and identity, never the reverse; a module that
   must react to messages consumes the journal instead of running inside
   messaging's transaction.
4. **Generate one composition root per target.** `modulegen` emits a package
   per target and process that wires every module dependency to either the
   local constructor or the generated gRPC client, and every `cmd/` main
   becomes a thin wrapper over one generated root. Generation must cover
   module-to-module clients, not only the HTTP-to-chat seam that exists
   today. `-chat-mode` is retired; a binary's composition is fixed when it is
   compiled. This is generated code, not build tags, so the existing rule
   that tags must not encode local/remote combinations still holds.
5. **One configuration schema.** Declare every flag and `SAMEOLDCHAT_*`
   environment variable once, in a shared package, and have every binary
   accept the whole schema, so one configuration serves the monolith and
   every process of a split deployment. Each binary requires the settings of
   the modules it links and the addresses and mutual-TLS material of the
   modules it reaches remotely. Decision needed: a setting that belongs only to
   a module the binary does not link is either accepted and reported as not
   applicable at startup, or rejected as contradictory, which is today's rule
   for `-db` in `-chat-mode grpc`.
6. **Scale each module independently.** A split target gives every module
   binary its own replica count, which `modules.json` already records, and
   `terraform/ecs-runtime` must express one service per binary. Each module
   that owns a dqlite store needs its own three-voter quorum, so splitting a
   module out multiplies database processes; the read path that hydrates a
   message from several modules needs batch lookups or a journal-built read
   model instead of one RPC per field.
7. **Publish both shapes.** Release the monolith image and one image per split
   binary under the same immutable commit tag, each with the provenance, SBOM,
   dual-architecture, and retention gates the current image passes, and
   publish the static binaries of both shapes as release assets. The
   retention script must group versions across all of the packages.

Exit criteria:

- `make build` produces the monolith and every split binary from generated
  roots, and a CI check asserts from `go list -deps` that the monolith links
  no internal gRPC transport and that each split binary links only its own
  module implementations.
- The existing composition parity and differential suites run against the
  compiled split binaries as well as the monolith.
- The same configuration file and environment start both shapes.
- Every published image and binary passes the container publication gate.

## Release gates

Every change must pass, as applicable to what it touches:

- formatting, vet, unit, integration, race, fuzz, mutation, and browser tests
  (`make check`, `make check-full`, `make browser-qualification`);
- the official Slack SDK suites (`make sdk-qualification`);
- the SQLite, PostgreSQL, and dqlite persistence suites;
- hibernation and wake tests when lifecycle code or schema changes;
- dependency age, integrity, provenance, and license checks
  (`make dependency-check`) and vulnerability scanning (`make vuln-check`);
- migration forward and restore compatibility checks; and
- the compatibility-ledger and gRPC wire ratchets (`make contract-ratchet`,
  `make proto-breaking`).

## Initial milestone

The first demonstrable milestone is a cold system receiving a request through
the activator, restoring its database, starting the Go application, allowing a
user to authenticate and post a threaded message through HTMX, and exposing the
same state through compatible Slack API calls from at least the Node, Python,
and Java official SDKs.
