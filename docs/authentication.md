# Authentication

SameOldChat separates Slack-compatible token authentication from browser
session authentication. The browser login flow uses OAuth 2.0 with one or more
explicitly configured authorization sources:

- Google;
- GitHub; and
- Microsoft Entra ID; and
- any standards-compliant OpenID Connect issuer discovered from its issuer URL.

The server accepts a source only when its required configuration is complete.
It rejects unknown source names, incomplete GitHub email configuration, empty
scope entries, and duplicate source names during startup. It does not select a
different source when the selected source is unavailable.

## Configuration

The server command accepts these credentials and settings:

```text
-google-client-id
-google-client-secret
-github-client-id
-github-client-secret
-entra-client-id
-entra-client-secret
-entra-tenant
-oidc-issuer
-oidc-client-id
-oidc-client-secret
-auth-workspace
-auth-lookup-user
-auth-public-url
-auth-cookie-domain
-auth-state-key-hex
```

Supplying an incomplete provider configuration is invalid. OpenID Connect
discovery requires an HTTPS issuer whose discovery document reports the same
issuer and HTTPS authorization, token, and user-info endpoints. If any external authorization
credential is supplied, the workspace, lookup user, public HTTPS URL, and
32-byte state key are required. GitHub login also requires the GitHub email
endpoint, which the server configures as `https://api.github.com/user/emails`.

For container deployment, `SAMEOLDCHAT_API_TOKEN`,
`SAMEOLDCHAT_SESSION_TOKEN`, `SAMEOLDCHAT_AUTH_STATE_KEY_HEX`,
`SAMEOLDCHAT_OIDC_ISSUER`, `SAMEOLDCHAT_OIDC_CLIENT_ID`, and
`SAMEOLDCHAT_OIDC_CLIENT_SECRET` provide the corresponding flag defaults.
`SAMEOLDCHAT_AUTH_COOKIE_DOMAIN` sets the shared parent DNS hostname for
cross-application browser single sign-on. `SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL`
provides the email address of the initial
workspace user. A configured OpenID Connect issuer is an authorization
boundary: an identity carrying a `developer` or `admin` role is provisioned as
an active workspace member on first sign-in, and its workspace role is kept in
sync on later sign-ins. Other external providers still require an existing
workspace user with the same verified email.

## Single sign-on and logout

Cross-application single sign-on comes from the configured OpenID Connect
issuer session. Each relying application keeps its own host-scoped session;
when a new application starts authorization, the identity provider recognizes
the existing identity session and completes the authorization-code flow
without asking the user to authenticate again. `SAMEOLDCHAT_AUTH_COOKIE_DOMAIN`
controls only the scope of SameOldChat's own secure, HTTP-only cookie. It is
not the cross-application identity boundary.

`POST /logout` revokes the current SameOldChat session and expires its cookie.
For a session created through the configured OpenID Connect provider, it then
redirects the browser to the discovered `end_session_endpoint` with the
durably retained ID token, the client ID, and
`https://<application-host>/signed-out` as `post_logout_redirect_uri`. This ends the
identity-provider session and coordinates logout with the other relying
applications instead of merely clearing SameOldChat's host-scoped cookie. The
provider returns the browser to SameOldChat's non-redirecting signed-out page;
only the explicit **Sign in again** link starts another authorization flow. If
provider logout metadata is incomplete, SameOldChat still revokes its local
session and reports the incomplete global logout on that application-owned
page instead of silently claiming success.
The identity provider also sends a signed OpenID Connect back-channel logout token to
`POST /auth/oidc/backchannel-logout`. SameOldChat verifies the issuer, audience,
signature, expiration, standard logout event, `sub`, `iat`, and `jti`, rejects a
token carrying `nonce`, resolves the verified issuer subject, and revokes every
local session for that user. The identity provider client must register
`https://<application-host>/auth/oidc/backchannel-logout` as its back-channel
logout URI and `https://<application-host>/signed-out` as an allowed post-logout redirect
URI. The application uses the same durable session store in monolith and
separate modes; the gRPC session adapter remains the authoritative path for
separate mode.

Administrative user removal deactivates workspace membership and revokes every
session and Slack-compatible token owned by that user in the same durable
mutation. Re-enabling membership does not restore revoked credentials; the user
must authenticate again or receive a newly issued token.

The server creates a short-lived signed state cookie and uses
Proof Key for Code Exchange (PKCE). OpenID Connect authorization requests also
bind the returned ID token to a per-request nonce. The resulting SameOldChat
session cannot outlive that ID token or the application's 24-hour maximum. It
links a returned external subject to an
existing workspace member by provider and subject, or by verified email when
the subject has not been linked. An OpenID Connect identity is provisioned only
when the configured issuer returns a supported `developer` or `admin` role;
missing or unknown roles fail closed.

## Administration

When external authorization is configured, an administrator with the relevant
Slack-compatible scope can use `/app/admin/auth` to enable or disable the
configured sources and create an active member manually. Manual creation uses
the supplied verified email, creates durable workspace membership, and accepts
only the `member` or `admin` role. It does not create a password or bypass the
configured authorization source.

The internal administration endpoints are:

```text
GET  /api/admin.auth.methods.list
POST /api/admin.auth.methods.set
GET  /api/admin.auth.users.list
POST /api/admin.auth.users.invite
POST /api/admin.auth.users.create
POST /api/admin.auth.users.set
```

The user list accepts `limit` from 1 through 100 and an opaque `cursor`. It
returns each user with the durable workspace role and active membership state.
The user mutation endpoint requires `user_id` and one of `disable`, `enable`,
or `role` as its explicit `action`. A role mutation accepts only `member` or
`admin` in this internal control plane. Deactivation also revokes the user's
sessions and access tokens. Re-enabling a user restores active membership
without assigning conversation membership.

Provider secrets remain deployment configuration. Enablement is workspace state
stored by the selected durable store. The invitation endpoint remains the
Slack-compatible invitation workflow and requires its channel and invitation
parameters. The manual-user endpoint is the explicit internal administration
workflow. A disabled source returns a handled not-found response from its login
and callback routes; it does not fall back to another source.

Related documents: [architecture](architecture.md),
[operations](operations.md), and [terminology](terminology.md).
