# Slack app platform compatibility

This document is the product-level compatibility boundary for Slack apps. It
exists because counting Web API method handlers does not prove that an app can
be created, installed, authenticated, receive a real event, handle an
interaction, or refresh a token. The sources of truth are Slack's current
official documentation and executable checks through current official SDKs.

The detailed Web API evidence remains in
[`compatibility.yaml`](compatibility.yaml), while SDK versions and immutable
artifact evidence remain in
[`sdk-compatibility.yaml`](sdk-compatibility.yaml). The product-wide UI and API
journey inventory is maintained in
[`product-gap-audit.md`](product-gap-audit.md).

## Current support matrix

| Journey | Current evidence | Status |
| --- | --- | --- |
| Distinguish user (`xoxp`), bot (`xoxb`), and app-level (`xapp`) credentials | Durable token type, app ID, bot ID, subject, scope, revocation, and expiry survive memory, SQLite, and gRPC composition; official Node and Python clients assert bot identity | Supported |
| Exchange an authorization code for a bot token | `oauth.v2.access` returns the bot token at the top level, the installer under `authed_user`, and `team{id,name}`; a reinstall keeps the bot user and bot the app already has in the workspace; `redirect_uri` is required at redemption only when the authorization named one; a wrong secret is `bad_client_secret` and a mismatched redirect `bad_redirect_uri`; official Node, Python, and Java SDKs decode it | Supported |
| Exchange a user-only authorization code | `oauth.v2.user.access`, and `oauth.v2.access` for a grant with no bot scopes, return the user token only under `authed_user`; official Node and Python SDK clients exercise the method | Supported |
| Legacy OAuth exchange | `oauth.access` and `oauth.token` are exercised by official SDKs | Supported |
| Uninstall an app | `apps.uninstall` verifies client credentials and token/app identity, disables the installation, and atomically revokes every installation token, webhook, and bot in memory and SQLite; official Node, Python, and Java SDKs exercise distinct installations | Supported |
| Install through browser consent | `/oauth/v2/authorize` presents authenticated consent, validates exact registered redirects and requested scope subsets, preserves state, supports PKCE S256, and creates one-time authorization codes; the browser and service suites redeem the result | Supported |
| Combined bot and user grants | A single consent and `oauth.v2.access` exchange atomically creates the bot installation and returns the installer grant under `authed_user`; storage and official SDK qualification assert both token families | Supported |
| OAuth token rotation | `oauth.v2.exchange` converts legacy tokens, expiring access tokens are rejected, refresh tokens are single-use, and refresh rotates the access/refresh pair; current official Node SDK qualification exercises the lifecycle | Supported |
| App registration and app manifests | Durable versioned apps, encrypted signing/client credentials, expiring configuration tokens, refresh rotation, `apps.manifest.*`, JSON validation, browser editing, installation, and uninstall are implemented across memory, SQL, local, and gRPC profiles | Supported for the parsed manifest surface |
| Slack-hosted app datastores | Manifest-declared schemas, isolated durable records, replace/merge/delete semantics, 25-item bulk operations, query/count with the complete documented expression operator set and post-filter scan behavior, uninstall cleanup, bot-token scopes, local/gRPC parity, the current official Node SDK raw-method path, and a first-party developer console for schema/query/item inspection are exercised end to end | Supported for the documented item, expression, and pagination contract |
| Bot execution | A bot token authenticates as its bot user and app, reports bot/app identity, writes as that subject, and is revoked with its installation | Supported |
| Incoming webhooks | Durable, revocable webhooks post as the installed app's verified bot only after bot membership is proved; unknown/disabled credentials are indistinguishable, and archived destinations return Slack's plain-text `410 channel_is_archived`. Hooks are minted at install time for the `incoming-webhook` scope (returned by `oauth.v2.access`) or by an internal administration route, and every returned `url` names this deployment's public base URL rather than `hooks.slack.com` | Supported |
| Events API over HTTP | Delivery is selected from each installed manifest, filtered independently across bot/user subscriptions, signed with the app secret, and retried on Slack's schedule (immediately, after one minute, after five minutes) with Slack's `X-Slack-Retry-Num`/`X-Slack-Retry-Reason` headers. Delivery state is per record: a callback the app fails waits for its own retry while the app's later events keep flowing, internal projection failures neither spend a retry nor send a non-Slack reason, a record that fans out is retried only for the callbacks the app did not accept, each fanned-out callback has its own stable `event_id`, and apps are served concurrently so one slow endpoint does not delay another. Scope and conversation visibility are evaluated for every active bot/user authorization; current callbacks carry one matching authorization plus a durable `event_context`, while `apps.event.authorizations.list` requires an `xapp` token and resolves the full event-backed set. Immutable outbox snapshots preserve the exact create/change/delete message version and file create/share projection across delayed delivery and SQL restart. The developer console exposes the durable HTTP/Socket position, queued evaluation, the earliest active lease and waiting retry (time/count/reason), and retained attempt history without leaking event payloads | Supported for the implemented event catalog; live-Slack differential comparison remains |
| Socket Mode | App-level tokens with `connections:write`, connection limits, envelopes, acknowledgements, retries, bot/user subscription perspective selection, and the same event callback projection as HTTP are implemented. Current official Node, Python, and Java clients receive real service events rather than fixture-planted callbacks | Supported for the implemented event surface |
| RTM | The current official Node RTM client connects through `rtm.connect` and receives a real stored message hydrated only after connected-user conversation membership is proved. Other events carrying `channel_id` are likewise membership-filtered | Supported for message creation and the implemented content-free event catalog; the legacy RTM event surface remains narrower than Slack's catalog |
| Slash commands | Manifest commands are validated and dispatched to signed HTTP or Socket Mode receivers with deduplication, exact `response_url` authorization, bounded trigger/response lifecycles, manifest-controlled `should_escape`, composer discovery, and implemented built-in commands. Current HTTP and Socket qualifications use distinct human callers and installed bot identities | Supported for implemented built-ins and installed app commands; workflow/Enterprise command breadth remains tracked |
| Interactivity | Message/Home/modal block actions, global and message shortcuts, view submissions and closures, external options, signed HTTP delivery, Socket Mode envelopes, triggers, and `response_url` mutations are wired end to end. A `response_url` answers Slack's plain-text refusals (400 `invalid_payload`/`no_text` without spending one of its five uses, 404 `used_url`/`expired_url`/`channel_not_found`), and every block and interactive element an app leaves unnamed is stored with a generated `block_id`/`action_id`. Modal input blocks render every current element with its declared constraints (`min_length`/`max_length`, `min_value`/`max_value`, `is_decimal_allowed`), which the server enforces before the app is asked; empty optional inputs report `null`; `rich_text_input` reports a `rich_text` value; a `datetimepicker` is shown and read in the viewer's own time zone; an input block with `dispatch_action` sends `block_actions` on its `dispatch_action_config` triggers; an external option chosen from `block_suggestion` results carries its text (vouched for by a server-signed token scoped to the view or message it was loaded for) in `view_submission` and in `block_actions` from views and messages, including ephemeral ones; every view in an interaction payload carries `view.state`; `views.update` and `response_action: update` keep entered values for elements whose `block_id`/`action_id` survive. A modal's `file_input` takes the member's files (multipart, through the composer's staging path) within its `filetypes` and `max_files`, keeps them attached when the form comes back with errors, and reports them in `state.values` as `files` — Slack file objects private to the member (shared into no conversation) that the app's bot is durably granted read access to, so `files.info` and `url_private` work with its bot token; the service accepts only the submitting member's own live uploads | Supported for the implemented Block Kit elements; `file_input` in a message or an actions block (which Slack does not accept) is shown with an explanation |
| Link unfurling | A manifest's `features.unfurl_domains` (at most five host names, no scheme, port or path) are validated and stored with the manifest. A message or edit that shares links on those domains or their subdomains — outside code, at most five per event — sends `link_shared` (channel, user, `message_ts`, `thread_ts`, `unfurl_id`, `source`, `is_bot_user_member`, links) over HTTP and Socket Mode to each subscribed app holding `links:read` that can see the channel, including a public channel its bot has not joined. A message posted with `unfurl_links=false` shares no links, and an edit follows the posted choice; a message that omits it shares them, whoever posted it. `chat.unfurl` accepts `channel`+`ts` or `unfurl_id`+`source`, lets a non-member app unfurl only its own domains' links in a public channel, and the previews render in the first-party client and return as `is_app_unfurl` attachments in history. The Node Bolt qualification runs the round trip | Supported for posted messages; composer (pre-send) unfurls and the `user_auth_*` prompt flow remain absent |
| Organization app access controls | `admin.apps.permissions.*` store each app's access control list per workspace (everyone, named users and user groups, or no one) and its channel restriction (all channels, an allowlist, or an exclusion list), with every documented argument limit and error; a slash command or shortcut from a member the list does not admit, or in a channel the restriction leaves out, is refused. A manifest declares MCP servers under `features.mcp_servers` (a unique `name` and an HTTPS `url`; Slack's method references publish no manifest schema for this, so the declaration is a local decision), and each server's identifier is derived from its app and name. `admin.apps.mcp.servers.list` pages the servers of approved, undeleted apps, so an uninstalled app's servers stay listed and a restricted app's leave; `admin.apps.mcp.servers.permissions.*` let a server narrow, never widen, its app's list. `apps.managed.permissions.set` authenticates a configuration token and validates in full, but no app here is created by a manager app, so a valid request answers `app_not_managed` | An app's list and channel restriction are enforced on slash commands and shortcuts (and hide the app's commands from a member it does not admit); block actions, App Home, mentions and event delivery are not yet gated, and there is no MCP runtime for a server rule to gate; Enterprise-only refusals (`not_an_enterprise`, `enterprise_is_restricted`) are not emitted |
| App Home | Installed apps appear in the first-party client, `views.publish` is durable and re-renders an open Home live, `app_home_opened` is emitted when the member opens the Home tab (`tab: "home"`, with the published view) or the Messages tab (`tab: "messages"`) and not when the Home is merely re-read after an action or a live update, and Home-tab actions use the same signed HTTP/Socket delivery as message interactions | Supported |
| Agent sessions | `agents.sessions.setStatus` creates a thread's session on first use (title and initiator apply only then) and writes the calling agent's `active`/`processing`/`suspended`/`closed` status, answering the session-level status (suspended > processing > active > closed across agents), the agent's own `agent_status`, and `missing_agent_session_stopped_event_subscription` while the app does not subscribe to `agent_session_stopped`; `agents.sessions.rename` retitles it for one of its agents. The thread pane shows the session live, with a stop control while a subscribed agent is processing: a stop sends that agent `agent_session_stopped` with the timestamps of its streams it stopped and leaves the status to the app, and a member's retitle sends every agent `agent_session_title_changed`; both events need `chat:write` and the subscription, over HTTP or Socket Mode. A code channel (`agents.conversations.create`) is a session channel whose session is named without `thread_ts`. The agent's `icon_emoji`, `icon_url` and `username` override needs `chat:write.customize` and is shown in the session panel. The pinned Node, Python and Java SDKs drive both methods | Partially supported |
| Assistant threads | `assistant.threads.setTitle`, `setStatus` and `setSuggestedPrompts` write a thread's title, transient status (with up to ten rotating `loading_messages`) and one-click prompts, each without touching the others and none of them a message. `setStatus` takes the `icon_emoji`, `icon_url` and `username` override python-slack-sdk 3.45 and slack-bolt 1.30 send, under `agents.sessions.setStatus`'s contract (`chat:write.customize`, JSON null as absent); the override belongs to the status and is cleared with it. The thread pane shows the state live, the status under the override's name and icon or the app's own name | Supported for the SDK-qualified arguments |
| Block Kit in the first-party UI | Every block in Slack's current 2026 catalog has an explicit safe projection, including interactive controls, Markdown/rich text, container, data table, task/plan, card/carousel, and accessible pie/bar/area/line visualizations; Playwright qualifies the user-visible path | Supported for current block types; element-by-element parity remains tracked |
| Workflow functions | A workflow step running an app's remote function delivers `function_executed` with an execution-scoped bot token (`xwfp-`, minted once per execution and sealed with the application credential key) as `bot_access_token`. The token acts as the app's bot for that execution only: messages posted and modals opened or pushed with it belong to the execution, and `block_actions`, `view_submission` and `view_closed` from them carry `function_data` (`execution_id`, `function.callback_id`, `inputs`), the same `bot_access_token`, and `interactivity` (`interactor`, `interactivity_pointer`, which `views.open`/`views.push` accept in place of `trigger_id`). `functions.completeSuccess`/`completeError` accept it for its own execution only (another is `access_denied`), and it answers `token_expired` once the execution completes, fails or is cancelled, and `token_revoked` once the app loses its bot installation. Official Node and Python Bolt suites run a function listener that posts a button and an action listener whose `complete()` finishes the execution. There is no execution timeout, so a token lives as long as its running execution; ephemeral messages and scheduled messages do not record an execution; a workflow step may run only its own app's remote functions, so cross-app and connector functions are refused | Partially supported |
| App administration UI | The browser provides manifest creation/validation/edit/delete, one-time credentials, app-level tokens, OAuth installation, installed-app discovery, App Home, shortcuts, commands, interactive surfaces, developer-owned hosted-datastore schema/query/item administration, and payload-redacted live event-delivery state with retained attempt history and success metrics, install-time incoming-webhook channel selection, human-readable scope explanations at consent, app-token inventory and per-token or bulk revocation, public-distribution activation, and external-auth provider declaration and member connection, all over the same durable local/gRPC boundary. Structured editors for every manifest section remain absent | Partially supported |

## Measured remaining gaps

The compatibility ledger is generated from Slack's current method catalog and
ratcheted in CI. `make compatibility-report` prints the current figures, and
the [project status](../PLAN.md#status) records them; every current Web API
method is implemented and names method-level evidence. Implemented is a
coverage statement, not a claim that every published detail of the method is
asserted.

Every method is enforced at the rate-limit tier the pinned Java SDK's table
publishes for it. The next app-runtime priorities are citing the reference-page
tiers of the 42 methods that table predates (the `rate-limit-tiers` decision in
the ledger), then the manifest sections that are parsed and stored but not
executed (for example agent/assistant views), then cross-app and connector
workflow functions, before Enterprise-only breadth.

User-scoped `star_added`/`star_removed` callbacks are delivered only through a
matching user-token authorization with `stars:read`; generic event JSON remains
fail-closed, so recipient-scoped state cannot leak through an audience-less
worker.

“Supported” here means the stated slice is real; it is not a claim that the row
covers every Slack variant. In particular, Enterprise Grid organization
installs, GovSlack domains, Marketplace distribution, admin app activities,
granular bot scope additivity, and data residency require their own evidence
before being claimed.

## Official contracts used by this audit

- [Installing with OAuth](https://docs.slack.dev/authentication/installing-with-oauth/)
- [`oauth.v2.access`](https://docs.slack.dev/reference/methods/oauth.v2.access/)
- [`oauth.v2.user.access`](https://docs.slack.dev/reference/methods/oauth.v2.user.access/)
- [Using token rotation](https://docs.slack.dev/authentication/using-token-rotation/)
- [Slack token types](https://docs.slack.dev/authentication/tokens/)
- [App manifests](https://docs.slack.dev/app-manifests/)
- [Using app datastores](https://docs.slack.dev/tools/deno-slack-sdk/guides/using-datastores/)
- [`apps.datastore.put`](https://docs.slack.dev/reference/methods/apps.datastore.put/)
- [`apps.datastore.bulkPut`](https://docs.slack.dev/reference/methods/apps.datastore.bulkPut/)
- [Events API](https://docs.slack.dev/apis/events-api/)
- [`star_added` event](https://docs.slack.dev/reference/events/star_added/)
- [`star_removed` event](https://docs.slack.dev/reference/events/star_removed/)
- [HTTP request URLs](https://docs.slack.dev/apis/events-api/using-http-request-urls/)
- [Verifying Slack requests](https://docs.slack.dev/authentication/verifying-requests-from-slack/)
- [Slash commands](https://docs.slack.dev/interactivity/implementing-slash-commands/)
- [Handling interactivity](https://docs.slack.dev/interactivity/handling-user-interaction/)
- [Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/)
- [Sending messages using incoming webhooks](https://api.slack.com/messaging/webhooks)

## Qualification rule

A Slack app journey is not promoted to “Supported” from a handler unit test.
It needs:

1. a durable test in both local storage implementations;
2. local-versus-gRPC composition parity;
3. an end-to-end request through an applicable current official SDK;
4. a browser test for any user-visible configuration or consent step; and
5. before `conforms-to-published-source` is recorded, tests that assert every
   request and response field, header, signature, retry, and error that the
   journey's published references and official SDK fixtures specify.

Fixture-planted event envelopes qualify only the transport that carried them.
They do not qualify event production, subscription filtering, or the app UI.
