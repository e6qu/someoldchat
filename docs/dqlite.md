# dqlite qualification

The dqlite adapter is an explicit `dqlite` build profile using Canonical’s
`github.com/canonical/go-dqlite/v3` binding pinned in `go.mod`. It is not part
of the default static build because the binding requires the native dqlite
library and headers.

Run the qualification commands on a Linux host with the matching native dqlite
release and headers installed. The Canonical binding is not a portable macOS
dependency. The dqlite qualification suite is kept under
[`tests/dqlite-qualification`](../tests/dqlite-qualification/README.md).
The shared repository contract is kept under
[`tests/persistence-qualification`](../tests/persistence-qualification/README.md)
and runs against dqlite in the same native test profile.

```sh
make build-dqlite
make test-dqlite
```

`make build-dqlite` produces the HTTP composition, separate `chatd`, and
blob-cleanup binaries with the same explicit native profile.

The server selects dqlite explicitly. The first node bootstraps with an empty
cluster seed list:

```sh
bin/sameoldchat-dqlite -chat-mode local -store dqlite \
  -dqlite-directory /var/lib/sameoldchat/dqlite \
  -dqlite-address node-a:19001 \
  -dqlite-database sameoldchat \
  -api-token xoxb-production -session-token browser-session
```

Additional nodes use the first node as an explicit seed:

```sh
sameoldchat-dqlite -chat-mode local -store dqlite \
  -dqlite-directory /var/lib/sameoldchat/dqlite-node-b \
  -dqlite-address node-b:19001 \
  -dqlite-cluster node-a:19001 \
  -dqlite-database sameoldchat \
  -api-token xoxb-production -session-token browser-session
```

Start a third node with the same join seed and a distinct state directory and
address. The `chatd` binary accepts the same dqlite storage parameters. Empty
or mixed storage settings are configuration errors; an empty seed list is
valid only for the bootstrap node.

If the native prerequisite is absent, these commands must fail loudly. The
project must not replace dqlite with SQLite under the `dqlite` profile. The
server and `chatd` composition roots reach the adapter only when the `dqlite`
build profile is selected. The adapter uses the same portable `database/sql`
repositories and snapshot primitive as SQLite, but leader failure and changing
cluster topology are separate qualification concerns rather than claims
supported by the default build. The Linux qualification job runs the
three-node commit, read, and handover test against the native library and
headers documented by the
[official go-dqlite project](https://github.com/canonical/go-dqlite/tree/v3).
The qualification also writes through the dqlite adapter and reads the
repository state from other cluster nodes. The dqlite adapter exposes a typed
`Health` result containing the current leader, node count, configured voter
count, reachable voter count, and majority quorum status. It also closes the
bootstrap leader without a handover and verifies that a new leader serves
committed repository state with quorum retained. Changing node addresses after
filesystem restoration uses the explicit `dqlite.RecoverTopology` procedure.
The caller must stop every node and provide unique absolute state directories,
Raft IDs, addresses, and roles. The procedure reads each node's last Raft
entry, selects the newest node, invokes Canonical's
`ReconfigureMembershipExt` once on that node, copies its data to staged node
directories without `metadata1` or `metadata2`, and writes the target
`cluster.yaml` and per-node `info.yaml`. The native qualification restores all
three state directories, changes all three addresses, restarts the recovered
cluster, and verifies the committed repository data. A provider-specific
snapshot upload procedure remains unqualified.

The lifecycle activator must use `-snapshot-mode directory` for a dqlite state
directory. The coordinator stops persistence before creating the directory
archive, and the directory snapshotter requires an explicit stopped-source
state, before restoring it and starting the new dqlite process. A separate
qualification must still exercise this path with a new cluster topology;
joining a live cluster is not a substitute for filesystem restoration.
