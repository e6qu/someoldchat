# SameOldChat dependency admission policy

## Purpose

The project uses current dependencies while avoiding the first 24 hours after
publication. This quarantine reduces exposure to newly published malicious or
compromised releases; it does not by itself prevent supply-chain attacks.

## Selection rule

At resolution time `T`, a version is eligible only if all are true:

- it is a stable release from the canonical project source or registry;
- its verified publication time is at or before `T - 24 hours`;
- it has not been yanked, retracted, or deprecated for security reasons;
- its license is approved;
- required integrity and provenance evidence is available;
- no policy-blocking known vulnerability applies to the built product; and
- it satisfies the project's compatibility constraints.

The selected version MUST be the newest eligible version. If publication time
cannot be established, the version is ineligible.

## Covered inputs

The rule applies to runtime, build, development, test, and deployment inputs:

- Go modules and Go toolchain releases;
- HTMX and all browser assets;
- SQLite, dqlite, Go bindings, and native libraries;
- Slack SDK and Bolt releases used by compatibility tests;
- linters, code generators, scanners, and packaging tools;
- container images and operating-system packages; and
- CI actions and reusable workflows.

The rule applies to artifacts and versions selected by SameOldChat. A managed
cloud control plane that the provider updates without customer version choice
is not a resolvable project dependency. Its documented capability set MUST
instead be recorded and re-qualified regularly. Provider-supplied container
images, actions, CLIs, modules, and runtimes that SameOldChat can select remain
fully subject to this policy.

## Evidence

Each pinned input MUST record:

- canonical name and source;
- exact version and immutable revision/digest;
- UTC publication timestamp and evidence source;
- cryptographic checksum;
- provenance/attestation status;
- license;
- direct purpose; and
- whether it ships in the runtime artifact.

Registry publication metadata SHOULD be cross-checked against canonical VCS
release/tag/commit data. Contradictory timestamps MUST fail admission pending
review.

## Pinning rules

- `go.mod` and `go.sum` MUST be committed.
- The Go checksum database MUST remain enabled for public modules.
- Browser assets MUST be vendored and served locally with content hashes.
- Runtime pages MUST NOT fetch executable code from a public CDN.
- Container bases MUST be pinned by digest.
- CI actions MUST be pinned by full commit SHA.
- Native libraries MUST use exact releases and verified source hashes.
- Floating tags, branches, wildcard ranges, and `latest` are forbidden.
- Generated dependency and SBOM files MUST be committed or produced as signed
  release artifacts.

## CI controls

Every dependency-changing pull request MUST run:

1. Age verification against the 24-hour UTC cutoff.
2. Integrity verification for modules, assets, actions, images, and archives.
3. `govulncheck` for Go source and built binaries as applicable.
4. OSV/advisory and container/native-library scanning.
5. License and newly introduced transitive dependency review.
6. Available provenance/attestation verification.
7. SBOM generation.
8. The complete compatibility, persistence, lifecycle, and product test suite.

CI MUST fail closed when age or integrity evidence is unavailable.

## Automation

The committed dependency admission inventory is the machine-readable record of
the selected versions and their evidence. `make dependency-check` MUST run in
local checks and pull-request continuous integration. It fails when an entry
is incomplete, uses a mutable revision or checksum, lacks HTTPS evidence, uses
a prerelease version, has a future publication time, or has not passed the
publication quarantine. Pin syntax for workflow actions and container images
is checked separately by `make workflow-check` and `make container-check`.
CI language runtimes and Terraform MUST use exact versions; `workflow-check`
rejects bare major or minor selections and requires an explicit Terraform
version.

A daily job SHOULD propose the newest eligible version after its quarantine has
elapsed. Updates MUST be narrow, reviewable, and never automatically merged.
Coupled packages MAY update together when separate versions cannot be tested
meaningfully.

The resolver itself and CI actions enforcing this policy are dependencies and
MUST be pinned under the same rules.

## Security-fix handling

A release younger than 24 hours remains ineligible, including an urgent security
release. Until it ages into eligibility, maintainers MUST choose one of:

- retain or downgrade to an older unaffected eligible release;
- disable or remove the affected feature/dependency;
- apply a small reviewed local patch to an eligible source release; or
- suspend the affected build or deployment.

There is no automatic age-policy bypass. A local security patch MUST record its
upstream reference, review, tests, and new artifact digest.

## Dependency minimization

New direct dependencies require a written justification. Preference SHOULD be
given to the Go standard library, existing transitive dependencies, small
auditable packages, maintained canonical projects, and artifacts with signed
provenance. Convenience alone is insufficient when a small local implementation
is clearer and safer.

## Release output

Each release MUST include or reference:

- an SPDX or CycloneDX SBOM;
- exact source and image digests;
- dependency-age and vulnerability reports;
- build provenance;
- signatures for distributed artifacts; and
- the pinned Slack compatibility-source inventory.
