# Implementation status

This document records the current implementation and qualification boundaries.
It does not describe the repository's development history.

## Application surface

The compatibility ledger in [`../specs/compatibility.yaml`](../specs/compatibility.yaml)
is the authoritative operation tracker. The contract checker validates the
ledger against the pinned Slack specifications and handler registrations.

At the current pinned revision:

- 176 of 176 listed operations have an implementation.
- 176 operations have behavior-compatible status in the ledger.
- 0 operations have verified-against-Slack status.
- No operation is marked unimplemented.

Run `make contract-check` to validate the ledger and
`make compatibility-report` to print these counts.

## Persistence

The local profiles are explicit choices:

- SQLite uses the shared SQL store and runs in the regular Go build.
- dqlite uses the `dqlite` build tag and native Linux dqlite libraries.
- In-memory storage is an explicit development profile.

The SQLite repository suite and the dqlite qualification suite use the same
application-facing store contracts. dqlite requires the Linux native library
provided by the continuous integration qualification environment.

## Process topology

The monolith composition root connects module interfaces with direct Go calls.
The distributed composition root connects the same interfaces through generated
gRPC adapters. Replicas remain stateless application processes; durable state
lives in the configured store, event outbox, request spool, or blob store.

## Scale to zero

The AWS Elastic Container Service scale-to-zero module is implemented under
[`../deploy/ecs-scale-zero/`](../deploy/ecs-scale-zero/). The local checks are:

```sh
terraform fmt -check -recursive
terraform init -backend=false -input=false -lockfile=readonly
terraform validate
make ecs-qualification
```

The module uses an activator for the first request and a separate WebSocket
edge service. It does not use an Application Load Balancer.

## Qualification commands

Run the repository gates with:

```sh
make check
make test-race
make build
make build-static
make browser-qualification
make sdk-qualification
make test-dqlite
```

The browser, official SDK, dqlite, and live Slack checks require their stated
external runtimes or services. A local pass of the contract checker does not
provide live Slack verification.

The seven pinned official SDK suites currently pass locally: Deno Slack
runtime, Node Web API, Node Bolt, Python Slack Software Development Kit,
Python Bolt, Java Slack API, and Java Bolt. The browser qualification requires
the pinned Playwright Chromium runtime. The dqlite qualification requires
Linux native dqlite libraries.
