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
- [PostgreSQL storage](postgresql.md) describes the explicit PostgreSQL storage
  profile and qualification command.
- [Files](files.md) describes durable file uploads and the external upload
  lifecycle.
- [Blob lifecycle and reconciliation](blob-lifecycle.md) describes cleanup
  leases and the bounded reconciliation audit.
- [Incoming Webhooks](incoming-webhooks.md) describes the delivery endpoint,
  administrative lifecycle, and payload compatibility boundary.
- [Rebase audit](rebase-audit.md) describes checking that a rebased branch
  kept the work it contained.
- [Benchmarks and profiling](performance.md) describes measuring the message
  write and pagination paths.
- [Terminology](terminology.md) defines the Slack terms used by this project.

Normative, testable requirements and pinned upstream contract sources live in
[`../specs/`](../specs/README.md). Current status and planned work are in
[`../PLAN.md`](../PLAN.md). The [SDK qualification inventory](../specs/sdk-compatibility.yaml)
records the official SDK sources used by the compatibility checks.

The [Slack user-journey catalog](../specs/journeys/README.md) defines the
first-party UI target independently from current implementation coverage.
Browser, accessibility, API, SDK, and live-differential tests use its stable
journey identifiers.

The [Slack app platform compatibility matrix](../specs/slack-app-platform.md)
separately tracks complete app journeys—registration, installation, token
types, events, commands, interactivity, and UI—so Web API method counts cannot
stand in for end-to-end app support.

The repository's binding engineering rules are in
[`../AGENTS.md`](../AGENTS.md).

The repository is licensed under the GNU Affero General Public License,
version 3 or any later version. See the [license](../LICENSE).
