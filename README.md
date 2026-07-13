# SameOldChat

SameOldChat is a self-hostable, Slack-compatible chat system with a Go
backend, an HTMX interface, SQLite/dqlite persistence, and full-stack
scale-to-zero through verified snapshot and request-triggered restoration.

## Documents

- [Delivery plan](PLAN.md)
- [Architecture and operational documentation](docs/README.md)
- [Separable module architecture](docs/modules.md)
- [dqlite qualification](docs/dqlite.md)
- [SDK qualification inventory](specs/sdk-compatibility.yaml)
- [Normative specifications](specs/README.md)

## Core constraints

- Slack compatibility is derived from pinned published specifications, official
  open-source SDKs, current documentation, and recorded behavioral evidence.
- SQLite is the simple explicit local profile; dqlite is the explicit replicated
  production profile and requires the `dqlite` build tag plus native libraries.
- All paid SameOldChat compute, including database processes, can hibernate at
  zero after a snapshot is independently verified.
- A small logical activator endpoint remains reachable to restore the stack.
- Runtime and build inputs use the newest eligible stable release only after a
  mandatory 24-hour publication quarantine.
- Supported hosting targets include ordinary Linux VMs, AWS ECS/Fargate,
  Google Cloud Run, and Azure Container Apps, with companion database compute
  where a managed container platform cannot safely host dqlite.

The documents define the current architecture and its remaining qualification
work. The implementation is intentionally incremental: each vertical slice is
kept portable across direct-call monolith and distributed gRPC assembly.

## Development commands

```sh
make check
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
