# Terminology

This document defines the Slack terms used in the project. The definitions
describe the project’s data and interface model; they do not claim behavior
that the pinned Slack contracts do not specify.

- A workspace is the tenant boundary that owns members, conversations, and
  application data.
- A member is a person represented by a user record in a workspace. The Web
  API commonly calls this a user.
- A conversation is a channel, direct message, or group direct message.
- A channel is a named conversation with membership and access rules.
- A direct message is a private conversation between members. A group direct
  message is a private conversation with more than two members.
- A message is a piece of conversation content. A thread is a root message and
  its replies.
- A reaction is an emoji attached to a message by a member.
- A file is an uploaded object with metadata and optional external content.
- A user group is a named group of members that can be mentioned together.
- An app is an integration that calls the Web API or receives events.
- A token authenticates an app or member when a Slack-compatible method
  requires one. A scope grants a token a defined permission.
- An event is a notification delivered through a Slack-compatible event
  contract. A webhook is an HTTP delivery endpoint for an event or message.
- OAuth is the authorization exchange used to connect an identity or app.
- A view is a structured interface surface. Block Kit is the structured format
  used by views and messages where the relevant Slack contract supports it.

## Composition and deployment terms

One name per concept. The documentation previously used four competing names for
each of the two compositions selected by the single flag `-chat-mode`, which made
it impossible to tell whether two passages described the same thing.

- *Local composition* (`-chat-mode local`) runs the chat module by direct Go call
  inside one process. It is the `monolith` target in `modules.json`. Not "monolith
  mode", "monolithic mode", or "local mode".
- *Distributed composition* (`-chat-mode grpc`) runs the chat module in
  `sameoldchat-chatd` and reaches it through the generated gRPC adapters. It is
  the `separate` target in `modules.json`. Not "grpc mode", "distributed mode",
  "separate mode", or "split-process".
- An *activator* is a process that keeps a public endpoint reachable while the
  application is scaled to zero and restores it on demand. *Fencing generation*,
  *snapshot manifest*, *outbox*, and *lease* are defined in the
  [scale-to-zero specification](../specs/scale-to-zero.md) and the
  [operations guide](operations.md).

The project uses the fully qualified names of Go packages, commands, cloud
services, and deployment components elsewhere in the documentation.

Related documents: [repository overview](../README.md),
[architecture](architecture.md), and [Slack compatibility specification](../specs/api-compatibility.md).
