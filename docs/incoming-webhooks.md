# Incoming Webhooks

Someoldchat implements Slack Incoming Webhook delivery through the
`/services/{workspace_id}/{app_id}/{secret}` endpoint. It accepts JSON with
`text`, optional `thread_ts`, and an optional `Idempotency-Key` header. A
successful request returns the plain-text body `ok`.

Each webhook belongs to one workspace, application, and conversation. The
endpoint does not accept a channel override. The secret is returned once by
the internal administrative API and stored only as a SHA-256 hash.

Administrators create a webhook with
`/internal/admin/incoming-webhooks/create`, providing `app_id`, `channel_id`,
and `bot_user_id`. They enable or disable it with
`/internal/admin/incoming-webhooks/enable`, providing `webhook_id` and an
explicit `enabled` value.

The monolith invokes the typed service methods directly. Distributed mode uses
the generated gRPC adapters for the same methods. The SQLite migration and
memory store both enforce the enabled state and never store the plaintext
secret.

The current message model stores text and thread timestamps, but not Block Kit
payloads. Requests containing `blocks` therefore receive `invalid_payload`
instead of being silently discarded or partially stored. This is an explicit
compatibility boundary. Block Kit support requires a versioned message-model
change across all storage backends, gRPC, and the browser representation.
The message model stores Block Kit payloads as normalized JSON arrays. The
payload travels through the direct service boundary or generated gRPC and is
persisted by every storage backend. Invalid block arrays receive
`invalid_payload`; the implementation does not silently discard them.

For upstream behavior, see [Sending messages using incoming webhooks](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks)
and the [`incoming-webhook` scope](https://docs.slack.dev/reference/scopes/incoming-webhook/).

Related architecture: [Modules](modules.md), [API compatibility](../specs/api-compatibility.md),
and [Persistence](../specs/persistence.md).
