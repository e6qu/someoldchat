# Blob lifecycle

SameOldChat stores file and user-photo bytes outside the state store. The state
store remains authoritative for which objects are live. A successful mutation
first writes the bounded object and then commits metadata and a durable cleanup
event when the previous object must be removed. The cleanup worker claims those
events with a lease, deletes the object, and acknowledges the event. It renews
the event lease while the object store operation is running, claims at most the
configured worker limit one event at a time, and does not retain an unbounded
batch. An expired lease makes the work visible to another replica after a
crash.

The `sameoldchat-blobgc` binary also supports a bounded reconciliation audit.
It streams live file and user-photo references from the selected state store
and provider objects from the selected blob store. It reports orphan objects
and metadata that points at missing objects. It fails on malformed provider
records, invalid references, provider errors, and result limits; it does not
silently treat an unavailable provider as empty.

Run an audit for one workspace:

```sh
./bin/sameoldchat-blobgc \
  -store postgresql \
  -db "$SAMEOLDCHAT_POSTGRES_DSN" \
  -blob-s3-bucket sameoldchat \
  -workspace T1 \
  -owner blob-auditor \
  -audit
```

Add `-enqueue-orphans` only after reviewing the audit output. This writes the
orphan keys to the durable cleanup outbox. The regular cleanup worker performs
the deletion under its existing lease and treats an already absent object as
success. Missing objects are never repaired by guessing a replacement.

The audit keeps the result set bounded by `-max-audit-results`. A large result
is an operational condition that requires an explicit larger limit or a
separate investigation. It is not truncated silently.

Filesystem and Amazon Simple Storage Service providers implement the same
bounded enumeration contract. In monolithic mode the reconciler calls the
state store directly. In separate mode the module boundary remains explicit;
the state and blob owners must expose the same durable contract before a
reconciler is started.

Related documents:

- [Operations](operations.md)
- [Persistence specification](../specs/persistence.md)
- [Scale-to-zero specification](../specs/scale-to-zero.md)
- [dqlite qualification](dqlite.md)
- [PostgreSQL storage](postgresql.md)
