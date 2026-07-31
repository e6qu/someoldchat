# Apps, bots, commands, and interactions

The complete platform contract is also inventoried in
[`../slack-app-platform.md`](../slack-app-platform.md). These are the user-facing
journeys that make those APIs usable.

## APP-01 — Discover and inspect an app

Apps exposes installed apps and Slack-compatible discovery where configured.
An app page identifies app name/icon, developer/publisher, requested and granted
capabilities, workspace installation state, bot identity, App Home availability,
and support/privacy information supplied by the manifest. Missing icons or
disabled apps have honest fallbacks; app content MUST not impersonate native
security UI.

## APP-02 — Install through OAuth

The installer starts from the intended app/workspace, reviews Slack-compatible
scope explanations and any incoming-webhook channel selection, then allows or
denies consent. OAuth state, redirect URI, PKCE where applicable, client
credentials, team/enterprise target, and installer authorization are validated.

Success creates exactly one enabled installation and the appropriate bot/user/
configuration tokens, scopes, identities, webhook, and App Home visibility.
Refresh/replay cannot install twice or reveal a token. Denial, changed scopes,
wrong workspace, disabled distribution, invalid state/code, and concurrent
installation have distinct outcomes.

## APP-03 — Use an app bot and App Home

Bot messages are visibly app/bot-authored and honor Slack's channel membership,
token scopes, blocks, attachments, streaming, threads, edits, files, and
interactions. A human MUST not be fabricated as the bot author.

App Home exposes Home, Messages, and About tabs only where enabled. Published
views render current Block Kit, dispatch interactions, preserve app/workspace/
user identity, and show a recoverable app-specific failure when publication or
delivery fails.

## APP-04 — Invoke global and message shortcuts

Global shortcuts appear in Slack's shortcuts browser and app surfaces according
to the installed manifest. Message shortcuts appear only for an eligible
message and include its exact context. Activation creates a current Slack
interaction payload with `trigger_id`, app/team/user identity, action/callback
identifier, and message/channel context where applicable.

The app must acknowledge within Slack's deadline. A valid response can open or
update a modal and later respond through authorized response mechanisms.
Timeout, app error, disabled installation, stale message, and duplicate
delivery are visible and idempotent.

## APP-05 — Discover and invoke slash commands

Typing `/` at the start of an otherwise empty composer opens the shortcuts
browser populated from Slack built-ins plus the installed apps' manifest
commands and workflows. Each app command shows its command, description,
usage hint, and app identity. Continued text filters; keyboard selection inserts
the command without sending it.

Submitting an app command:

1. does not create an ordinary message;
2. is rejected in threads, because Slack does not invoke app slash commands
   there;
3. selects the **most recently installed** app when multiple apps register the
   same command;
4. applies manifest `should_escape` semantics to users, channels, and links;
5. sends Slack's current form-encoded payload, including command, text,
   `api_app_id`, team/channel/user identifiers and names where supplied,
   `trigger_id`, `response_url`, and compatibility token;
6. requires acknowledgement within three seconds; and
7. presents dispatch/timeout errors without posting the raw command.

Slack built-ins such as `/shrug`, `/search`, `/people`, `/archive`, `/leave`,
`/topic`, `/rename`, `/remind`, `/dnd`, `/status`, `/drafts`, `/mentions`,
`/saved`, and `/shortcuts` perform their current Slack actions and MUST not be
intercepted by an installed app. Built-ins that Slack permits in threads remain
available there.

## APP-06 — Handle response URLs and interaction responses

Every `response_url` is an unguessable app/workspace/user/conversation-scoped
capability. It may be used up to five times within thirty minutes, bypasses
ordinary channel posting permission only as Slack documents, and defaults to an
ephemeral response. `response_type=in_channel`, replacement, deletion, and
original-message ownership follow Slack's exact rules. Expired, exhausted,
wrong-app, cross-workspace, invalid replacement/deletion, and replay attempts
return handled Slack-compatible outcomes.

Ephemeral content is visible only to its intended member, is never returned as
ordinary history/search, and survives/reconnects only to the extent Slack does.

## APP-07 — Use interactive components and modals

Current Block Kit buttons, selects, overflow, date/time pickers, checkboxes,
radio buttons, rich-text inputs, workflow buttons, and external options expose
correct labels, state, validation, confirm dialogs, accessibility, and action
payloads. External options respect minimum query length, deadlines, app
identity, and option schema.

`views.open`, `views.push`, and `views.update` honor one-time trigger IDs,
view hashes, stack order, private metadata, input state, validation errors,
close/clear behavior, and submit acknowledgement. Concurrent stale hashes do
not overwrite a newer view.

## APP-08 — Receive Events API and Socket Mode traffic

Installed apps receive only subscribed, scoped, visibility-authorized events.
HTTP delivery verifies signing secret, timestamp freshness, URL verification,
retry headers, and idempotency. Socket Mode verifies app-level token and
envelope acknowledgement. Both transports project the same event envelope and
object hydration, including create/change/delete and file share/unshare
variants.

Retries/backoff expose delivery health and never become duplicate app-visible
effects when the app handles idempotency. A bot losing channel access stops
receiving protected events.

## APP-09 — Manage tokens, webhooks, distribution, and uninstall

Authorized app administrators can inspect token type/scope/creation/last-use
metadata without re-reading secrets, rotate/revoke credentials, manage incoming
webhooks and event delivery, configure distribution and external-auth providers,
and uninstall. Revocation takes effect across HTTP, Socket Mode, scheduled work,
response URLs, and refresh flows. Uninstall removes app access and UI entries
without deleting user-owned workspace history Slack retains.

## Evidence

- Current official Bolt/SDK clients perform OAuth, commands, shortcuts, Block
  Kit interactions, modal concurrency, incoming webhooks, signed Events API,
  Socket Mode, streaming, files, token revocation, and uninstall against
  SameOldChat—without hand-authored callback payloads standing in for delivery.
- Browser tests discover manifest-derived commands/shortcuts, distinguish bot
  identity, exercise all response types, and inspect app administration.
- The `[ADMIN-04 APP-08 APP-09 WORKFLOW-02]` browser journey installs a
  Socket Mode app and inspects the same payload-redacted durable delivery cursor
  used by local and generated-gRPC workers. SQL restart tests preserve queued
  retry time/count/reason, while the console labels unevaluated journal work
  separately from acknowledged callbacks.
- Live differential tests compare exact form/JSON envelopes after normalizing
  secrets, IDs, and timestamps.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| APP-01 | [Guide to apps in Slack](https://slack.com/help/articles/360001537467-Guide-to-apps-in-Slack) | Slack identifies installed apps, their publisher, permissions, and app surfaces. |
| APP-02 | [Installing with OAuth](https://docs.slack.dev/authentication/installing-with-oauth/) | Slack app installation exchanges explicit approved scopes for installation credentials. |
| APP-03 | [Guide to apps in Slack](https://slack.com/help/articles/360001537467-Guide-to-apps-in-Slack) | Installed apps expose bot identities, messages, and App Home surfaces. |
| APP-04 | [Shortcuts](https://docs.slack.dev/interactivity/implementing-shortcuts/) | Global and message shortcuts deliver contextual interaction payloads. |
| APP-05 | [Implementing slash commands](https://docs.slack.dev/interactivity/implementing-slash-commands/) | Slash commands send form payloads, escape configured entities, and require prompt acknowledgement. |
| APP-06 | [Handling user interaction](https://docs.slack.dev/interactivity/handling-user-interaction/) | Response URLs are bounded capabilities for ephemeral or in-channel interaction responses. |
| APP-07 | [Modals](https://docs.slack.dev/surfaces/modals/) | Trigger IDs, hashes, view stacks, validation, and acknowledgement govern modal interaction. |
| APP-08 | [Events API](https://docs.slack.dev/apis/events-api/) | Events API and Socket Mode require acknowledgements, retries, and scoped event delivery. |
| APP-09 | [Installing with OAuth](https://docs.slack.dev/authentication/installing-with-oauth/) | Revocation and uninstall disable associated tokens, commands, webhooks, and bot access. |

Sources checked 2026-07-31:

- [Guide to apps in Slack](https://slack.com/help/articles/360001537467-Guide-to-apps-in-Slack)
- [Implementing slash commands](https://docs.slack.dev/interactivity/implementing-slash-commands/)
- [Handling user interaction](https://docs.slack.dev/interactivity/handling-user-interaction/)
- [Shortcuts](https://docs.slack.dev/interactivity/implementing-shortcuts/)
- [Modals](https://docs.slack.dev/surfaces/modals/)
- [OAuth](https://docs.slack.dev/authentication/installing-with-oauth/)
- [Events API](https://docs.slack.dev/apis/events-api/)
- [Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/)
