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
-release-revision
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
`SAMEOLDCHAT_AUTH_COOKIE_DOMAIN` optionally scopes SameOldChat's own session
cookies to a parent DNS hostname used only by this SameOldChat deployment. It
must never be set to a parent shared with unrelated relying applications;
cross-application single sign-on comes from the issuer session instead.
`SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL`
provides the email address of the initial
workspace user. `SAMEOLDCHAT_RELEASE_REVISION` provides the immutable deployed
commit or image digest exposed by the authenticated validation page. A
configured OpenID Connect issuer is an authorization
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
`https://<application-host>/auth/shauth/logout/complete` as
`post_logout_redirect_uri`. The application bridge accepts no caller-selected
destination and redirects exactly once to Shauth's `/oauth/logout/complete`.
Shauth correlates that one-time completion and returns the browser to
SameOldChat's exact, non-redirecting `/signed-out` page. This ends the
identity-provider session and coordinates logout with the other relying
applications instead of merely clearing SameOldChat's host-scoped cookie. Only
the explicit **Sign in with Shauth** action starts another authorization flow. If
provider logout metadata is incomplete, SameOldChat still revokes its local
session and reports the incomplete global logout on that application-owned
page instead of silently claiming success.
The identity provider also sends a signed OpenID Connect back-channel logout token to
`POST /auth/oidc/backchannel-logout`. SameOldChat verifies the issuer, audience,
signature, expiration, exact standard logout event, `iat`, and `jti`, rejects a
token carrying `nonce`, and requires `sid`, `sub`, or both. A session-bound token
revokes only local sessions carrying the verified provider `sid`; a subject-bound
token revokes that provider subject's local sessions. Consumed `jti` values remain
durable until expiration so a replay is rejected across application replicas and
restarts. The receiver accepts exactly one `logout_token` in an
`application/x-www-form-urlencoded` POST body; query-string and duplicate token
delivery are rejected. The identity provider client registers
`https://<application-host>/auth/oidc/backchannel-logout` as its back-channel
logout URI and `https://<application-host>/auth/shauth/logout/complete` as its
allowed post-logout redirect URI; Shauth owns the final return to
`/signed-out`. The
application uses the same durable session store in monolith and
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

Shauth-managed deployments register `/auth/validation` as their authenticated
validation URL and `/signed-out` as their signed-out URL. `/auth/validation`
and `/me` expose the verified username, email address, synchronized
`developer` or `admin` role, and immutable release revision. Anonymous access
fails closed to the application-owned signed-out page. The repository's
`scripts/test-shauth-sso.sh` qualification starts real PostgreSQL, Ory Hydra,
Shauth, and two isolated SameOldChat relying parties, then runs Shauth's exact
browser validator for direct and catalog entry, silent SSO, application and
provider global logout, witness-session revocation, exact bridge routing,
identity, release, and credential-boundary behavior.

The Slack-compatible Sign in with Slack API is separate from the browser login
provider configuration. `POST /api/openid.connect.token` exchanges a durable,
single-use authorization code or rotates a durable refresh token. The exchange
requires the `openid` scope and verifies Proof Key for Code Exchange when the
authorization code carries a challenge. `POST /api/openid.connect.userInfo`
accepts the resulting bearer token and returns the Slack-shaped user identity.
These methods use the same selected storage backend in monolith and separate
mode; the separate mode reaches the implementation through the generated
gRPC client.

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
