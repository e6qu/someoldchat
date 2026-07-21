# SameOldChat hosting specification

## Supported targets

SameOldChat MUST be deployable in these environments:

1. One or more ordinary Linux virtual machines on any provider.
2. Amazon Elastic Container Service (ECS) using the AWS Fargate launch type.
3. Google Cloud Run for stateless services, with lifecycle-controlled companion
   compute for persistence when required.
4. Azure Container Apps for stateless services and, only after qualification,
   dqlite peers; companion compute MUST be used otherwise.

Provider integrations MUST be adapters around one lifecycle protocol. Domain,
API, HTMX, persistence-query, and snapshot code MUST remain provider-neutral.

All application units MUST be stateless replicas over durable state stores.
Monolith deployments MAY run multiple replicas of the direct-call composition;
separate deployments MAY assign independent replica counts to each module.
Replica count MUST NOT change data ownership or require sticky sessions. The
in-memory backend is a one-replica development profile and MUST be rejected for
multi-replica production profiles.

## Deployment units

Every hosting profile MUST account for these logical units:

- `activator`: public cold-path endpoint and lifecycle coordinator;
- `server`: HTMX and Slack-compatible HTTP service, scaled `0..N`;
- `worker`: asynchronous work, scaled `0..N`;
- `lifecycle`: fenced restore, migration, snapshot, and verification job;
- `database`: SQLite `0..1`, PostgreSQL `1..N` as an external durable service,
  or active dqlite `3..N`, hibernated at zero;
- object storage: immutable snapshots, files, and durable request spools; and
- lifecycle metadata: small compare-and-swap state available while cold.

One binary MAY expose multiple commands, but deployment permissions MUST remain
separable.

## Provider-neutral lifecycle driver

The activator MUST invoke a driver with equivalent operations:

```text
Inspect(generation)
StartPersistence(generation, snapshot)
RunMigration(generation, version)
StartWorkers(generation)
StartServers(generation)
DrainServers(generation)
StopWorkers(generation)
StopPersistence(generation)
ReleaseActiveStorage(generation)
```

Every mutating call MUST be idempotent and fenced by lifecycle generation.
Provider retries MUST NOT create multiple active database clusters.

## Linux VM profile

The VM profile MUST:

- support a single-VM SQLite installation as the simplest default;
- support a multi-VM dqlite installation;
- run under systemd or an OCI-compatible container runtime;
- require no Kubernetes control plane;
- keep the activator on a small always-on VM or provider request-triggered
  function;
- start and stop application/database units through narrowly scoped local or
  cloud APIs; and
- store snapshots in S3-compatible object storage or a filesystem target with
  equivalent durability and atomic publication.

The installation MUST provide configuration validation, service units,
firewall guidance, TLS termination, upgrade, backup, and restore commands.

## Amazon ECS on AWS Fargate profile

- ECS service desired counts for server, worker, and database units MUST reach
  zero during hibernation.
- The activator SHOULD use API Gateway plus Lambda, or an equivalently cheap
  request-triggered path. A tiny always-running ECS activator MAY be offered.
- S3 SHOULD store snapshots, files, and spooled request bodies.
- DynamoDB conditional writes or an equivalent mechanism SHOULD store lifecycle
  generations and activation leases.
- KMS and Secrets Manager SHOULD protect encryption keys and secrets.
- Active dqlite peers MUST use three distinct single-replica ECS services or an
  equivalently stable identity arrangement with Cloud Map/service discovery.
- dqlite services MUST not use ordinary metric autoscaling while active; the
  lifecycle driver controls their count.
- Fargate ephemeral storage MAY hold restored active data because the verified
  snapshot is authoritative while hibernated. Peer loss and simultaneous task
  loss MUST be covered by qualification tests.

## Google Cloud Run profile

- Server and activator services MUST set minimum instances to zero unless an
  operator explicitly chooses a warm profile.
- The server MUST tolerate disposable local files and arbitrary instance
  replacement.
- Cloud Storage SHOULD store snapshots, files, and durable request spools.
- A generation-match object or another transactional Google Cloud service MUST
  provide lifecycle fencing.
- Cloud Run services MUST NOT host the authoritative SQLite file because their
  writable filesystem is disposable and shutdown is platform-controlled.
- Ordinary Cloud Run services MUST NOT be claimed as a dqlite transport: their
  service contract is HTTP/gRPC and does not provide addressable peer instances
  for a normal raw-TCP dqlite quorum.
- The supported production profile therefore runs stateless SameOldChat units
  on Cloud Run and starts temporary Compute Engine database nodes, or another
  explicitly qualified raw-TCP stateful target, through the lifecycle driver.
- All companion database nodes MUST stop after snapshot publication, preserving
  application scale-to-zero apart from the request-triggered control plane.

## Azure Container Apps profile

- HTTP-triggered server and activator apps MUST allow a minimum replica count of
  zero unless an operator selects a warm profile.
- Blob Storage SHOULD hold snapshots, files, and durable request spools.
- A transactional Azure service or blob lease MUST hold lifecycle fencing.
- Internal TCP ingress and stable app names MAY be used for dqlite only with
  three separately named, maximum-one-replica container apps.
- The dqlite arrangement remains conditional until qualification proves peer
  identity, quorum behavior, controlled wake, snapshot, restore, and shutdown.
- If qualification fails, the production profile MUST put dqlite on temporary
  lifecycle-controlled Azure VMs and keep stateless tiers on Container Apps.
- CPU- or memory-only scaling rules MUST NOT be used where they prevent scale
  to zero; HTTP or explicit lifecycle activation MUST be used.

## SQLite on managed container platforms

SQLite MAY be used only when exactly one lifecycle-controlled database process
owns the file and the platform supplies storage with verified SQLite locking and
durability semantics. Mounting object storage as a filesystem does not satisfy
this requirement.

If a managed container can be terminated before SameOldChat can publish a
consistent snapshot, it MUST NOT own the sole authoritative SQLite copy.

## Activator permissions

The activator MUST use least-privilege identity. It MAY:

- read and conditionally update lifecycle metadata;
- read snapshot manifests and start the lifecycle job;
- adjust only SameOldChat service/task/instance counts; and
- read lifecycle readiness.

It MUST NOT have general administrator access, read chat data, decrypt arbitrary
tenant files, or possess normal user/API bearer-token secrets.

## Qualification levels

Each provider/profile combination MUST be labeled:

- `experimental`: templates exist but failure testing is incomplete;
- `qualified`: cold wake, steady state, hibernation, rollback, and fault tests
  pass for pinned platform capabilities; or
- `recommended`: qualified and exercised continuously in release CI.

Marketing and documentation MUST NOT call an experimental profile production
ready.

## Required qualification tests

- Deploy from an empty account/project/subscription using documented inputs.
- Wake from zero on browser, Slack API, webhook, and scheduled triggers.
- Deduplicate concurrent wake requests.
- Restore SQLite or form a three-node dqlite quorum.
- Replace one active compute/database instance without data loss.
- Hibernate, verify snapshot, and reduce all paid SameOldChat compute to zero.
- Reject a stale lifecycle generation.
- Restore the previous known-good snapshot and application version.
- Rotate secrets and snapshot encryption keys.
- Prove that provider IAM cannot access unrelated resources.
- Report cold-start duration and idle baseline cost.

Related documents: [deployment guide](../docs/deployment.md),
[architecture](../docs/architecture.md), and
[scale-to-zero specification](scale-to-zero.md).
