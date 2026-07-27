# SameOldChat Amazon ECS runtime

This module owns the durable, application-specific resources for a SameOldChat
Amazon Elastic Container Service deployment:

- a private Amazon Simple Storage Service bucket for uploads, with versioning
  explicitly suspended, `prevent_destroy` set, and incomplete multipart uploads
  removed after one day. Hierarchical application names are normalized to an
  Amazon S3-safe bucket prefix while remaining unchanged for secrets and tags;
- distinct AWS Secrets Manager values for the API token and the OpenID Connect
  authorization-state key; and
- the least-privilege task-role policy needed to access the bucket.

This module deliberately creates **no** browser session token. `-session-token`
seeds one static browser session shared by every holder of the value, and
`cmd/server` refuses it as soon as an identity provider is configured. Because
`oidc_issuer` is required here, exporting a session token made every task built
from these outputs exit 2 on startup. `make module-startup-check` starts the
binary with exactly the keys `environment` and `secrets` export and fails if it
refuses them.

Known boundaries, recorded rather than guessed at: bucket versioning is
`Suspended`, so a `sameoldchat-blobgc` deletion is not recoverable, and the two
AWS Secrets Manager secrets use the AWS-managed key rather than a customer
managed key. Both need a deliberate input (a retention/cost decision and a KMS
key ARN respectively) and neither can be validated without applying.

The caller owns the generic HTTP service, network, DNS, certificate, and EFS
mount. This separation keeps SameOldChat portable while allowing an environment
to use its own Amazon Elastic Container Service ingress module. Pass the
`environment`, `secrets`, and `task_policy_json` outputs into that service.

It is a sibling of [`deploy/ecs-scale-zero`](../../deploy/ecs-scale-zero), not a
replacement: this module owns the durable application resources, that one owns
request-triggered activation. Both pin the same exact AWS provider version so a
single root configuration can consume them together.

This module configures **local composition only**. It provisions no chat gRPC
address, certificate authority, or client certificate, and the `environment`
output always carries the storage and bootstrap settings that `cmd/server`
refuses in `grpc` composition, where `sameoldchat-chatd` owns the store instead.
The `chat_mode` variable used to accept `grpc` and produced exactly that
refusal, so it is gone and `SAMEOLDCHAT_CHAT_MODE` is exported as `local`.

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

The `environment` output carries `SAMEOLDCHAT_CHAT_MODE`, `SAMEOLDCHAT_STORE`,
`SAMEOLDCHAT_AUTH_WORKSPACE`, `SAMEOLDCHAT_AUTH_LOOKUP_USER`,
`SAMEOLDCHAT_AUTH_PUBLIC_URL`, `SAMEOLDCHAT_BLOB_S3_BUCKET`, and
`SAMEOLDCHAT_BLOB_S3_PREFIX` in addition to the OpenID Connect and release
values. All of these are required for the server to start, and the blob settings
are exported so the task's `-blob-s3-prefix` cannot diverge from the prefix
`task_policy_json` actually grants.

`bootstrap_admin_email` reaches the store through the process that owns it,
which for this module is always `sameoldchat` itself. A distributed deployment
puts it on `sameoldchat-chatd`; `cmd/server` rejects it, and every other
store-owning setting, in `grpc` composition rather than accepting and dropping
it, which is why this module is local-composition only.

`bootstrap_admin_email` is deliberately required for the initial local
administrator used by the authorization control plane. An authorized OpenID
Connect identity carrying a `developer` or `admin` role is provisioned as its
own durable workspace user on first sign-in; it is not collapsed into the
bootstrap account by email.

The OpenID Connect client registration must allow the exact
`https://<application-host>/auth/shauth/logout/complete` bridge as the
RP-initiated post-logout redirect URI and register
`https://<application-host>/auth/oidc/backchannel-logout` as the back-channel
logout URI. `release_revision` must identify the exact deployed commit or image
digest; the module exposes it to the task as `SAMEOLDCHAT_RELEASE_REVISION` for
Shauth validation.
