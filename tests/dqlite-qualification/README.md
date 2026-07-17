# dqlite qualification

This directory contains tests for the pinned Canonical dqlite binding and its
three-node behavior, including the application adapter’s leader, configured
voter, reachable voter, and quorum health results, plus cross-node repository
replication and unplanned bootstrap-leader failure recovery. The tests require
the native dqlite library and the explicit
`dqlite` build profile.

The suite also stops a three-node cluster, archives its state directories with
the lifecycle directory snapshotter, restores them into a new state root, and
reads previously committed repository data after the cluster restarts. It
also changes all three restored node addresses through
`dqlite.RecoverTopology`, restarts the cluster, and verifies the committed
repository data. It does not qualify a provider-specific snapshot upload
procedure.

Run them with:

```sh
go test -tags dqlite ./tests/dqlite-qualification
```

The application adapter remains under `internal/store/dqlite`. This suite
qualifies same-topology snapshot restore and changed-address recovery using
local state directories. It does not qualify a provider-specific snapshot
upload procedure.
