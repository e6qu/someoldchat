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
- [SDK qualification inventory](specs/sdk-compatibility.yaml)
- [Browser qualification](tests/browser/README.md)
- [Qualification suites](tests/README.md)
- [Specifications and pinned contract sources](specs/README.md)
- [Terminology](docs/terminology.md)

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
  AWS Elastic Container Service, Google Cloud Run, and Azure Container Apps.
  The AWS Elastic Container Service scale-to-zero module is the current
  provider-specific infrastructure implementation; the other profiles require
  their stated qualification work.
- The production container uses standard OpenID Connect discovery, so Shauth
  and other conforming identity providers are configured by issuer URL rather
  than by cloud-specific integration.

The documents distinguish implemented behavior from qualification work. The
same module interfaces support direct Go calls in monolith mode and generated
gRPC adapters in distributed mode.

## License

SameOldChat is licensed under the GNU Affero General Public License, version 3
or any later version. See [LICENSE](LICENSE).

## Development commands

```sh
make check
make browser-qualification
make build
make build-static
make run                    # explicitly selects local mode, memory, and dev credentials
./bin/sameoldchat -chat-mode local -store sqlite -db 'file:sameoldchat.db' \
  -api-token "$SAMEOLDCHAT_API_TOKEN" -session-token "$SAMEOLDCHAT_SESSION_TOKEN"
```

Storage selection is mandatory. `memory` and `sqlite` are separate operating
modes, not fallback behavior; unsupported or incomplete configuration fails at
startup. The architecture also treats typed domain values, boundary
normalization, minimal seams, and easy deletion as correctness constraints.
