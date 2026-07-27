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
- SQLite and PostgreSQL are explicit SQL storage profiles; dqlite is the
  explicit replicated SQLite-compatible profile and requires the `dqlite` build
  tag plus native libraries.
- All paid SameOldChat compute, including database processes, can hibernate at
  zero after a snapshot is independently verified.
- A small logical activator endpoint remains reachable to restore the stack.
- Runtime and build inputs use the newest eligible stable release only after a
  mandatory 24-hour publication quarantine.
- The repository contains deployment guidance for Linux virtual machines,
  Amazon Elastic Container Service (ECS) on AWS Fargate, Google Cloud Run, and
  Azure Container Apps. The two Amazon ECS Terraform modules —
  [`terraform/ecs-runtime`](terraform/ecs-runtime/README.md) for durable
  application resources and
  [`deploy/ecs-scale-zero`](deploy/ecs-scale-zero/README.md) for
  request-triggered activation — are the current provider-specific
  infrastructure implementation; the other profiles ship no templates and
  require their stated qualification work.
- The production container uses standard OpenID Connect discovery, so a
  conforming identity provider is configured by issuer URL rather than by a
  cloud-specific integration.

The documents distinguish implemented behavior from qualification work. The same
module interfaces support direct Go calls in local composition
(`-chat-mode local`) and generated gRPC adapters in distributed composition
(`-chat-mode grpc`); see [Terminology](docs/terminology.md).

## License

SameOldChat is licensed under the GNU Affero General Public License, version 3
or any later version. See [LICENSE](LICENSE).

## Development commands

```sh
make check                  # every offline gate, including go vet and the activator tests
make check-full             # adds the Terraform, vulnerability, race, load, and fuzz gates
# check-full covers the CI `go`, `terraform`, and `scale-zero-artifacts` jobs. It
# deliberately does not reach `sdk`, `browser`, `shauth-sso`, `dqlite`, or
# `postgres`, or the dual-architecture edge image build, each of which needs a
# service, a second language runtime, or a container build; run those explicitly.
make module-startup-check   # starts the server with terraform/ecs-runtime's own outputs
# The two ratchets compare against a base revision, so they take BASE_REF and are
# not part of `make check`. CI runs both with the pull request's base branch.
make contract-ratchet BASE_REF=origin/main   # Slack HTTP compatibility ledger
make proto-breaking   BASE_REF=origin/main   # gRPC wire compatibility
make browser-qualification
SHAUTH_SOURCE_DIR=/path/to/shauth make shauth-sso-qualification
make build
make build-static
make run                    # explicitly selects local composition, memory, and dev credentials
./bin/sameoldchat -chat-mode local -store sqlite -db 'file:sameoldchat.db' \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

The `sameoldchat-socketmode-worker` binary claims durable Socket Mode
responses and delivers them to an explicitly configured HTTP destination. It
requires `-store`, `-app-id`, `-owner`, and `-response-url`, together with
the storage settings for the selected backend. A delivery failure releases
the response at the configured retry time; a process crash leaves the lease
for another replica to reclaim.

Storage selection is mandatory. `memory` and `sqlite` are separate operating
modes, not fallback behavior; unsupported or incomplete configuration fails at
startup. The architecture also treats typed domain values, boundary
normalization, minimal seams, and easy deletion as correctness constraints.
