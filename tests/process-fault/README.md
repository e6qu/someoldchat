# Process-fault qualification

`specs/persistence.md` requires that components be "safe to terminate abruptly
and restart through the normal startup/recovery path", and `docs/architecture.md`
lists process faults among the crash/restart tests that run during normal
verification. Every other restart test in this repository stops short of that:
`tests/persistence-qualification` drops a store *handle* and opens a new one,
and the outbox tests model a crash as a lease expiring on a store that never
stopped running. Both are useful, and neither exercises a process that died.

This suite does. It builds `cmd/server`, runs it, writes through the Web API,
sends `SIGKILL` — so no deferred function, flush, checkpoint or shutdown hook
can run — and starts the same command line again against the same database.

## What it covers that a handle reopen does not

- **The startup path against a non-empty database.** The first boot migrates an
  empty file and seeds credentials, memberships and the default channel. The
  second runs that same code against rows that already exist. A migration that
  is not idempotent, or a seed that inserts where it should upsert, fails here
  and passes everywhere else — and it would fail on every production restart,
  not on deployment.
- **Durability at acknowledgement time.** What the API answered `ok` to before
  the kill is required to be readable after it, across three tables written in
  three transactions: the message, a reaction on it, and a pin.

## Running it

    go test ./tests/process-fault

No container, DSN, or second language runtime is required. It builds the server
binary once per test binary and picks a free port per instance, so it is safe
under `go test ./...`.

## Scope

SQLite is the profile under test, because it is the durable profile that needs
no external service. PostgreSQL durability across a *client* restart is covered
by `TestPostgresRestartQualification`; dqlite node failure is covered by
`tests/dqlite-qualification`. What is deliberately not covered here is a fault
injected *between* the write and the acknowledgement — that needs a filesystem
fault injector, not a signal.
