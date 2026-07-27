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
- Floating tags, branches, wildcard ranges, and `latest` are forbidden. This
  includes CI runner labels: `runs-on:` and matrix `runner:` values MUST name an
  exact runner image such as `ubuntu-24.04`, never `ubuntu-latest`, because
  GitHub moves that label between operating-system images and the toolchain a
  green run was produced on is then not the toolchain the next run uses.
- Generated dependency and SBOM files MUST be committed or produced as signed
  release artifacts.

## CI controls

Every dependency-changing pull request MUST run:

1. Age verification against the 24-hour UTC cutoff.
2. Integrity verification for modules, assets, actions, images, archives, and
   CI runner labels.
3. `govulncheck` for Go source and built binaries as applicable.
4. OSV/advisory and container/native-library scanning.
5. License and newly introduced transitive dependency review.
6. Available provenance/attestation verification.
7. SBOM generation.
8. The complete compatibility, persistence, lifecycle, and product test suite.

CI MUST fail closed when age or integrity evidence is unavailable.

Implementation status of these controls, so the policy is not read as a
description of enforcement that does not exist:

| Control | Where it runs |
|---|---|
| 1. Age verification | `make dependency-check` and `make workflow-check` |
| 2. Integrity verification | `make dependency-check`, `go mod verify`, and the digest, commit-SHA, and lock-hash comparisons in `tests/dependency-admission` |
| 3. `govulncheck` over Go source | `make vuln-check` (default and `postgres` build configurations) and `make vuln-check-dqlite` in the dqlite job, which is the only job with the native headers. Both run in the pull-request workflow. Built-binary scanning is not wired up. |
| 4. OSV/advisory and container/native-library scanning | **not implemented** |
| 5. License review | inventory `license` fields are required and reviewed by hand; no automated gate |
| 6. Provenance verification | produced on publication by `publish-container.yml`; not verified on a pull request |
| 7. SBOM generation | produced on publication by `publish-container.yml`; not produced on a pull request |
| 8. Full test suite | `.github/workflows/ci.yml` |

## Automation

The committed dependency admission inventory is the machine-readable record of
the selected direct inputs and their evidence. Every module declared in
`go.mod`, including indirect requirements, must also have archive and `go.mod`
checksums in `go.sum`; this is an integrity check, not a substitute for
provenance evidence.

The inventory covers Go modules, npm packages, CI actions, container images,
Terraform providers, pinned tools (`terraform`, `buf`, `protoc-gen-go-grpc`,
`govulncheck`), and the third-party source checkout the Shauth qualification
executes. Indirect Go modules are covered by `go.sum` checksums and `go mod
verify` only; the inventory records the direct inputs, so a bump of an indirect
module — for example the `golang.org/x/text` bump that cleared GO-2026-5970 — is
integrity-checked but not quarantine-checked.

Three gaps in that coverage, stated so the policy is not read as enforcement
that does not exist:

- Operating-system packages installed by CI (`dqlite-tools-v3`, `libdqlite*`,
  `libuv1-dev`) and the CI language runtimes (Node.js, CPython, Temurin, Deno)
  are enforced structurally — every package must carry an explicit `=version`
  and every runtime version key must be an exact major.minor.patch value — but
  have no inventory entry, so the 24-hour quarantine does not apply to them and
  no publication timestamp or checksum is recorded for them.
- `buf` has an inventory entry and an exact `BUF_VERSION` pin in the `Makefile`,
  but the two are not compared: the Makefile-to-inventory comparison covers only
  `PROTOC_GEN_GO_GRPC_VERSION` and `GOVULNCHECK_VERSION`. `terraform` is
  compared through its `*.tf` `required_version`, not through the `Makefile`.
- Indirect Go modules, as above.

`make dependency-check` MUST run `go list -mod=readonly`
and `go mod verify` before its repository checks. It MUST run in local checks
and pull-request continuous integration. It fails when an entry
is incomplete, uses a mutable revision or checksum, lacks HTTPS evidence, uses
a prerelease version, has a future publication time, or has not passed the
publication quarantine.

`make workflow-check` runs the same verifier for its pin-syntax rules. It rejects
a CI action that is not pinned to a full 40-character commit SHA, a workflow
version key whose value is not an exact major.minor.patch version, an
`apt-get install` package without an explicit `=version`, a workflow container
image without a digest, a `*.tf` `required_version` or provider `version` that is
not exact, and a pinned generator in the `Makefile` whose version disagrees with
the inventory. Every digest-pinned container image and every provider version
must additionally match an inventory entry, so a base-image or provider bump is
subject to the publication quarantine rather than merely being "a digest".
`make container-check` additionally validates the publication workflow's own
shape and its retention behaviour.

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

- an SPDX or CycloneDX SBOM distributed separately from architecture-specific
  OCI image tags;
- exact source and image digests;
- dependency-age and vulnerability reports;
- signed provenance and SBOM attestations bound to each architecture image
  digest;
- verification that published architecture tags are direct image manifests
  and the generic tag is exactly the Linux amd64 and Linux arm64 image index;
- the pinned Slack compatibility-source inventory.
