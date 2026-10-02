# SameOldChat Amazon ECS runtime

This module owns the durable, application-specific resources for a SameOldChat
Amazon Elastic Container Service deployment:

- a private Amazon Simple Storage Service bucket for uploads, with versioning
  explicitly suspended, `prevent_destroy` set, and incomplete multipart uploads
  removed after one day. A hierarchical `name` has its `/` replaced with `-` in
  the bucket prefix and is used unchanged for secrets and tags;
- distinct AWS Secrets Manager values for the API token, the application
  credential key, and the OpenID Connect authorization-state key; and
- the least-privilege task-role policy needed to access the bucket.

The caller owns the HTTP service, network, DNS, certificate, and EFS mount, so
an environment can use its own Amazon Elastic Container Service ingress module.
Pass the `environment`, `secrets`, and `task_policy_json` outputs into that
service. The service runs `cmd/server`, published as `ghcr.io/e6qu/someoldchat`
(see [published container verification](../../docs/deployment.md#published-container-verification)).

It is a sibling of [`deploy/ecs-scale-zero`](../../deploy/ecs-scale-zero/README.md),
not a replacement: this module owns the durable application resources, that one
owns request-triggered activation. Both pin the same exact AWS provider version
so a single root configuration can consume them together.

Every variable shown in the example below is required. See
[`variables.tf`](variables.tf) for the optional ones and their defaults.

```hcl
module "chat_runtime" {
  source = "./terraform/ecs-runtime"

  name                   = "sameoldchat"
  store                  = "postgresql"
  auth_workspace         = "Tdev"
  auth_lookup_user       = "Udev"
  auth_public_url        = "https://chat.example.com"
  bootstrap_admin_email  = "admin@example.com"
  oidc_issuer            = "https://id.example.com"
  oidc_client_id         = var.oidc_client_id
  oidc_client_secret_arn = aws_secretsmanager_secret.oidc_client_secret.arn
  release_revision       = var.release_revision
}
```

## Outputs

The `environment` output carries `SAMEOLDCHAT_CHAT_MODE` (always `local`),
`SAMEOLDCHAT_STORE`, `SAMEOLDCHAT_AUTH_WORKSPACE`,
`SAMEOLDCHAT_AUTH_LOOKUP_USER`, `SAMEOLDCHAT_AUTH_PUBLIC_URL`,
`SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL`, `SAMEOLDCHAT_OIDC_ISSUER`,
`SAMEOLDCHAT_OIDC_CLIENT_ID`, `SAMEOLDCHAT_RELEASE_REVISION`,
`SAMEOLDCHAT_BLOB_S3_BUCKET`, `SAMEOLDCHAT_BLOB_S3_PREFIX`, and
`SAMEOLDCHAT_SOCKET_TLS=1`. The server needs these to start; the blob settings
are exported so the task's `-blob-s3-prefix` cannot diverge from the prefix
`task_policy_json` grants. `SAMEOLDCHAT_SOCKET_TLS=1` is set because
`auth_public_url` must be HTTPS and a caller-owned ingress may not send
`X-Forwarded-Proto`, so Socket Mode connection URLs are `wss://` on whatever
host the app called `apps.connections.open` on (see
[Socket Mode](../../docs/operations.md#connections)).

The `secrets` output maps `SAMEOLDCHAT_API_TOKEN`,
`SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX`, `SAMEOLDCHAT_AUTH_STATE_KEY_HEX`, and
`SAMEOLDCHAT_OIDC_CLIENT_SECRET` to Secrets Manager ARNs. It deliberately
contains no `SAMEOLDCHAT_SESSION_TOKEN`: `cmd/server` refuses a static browser
session when an identity provider is configured, and `oidc_issuer` is required
here. `make module-startup-check` starts the binary with exactly the keys
`environment` and `secrets` export and fails if it refuses them.

## Local composition only

This module configures local composition only. It provisions no chat gRPC
address, certificate authority, or client certificate, and the `environment`
output carries the storage and bootstrap settings that `cmd/server` refuses in
`grpc` composition, where `sameoldchat-chatd` owns the store instead. In
particular, `bootstrap_admin_email` reaches the store through the process that
owns it, which here is always `cmd/server`.

`bootstrap_admin_email` is required for the initial local administrator used by
the authorization control plane. An authorized OpenID Connect identity carrying
a `developer` or `admin` role is provisioned as its own durable workspace user
on first sign-in; it is not collapsed into the bootstrap account by email.

## Identity provider registration

The OpenID Connect client registration must allow
`https://<application-host>/auth/shauth/logout/complete` as the RP-initiated
post-logout redirect URI and register
`https://<application-host>/auth/oidc/backchannel-logout` as the back-channel
logout URI; see [authentication](../../docs/authentication.md#single-sign-on-and-logout).
`release_revision` must be the exact 12-character commit tag of the deployed
image, or its complete image digest; the module exposes it to the task as
`SAMEOLDCHAT_RELEASE_REVISION` for Shauth validation.

## Known boundaries

Bucket versioning is `Suspended`, so a `sameoldchat-blobgc` deletion is not
recoverable, and the three Secrets Manager secrets the module creates use the
AWS-managed key rather than a customer managed key. Changing either needs a
deliberate input (a retention/cost decision and a KMS key ARN respectively),
and neither can be validated without applying.
