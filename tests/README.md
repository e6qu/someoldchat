# Tests

This directory contains qualification suites that exercise the application
against browser behavior, official Slack SDKs, and native dqlite behavior.

- [Browser qualification](browser/README.md) checks the server-rendered user
  journey with Playwright.
- The Shauth SSO qualification runs the provider's exact pinned validator
  against real Shauth, Ory Hydra, PostgreSQL, and two isolated SameOldChat
  relying parties.
- [Official SDK qualification](official-sdk-qualification/README.md) checks
  pinned releases of the official Slack SDKs.
- [dqlite qualification](dqlite-qualification/README.md) checks the pinned
  Canonical dqlite binding on Linux with the native library installed.
- The PostgreSQL qualification runs the shared repository contract against a
  real PostgreSQL server when `SAMEOLDCHAT_POSTGRES_DSN` is set.
- [Persistence qualification](persistence-qualification/README.md) runs the
  same repository contract against SQLite, PostgreSQL, and dqlite, including
  the restart contracts that drop the store handle and open it again.
- [Process-fault qualification](process-fault/README.md) runs the real
  `cmd/server` binary, kills it with `SIGKILL`, and starts it again on the same
  database. It needs no external runtime, so it runs under a plain
  `go test ./...` and therefore under `make check`.
- [Load tests](load/README.md) exercise bounded concurrent writes and
  pagination invariants against the in-memory repository.
- The authorization matrix in `authorization` drives every `chatapi.Service`
  operation as an owner, an admin, a member, a multi-channel guest, a
  single-channel guest, a deactivated account and an identifier belonging to
  nobody. The operation set is derived by reflection, so an operation that
  arrives without a declared authority fails rather than defaulting to "anybody
  may". It runs under a plain `go test ./...`.
- The policy-enforcement gate in `policy` requires every policy-shaped store
  reader to name where it is read back to decide something, or be recorded as
  shown-only, or be recorded as unapplied with a reason — and the unapplied set
  only shrinks. It runs under a plain `go test ./...`.
- The lifecycle gate in `lifecycle` derives every status type from the domain
  and requires each to declare its states, which of them are terminal, which
  deliberately never finish, and what may follow what. A lifecycle with a driver
  is held to that declaration by the real service: every move the machine
  forbids must be refused and every move it allows must be taken. It runs under
  a plain `go test ./...`.
- The guard-mutation gate in `mutation` strips every authorization guard
  standing in front of one operation in `internal/service` and requires a suite
  to notice. Whole operations rather than single guards: where a function holds
  two, removing either leaves the other to refuse, so a per-guard sweep measures
  redundancy and reports it as absence. It is the answer to a question a green
  suite cannot ask about itself, and it is the only suite here skipped by
  default — each operation is a separate compile and suite run, so it needs
  `SAMEOLDCHAT_MUTATION=1` or `make test-mutation`.

Application unit and integration tests remain next to the Go packages they
test. This directory is reserved for qualification suites with external
runtime or published-contract dependencies.
