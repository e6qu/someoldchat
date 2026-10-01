# SameOldChat deployment guide

This guide separates current infrastructure from deployment profiles that still
need qualification. The only provider-specific implementation is Amazon Elastic
Container Service (ECS), split across two Terraform modules:

- [terraform/ecs-runtime](../terraform/ecs-runtime/README.md) owns the durable
  application resources — the private uploads bucket, the API-token,
  application-credential-key, and authorization-state secrets, the
  least-privilege task-role policy, and the `environment`/`secrets` values a
  task needs to start; and
- [deploy/ecs-scale-zero](../deploy/ecs-scale-zero/README.md) owns
  request-triggered task activation and scale-down for the HTTP path, plus the
  always-on multi-workspace worker, always-on WebSocket edge, and the
  scale-to-zero WebSocket application tier.

Both pin the same exact AWS provider version, so one root configuration can
consume them together. Neither deploys the provider-neutral
[lifecycle activator](#lifecycle-activator), which owns hibernation, snapshot
publication, and restore; the operator deploys it separately.

For process roles and composition, see [architecture](architecture.md); for
running a deployment, see [operations](operations.md).

## Deployment philosophy

SameOldChat ships one provider-neutral application and multiple lifecycle
drivers. A cloud service is not considered supported merely because it can run
the container image; it must also satisfy persistence, peer networking,
hibernation, wake, fencing, and recovery tests.

## Capability matrix

| Profile | Stateless tiers | SQLite | PostgreSQL | dqlite | Cold database |
|---|---|---|---|---|---|
| Linux VM | Native | Recommended for one VM | Supported with a durable PostgreSQL service | Supported on 3+ VMs | Snapshot, then stop units/VMs |
| Amazon ECS on AWS Fargate | Native | Conditional single-owner | Supported when PostgreSQL is external to ECS | Targeted via stable ECS services | S3 snapshot, desired count 0 |
| Google Cloud Run | Native | Not authoritative on local disk | Use an external PostgreSQL service | Companion compute required | Cloud Storage snapshot, compute 0 |
| Azure Container Apps | Native | Conditional single-owner | Use an external PostgreSQL service | Conditional raw-TCP profile; VM profile is a separate qualified option | Blob snapshot, replicas/VMs 0 |

The matrix describes intended capability, not shipped infrastructure.
“Conditional” means the profile must pass the version-pinned qualification
suite before production use. Only the Amazon ECS row has templates in this
repository, including a durable PostgreSQL-backed worker for scheduled messages
and app-event delivery; those templates do not deploy the hibernation state
machine or snapshot and restore. There are no systemd units, cloud-init files,
Cloud Run services, or Container Apps templates in the repository, so the other
rows are targets an operator must build (see
[PLAN.md Phase 4](../PLAN.md#phase-4-hosting-profiles)). Using the
qualification vocabulary of the [hosting specification](../specs/hosting.md):

| Profile | Qualification level |
|---|---|
| Linux VM | experimental — no templates shipped |
| Amazon ECS on AWS Fargate | experimental — request-triggered activation shipped; hibernation and snapshot/restore not shipped |
| Google Cloud Run | experimental — no templates shipped |
| Azure Container Apps | experimental — no templates shipped |

## Common configuration

Every deployment profile decides the same logical configuration. This block is
an illustrative schema, not a file format the application parses:

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
  retained_snapshots: 3   # target only; nothing implements retention yet

scaling:
  server_min: 0
  server_max: 20
  worker_min: 0
  worker_max: 10
```

`retained_snapshots` is read by no code: every published generation is retained
(see [snapshot retention](operations.md#snapshot-retention-and-verification)).
Provider-specific files bind logical object storage, lifecycle metadata,
identity, networking, and compute operations without leaking them into domain
configuration.

## Self-hosted VM installation

The first supported installation SHOULD be a Linux VM with:

- the SameOldChat binaries built with `make build` (the published OCI image
  contains only `cmd/server`; see
  [replicas and images](operations.md#replicas-and-images));
- systemd units for the activator, server, worker, socketmode-worker, blobgc,
  and lifecycle commands (none are shipped; the operator writes them);
- SQLite for the simplest topology;
- Caddy, nginx, or a cloud load balancer for TLS;
- an S3-compatible bucket for snapshots and files; and
- a narrowly scoped credential allowing the activator to start stopped units or
  additional database VMs.

### Lifecycle activator

No Terraform module in this repository deploys `sameoldchat-activator`, so the
list below is its startup contract. `make task-flags-check` cross-references
every flag named between the markers against the binary's own flag set, so a
flag the binary drops, renames, or newly requires fails the check.

<!-- flag-contract: cmd/activator -->
`sameoldchat-activator` fails loudly at startup when any of these is missing:

- the control plane: `-listen`, `-forward-url`, `-control-token`, `-state-db`;
- the snapshot store: `-snapshot-store` (`filesystem` or `s3`), `-snapshot-mode`
  (`file` for one database file, `directory` for a stopped dqlite state
  directory), `-snapshot-source`, `-snapshot-output`, `-snapshot-max-bytes`,
  `-snapshot-key-id`, `-snapshot-encryption-key-hex`,
  `-snapshot-signing-key-hex`;
- the recorded identity of what a snapshot contains: `-backend`,
  `-schema-version`, `-application-version`;
- the cold-request spool: `-request-spool-key-hex`, `-request-spool-owner`
  (a stable, unique replica owner), `-request-spool-max-bytes`,
  `-request-spool-max-requests`;
- every lifecycle command: `-cmd-inspect`, `-cmd-run-migration`,
  `-cmd-start-persistence`, `-cmd-stop-persistence`, `-cmd-start-workers`,
  `-cmd-stop-workers`, `-cmd-start-servers`, `-cmd-drain-servers`,
  `-cmd-release-storage`.

The snapshot-store settings are conditionally required and mutually exclusive:
`-snapshot-root` is required for `-snapshot-store=filesystem` and refused for
`s3`; `-snapshot-s3-bucket` is required for `-snapshot-store=s3` and refused for
`filesystem`, and `-snapshot-s3-prefix` applies only to `s3`. Otherwise the
process exits 2 rather than choosing a snapshot store, key, or command for the
operator.

Three settings have defaults:

| Flag | Default | Effect |
|---|---|---|
| `-wake-deadline` | `2m` | How long a caller waits for a cold start before the activator gives up on that request. |
| `-wake-safety-margin` | `5m` | Measured restore time plus margin reserved before a scheduled wake deadline. Hibernation is refused with 409 and `Retry-After` when a published deadline falls inside it, and the scheduled-wake loop polls at a tenth of it. |
| `-request-max-bytes` | `4194304` | One cap for both the spooled request body and the captured response, so the two cannot diverge. |
<!-- /flag-contract -->

Commands receive the fencing generation through
`SAMEOLDCHAT_LIFECYCLE_GENERATION`; persistence startup also receives the
selected backend, snapshot artifact, and schema version. The activator owns
lifecycle metadata only and does not open the tenant chat database while
hibernated. Its request spool uses a separately supplied encryption key and
stores accepted cold requests until replay succeeds; overflow beyond the
configured bytes or request count is rejected before durable acceptance, and
replay supplies a stable spool-derived idempotency key when the caller did not
provide one. The control routes it serves are described in
[operations](operations.md#disaster-recovery).

### File storage

Profiles select file storage explicitly with `-blob-dir`, or select Amazon
Simple Storage Service with `-blob-s3-bucket` and `-blob-s3-prefix`. These
choices are mutually exclusive; the application does not fall back from one to
the other. `-blob-max-bytes` bounds an individual object and is not a storage
selection; it defaults to 100 MiB, so a profile that needs a different ceiling
must state it. File bytes are never placed in the chat database. A distributed
profile configures the blob store on the owning module process
(`sameoldchat-chatd`), not on the HTTP-only replica.

### Hibernating VM profiles

For a one-VM deployment, the VM remains the cheap always-on host and only the
activator stays running. For a three-VM dqlite deployment the two steps are
separate and the order matters: `directory` snapshot mode archives a **stopped**
state directory, so the database processes stop before the snapshot is taken,
while the active storage is released only after the manifest is verified and
published. Stopping is reversible and releasing is not; the crash-recovery rule
for each interrupted phase is in
[operations](operations.md#normal-hibernation). The activator host stays up
throughout.

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
disposable and ordinary service ingress terminates HTTP/gRPC. A Cloud Run
profile therefore uses Cloud Run for stateless units and lifecycle-controlled
companion database compute.

Azure Container Apps defaults HTTP apps to zero minimum replicas and supports
internal raw TCP. A three-app dqlite profile is plausible but remains gated on
the qualification suite. A temporary Azure VM profile is a separate explicit
deployment choice, not an automatic substitution.

The exact provider documentation revisions used to validate these assumptions
MUST be retained in SameOldChat's immutable source inventory, and qualification
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

## Published container verification

[`publish-container.yml`](../.github/workflows/publish-container.yml) runs on
every push to `main` and publishes only `cmd/server`. It uses the first 12
lowercase hexadecimal characters of the commit identifier as the immutable
release tag and publishes three references:

- `ghcr.io/e6qu/someoldchat:<sha12>` is an OCI image index containing exactly
  Linux amd64 and Linux arm64;
- `ghcr.io/e6qu/someoldchat:<sha12>-amd64` is a direct Linux amd64 image
  manifest; and
- `ghcr.io/e6qu/someoldchat:<sha12>-arm64` is a direct Linux arm64 image
  manifest.

The image build embeds the full commit in the server binary, which reports its
first 12 characters — the image tag — on the Shauth validation page
(see [authentication](authentication.md)). `SAMEOLDCHAT_RELEASE_REVISION` or
`-release-revision` overrides the embedded value. A manual build must supply
the commit explicitly; the Dockerfile refuses to build without it:

```sh
docker build --build-arg RELEASE_REVISION="$(git rev-parse HEAD)" .
```

Deployments that register application monitoring with Shauth also provide a
unique `SAMEOLDCHAT_MONITORING_TOKEN` secret (see
[health and monitoring endpoints](operations.md#health-and-monitoring-endpoints)).
The same value belongs only in Shauth's application registration and this
workload; it must not reuse an API, session, or OpenID Connect client
credential.

BuildKit registry attachments are disabled on the publishing build so the
architecture-specific references remain direct image manifests for runtimes
that cannot consume OCI indexes. A second cache-backed BuildKit export produces
an SPDX SBOM for the exact architecture manifest digest. GitHub's native SLSA
provenance generator signs the workflow build context, while the SBOM gate
extracts and validates BuildKit's SPDX 2.3 document before GitHub signs it.
GitHub stores both attestations for the architecture digest without changing
the tag's media type. The workflow reads all three references back from GitHub
Container Registry and fails unless their media types, digests, and platforms
form exactly that shape.

The workflow then removes every package version outside the newest 20 complete
release groups, including incomplete release roots, mixed-tag versions, and
untagged versions. It verifies that every retained root has exactly one direct
amd64 sibling and one direct arm64 sibling, and that at most 60 package
versions remain. GitHub stores signed attestations outside the container
package-version records, so retention cannot delete provenance or SBOM
attestations.

Deployments record and use the verified digest for the selected reference and
verify its signed attestations:

```sh
gh attestation verify \
  oci://ghcr.io/e6qu/someoldchat@sha256:<architecture-manifest-digest> \
  --repo e6qu/someoldchat
```

Related documents: [architecture](architecture.md), [operations](operations.md),
[hosting specification](../specs/hosting.md), and
[scale-to-zero specification](../specs/scale-to-zero.md).
