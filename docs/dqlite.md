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

The server selects dqlite explicitly and requires all cluster parameters:

```sh
bin/sameoldchat-dqlite -chat-mode local -store dqlite \
  -dqlite-directory /var/lib/sameoldchat/dqlite \
  -dqlite-address node-a:19001 \
  -dqlite-cluster node-a:19001,node-b:19001,node-c:19001 \
  -dqlite-database sameoldchat \
  -api-token xoxb-production -session-token browser-session
```

The `chatd` binary accepts the same dqlite storage parameters. Empty or mixed
storage settings are configuration errors.

If the native prerequisite is absent, these commands must fail loudly. The
project must not replace dqlite with SQLite under the `dqlite` profile. The
server and `chatd` composition roots reach the adapter only when the `dqlite`
build profile is selected. The adapter uses the same portable `database/sql`
repositories and snapshot primitive as SQLite, but cluster bootstrap, quorum,
leader failure, and three-node restore remain qualification gates rather than
claims supported by the default build.
