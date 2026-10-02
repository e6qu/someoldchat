# Separable module architecture

SameOldChat has explicit service seams so one deployment can be a static Go
binary while another can place modules in separate processes. Business code
depends on module APIs, never on a transport or on a composition decision.

## Manifest and generation

The module manifest is [modules.json](../modules.json). It is JSON so that
`cmd/modulegen` can validate it with the standard library alone.
`internal/modules/*/api` holds the stable interfaces, implementations live
elsewhere, and generated bindings live in `internal/generated`.

```sh
go generate ./...
make generated-check
```

`modulegen` validates module names and imports, target placement, explicit
storage selection, and replica counts, then generates the local provider, the
remote client and server-registration bindings, and typed target profiles with
per-process replica counts. `go generate` is never assumed to run as part of
`go build`; stale generated files fail `make check`.

The manifest declares one module, `chat`, and four targets:

| Target | Composition | Storage | Replicas |
|---|---|---|---|
| `monolith` | local | memory | app 1 |
| `monolith-replicated` | local | dqlite | app 3 |
| `separate-chat` | distributed | memory | http 1, chat 1 |
| `separate-chat-replicated` | distributed | dqlite | http 4, chat 3 |

`modulegen` refuses more than one replica of a module-owning process on
`memory` or `sqlite`, and fewer than three on `dqlite` (the configured
three-voter quorum). PostgreSQL has no replica limit. A process that owns no
module, such as `http` in the distributed targets, can scale freely.

## Selecting the composition

Today the composition is selected at runtime. `cmd/server` links both the
local and the remote chat provider and picks one from `-chat-mode`:

- `-chat-mode local` calls `generated.ProvideChatServiceLocal` (through
  `internal/app/localchat`) and opens the selected store in process.
- `-chat-mode grpc` calls `generated.ProvideChatServiceRemote` and reaches
  `sameoldchat-chatd` over mutual-TLS gRPC.

`sameoldchat-chatd` opens its store through the same local provider and serves
it with the chat gRPC transport. The target profiles validate topology data;
no build output corresponds to a target yet. Generating a composition root per
target, so the transport is chosen at build time and `-chat-mode` is retired,
is planned in [PLAN.md Phase 7](../PLAN.md#phase-7-compile-time-module-composition).

Build tags are reserved for coarse binary roles (for example `dqlite`). They
must not encode every local/remote combination. No remote transport is
silently substituted for local composition.

```sh
sameoldchat -chat-mode local -store sqlite -db 'file:sameoldchat.db' \
  -app-credential-key-hex "$SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX" \
  -auth-public-url https://chat.example.com

sameoldchat-chatd -listen :9443 -store sqlite -db 'file:chat.db' \
  -tls-cert chat.crt -tls-key chat.key \
  -tls-client-ca client-ca.crt \
  -app-credential-key-hex "$SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX" \
  -auth-public-url https://chat.example.com \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
sameoldchat -chat-mode grpc -chat-address chatd:9443 \
  -chat-ca server-ca.crt -chat-server-name chatd.internal \
  -chat-client-cert http-client.crt -chat-client-key http-client.key \
  -auth-public-url https://chat.example.com \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

Both processes take the same `-auth-public-url`: the HTTP process builds the
Web API's URLs on it and chatd builds the event payloads; see
[Public URL](operations.md#public-url).

The two certificate authorities must be different files. `-tls-client-ca`
answers "who may connect to chatd" and `-chat-ca` answers "which server is
chatd". With one authority for both, every certificate it issues, including
chatd's own server certificate, authenticates as a client to the whole internal
data plane. Issue the server certificate from `server-ca.crt` and the HTTP
process's client certificate from `client-ca.crt`, and give chatd only
`client-ca.crt`. chatd refuses to start when its own certificate would verify
as a client against `-tls-client-ca`.

In distributed composition the HTTP process opens no local store. Settings
that only the chat owner can act on (`-store`, `-db`, the `-dqlite-*` and
`-blob-*` flags, `-bootstrap-admin-email`, `-app-credential-key-hex`,
`-app-token`, `-app-id`) are rejected as contradictory configuration, and
`SAMEOLDCHAT_DATABASE_URL` is read only in local composition. Give those
settings to `sameoldchat-chatd`.

Authentication lookups also cross the module seam: HTTP replicas use the
generated remote token and session stores, while `sameoldchat-chatd` owns
their durable records. Session revocation crosses the same seam as an explicit
durable mutation. No HTTP replica keeps authoritative authentication state in
memory or treats a local cookie or process cache as authoritative.

The blob cleanup process is an operational worker, not a business module. It
has its own binary and replica count, and shares the owning module's durable
store and external blob store.

## Boundary rules

Adapters see a module only through its API package and the shared `domain`
vocabulary: the error sentinels a module returns, its request limits, and the
pure parsers its callers share (reminder phrases, dialog definitions, search
highlight terms) live in `internal/domain`, not beside the implementation.
`internal/modules/boundary_test.go` walks each adapter's import graph and fails
when it reaches `internal/service`, a storage backend, or the gRPC transport.

Remote module APIs must be coarse enough to survive a process boundary: they
carry explicit request objects, context cancellation, deadlines, typed errors,
and bounded or streamable results. Every chat operation uses typed protobuf
contracts and generated gRPC client and server adapters. File uploads use a
client-streaming method and downloads a server-streaming method; each stream
carries typed metadata and bounded byte chunks as protobuf `oneof` parts, and
the server moves bytes between the transport and the blob store without
holding the object in memory. Transaction and data ownership stay inside the
module that owns the data. Transport generation must use a qualified RPC
implementation rather than inventing framing, flow control, or schema
evolution in application code.

The gRPC server does not embed the generated `Unimplemented` server, so each
declared RPC must have an explicit implementation in the chat transport;
adding an RPC without one fails the build.

The transport schema lives under [`proto/`](../proto/). Generated protobuf
messages and service adapters are checked into
`internal/modules/chat/transport/grpc` and regenerated through `go generate`.

Incoming Webhook delivery follows the same module boundary. See
[Incoming Webhooks](incoming-webhooks.md) for its endpoint, administrative
lifecycle, storage rules, and current payload compatibility boundary.
