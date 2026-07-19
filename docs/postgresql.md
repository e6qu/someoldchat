# PostgreSQL storage

SameOldChat supports PostgreSQL as an explicit durable SQL storage profile. The
application uses the pinned `github.com/jackc/pgx/v5` driver through
`database/sql` and applies the PostgreSQL-specific SQL at the storage adapter
boundary.

Select PostgreSQL at startup. The `-db` value is a PostgreSQL connection string
and the application fails at startup when it is missing or invalid:

```sh
./bin/sameoldchat \
  -chat-mode local \
  -store postgresql \
  -db 'postgres://sameoldchat:secret@db.example.com:5432/sameoldchat?sslmode=verify-full' \
  -api-token "$SAMEOLDCHAT_API_TOKEN" \
  -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

For container deployments, set `SAMEOLDCHAT_DATABASE_URL` instead of placing a
connection string in the command line. The environment value is the default for
`-db`; an explicit `-db` flag takes precedence. This lets the runtime obtain
the tenant-specific URL from its secret store without exposing it in task
definitions or process arguments.

PostgreSQL is a separate storage selection. SameOldChat does not switch to
PostgreSQL when SQLite or dqlite configuration fails, and it does not change
storage profiles after startup.

The PostgreSQL server owns durable state and may serve multiple stateless
SameOldChat replicas. Configure PostgreSQL backups, replication, connection
limits, transport security, and failover according to the selected PostgreSQL
deployment. SameOldChat does not claim PostgreSQL high availability from the
client driver alone.

Run the repository qualification against a real PostgreSQL server with:

```sh
SAMEOLDCHAT_POSTGRES_DSN='postgres://sameoldchat:sameoldchat@localhost:5432/sameoldchat?sslmode=disable' \
  make test-postgres
```

The qualification requires `SAMEOLDCHAT_POSTGRES_DSN`; an absent value is an
error. It runs the shared repository contract, including the first published
storage wave and migration path, against the configured server.

Related documents:

- [Persistence specification](../specs/persistence.md)
- [Deployment guide](deployment.md)
- [Persistence qualification](../tests/persistence-qualification/README.md)
