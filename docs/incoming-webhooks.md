# Incoming Webhooks

SameOldChat implements Slack Incoming Webhook delivery through the
`/services/{workspace_id}/{app_id}/{secret}` endpoint. It accepts JSON with
`text`, `blocks`, `attachments`, an optional `thread_ts`, and an optional
`Idempotency-Key` header. The JSON is either the request body or, as Slack also
accepts, the `payload` field of an `application/x-www-form-urlencoded` body;
either encoding is limited to 1 MiB. A successful request returns the
plain-text body `ok`.

Failures are plain-text bodies with a non-200 status, as on
`hooks.slack.com`:

- 400 `invalid_payload`: an oversized or undecodable body, none of `text`,
  `blocks`, or `attachments`, an invalid `attachments` array, or another
  rejected message;
- 400 `invalid_blocks`: an invalid `blocks` array;
- 404 `no_team`: an unknown workspace, app, or secret, or a disabled webhook,
  which are deliberately indistinguishable;
- 410 `channel_is_archived`: the destination conversation is archived.

With `-api-rate-limit` on (the default), each webhook URL carries Slack's
documented allowance of one message per second with a short burst. A delivery
beyond it answers HTTP 429 with a `Retry-After` header and the plain-text body
`rate_limited`, which the official SDK webhook clients' retry handlers read.
The budget is shared like the Web API budgets: where a deployment can run
more than one web replica (the distributed composition, PostgreSQL, or
dqlite) every replica draws from one budget in the chat module's store, and on
memory and SQLite, which are single-replica, it is held in process.

Each webhook belongs to one workspace, application, and conversation. The
endpoint does not accept a channel override. The secret is returned once — by
the internal administrative API, or in the `incoming_webhook` object of the
`oauth.v2.access` response when an install requested the `incoming-webhook`
scope — and stored only as a SHA-256 hash.

The returned `url` is absolute and names this deployment, never
`hooks.slack.com`: it is built from `-auth-public-url` when that is configured
and from the origin the request reached otherwise, the same rule the web
handler uses for interaction `response_url`s. The install response's
`configuration_url` is the installed app's page, `/app/apps/{app_id}`.

Administrators create a webhook with
`/internal/admin/incoming-webhooks/create`, providing `app_id`, `channel_id`,
and `bot_user_id`. They enable or disable it with
`/internal/admin/incoming-webhooks/enable`, providing `webhook_id` and an
explicit `enabled` value.

Local composition invokes the typed service methods directly; distributed
composition uses the generated gRPC adapters for the same methods. Every
storage backend enforces the enabled state and never stores the plaintext
secret.

Block Kit and legacy attachment payloads are stored as normalized JSON arrays
of at most 100 objects each. An invalid array is refused, never silently
discarded.

For upstream behavior, see [Sending messages using incoming webhooks](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks)
and the [`incoming-webhook` scope](https://docs.slack.dev/reference/scopes/incoming-webhook/).

Related architecture: [Modules](modules.md), [API compatibility](../specs/api-compatibility.md),
and [Persistence](../specs/persistence.md).
