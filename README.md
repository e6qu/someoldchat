# SameOldChat

SameOldChat is a self-hostable, Slack-compatible chat system with a Go
backend, an HTMX interface, SQLite, PostgreSQL, or dqlite persistence, and explicit
request-triggered restoration for deployments that support scale-to-zero.

## Documents

- [Project status and planned work](PLAN.md)
- [Architecture and operational documentation](docs/README.md)
- [Separable module architecture](docs/modules.md)
- [Authentication](docs/authentication.md)
- [PostgreSQL storage](docs/postgresql.md)
- [dqlite qualification](docs/dqlite.md)
- [Blob lifecycle and reconciliation](docs/blob-lifecycle.md)
- [SDK qualification inventory](specs/sdk-compatibility.yaml)
- [Browser qualification](tests/browser/README.md)
- [Qualification suites](tests/README.md)
- [Specifications and pinned contract sources](specs/README.md)
- [Terminology](docs/terminology.md)
- [Engineering rules](AGENTS.md)

## Core constraints

- Slack compatibility is derived from pinned published specifications, official
  open-source SDKs, current documentation, and recorded behavioral evidence.
- Storage is selected explicitly with `-store memory|sqlite|postgresql|dqlite`.
  These are operating modes, not fallbacks; unsupported or incomplete
  configuration fails at startup. dqlite requires the `dqlite` build tag and
  native libraries.
- All paid SameOldChat compute, including database processes, can hibernate at
  zero after a snapshot is independently verified. A small logical activator
  endpoint remains reachable to restore the stack.
- Runtime and build inputs use the newest eligible stable release only after a
  mandatory 24-hour publication quarantine.
- The repository contains deployment guidance for Linux virtual machines,
  Amazon Elastic Container Service (ECS) on AWS Fargate, Google Cloud Run, and
  Azure Container Apps. Only Amazon ECS ships Terraform:
  [`terraform/ecs-runtime`](terraform/ecs-runtime/README.md) for durable
  application resources and
  [`deploy/ecs-scale-zero`](deploy/ecs-scale-zero/README.md) for
  request-triggered activation. The other profiles require their stated
  qualification work.
- The production container uses standard OpenID Connect discovery, so a
  conforming identity provider is configured by issuer URL rather than by a
  cloud-specific integration.

The documents distinguish implemented behavior from qualification work. The same
module interfaces support direct Go calls in local composition
(`-chat-mode local`) and generated gRPC adapters in distributed composition
(`-chat-mode grpc`); see [Terminology](docs/terminology.md) and
[separable module architecture](docs/modules.md).

## Binaries

`make build` writes these to `bin/`; their roles and contracts are in
[Architecture](docs/architecture.md).

| Binary | Source | Role |
|---|---|---|
| `sameoldchat` | `cmd/server` | web UI and Slack-compatible API |
| `sameoldchat-chatd` | `cmd/chatd` | chat module behind gRPC in distributed composition |
| `sameoldchat-worker` | `cmd/worker` | outbox, scheduled-message, reminder, workflow-delay, and Slackbot-response delivery; guest expiry |
| `sameoldchat-socketmode-worker` | `cmd/socketmode-worker` | Socket Mode response delivery |
| `sameoldchat-blobgc` | `cmd/blobgc` | blob cleanup and reconciliation audit |
| `sameoldchat-activator` | `cmd/activator` | wake coordinator and reverse proxy |
| `sameoldchat-ecs-ws-activator` | `cmd/ecs-ws-activator` | WebSocket edge for `deploy/ecs-scale-zero` |

## Development commands

```sh
make check                  # every offline gate, including go vet and the activator tests
make check-full             # adds Terraform, vulnerability, race, load, mutation, and fuzz gates
# check-full covers the CI `go`, `race`, `mutation`, and `terraform` jobs and the
# activator tests of `scale-zero-artifacts`. It does not run the `sdk`,
# `browser`, `shauth-sso`, `dqlite`, or `postgres` jobs or the dual-architecture
# edge image build; each needs a service, a second language runtime, or a
# container build, so run those explicitly.
# The two ratchets compare against a base revision, so they take BASE_REF and are
# not part of `make check`. CI runs both with the pull request's base branch.
make contract-ratchet BASE_REF=origin/main   # Slack HTTP compatibility ledger
make proto-breaking   BASE_REF=origin/main   # gRPC wire compatibility
make browser-qualification
SHAUTH_SOURCE_DIR=/path/to/shauth make shauth-sso-qualification
make build
make build-static
make run                    # local composition, memory store, dev credentials, and a .cache/dev-blobs file store
./bin/sameoldchat -chat-mode local -store sqlite -db 'file:sameoldchat.db' \
  -app-credential-key-hex "$SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX" \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

Every durable store (anything but `memory`) requires `-app-credential-key-hex`.
`sameoldchat -check-config` validates the full startup configuration and exits
without opening a store or binding a listener.

## License

SameOldChat is licensed under the GNU Affero General Public License, version 3
or any later version. See [LICENSE](LICENSE).
