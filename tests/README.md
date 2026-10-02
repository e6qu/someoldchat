# Tests

Unit and integration tests live next to the Go packages they test. This
directory holds cross-package qualification suites and repository gates.

## Suites that need an external runtime or service

- [Browser qualification](browser/README.md) checks the server-rendered user
  journeys with Playwright (`make browser-qualification`).
- The Shauth SSO qualification (`make shauth-sso-qualification`, driven by
  `scripts/test-shauth-sso.sh`) runs the provider's exact pinned validator
  against real Shauth, Ory Hydra, PostgreSQL, and two isolated SameOldChat
  relying parties; see [browser qualification](browser/README.md).
- [Official SDK qualification](official-sdk-qualification/README.md) checks
  pinned releases of the official Slack SDKs (`make sdk-qualification`).
- `external-contract-qualification` (`make external-contract-qualification`)
  fetches the official Slack help and API pages that the journey catalog cites
  and fails when a cited contract statement is no longer there.
- [dqlite qualification](dqlite-qualification/README.md) checks the pinned
  Canonical dqlite binding on Linux with the native library installed.
- [Persistence qualification](persistence-qualification/README.md) runs one
  repository contract, including restart contracts that drop the store handle
  and open it again, against every storage profile: SQLite and memory by
  default, dqlite under `make test-dqlite`, and PostgreSQL under
  `make test-postgres` with `SAMEOLDCHAT_POSTGRES_DSN` set.

## Gates that run under `go test ./...`

These need no external runtime, so they run under `make check`.

- [Process-fault qualification](process-fault/README.md) runs the real
  `cmd/server` binary, kills it with `SIGKILL`, and starts it again on the same
  database.
- `authorization` drives every `chatapi.Service` operation as an owner, an
  admin, a member, a multi-channel guest, a single-channel guest, a deactivated
  account, and an identifier belonging to nobody. The operation set is derived
  by reflection, so an operation without a declared authority fails rather than
  defaulting to "anybody may".
- `policy` requires every policy-shaped store reader to name where it is read
  back to decide something, or be recorded as shown-only, or be recorded as
  unapplied with a reason. The unapplied set may only shrink.
- `lifecycle` derives every status type from the domain and requires each to
  declare its states, which are terminal, which deliberately never finish, and
  what may follow what. A lifecycle with a driver is held to that declaration by
  the real service: every move the machine forbids must be refused and every
  move it allows must be taken.
- `fuzzcoverage` requires every `Fuzz` target in the tree to be run by
  `make test-fuzz`, and requires that gate to name no deleted target.

## Gates with their own targets

- `dependency-admission` (`make dependency-check`, part of `make check`)
  verifies dependency pins and the 24-hour publication quarantine recorded in
  [`specs/dependency-admission.yaml`](../specs/dependency-admission.yaml).
- [Load tests](load/README.md) exercise bounded concurrent writes, recovery, and
  pagination invariants against the in-memory repository. They also run under
  `go test ./...`; `make test-load` runs them alone without the test cache.
- `mutation` strips every authorization guard in front of one operation in
  `internal/service` and requires a suite to notice. It removes whole
  operations' guards rather than single guards: where a function holds two,
  removing either leaves the other to refuse, so a per-guard sweep would report
  redundancy as absence. Each operation is a separate compile and suite run, so
  it is skipped unless `SAMEOLDCHAT_MUTATION=1` is set; run it with
  `make test-mutation`.
