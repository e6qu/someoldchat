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

Application unit and integration tests remain next to the Go packages they
test. This directory is reserved for qualification suites with external
runtime or published-contract dependencies.
