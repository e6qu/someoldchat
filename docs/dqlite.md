# dqlite qualification

dqlite is an explicit `dqlite` build profile using Canonical's
`github.com/canonical/go-dqlite/v3` binding pinned in `go.mod`. It is not part
of the default static build because the binding requires the native dqlite
library and headers, so build and test it on a Linux host with the matching
native release installed (the binding is not a portable macOS dependency):

```sh
make build-dqlite   # bin/sameoldchat-dqlite, bin/sameoldchat-chatd-dqlite, bin/sameoldchat-blobgc-dqlite
make test-dqlite    # go test -p 1 -tags dqlite ./...
```

The dqlite-specific suite is [`tests/dqlite-qualification`](../tests/dqlite-qualification/README.md);
the shared repository contract in
[`tests/persistence-qualification`](../tests/persistence-qualification/README.md)
runs against dqlite in the same profile. CI runs both in the `dqlite` job.

## Running a cluster

The first node bootstraps with an empty seed list:

```sh
bin/sameoldchat-dqlite -chat-mode local -store dqlite \
  -dqlite-directory /var/lib/sameoldchat/dqlite \
  -dqlite-address node-a:19001 \
  -dqlite-database sameoldchat \
  -app-credential-key-hex "$SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX" \
  -api-token xoxb-production -session-token browser-session
```

Additional nodes name the first node as their seed:

```sh
bin/sameoldchat-dqlite -chat-mode local -store dqlite \
  -dqlite-directory /var/lib/sameoldchat/dqlite-node-b \
  -dqlite-address node-b:19001 \
  -dqlite-cluster node-a:19001 \
  -dqlite-database sameoldchat \
  -app-credential-key-hex "$SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX" \
  -api-token xoxb-production -session-token browser-session
```

Start a third node the same way with its own state directory and address.
Every node takes the same `-app-credential-key-hex`, as any durable store does.

Nodes replicate over plain TCP: the adapter does not yet authenticate or
encrypt node-to-node traffic, which [the persistence
specification](../specs/persistence.md#dqlite-adapter) requires. Until it does,
the `-dqlite-address` listeners MUST be reachable only on a private network
that carries nothing but the cluster's members.
`chatd`, `worker`, `socketmode-worker`, and `blobgc` accept the same
`-dqlite-*` flags. Missing directory, address, or database settings are
configuration errors; an empty seed list is valid only for the bootstrap node.

Without the native prerequisite these commands fail loudly. The `dqlite`
profile never substitutes SQLite, and the composition roots reach the adapter
only when the profile is selected. The adapter shares the portable
`database/sql` repositories and snapshot primitive with SQLite.

## What qualification covers

- A three-node commit, read, and leader handover against the native library
  ([official go-dqlite project](https://github.com/canonical/go-dqlite/tree/v3)),
  with writes through the adapter read back from the other nodes.
- Leader failure: the bootstrap leader is closed without a handover and a new
  leader serves committed state with quorum retained.
- A typed `Health` result: current leader, node count, configured voters,
  reachable voters, and majority quorum.
- Topology change after filesystem restoration through
  `dqlite.RecoverTopology`. Every node must be stopped, and the caller supplies
  unique absolute state directories, Raft IDs, addresses, and roles. The
  procedure reads each node's last Raft entry, selects the newest node, runs
  Canonical's `ReconfigureMembershipExt` once on it, copies its data to staged
  node directories without `metadata1`/`metadata2`, and writes the target
  `cluster.yaml` and per-node `info.yaml`. The test restores all three
  directories, changes all three addresses, restarts, and verifies the data.

Not yet qualified: a provider-specific snapshot upload procedure, and the
lifecycle snapshot path below exercised with a new cluster topology (joining a
live cluster is not a substitute for filesystem restoration).

## Lifecycle snapshots

The lifecycle activator must use `-snapshot-mode directory` for a dqlite state
directory. The coordinator stops persistence before archiving the directory,
and the directory snapshotter refuses a source not explicitly declared stopped;
see [Snapshot boundary by profile](../specs/scale-to-zero.md#snapshot-boundary-by-profile).
