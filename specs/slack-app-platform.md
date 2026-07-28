# Slack app platform compatibility

This document is the product-level compatibility boundary for Slack apps. It
exists because counting Web API method handlers does not prove that an app can
be created, installed, authenticated, receive a real event, handle an
interaction, or refresh a token. The sources of truth are Slack's current
official documentation and executable checks through current official SDKs.

The detailed Web API evidence remains in
[`compatibility.yaml`](compatibility.yaml), while SDK versions and immutable
artifact evidence remain in
[`sdk-compatibility.yaml`](sdk-compatibility.yaml).

## Current support matrix

| Journey | Current evidence | Status |
| --- | --- | --- |
| Distinguish user (`xoxp`), bot (`xoxb`), and app-level (`xapp`) credentials | Durable token type, app ID, bot ID, subject, scope, revocation, and expiry survive memory, SQLite, and gRPC composition; official Node and Python clients assert bot identity | Supported |
| Exchange an authorization code for a bot token | `oauth.v2.access` returns the bot token at the top level and installer identity under `authed_user`; official Node, Python, and Java SDKs decode it | Supported for bot-only grants |
| Exchange a user-only authorization code | `oauth.v2.user.access` returns the user token only under `authed_user`; official Node and Python SDK clients exercise the method | Supported |
| Legacy OAuth exchange | `oauth.access` and `oauth.token` are exercised by official SDKs | Supported |
| Uninstall an app | `apps.uninstall` verifies client credentials and token/app identity, disables the installation, and atomically revokes every installation token, webhook, and bot in memory and SQLite; official Node, Python, and Java SDKs exercise distinct installations | Supported |
| Install through browser consent | There is no `/oauth/v2/authorize` consent journey and authorization codes are currently provisioned internally | Not supported |
| Combined bot and user grants | A single `oauth.v2.access` exchange cannot yet atomically issue both grants | Not supported |
| OAuth token rotation | Explicit access-token expiry is enforced, but `oauth.v2.exchange`, refresh-token persistence, single-use refresh rotation, and the two-active-token rule are absent | Not supported |
| App registration and app manifests | No durable app registration model or `apps.manifest.*` API exists | Not supported |
| Bot execution | A bot token authenticates as its bot user and reports its app/bot identity; Web API writes use that subject | Partially supported |
| Incoming webhooks | Durable, revocable webhooks can post messages, but they are provisioned through an internal administration route rather than an app install selection | Partially supported |
| Events API over HTTP | The worker signs outbound Slack-shaped requests, but subscriptions and request URLs are operator-global rather than app configuration, and several product events cannot yet be translated from the internal payload | Partially supported |
| Socket Mode | App-level tokens with `connections:write`, connection limits, envelopes, acknowledgements, retries, and official Node/Python/Java clients are exercised | Supported for the implemented event surface |
| Slash commands | No app-configured command registry, browser trigger, signed HTTP delivery, acknowledgement deadline, or response lifecycle exists | Not supported |
| Interactivity | View/dialog Web API storage exists, but Block Kit actions, shortcuts, view submissions, options requests, signed delivery, and `response_url` handling are not wired end to end | Not supported |
| App Home | Views can be published, but no app configuration/install journey makes this a complete Slack app flow | Partially supported |
| Workflow functions | Selected Web API acknowledgement methods exist; app distribution and real trigger/delivery journeys are incomplete | Partially supported |
| App administration UI | The browser UI exposes authorization administration only; it has no app creation, manifest editor, OAuth settings, event subscriptions, commands, interactivity, installation, token, or delivery-health screens | Not supported |

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
- [Events API](https://docs.slack.dev/apis/events-api/)
- [HTTP request URLs](https://docs.slack.dev/apis/events-api/using-http-request-urls/)
- [Verifying Slack requests](https://docs.slack.dev/authentication/verifying-requests-from-slack/)
- [Slash commands](https://docs.slack.dev/interactivity/implementing-slash-commands/)
- [Handling interactivity](https://docs.slack.dev/interactivity/handling-user-interaction/)
- [Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode/)

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
