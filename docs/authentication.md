# Authentication

SameOldChat separates Slack-compatible token authentication from browser
session authentication. The browser login flow uses OAuth 2.0 with one or more
explicitly configured authorization sources:

- Google;
- GitHub; and
- Microsoft Entra ID.

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
-auth-workspace
-auth-lookup-user
-auth-public-url
-auth-state-key-hex
```

Supplying one credential from a pair is invalid. If any external authorization
credential is supplied, the workspace, lookup user, public HTTPS URL, and
32-byte state key are required. GitHub login also requires the GitHub email
endpoint, which the server configures as `https://api.github.com/user/emails`.
Microsoft Entra login also requires `-entra-tenant`; there is no implicit
tenant default. The value may be a tenant identifier, verified domain, or an
explicit Microsoft tenant selector such as `common`, `organizations`, or
`consumers`.

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
stored by the selected durable store. A configured source is disabled until an
administrator explicitly enables it. A disabled source returns a handled
not-found response from its login and callback routes; it does not fall back to
another source.

Related documents: [architecture](architecture.md),
[operations](operations.md), and [terminology](terminology.md).
