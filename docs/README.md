# Documentation

Start with the [repository overview](../README.md). This directory explains
how the application is structured, built, operated, and deployed.

- [Architecture](architecture.md) describes component boundaries and data flow.
- [Separable module architecture](modules.md) describes local and split wiring.
- [dqlite qualification](dqlite.md) describes the explicit native build profile.
- [Persistence qualification](../tests/persistence-qualification/README.md)
  describes the shared SQLite and dqlite repository contract.
- [Operations](operations.md) describes deployment, hibernation, restoration,
  backup, and recovery expectations.
- [Deployment](deployment.md) describes implemented and qualification-target
  deployment profiles.
- [Authentication](authentication.md) describes browser authorization sources
  and internal administration.
- [Implementation status](implementation.md) records implemented operations and
  qualification boundaries.
- [Terminology](terminology.md) defines the Slack terms used by this project.

Normative, testable requirements and pinned upstream contract sources live in
[`../specs/`](../specs/README.md). The [SDK qualification inventory](../specs/sdk-compatibility.yaml)
records the official SDK sources used by the compatibility checks.
