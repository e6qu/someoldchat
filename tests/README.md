# Tests

This directory contains qualification suites that exercise the application
against browser behavior, official Slack SDKs, and native dqlite behavior.

- [Browser qualification](browser/README.md) checks the server-rendered user
  journey with Playwright.
- [Official SDK qualification](official-sdk-qualification/README.md) checks
  pinned releases of the official Slack SDKs.
- [dqlite qualification](dqlite-qualification/README.md) checks the pinned
  Canonical dqlite binding on Linux with the native library installed.
- The PostgreSQL qualification runs the shared repository contract against a
  real PostgreSQL server when `SAMEOLDCHAT_POSTGRES_DSN` is set.
- [Persistence qualification](persistence-qualification/README.md) runs the
  same repository contract against SQLite, PostgreSQL, and dqlite.

Application unit and integration tests remain next to the Go packages they
test. This directory is reserved for qualification suites with external
runtime or published-contract dependencies.
