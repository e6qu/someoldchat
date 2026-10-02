# PostgreSQL storage

PostgreSQL is an explicit durable storage profile. The application uses the
pinned `github.com/jackc/pgx/v5` driver through `database/sql` and applies the
PostgreSQL-specific SQL at the storage adapter boundary.

Select it at startup. `-db` is the connection string; startup fails when it is
missing or invalid:

```sh
./bin/sameoldchat \
  -chat-mode local \
  -store postgresql \
  -db 'postgres://sameoldchat:secret@db.example.com:5432/sameoldchat?sslmode=verify-full' \
  -app-credential-key-hex "$SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX" \
  -api-token "$SAMEOLDCHAT_API_TOKEN" \
  -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

In container deployments, set `SAMEOLDCHAT_DATABASE_URL` instead, so the
runtime can inject the URL from its secret store without exposing it in task
definitions or process arguments. It is the default for `-db` in
`cmd/server` local composition and in `cmd/worker`; an explicit `-db` takes
precedence. `-chat-mode grpc` rejects a database DSN because the separate
`chatd` process owns storage.

PostgreSQL is never a fallback: SameOldChat does not switch to it when SQLite or
dqlite configuration fails, and never changes storage profile after startup.

The PostgreSQL server owns durable state and may serve multiple stateless
SameOldChat replicas. Backups, replication, connection limits, transport
security, and failover belong to the PostgreSQL deployment; SameOldChat does
not claim high availability from the client driver alone.

## Qualification

```sh
SAMEOLDCHAT_POSTGRES_DSN='postgres://sameoldchat:sameoldchat@localhost:5432/sameoldchat?sslmode=disable' \
  make test-postgres
```

`SAMEOLDCHAT_POSTGRES_DSN` is required. The target runs the shared repository
contract (storage waves, integration state, and migration path),
`internal/store/postgres`, and `internal/web` against that server with
`-p 1`, because every package shares the one database. CI runs it in the
`postgres` job.

The adapter translates the shared schema's `INTEGER` declarations to
`BIGINT`, because some timestamps are stored as nanoseconds and need the
`int64` range on both backends.

Related documents:

- [Persistence specification](../specs/persistence.md)
- [Deployment guide](deployment.md)
- [Persistence qualification](../tests/persistence-qualification/README.md)
