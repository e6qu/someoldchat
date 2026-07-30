# Authentication and workspace entry

These journeys cover Slack-compatible entry to a workspace. SameOldChat's
identity provider may differ from Slack's hosted sign-in service, but the user
journey, workspace isolation, session behavior, and visible failure semantics
MUST remain equivalent.

## AUTH-01 — Enter a known workspace

**Preconditions:** The person is signed out and has a valid account in at least
one workspace.

**Entry points:** A workspace URL, a workspace link from a catalog, or a
protected permalink.

**Target behavior:**

1. The entry page identifies the workspace and available sign-in method without
   disclosing private membership data.
2. Successful authentication returns to the originally requested, authorized
   destination. An invalid or no-longer-visible destination falls back to the
   workspace home with a specific explanation.
3. The workspace shell identifies the signed-in member and workspace. The first
   usable conversation or the member's last valid destination receives focus;
   focus MUST not remain on a transient loading control.
4. A non-member cannot enter the shell or learn private conversation content.
   The response offers an applicable request/join/sign-in path rather than a
   fabricated empty workspace.

**Durable/API effects:** Authentication creates one revocable, workspace-bound
session. It MUST NOT silently create conversation memberships.

**Required variants:** expired authorization response, identity-provider
denial, disabled member, deleted workspace, stale permalink, cold wake, and two
concurrent first sign-ins for the same external identity.

## AUTH-02 — Choose among workspaces

**Preconditions:** The person belongs to multiple workspaces.

**Target behavior:** The workspace chooser exposes only memberships the person
may use, identifies each workspace unambiguously, and opens the selected
workspace in an isolated session context. Switching MUST NOT reuse the prior
workspace's channel, search, draft, file, app, or notification data. Browser
back/forward navigation MUST not leak the old workspace into the current shell.

## AUTH-03 — Sign out

**Entry points:** The account menu and an administrator/provider-initiated
logout.

**Target behavior:** Sign-out revokes the local session before any external
provider redirect. Returning with browser history, replaying an HTMX request,
or reconnecting SSE MUST not restore protected content. Provider logout failure
leaves the application signed out and presents a retryable, non-500 result.
Global provider logout revokes only correlated sessions.

## AUTH-04 — Session expiry and reauthentication

When a session expires during a read, mutation, upload, modal, or live stream,
the client MUST stop protected delivery, retain unsent local composer content,
and offer reauthentication. After reauthentication, an idempotent retry may
complete once; a non-idempotent action MUST require a clear user decision.

## AUTH-05 — Workspace invitation and first-use state

An invited person sees the target workspace, inviter when Slack exposes it,
invitation validity, and the exact acceptance consequence. Expired, revoked,
already-used, wrong-account, and workspace-disabled invitations are distinct.
After acceptance, the new membership is durable before the shell appears and
the initial channel visibility matches workspace policy.

## Evidence

- Browser: direct entry, protected permalink return, workspace switch, sign-out
  history/reconnect, session expiry with a non-empty composer, and invitation
  variants.
- Backend: durable nonce/replay protection, workspace-bound authorization,
  concurrent identity convergence, and revocation across local and distributed
  composition.
- Differential: perform equivalent entry, switching, and sign-out observations
  in a dedicated Slack workspace.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| AUTH-01 | [Sign in to Slack](https://slack.com/help/articles/212681477-Sign-in-to-Slack) | Slack identifies and opens an authorized workspace after sign-in. |
| AUTH-02 | [Switch between workspaces](https://slack.com/help/articles/212675257-Switch-between-workspaces) | Slack exposes explicit switching among signed-in workspaces. |
| AUTH-03 | [Sign out of Slack](https://slack.com/help/articles/201375146-Sign-out-of-Slack) | Slack provides workspace and global sign-out entry points. |
| AUTH-04 | [Sign in to Slack](https://slack.com/help/articles/212681477-Sign-in-to-Slack) | Reauthentication returns the member to an authorized Slack workspace. |
| AUTH-05 | [Sign in to Slack](https://slack.com/help/articles/212681477-Sign-in-to-Slack) | Workspace invitation and account entry precede authenticated workspace use. |

Sources checked 2026-07-29:

- [Sign in to Slack](https://slack.com/help/articles/212681477-Sign-in-to-Slack)
- [Switch between workspaces](https://slack.com/help/articles/212675257-Switch-between-workspaces)
- [Sign out of Slack](https://slack.com/help/articles/201375146-Sign-out-of-Slack)
