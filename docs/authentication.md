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
`SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL` provides the email address of the initial
workspace user. It must be the verified email the issuer returns: external
identities are linked only to existing users and are never auto-provisioned.

The server creates a short-lived signed state cookie and uses
Proof Key for Code Exchange (PKCE). It links a returned external subject to an
existing workspace member by provider and subject, or by verified email when
the subject has not been linked. It does not provision an unapproved member
implicitly.

## Administration

When external authorization is configured, an administrator with the relevant
Slack-compatible scope can use `/app/admin/auth` to enable or disable the
configured sources and invite a member manually.

The internal administration endpoints are:

```text
GET  /api/admin.auth.methods.list
POST /api/admin.auth.methods.set
POST /api/admin.auth.users.invite
```

Provider secrets remain deployment configuration. Enablement is workspace state
stored by the selected durable store. A disabled source returns a handled
not-found response from its login and callback routes; it does not fall back to
another source.

Related documents: [architecture](architecture.md),
[operations](operations.md), and [terminology](terminology.md).
