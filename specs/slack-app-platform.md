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
| Exchange an authorization code for a bot token | `oauth.v2.access` returns the bot token at the top level and installer identity under `authed_user`; official Node, Python, and Java SDKs decode it | Supported for bot-only grants |
| Exchange a user-only authorization code | `oauth.v2.user.access` returns the user token only under `authed_user`; official Node and Python SDK clients exercise the method | Supported |
| Legacy OAuth exchange | `oauth.access` and `oauth.token` are exercised by official SDKs | Supported |
| Uninstall an app | `apps.uninstall` verifies client credentials and token/app identity, disables the installation, and atomically revokes every installation token, webhook, and bot in memory and SQLite; official Node, Python, and Java SDKs exercise distinct installations | Supported |
| Install through browser consent | `/oauth/v2/authorize` presents authenticated consent, validates exact registered redirects and requested scope subsets, preserves state, supports PKCE S256, and creates one-time authorization codes; the browser and service suites redeem the result | Supported |
| Combined bot and user grants | A single consent and `oauth.v2.access` exchange atomically creates the bot installation and returns the installer grant under `authed_user`; storage and official SDK qualification assert both token families | Supported |
| OAuth token rotation | `oauth.v2.exchange` converts legacy tokens, expiring access tokens are rejected, refresh tokens are single-use, and refresh rotates the access/refresh pair; current official Node SDK qualification exercises the lifecycle | Supported |
| App registration and app manifests | Durable versioned apps, encrypted signing/client credentials, expiring configuration tokens, refresh rotation, `apps.manifest.*`, JSON validation, browser editing, installation, and uninstall are implemented across memory, SQL, local, and gRPC profiles | Supported for the parsed manifest surface |
| Slack-hosted app datastores | Manifest-declared schemas, isolated durable records, replace/merge/delete semantics, 25-item bulk operations, query/count with the complete documented expression operator set and post-filter scan behavior, uninstall cleanup, bot-token scopes, local/gRPC parity, the current official Node SDK raw-method path, and a first-party developer console for schema/query/item inspection are exercised end to end | Supported for the documented item, expression, and pagination contract |
| Bot execution | A bot token authenticates as its bot user and app, reports bot/app identity, writes as that subject, and is revoked with its installation | Supported |
| Incoming webhooks | Durable, revocable webhooks post as the installed app's verified bot only after bot membership is proved; unknown/disabled credentials are indistinguishable, and archived destinations return Slack's plain-text `410 channel_is_archived`. Provisioning still uses an internal administration route rather than install-time selection | Partially supported |
| Events API over HTTP | Delivery is selected from each installed manifest, filtered independently across bot/user subscriptions, signed with the app secret, and retried with Slack headers. Scope and conversation visibility are evaluated for every active bot/user authorization; current callbacks carry one matching authorization plus a durable `event_context`, while `apps.event.authorizations.list` requires an `xapp` token and resolves the full event-backed set. Immutable outbox snapshots preserve the exact create/change/delete message version and file create/share projection across delayed delivery and SQL restart. The developer console exposes the real durable HTTP/Socket cursor, queued evaluation, active lease, and retry time/count/reason without leaking event payloads | Supported for the implemented event catalog; retained attempt history/metrics, `file_unshared` production, and live-Slack differential comparison remain |
| Socket Mode | App-level tokens with `connections:write`, connection limits, envelopes, acknowledgements, retries, bot/user subscription perspective selection, and the same event callback projection as HTTP are implemented. Current official Node, Python, and Java clients receive real service events rather than fixture-planted callbacks | Supported for the implemented event surface |
| RTM | The current official Node RTM client connects through `rtm.connect` and receives a real stored message hydrated only after connected-user conversation membership is proved. Other events carrying `channel_id` are likewise membership-filtered; the previous hand-authored journal event is gone | Supported for message creation and the implemented content-free event catalog; the legacy RTM event surface remains narrower than Slack's catalog |
| Slash commands | Manifest commands are validated and dispatched to signed HTTP or Socket Mode receivers with deduplication, exact `response_url` authorization, bounded trigger/response lifecycles, manifest-controlled `should_escape`, composer discovery, and implemented built-in commands. Current HTTP and Socket qualifications use distinct human callers and installed bot identities | Supported for implemented built-ins and installed app commands; workflow/Enterprise command breadth remains tracked |
| Interactivity | Message/Home/modal block actions, global and message shortcuts, view submissions and closures, external options, signed HTTP delivery, Socket Mode envelopes, triggers, and `response_url` mutations are wired end to end | Supported for the implemented Block Kit elements |
| App Home | Installed apps appear in the first-party client, `views.publish` is durable, `app_home_opened` is emitted, and Home-tab actions use the same signed HTTP/Socket delivery as message interactions | Supported |
| Block Kit in the first-party UI | Every block in Slack's current 2026 catalog has an explicit safe projection, including interactive controls, Markdown/rich text, container, data table, task/plan, card/carousel, and accessible pie/bar/area/line visualizations; Playwright qualifies the user-visible path | Supported for current block types; element-by-element parity remains tracked |
| Workflow functions | Selected Web API acknowledgement methods exist; app distribution and real trigger/delivery journeys are incomplete | Partially supported |
| App administration UI | The browser provides manifest creation/validation/edit/delete, one-time credentials, app-level tokens, OAuth installation, installed-app discovery, App Home, shortcuts, commands, interactive surfaces, developer-owned hosted-datastore schema/query/item administration, and payload-redacted live event-delivery state over the same durable local/gRPC boundary. Retained delivery history/metrics, install-time incoming-webhook selection, scope explanation, token inventory/revocation, distribution, external-auth providers, and structured editors for every manifest section remain absent | Partially supported |

## Measured remaining gaps

The compatibility ledger is generated from Slack's current method catalog and
ratcheted in CI. At this revision, 223 of 310 current Web API methods have
registered implementations. Ten additional legacy methods remain in the
320-entry ledger but are not counted in the current denominator. The 87
unimplemented current methods break down as:

| Namespace | Missing | Boundary |
| --- | ---: | --- |
| `admin.*` | 50 | Enterprise Grid policy, analytics, roles, org-wide app/workflow administration, and newer object-linked conversation operations |
| `apps.*` | 5 | External-auth token access (2), activity history, app icons, and user connection state |
| `conversations.*` | 9 | Slack Connect invitations/approvals and external-invite policy |
| `workflows.*` / `functions.*` | 14 | Distribution/trigger permissions, featured workflows, step discovery, and response export |
| `assistant.*` | 5 | Assistant thread presentation and search context |
| `team.*`, `users.*` | 4 | Billing/external-team administration and contact discovery |

That count is a coverage inventory, not a claim that all implemented methods
are live-Slack-equivalent. The ledger currently records 200 current methods as
behavior-compatible, 21 as SDK-compatible, two as schema-compatible, and
zero as live-differential `verified-against-slack`. Only 26 of the 221
current methods claimed at SDK compatibility or better carry method-level
ledger evidence; the aggregate SDK path inventory does not promote the other
195 claims. The next app-runtime priorities are the remaining file-unshare
mutation, retained delivery history, and controlled HTTP/Socket differential
coverage, then the manifest sections that are stored but not yet
executable—functions/workflows, external authentication, incoming-webhook
install selection, and agent/assistant configuration—before Enterprise-only
breadth.

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
5. a live Slack differential observation before
   `verified-against-slack` is recorded.

Fixture-planted event envelopes qualify only the transport that carried them.
They do not qualify event production, subscription filtering, or the app UI.
