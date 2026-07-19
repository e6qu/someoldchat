# Authentication

SameOldChat separates Slack-compatible token authentication from browser
session authentication. The browser login flow uses OAuth 2.0 with one or more
explicitly configured authorization sources:

- Google;
- GitHub; and
- Microsoft Entra ID; and
- any standards-compliant OpenID Connect issuer discovered from its issuer URL,
  including Shauth.

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
workspace user. It must be the verified email the issuer returns: external
identities are linked only to existing users and are never auto-provisioned.

## Single sign-on and logout

All browser applications that should share one sign-in must use the same
durable session store and the same `SAMEOLDCHAT_AUTH_COOKIE_DOMAIN` value. The
value is a parent DNS hostname such as `example.com`, and the application URLs
must be subdomains of it. The server then issues one secure, HTTP-only session
cookie for that domain. An empty value deliberately scopes the cookie to the
single host and does not provide cross-application single sign-on.

Each application validates the shared session against the durable session
store. `POST /logout` and the existing application sign-out control revoke that
session in the store and expire the shared cookie, so logout from any
application signs the user out of all applications using that session. The
application must use the same session store in monolith and separate modes;
the gRPC session adapter is the authoritative path for separate mode.

Administrative user removal deactivates workspace membership and revokes every
session and Slack-compatible token owned by that user in the same durable
mutation. Re-enabling membership does not restore revoked credentials; the user
must authenticate again or receive a newly issued token.

The server creates a short-lived signed state cookie and uses
Proof Key for Code Exchange (PKCE). It links a returned external subject to an
existing workspace member by provider and subject, or by verified email when
the subject has not been linked. It does not provision an unapproved member
implicitly.

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
