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

A guest account given an expiration is deactivated when it arrives, not merely
refused. Credential lookup already declines a lapsed account, so access ends at
the instant; deactivation is what makes the rest of the workspace agree, rather
than leaving a member other people can direct message, mention, add to channels
and be billed for, whom nobody can sign in as.

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

## ADMIN-05 — Enforce data retention

An authorized administrator sets how long messages and files are kept, at the
workspace and — for messages — per conversation. The control states before it
is used that deletion is permanent and that it is applied on a schedule rather
than the instant content expires, and reports when the sweep last ran so a
stopped worker is visible rather than silently breaking the policy's promise.

Deletion removes the content and everything that references it, so nothing is
left pointing at a message that no longer exists. A thread is retained until
its newest reply expires. Conversation types that cannot carry a policy, and
durations outside the permitted range, are explicit refusals rather than
silent no-ops. Content past the horizon remains readable until the sweep
reaches it.

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

An invitation lapses fourteen days after it is sent. Expiry is a state the
surfaces show and the operations enforce, not a date left sitting in the past:
a lapsed invitation cannot be approved, because acceptance refuses it on the
same deadline and the approval would record a live invitation that nothing
could ever accept. Withdrawing one stays available, so a queue of lapsed
invitations can be cleared rather than accumulating for ever.

## CONNECT-03 — Use and manage a shared channel

The UI identifies external organizations and members, respects each side's
roles/identity, and applies Slack Connect visibility for messages, files, apps,
workflows, canvases, retention, editing, and administration. API objects and
events include current shared-channel identity fields. Removing an organization
follows Slack's history/copy/disconnect behavior and does not corrupt the
remaining channel.

An administrator can see a member's live sessions and end them without touching
the account. Signing out and deactivating are different acts — losing a device
is a reason for the first and not the second — and the product used to offer
only the second. Each session is identified by the stored hash of its token and
never by the token: telling two sessions apart and ending one need no
credential, and a review that handed out tokens would be a way to become the
member rather than a way to see them. Revoked and expired sessions are left out,
because an administrator asking who is signed in is asking who can act right
now, and a session whose start was never recorded says so rather than claiming
to have begun at the epoch. A bulk sign-out naming a member who is not in the
workspace stops before anything is revoked, so an administrator acting on a
pasted list finds out they were wrong instead of signing out an arbitrary prefix
of it. Session *policy* — Slack's per-member duration and desktop settings behind
`admin.users.session.{get,set,clear}Settings` — is stored, reported and now
applied. A duration an administrator sets is what the session a member is given
lives for, in both places one is minted: signing in, and switching into a
workspace, where the target workspace's own policy governs because settings are
held per workspace. Slack's `desktop_app_browser_quit` becomes a cookie with no
Max-Age, which is what "ends when the browser is quit" is on the web; the
durable session keeps its own expiry, so quitting is one of the two things that
ends it rather than the only one. An identity provider's asserted expiry still
caps the result: the policy says how long this deployment will trust a sign-in
and the provider says how long its assertion holds, and neither extends the
other. The duration decided nothing at all until then — it was written, read
back by `getSettings`, and every session was minted at a hardcoded day, so an
administrator who set eight hours got twenty-four. `mobile_device_check` remains
recorded and unapplied, and the reason is that there is no mobile client here to
check a device on; it is named rather than quietly treated as enforced. The surface has no browser
citation, and the reason is structural rather than an unwritten test: it lives
on the identity-provider administration page, whose routes are registered only
where a provider is configured, and the browser servers run without one. That is
the same reason already recorded for AUTH-02 and AUTH-05. The web tests and the
cross-profile contract carry it instead, and they can arrange a second member,
which this suite's single signed-in member cannot.

An administrator can also see the whole picture rather than one channel of it:
the workspace settings page lists every organization this workspace shares
channels with, and how many channels each is in, because that count is what a
disconnection ends. Disconnecting one ends it in every shared channel in a
single transaction — doing it channel by channel would leave the connection
alive wherever an administrator missed one, and an administrator told an
organization is disconnected while one channel still carries it has been told
something false about who can read their messages. A connection is derived from
the channels that carry it rather than recorded a second time, so the product
has one answer to "are we connected" rather than two that can disagree. There
is no "connected since": nothing records when a connection began, and a date
derived from the oldest shared channel would look precise and mean something
else.

## What this deployment implements, and what it does not

**Slack Connect** is implemented as an invitation lifecycle inside one
deployment. Approval and acceptance are distinct transitions taken by different
organizations, denying (the host refusing to send) and declining (the invited
organization's answer) are recorded as different facts, and the
250-organization capacity is claimed inside the transaction that appends the
organization — never from a count read earlier, which is stale by definition.

An external organization here is **another workspace on this deployment**.
Cross-deployment federation — an invitation that leaves this process and is
accepted by a Slack workspace elsewhere — needs a federation transport this
product does not have, and, as recorded below, a single-deployment mock cannot
qualify it either way.

**Administration** carries the settings with a durable backend and an enforced
effect: workspace name, description, icon, discoverability, default channels
and message/file retention; the invitation and app-request queues; analytics counted from the
durable rows on each load; and an audit view over the durable event journal and
the access log, whose export comes from the same query as the page.

Absent, and named on the page rather than rendered as an inert control:

- **Retention of canvases and lists.** Slack's retention covers them; this
  deployment retains them indefinitely. Messages and files are governed — see
  ADMIN-05.
- **Audit visibility across private conversations.** The audit view reads the
  journal through the same visibility-filtered path the event stream uses, so
  it cannot show an administrator that a private conversation they are not in
  exists. The page says so rather than implying it shows everything.

## Evidence

- Multi-workspace browser and API fixtures use different administrators and
  external members; a single-workspace mock cannot qualify Slack Connect.
- Authorization tests attempt every action as owner, admin, member,
  multi-channel guest, single-channel guest, deactivated account, and an
  identifier belonging to nobody. The matrix in `tests/authorization` derives
  the operation set by reflection over the chat seam, so an operation that
  arrives without a declared authority fails rather than defaulting to
  "anybody may". This line described a test that did not exist until then: scope
  enforcement was exhaustive and role enforcement was not tested at all, which
  is how a single-channel guest could reach every public channel in the
  workspace and how three operations answered a deactivated account.
- The guard-mutation gate in `tests/mutation` strips every authorization guard
  standing in front of an operation and requires a suite to notice. It is what
  keeps the line above honest, and its first run showed the line was overstated:
  the matrix accepted `store.ErrNotFound` as a refusal about standing, and that
  is equally what an operation says when the object is simply not there, so
  ninety operations stayed green with nothing in front of them. The matrix now
  asks a caller who holds the authority as well, and the operations whose
  refusal still cannot tell the two apart are named in
  `refusalDoesNotDistinguishTheHolder` under a ceiling that only shrinks. What
  shrinks it is a fixture rich enough for each operation to find its object: an
  exhaustive audit took that ceiling from 103 to 62 and the surviving-guard
  ceiling from 82 to 75, closing every operation the fixture can reach as the
  holder — the list-item, message-reaction, posting, scheduling, draft, role,
  retention, notification, own-scheduled-status, own-saved-item, own-Activity-view,
  own-sidebar-section, own-draft, user-group-channel, channel-canvas, workflow
  delete, trigger-permission, canvas-revision-restore, list-download,
  list-column-removal and group-DM-to-private operations. What remains is named residue: operations owner-only on a
  member-owned object, a developer-app operation the member-owned fixture app
  shadows, a second-organization or enterprise-grid action, a per-message pin or
  star whose add and remove share the probe's one timestamp, and the
  complex-state workflow and block-kit-payload operations whose front door needs
  a multi-step object graph the probe does not yet build.
- Current official SDKs exercise applicable `admin.*`, `team.*`, `users.*`,
  `apps.*`, and Slack Connect `conversations.*` methods.
- The `[ADMIN-04 APP-08 APP-09 WORKFLOW-02]` browser journey creates and
  installs a hosted app, opens its manifest-declared datastore from app
  administration, queries persisted items with Slack's expression semantics,
  replaces and merges one item, deletes it, and inspects the payload-redacted
  durable Socket Mode cursor before cleaning up the app.
- Opt-in Slack Enterprise/sandbox evidence is required before claiming plan-
  restricted administration equivalence; unavailable live evidence remains a
  named gap.
## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| ADMIN-01 | [Roles in Slack](https://slack.com/help/articles/360018112273-Roles-in-Slack) | Slack distinguishes owners, admins, members, guests, and role-specific authority. |
| ADMIN-02 | [Slack Enterprise APIs](https://docs.slack.dev/enterprise/) | Workspace and organization policies govern channels, members, apps, and data. |
| ADMIN-03 | [Slack Enterprise APIs](https://docs.slack.dev/enterprise/) | Enterprise APIs expose scoped audit and analytics administration. |
| ADMIN-05 | [Customize message and file retention policies](https://slack.com/help/articles/203457187-Customize-message-and-file-retention-policies) | Workspace and per-conversation retention delete messages and files permanently on a daily schedule. |
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
