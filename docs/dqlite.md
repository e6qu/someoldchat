# dqlite qualification

The dqlite adapter is an explicit `dqlite` build profile using Canonical’s
`github.com/canonical/go-dqlite/v3` binding pinned in `go.mod`. It is not part
of the default static build because the binding requires the native dqlite
library and headers.

Run the qualification commands on a Linux host with the matching native dqlite
release and headers installed. The Canonical binding is not a portable macOS
dependency:

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
repositories and snapshot primitive as SQLite, but cluster bootstrap, quorum,
leader failure, and three-node restore remain qualification gates rather than
claims supported by the default build. The Linux qualification job runs the
three-node test against the native library and headers documented by the
[official go-dqlite project](https://github.com/canonical/go-dqlite/tree/v3).
