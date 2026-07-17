# Persistence qualification

This directory contains one backend-neutral repository contract. The default
test run executes it against SQLite. The `dqlite` build profile executes the
same contract against a three-node Canonical dqlite cluster.

Run the SQLite contract with:

```sh
go test ./tests/persistence-qualification
```

Run the dqlite contract with the native dqlite dependencies installed:

```sh
go test -tags dqlite ./tests/persistence-qualification
```

The contract currently covers normalized user lookup, seeded workspace and
conversation state, message persistence, idempotent message replay, and
bounded message listing. The package does not replace the broader SQLite
repository tests or the selected dqlite cluster and snapshot tests.
