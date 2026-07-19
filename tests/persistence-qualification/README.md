# Persistence qualification

This directory contains one backend-neutral repository contract. The default
test run executes it against SQLite. The `dqlite` build profile executes the
same contract against a three-node Canonical dqlite cluster. The `postgres`
build profile executes it against the PostgreSQL server named by
`SAMEOLDCHAT_POSTGRES_DSN`.

Run the SQLite contract with:

```sh
go test ./tests/persistence-qualification
```

Run the dqlite contract with the native dqlite dependencies installed:

```sh
go test -tags dqlite ./tests/persistence-qualification
```

Run the PostgreSQL contract with a reachable PostgreSQL server:

```sh
SAMEOLDCHAT_POSTGRES_DSN='postgres://sameoldchat:sameoldchat@localhost:5432/sameoldchat?sslmode=disable' make test-postgres
```

The shared contract covers normalized user lookup, seeded workspace and
conversation state, message persistence, idempotent message replay, bounded
message listing, search, presence, do-not-disturb state, stars, files, remote
file sharing and updates, reminders, scheduled-message claim and delivery,
workspace settings, user groups and their bindings, and custom emoji.
The package does not replace the broader SQLite repository tests or the
selected dqlite cluster and snapshot tests.
