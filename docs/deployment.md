# SameOldChat deployment guide

This guide separates current infrastructure from deployment profiles that still
need qualification. The current provider-specific implementation is the AWS
Elastic Container Service module in
[deploy/ecs-scale-zero](../deploy/ecs-scale-zero/README.md).
That module provides request-triggered task activation and scale-down. The
provider-neutral Go lifecycle activator remains a separate deployment unit for
hibernation, snapshot publication, and restore.

## Deployment philosophy

SameOldChat ships one provider-neutral application and multiple lifecycle
drivers. A cloud service is not considered supported merely because it can run
the container image; it must also satisfy persistence, peer networking,
hibernation, wake, fencing, and recovery tests.

## Capability matrix

| Profile | Stateless tiers | SQLite | PostgreSQL | dqlite | Cold database |
|---|---|---|---|---|---|
| Linux VM | Native | Recommended for one VM | Supported with a durable PostgreSQL service | Supported on 3+ VMs | Snapshot, then stop units/VMs |
| AWS ECS/Fargate | Native | Conditional single-owner | Supported when PostgreSQL is external to ECS | Targeted via stable ECS services | S3 snapshot, desired count 0 |
| Google Cloud Run | Native | Not authoritative on local disk | Use an external PostgreSQL service | Companion compute required | Cloud Storage snapshot, compute 0 |
| Azure Container Apps | Native | Conditional single-owner | Use an external PostgreSQL service | Conditional raw-TCP profile; VM profile is a separate qualified option | Blob snapshot, replicas/VMs 0 |

“Conditional” means the profile must pass the version-pinned qualification
suite before production use.

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
  retained_snapshots: 3

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
- systemd units for activator, server, worker, and lifecycle commands;
- SQLite for the simplest topology;
- Caddy, nginx, or a cloud load balancer for TLS;
- an S3-compatible bucket for snapshots and files; and
- a narrowly scoped credential allowing the activator to start stopped units or
  additional database VMs.

The provider-neutral `sameoldchat-activator` requires a durable SQLite control
DSN, an explicit snapshot store (`filesystem` with `-snapshot-root` or `s3`
with `-snapshot-s3-bucket` and optional `-snapshot-s3-prefix`), a forward URL,
an authenticated control token, an explicit snapshot mode (`file` for one
database file or `directory` for a stopped dqlite state directory), and every
lifecycle command at startup.
Commands receive the fencing
generation through `SAMEOLDCHAT_LIFECYCLE_GENERATION`; persistence startup also
receives the selected backend, snapshot artifact, and schema version. Missing
commands, keys, or endpoints fail startup. The activator owns lifecycle
metadata only and does not open the tenant chat database while hibernated. Its
request spool uses a separately supplied encryption key and stores accepted
cold requests until replay succeeds; replay supplies a stable spool-derived
idempotency key when the caller did not provide one.

Local profiles select file storage explicitly with `-blob-dir` and
`-blob-max-bytes`, or select Amazon Simple Storage Service with
`-blob-s3-bucket`, `-blob-s3-prefix`, and `-blob-max-bytes`. These choices are
mutually exclusive; the application does not fall back from one to the other.
File bytes are never placed in the chat database. The
activator additionally requires a stable replica spool owner plus explicit
maximum queued bytes and request count; overflow is rejected before durable
acceptance. A
distributed profile configures the blob directory on the owning module process,
not on the HTTP-only replica.

For a one-VM deployment, the VM remains the cheap always-on host and only the
activator stays running. For a three-VM dqlite deployment, database VMs may be
stopped or released after the verified snapshot while the activator host stays
up.

The same VM profile maps directly to the major clouds:

| Provider | Activator host | Active database compute | Snapshot/file storage | Lifecycle control |
|---|---|---|---|---|
| AWS | Small EC2 instance or Lambda front door | EC2 instances | S3 | EC2 APIs/systemd |
| Google Cloud | Small Compute Engine VM or Cloud Run front door | Compute Engine VMs | Cloud Storage | Compute Engine APIs/systemd |
| Azure | Small Azure VM or Container Apps front door | Azure VMs | Blob Storage | Azure Compute APIs/systemd |

The provider-neutral VM package MUST also work with other clouds and on-premises
virtualization when it is given compatible object storage and lifecycle hooks.

## Managed-container notes

AWS ECS services expose an explicit desired task count and can be reduced to
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

BuildKit registry attachments are disabled on the publishing build so the
architecture-specific references remain direct image manifests for runtimes
that cannot consume OCI indexes. A second cache-backed BuildKit export produces
an SPDX SBOM for the exact architecture manifest digest. GitHub signs and
stores separate provenance and SBOM attestations for that digest; they remain
verifiable without changing the tag's media type. The SLSA v1 provenance uses
the originating GitHub Actions run URL as its non-empty HTTPS builder identity;
the extraction gate rejects incomplete provenance before signing. The workflow
reads all three references back from GitHub Container Registry and fails unless
their media types, digests, and platforms form exactly that shape. It then
removes every package version outside the newest 20 complete release groups,
including untagged versions, and verifies that at most 60 package versions
remain.
Deployments record and use the verified digest for the selected reference and
verify its signed attestations:

```sh
gh attestation verify \
  oci://ghcr.io/e6qu/someoldchat@sha256:<architecture-manifest-digest> \
  --repo e6qu/someoldchat
```
