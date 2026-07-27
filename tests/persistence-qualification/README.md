# Persistence qualification

This directory contains one backend-neutral repository contract. Every storage
profile the product can be configured with runs it: the default test run executes
it against **SQLite and the in-memory repository**, and the `dqlite` and
`postgres` build profiles run the same contract against their engines.

`-store memory` is a selectable storage profile, not a test double, so it belongs
in this suite. It was previously excluded, which is why the in-memory profile
could disagree with the SQL profiles about validation, referential integrity,
sentinel errors, ordering and normalization without anything failing. The `dqlite` build profile executes the
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
workspace settings, user groups and their bindings, custom emoji, and the
integration state used by OAuth, views, workflows, dialogs, app approvals,
invites, conversation preferences, calls, and RTM connections. It also
qualifies durable event replay, topic-specific claims, lease renewal, delayed
release, acknowledgement, and duplicate-acknowledgement rejection.
The package does not replace the broader SQLite repository tests or the
selected dqlite cluster and snapshot tests.

## Divergence contracts

`divergence_test.go` holds the contracts the storage profiles were verified to
answer differently, so a profile cannot drift again: chronological message order
and keyset-pagination completeness, unread counting against a read cursor,
outbox lease fencing for an expired owner, exclusion of repository-internal
topics from client replay, actor attribution on stored events, Unicode-safe
e-mail identity, literal treatment of `LIKE` metacharacters in conversation
search, sentinel errors for referential and uniqueness failures, refusal to
revive an expired Socket Mode connection, all-or-nothing Socket Mode batches, and
rejection of invalid seed input.

## Known follow-up

The in-memory `Seed*` helpers take no `context.Context`, while the SQL
repositories' take one; `memory_test.go` bridges the difference. Unifying the
signatures means editing roughly two hundred call sites across `internal/service`,
`internal/web`, `internal/api/slack`, `internal/modules/chat/transport/grpc`,
`internal/scheduler`, `internal/outbox`, `internal/blob`, `tests/load` and
`tests/official-sdk-qualification`, which is out of scope for a persistence-layer
change. The helpers already return `error` on the same conditions the SQL
repositories reject, which is the part of the divergence that affected behaviour.
