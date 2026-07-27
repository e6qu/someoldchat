# SameOldChat deployment guide

This guide separates current infrastructure from deployment profiles that still
need qualification. The current provider-specific implementation is AWS Elastic
Container Service, split across two shipped Terraform modules:

- [terraform/ecs-runtime](../terraform/ecs-runtime/README.md) owns the durable
  application resources — the private uploads bucket, the API-token,
  session-token, and authorization-state secrets, the least-privilege task-role
  policy, and the `environment`/`secrets` values a task needs to start; and
- [deploy/ecs-scale-zero](../deploy/ecs-scale-zero/README.md) owns
  request-triggered task activation and scale-down for the HTTP path, plus the
  always-on WebSocket edge and the scale-to-zero WebSocket application tier.

Both pin the same exact AWS provider version, so one root configuration can
consume them together. The provider-neutral Go lifecycle activator remains a
separate deployment unit for hibernation, snapshot publication, and restore;
neither module deploys it.

## Deployment philosophy

SameOldChat ships one provider-neutral application and multiple lifecycle
drivers. A cloud service is not considered supported merely because it can run
the container image; it must also satisfy persistence, peer networking,
hibernation, wake, fencing, and recovery tests.

## Capability matrix

| Profile | Stateless tiers | SQLite | PostgreSQL | dqlite | Cold database |
|---|---|---|---|---|---|
| Linux VM | Native | Recommended for one VM | Supported with a durable PostgreSQL service | Supported on 3+ VMs | Snapshot, then stop units/VMs |
| Amazon Elastic Container Service (ECS) on AWS Fargate | Native | Conditional single-owner | Supported when PostgreSQL is external to ECS | Targeted via stable ECS services | S3 snapshot, desired count 0 |
| Google Cloud Run | Native | Not authoritative on local disk | Use an external PostgreSQL service | Companion compute required | Cloud Storage snapshot, compute 0 |
| Azure Container Apps | Native | Conditional single-owner | Use an external PostgreSQL service | Conditional raw-TCP profile; VM profile is a separate qualified option | Blob snapshot, replicas/VMs 0 |

The matrix describes intended capability, not shipped infrastructure. Only the
Amazon ECS rows have templates in this repository, and even those cover
request-triggered activation only — the hibernation state machine and snapshot
and restore procedures are not shipped by either ECS module. There are no
systemd units, cloud-init files, Cloud Run services, or Container Apps templates
anywhere in the repository, so the Linux VM, Google Cloud Run, and Azure
Container Apps rows are targets an operator must build. Using the qualification
vocabulary of the [hosting specification](../specs/hosting.md):

| Profile | Qualification level |
|---|---|
| Linux VM | experimental — no templates shipped |
| Amazon ECS on AWS Fargate | experimental — request-triggered activation shipped; hibernation and snapshot/restore not shipped |
| Google Cloud Run | experimental — no templates shipped |
| Azure Container Apps | experimental — no templates shipped |

“Conditional” in the matrix above means the profile must pass the
version-pinned qualification suite before production use.

## Common configuration

Every deployment supplies the same logical configuration:

```yaml
name: sameoldchat
region: provider-region

storage:
  driver: sqlite # or postgresql or dqlite
  database: sameoldchat

hibernation:
  enabled: true
  idle_after: 30m
  wake_deadline: 120s
  retained_snapshots: 3   # target only; see the note below

scaling:
  server_min: 0
  server_max: 20
  worker_min: 0
  worker_max: 10
```

Provider-specific files bind logical object storage, lifecycle metadata,
identity, networking, and compute operations without leaking them into domain
configuration.

## Self-hosted VM installation

The first supported installation SHOULD be a Linux VM with:

- the SameOldChat binary or OCI image;
- systemd units for the activator, server, worker, socketmode-worker, blobgc,
  and lifecycle commands;
- SQLite for the simplest topology;
- Caddy, nginx, or a cloud load balancer for TLS;
- an S3-compatible bucket for snapshots and files; and
- a narrowly scoped credential allowing the activator to start stopped units or
  additional database VMs.

The provider-neutral `sameoldchat-activator` requires all of the following at
startup, and fails loudly when any is missing: `-listen`; a durable SQLite
control DSN; an explicit snapshot store (`filesystem` with `-snapshot-root` or
`s3` with `-snapshot-s3-bucket` and optional `-snapshot-s3-prefix`); a forward
URL; an authenticated control token; an explicit snapshot mode (`file` for one
database file or `directory` for a stopped dqlite state directory);
`-snapshot-source`, `-snapshot-output`, `-snapshot-max-bytes`, `-snapshot-key-id`,
`-snapshot-encryption-key-hex`, and `-snapshot-signing-key-hex`; `-backend`,
`-schema-version`, and `-application-version`; `-request-spool-key-hex`,
`-request-spool-owner`, `-request-spool-max-bytes`, and
`-request-spool-max-requests`; and every lifecycle command.
`-wake-deadline`, `-wake-safety-margin`, and `-request-max-bytes` have defaults
and are documented in [operations](operations.md#observability).
Commands receive the fencing
generation through `SAMEOLDCHAT_LIFECYCLE_GENERATION`; persistence startup also
receives the selected backend, snapshot artifact, and schema version. Missing
commands, keys, or endpoints fail startup. The activator owns lifecycle
metadata only and does not open the tenant chat database while hibernated. Its
request spool uses a separately supplied encryption key and stores accepted
cold requests until replay succeeds; replay supplies a stable spool-derived
idempotency key when the caller did not provide one.

Local profiles select file storage explicitly with `-blob-dir`, or select Amazon
Simple Storage Service with `-blob-s3-bucket` and `-blob-s3-prefix`. These
choices are mutually exclusive; the application does not fall back from one to
the other. `-blob-max-bytes` bounds an individual object and is not a storage
selection; it defaults to 100 MiB, so a profile that needs a different ceiling
must state it.
File bytes are never placed in the chat database. The
activator additionally requires a stable replica spool owner plus explicit
maximum queued bytes and request count; overflow is rejected before durable
acceptance. A
distributed profile configures the blob directory on the owning module process,
not on the HTTP-only replica.

For a one-VM deployment, the VM remains the cheap always-on host and only the
activator stays running. For a three-VM dqlite deployment the two steps are
separate and the order matters: `directory` snapshot mode archives a **stopped**
state directory, so the database processes stop before the snapshot is taken,
while the active storage is released only after the manifest is verified and
published. Stopping is reversible — an interrupted hibernation restarts from the
live volume and reads no snapshot — and releasing is not. See
[scale-to-zero](../specs/scale-to-zero.md#snapshot-boundary-by-profile). The
activator host stays up throughout.

The same VM profile maps directly to the major clouds:

| Provider | Activator host | Active database compute | Snapshot/file storage | Lifecycle control |
|---|---|---|---|---|
| AWS | Small EC2 instance or Lambda front door | EC2 instances | S3 | EC2 APIs/systemd |
| Google Cloud | Small Compute Engine VM or Cloud Run front door | Compute Engine VMs | Cloud Storage | Compute Engine APIs/systemd |
| Azure | Small Azure VM or Container Apps front door | Azure VMs | Blob Storage | Azure Compute APIs/systemd |

The provider-neutral VM package MUST also work with other clouds and on-premises
virtualization when it is given compatible object storage and lifecycle hooks.

## Managed-container notes

Amazon ECS services expose an explicit desired task count and can be reduced to
zero. Fargate tasks provide ephemeral storage and ECS supports Cloud Map service
discovery, making a lifecycle-controlled temporary dqlite cluster a target for
qualification.

Cloud Run services scale to zero by default, but their writable filesystem is
disposable and ordinary service ingress terminates HTTP/gRPC. SameOldChat uses
Cloud Run for stateless units and lifecycle-controlled companion database
compute.

Azure Container Apps defaults HTTP apps to zero minimum replicas and supports
internal raw TCP. A three-app dqlite profile is plausible but remains gated on
the qualification suite. A temporary Azure VM profile is a separate explicit
deployment choice, not an automatic substitution.

Phase 0 MUST retain the exact provider documentation revisions used to validate
these assumptions inside SameOldChat's immutable source inventory. Qualification
MUST be repeated when the recorded platform capability set changes.

## Deliverables per provider

`retained_snapshots` is a target, not a setting the application reads: no code
consumes it, and `internal/lifecycle` implements no snapshot retention or
pruning at all, so every published generation is retained forever. The whole
"Common configuration" block above is an illustrative logical schema showing what
a provider profile must decide; it is not a file format the application parses.

Each provider implementation MUST ship:

- infrastructure templates with exact-pinned modules/actions;
- a lifecycle-driver implementation;
- IAM and network policy;
- secret and encryption-key setup;
- cold-wake and scheduled-wake configuration;
- dashboards and alerts;
- cost-sensitive defaults;
- upgrade and rollback instructions; and
- an automated qualification report.

Related documents: [architecture](architecture.md), [operations](operations.md),
[hosting specification](../specs/hosting.md), and
[scale-to-zero specification](../specs/scale-to-zero.md).

## Published container verification

The main-branch release workflow uses the first 12 lowercase hexadecimal
characters of the commit identifier as its immutable release tag. It publishes
three references:

- `ghcr.io/e6qu/someoldchat:<sha12>` is an OCI image index containing exactly
  Linux amd64 and Linux arm64;
- `ghcr.io/e6qu/someoldchat:<sha12>-amd64` is a direct Linux amd64 image
  manifest; and
- `ghcr.io/e6qu/someoldchat:<sha12>-arm64` is a direct Linux arm64 image
  manifest.

Every application image embeds the full immutable commit in the server binary
and exposes it through `SAMEOLDCHAT_RELEASE_REVISION` for Shauth validation. A
manual build must supply the same immutable coordinate explicitly:

```sh
docker build --build-arg RELEASE_REVISION="$(git rev-parse HEAD)" .
```

BuildKit registry attachments are disabled on the publishing build so the
architecture-specific references remain direct image manifests for runtimes
that cannot consume OCI indexes. A second cache-backed BuildKit export produces
an SPDX SBOM for the exact architecture manifest digest. GitHub's native SLSA
provenance generator signs the workflow build context, while the SBOM gate
extracts and validates BuildKit's SPDX 2.3 document before GitHub signs it.
GitHub stores both attestations for the architecture digest without changing
the tag's media type. The workflow
reads all three references back from GitHub Container Registry and fails unless
their media types, digests, and platforms form exactly that shape. It then
removes every package version outside the newest 20 complete release groups,
including incomplete release roots, mixed-tag versions, and untagged versions.
It verifies that every retained root has exactly one direct amd64 sibling and
one direct arm64 sibling, and that at most 60 package versions remain. GitHub
stores signed attestations outside the container package-version records, so
retention cannot delete provenance or SBOM attestations.
Deployments record and use the verified digest for the selected reference and
verify its signed attestations:

```sh
gh attestation verify \
  oci://ghcr.io/e6qu/someoldchat@sha256:<architecture-manifest-digest> \
  --repo e6qu/someoldchat
```
