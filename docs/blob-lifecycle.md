# Blob lifecycle

SameOldChat stores file and user-photo bytes outside the state store. The state
store remains authoritative for which objects are live. A mutation first writes
the bounded object, then commits its metadata together with a durable cleanup
event when a previous object must be removed.

## Cleanup worker

`sameoldchat-blobgc` without `-audit` is the cleanup worker. It claims cleanup
events under a lease, deletes each object, and acknowledges the event. It claims
one event at a time, up to `-batch-size` per cleanup topic (file and user photo) per pass, renews the lease while the
object-store call runs, and treats an already absent object as success. An
expired lease makes the work visible to another replica after a crash. Each
replica needs a distinct `-owner`; `-lease` and `-poll` tune the claim cycle.

## Reconciliation audit

`-audit` runs a bounded reconciliation instead. It enumerates provider objects
from the selected blob store **first**, then streams live file and user-photo
references from the state store, and reports orphan objects and metadata that
points at missing objects. It fails on malformed provider records, invalid
references, provider errors, and result limits; an unavailable provider is
never treated as empty.

The walk order is a correctness requirement. A mutation writes the object
before committing the metadata that references it, so walking references first
would classify every blob uploaded during the audit as an orphan. Two further
rules close the remaining windows:

- an unreferenced object counts as an orphan only once it is older than
  `-min-orphan-age` (default `1h`). A younger one, or one whose modification
  time the provider does not report, is held back and counted as
  `too_recent_for_orphan_cleanup` in the audit log line. `-min-orphan-age`
  must be at least the 15-minute external upload window and should comfortably
  exceed the longest upload the deployment accepts, because deleting live bytes
  is unrecoverable while deferring an orphan costs one audit cycle;
- a reference with no enumerated object is re-read directly from the provider
  before it is reported missing, because a reference committed after the
  object walk finished is present but was not enumerated.

Run an audit for one workspace (cleanup-only flags such as `-owner` are
rejected in audit mode):

```sh
./bin/sameoldchat-blobgc \
  -store postgresql \
  -db "$SAMEOLDCHAT_POSTGRES_DSN" \
  -blob-s3-bucket sameoldchat \
  -workspace T1 \
  -audit \
  -min-orphan-age 24h
```

`-enqueue-orphans` requires `-audit` and writes the reported orphan keys to the
durable cleanup outbox; the cleanup worker then deletes them under its lease.
Missing objects are never repaired by guessing a replacement.

Review the audit output anyway: an unexpected orphan or missing count is
evidence of a real defect or an interrupted cleanup, and a nonzero
`too_recent_for_orphan_cleanup` means a later audit will report more orphans.

`-max-audit-results` (default 1000) bounds the result set. Exceeding it is an
error, not a silent truncation; raise the limit explicitly or investigate
separately.

The filesystem (`-blob-dir`) and Amazon S3 (`-blob-s3-bucket`) providers
implement the same bounded enumeration contract. In local composition the
reconciler calls the state store directly. In distributed composition the
state and blob owners must expose the same durable contract before a
reconciler is started.

Related documents:

- [Operations](operations.md)
- [Persistence specification](../specs/persistence.md)
- [Scale-to-zero specification](../specs/scale-to-zero.md)
- [dqlite qualification](dqlite.md)
- [PostgreSQL storage](postgresql.md)
