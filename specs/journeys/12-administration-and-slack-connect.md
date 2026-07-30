# Administration and Slack Connect

## ADMIN-01 — Manage members and roles

Authorized administrators can invite, inspect, change role/type, deactivate,
reactivate, and remove members under Slack's current workspace/organization
rules. Every action identifies the exact member and consequence, applies
least-privilege authorization at the backend, emits an audit record, and
revokes/changes sessions, tokens, conversation access, and app visibility as
required.

Owners, primary owners, guests, external members, bots, service accounts,
pending invitations, last-owner protection, and self-administration follow
distinct Slack rules. Bulk operations report per-member outcomes and are safe
to retry.

## ADMIN-02 — Configure workspace policy

Workspace settings expose only implemented, durable policy: identity/access,
channel defaults and management, message/file retention and editing, posting
permissions, app approval, notifications, huddles, Slack Connect, profile
fields, emoji, and plan-dependent features. An unavailable backend policy MUST
not be rendered as a working toggle.

Policy changes identify scope and timing, validate conflicts, update enforcement
across UI/API/apps/workers, and write an immutable audit entry. Existing
content follows Slack's migration/retention behavior rather than being silently
rewritten.

## ADMIN-03 — Review audit and analytics

Eligible roles can filter/paginate Slack-compatible audit events and analytics
without crossing workspace/enterprise boundaries. Entries identify actor,
action, target, time, context, and result while redacting secrets. Export and
API results agree with the UI. Cursor, retention, and rate-limit behavior are
explicit.

## ADMIN-04 — Govern apps

App management lists requested/approved/installed/disabled apps, installer,
scopes, token types, distribution, event health, webhooks, external auth,
datastores, functions/workflows, and last relevant activity. Authorized roles
can approve/deny requests, restrict installation, revoke/rotate tokens,
disable/uninstall, and inspect audit effects.

App policy applies to OAuth before credentials are issued and to subsequent API,
event, Socket Mode, scheduled, and response-URL use. A disabled app does not
remain operational through a stale token.

## CONNECT-01 — Invite an external organization to a channel

An eligible member chooses or creates the intended channel, identifies the
external organization/people, reviews public/private and posting/history
consequences, and sends one Slack Connect invitation. Approval-required,
already-shared, wrong organization, policy, plan, expired, revoked, and
concurrent invitation states are explicit.

The current documented capacity is 250 organizations in one Slack Connect
channel, including the host organization. Capacity is checked atomically when
the invitation is accepted; the UI must not promise a place from a stale count.
External people cannot be added to Slack's special general/all-company channel.

The invite MUST NOT expose channel history or membership before Slack permits
it and MUST NOT be modeled as an ordinary internal member invitation.

## CONNECT-02 — Approve, accept, decline, or revoke

Workspace/organization approvers see the inviter, both organizations, channel,
requested permissions, and policy consequences. Approval and recipient
acceptance are distinct where Slack requires both. The channel becomes shared
only after all required durable states complete. Decline, expiry, cancellation,
and revocation notify eligible parties without leaking protected content.

## CONNECT-03 — Use and manage a shared channel

The UI identifies external organizations and members, respects each side's
roles/identity, and applies Slack Connect visibility for messages, files, apps,
workflows, canvases, retention, editing, and administration. API objects and
events include current shared-channel identity fields. Removing an organization
follows Slack's history/copy/disconnect behavior and does not corrupt the
remaining channel.

## Evidence

- Multi-workspace browser and API fixtures use different administrators and
  external members; a single-workspace mock cannot qualify Slack Connect.
- Authorization tests attempt every action as owner, admin, member, guest,
  external member, bot, disabled user, and wrong workspace.
- Current official SDKs exercise applicable `admin.*`, `team.*`, `users.*`,
  `apps.*`, and Slack Connect `conversations.*` methods.
- The `[ADMIN-04 APP-08 APP-09 WORKFLOW-02]` browser journey creates and installs a
  hosted app, opens its manifest-declared datastore from app administration,
  queries persisted items with Slack's expression semantics, replaces and
  merges one item, deletes it, and inspects the payload-redacted durable Socket
  Mode cursor before cleaning up the app. Service, memory/SQL, generated-gRPC,
  official Node SDK, and automated accessibility qualifications cover the same
  boundaries.
- Opt-in Slack Enterprise/sandbox evidence is required before claiming plan-
  restricted administration equivalence; unavailable live evidence remains a
  named gap.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| ADMIN-01 | [Roles in Slack](https://slack.com/help/articles/360018112273-Roles-in-Slack) | Slack distinguishes owners, admins, members, guests, and role-specific authority. |
| ADMIN-02 | [Slack Enterprise APIs](https://docs.slack.dev/enterprise/) | Workspace and organization policies govern channels, members, apps, and data. |
| ADMIN-03 | [Slack Enterprise APIs](https://docs.slack.dev/enterprise/) | Enterprise APIs expose scoped audit and analytics administration. |
| ADMIN-04 | [App approval settings](https://slack.com/help/articles/222386767-Guide-to-app-approval-settings) | Administrators approve, restrict, and manage workspace app installation. |
| CONNECT-01 | [Use Slack Connect with other companies](https://slack.com/help/articles/360035092414-What-is-Slack-Connect) | Slack Connect invites external people and supports up to 250 organizations in a channel. |
| CONNECT-02 | [Use Slack Connect with other companies](https://slack.com/help/articles/360035092414-What-is-Slack-Connect) | External channel invitations have approval, acceptance, decline, and revocation states. |
| CONNECT-03 | [Use Slack Connect with other companies](https://slack.com/help/articles/360035092414-What-is-Slack-Connect) | Shared channels identify and constrain participating external organizations. |

Sources checked 2026-07-31:

- [Roles in Slack](https://slack.com/help/articles/360018112273-Roles-in-Slack)
- [Guide to app approval settings](https://slack.com/help/articles/222386767-Guide-to-app-approval-settings)
- [What is Slack Connect?](https://slack.com/help/articles/360035092414-What-is-Slack-Connect)
- [Use Slack Connect to work with other companies](https://slack.com/help/articles/360035092414-What-is-Slack-Connect)
- [Slack Enterprise APIs](https://docs.slack.dev/enterprise/)
- [Retrieving items from a datastore](https://docs.slack.dev/tools/deno-slack-sdk/guides/retrieving-items-from-a-datastore/)
