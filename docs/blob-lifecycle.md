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
It enumerates provider objects from the selected blob store **first**, then
streams live file and user-photo references from the selected state store. It
reports orphan objects and metadata that points at missing objects. It fails on
malformed provider records, invalid references, provider errors, and result
limits; it does not silently treat an unavailable provider as empty.

That order is a correctness requirement, not a preference. A mutation writes the
object before it commits the metadata that references it, so walking references
first classified every blob uploaded during the audit as an orphan — and with
`-enqueue-orphans` deleted its bytes while its metadata stayed live.

Two further rules close the remaining windows:

- an unreferenced object is an orphan only once it is older than
  `-min-orphan-age` (default `1h`). A younger one, or one whose modification
  time the provider does not report, is held back and counted in the audit line
  as `too_recent_for_orphan_cleanup`. Deleting live bytes is unrecoverable,
  while deferring an orphan costs one audit cycle; `-min-orphan-age` must be
  positive, and it must comfortably exceed the longest upload the deployment
  accepts;
- a reference with no enumerated object is re-read directly from the provider
  before it is reported as missing, because a reference committed after the
  object walk finished is present but was not enumerated.

Run an audit for one workspace:

```sh
./bin/sameoldchat-blobgc \
  -store postgresql \
  -db "$SAMEOLDCHAT_POSTGRES_DSN" \
  -blob-s3-bucket sameoldchat \
  -workspace T1 \
  -owner blob-auditor \
  -audit \
  -min-orphan-age 24h
```

`-enqueue-orphans` requires `-audit` and writes the reported orphan keys to the
durable cleanup outbox. The regular cleanup worker performs the deletion under
its existing lease and treats an already absent object as success. Missing
objects are never repaired by guessing a replacement.

Reviewing the audit output before enqueueing is no longer the mitigation for
false orphans — the walk order and `-min-orphan-age` are. Review is still worth
doing for what it does tell an operator: an unexpected orphan or missing count
is evidence of a real defect or of an interrupted cleanup, and a nonzero
`too_recent_for_orphan_cleanup` means a later audit will report more orphans.

The audit keeps the result set bounded by `-max-audit-results`. A large result
is an operational condition that requires an explicit larger limit or a
separate investigation. It is not truncated silently.

Filesystem and Amazon Simple Storage Service providers implement the same
bounded enumeration contract. In local composition the reconciler calls the
state store directly. In distributed composition the module boundary remains
explicit;
the state and blob owners must expose the same durable contract before a
reconciler is started.

Related documents:

- [Operations](operations.md)
- [Persistence specification](../specs/persistence.md)
- [Scale-to-zero specification](../specs/scale-to-zero.md)
- [dqlite qualification](dqlite.md)
- [PostgreSQL storage](postgresql.md)
