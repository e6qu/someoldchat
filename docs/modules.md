# Separable module architecture

SameOldChat has explicit service seams so one deployment can be a static Go
binary while another can place modules in separate processes. Business code
depends on module APIs, never on a transport or on a composition decision.

The module manifest is [modules.json](../modules.json). It is deliberately JSON
and validated with the standard library so the generator has no hidden parser
dependency. `internal/modules/*/api` contains stable interfaces; implementations
live elsewhere; generated bindings live in `internal/generated`.

Run:

```sh
go generate ./...
make generated-check
```

`modulegen` validates module names, references, target placement, and explicit
storage selection, then
generates local bindings, remote client/server-registration bindings, and
typed target profiles containing process replica counts.
`go generate` is explicit and is never assumed to run as part of `go build`;
stale generated files fail `make check`.

Build tags are reserved for coarse binary roles. They must not encode every
local/remote combination. The current targets include a direct-call monolith
and a separate `sameoldchat-chatd` process using the explicit TLS gRPC adapter. The transport
is selected by composition, not by business logic. No remote transport is
silently substituted for the local mode. The server and `sameoldchat-chatd` composition
roots consume generated transport bindings, so the declared module seam is
the source of truth for both local and distributed assembly.

Both targets can run multiple replicas. Monolith replicas contain the direct
call composition and share the qualified state store. Separate module
processes have independent replica counts and share the state store owned by
that module. In-memory storage is restricted to one development replica;
replicated targets must select a qualified durable backend.

The runtime shapes are explicit:

```sh
sameoldchat -chat-mode local -store sqlite -db 'file:sameoldchat.db'

sameoldchat-chatd -listen :9443 -store sqlite -db 'file:chat.db' \
  -tls-cert chat.crt -tls-key chat.key \
  -tls-client-ca ca.crt \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
sameoldchat -chat-mode grpc -chat-address chatd:9443 \
  -chat-ca ca.crt -chat-server-name chatd.internal \
  -chat-client-cert http-client.crt -chat-client-key http-client.key \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

The separate example permits independent HTTP and chat replica counts behind
their respective load balancers. The manifest also includes replicated targets
(`monolith-replicated` and `separate-chat-replicated`) to validate that replica
counts are topology data rather than transport fallbacks. No local chat store
is opened by the HTTP process in gRPC mode.

Authentication lookups also cross the module seam: HTTP replicas use the
generated remote token/session stores, while `sameoldchat-chatd` owns their durable records.
No separate HTTP replica keeps authoritative authentication state in memory.
Session revocation crosses the same seam as an explicit durable mutation; the
HTTP replica never treats a local cookie or process cache as authoritative.

The blob cleanup process is an operational worker, not a business module. It
has its own binary and replica count, and shares the owning module's durable
store and external blob store.

Remote module APIs must be coarse enough to survive a process boundary: they
carry explicit request objects, context cancellation, deadlines, typed errors,
and bounded/streamable results. Directory, conversation reads/mutations,
message reads/mutations, presence, and file metadata operations use typed
protobuf contracts and generated gRPC service adapters. File uploads and downloads use typed streaming metadata
with bounded byte chunks. File uploads use a client-streaming gRPC
method, and downloads use a server-streaming method; the server feeds bytes
directly between the transport and blob store without materializing the object
in process memory. All chat services use generated protobuf gRPC client/server
contracts, including the file streams whose metadata and bounded chunks are
represented by explicit protobuf oneof parts. Transaction ownership and data ownership stay
inside the module that owns the data. Transport generation must use a qualified
RPC implementation rather than inventing framing, flow control, or schema
evolution in application code.

The transport source schema lives under [`proto/`](../proto/). Generated
protobuf messages and service adapters are checked into the module transport
package and regenerated through `go generate`; the dynamic envelope is not
part of the transport contract.
