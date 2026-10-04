# Documentation

Start with the [repository overview](../README.md). This directory explains
how the application is structured, built, operated, and deployed.

- [Architecture](architecture.md) describes component boundaries and data flow.
- [Separable module architecture](modules.md) describes local and distributed
  composition.
- [dqlite qualification](dqlite.md) describes the explicit native build profile.
- [Persistence qualification](../tests/persistence-qualification/README.md)
  describes the repository contract every storage profile must pass.
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
- [Localization](localization.md) describes message catalogs, how a request
  picks its language, the pseudo-locale, and how to add a language.
- [Rebase audit](rebase-audit.md) describes `make rebase-audit`, which checks
  that a rebased branch kept the work it contained.
- [Benchmarks and profiling](performance.md) describes measuring the message
  write and pagination paths.
- [Terminology](terminology.md) defines the Slack and composition terms used
  by this project.

Normative, testable requirements and pinned upstream contract sources live in
[`../specs/`](../specs/README.md). Within it:

- the [SDK qualification inventory](../specs/sdk-compatibility.yaml) records
  the official SDK sources used by the compatibility checks;
- the [Slack user-journey catalog](../specs/journeys/README.md) defines the
  first-party UI target independently of implementation coverage, and
  browser, accessibility, API, SDK, and live-differential tests cite its
  stable journey identifiers; and
- the [Slack app platform compatibility matrix](../specs/slack-app-platform.md)
  tracks complete app journeys (registration, installation, token types,
  events, commands, interactivity, and UI), so Web API method counts cannot
  stand in for end-to-end app support.

Current status and planned work are in [`../PLAN.md`](../PLAN.md). The
binding engineering rules are in [`../AGENTS.md`](../AGENTS.md).
