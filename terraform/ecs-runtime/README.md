# SameOldChat Amazon ECS runtime

This module owns the durable, application-specific resources for a SameOldChat
Amazon Elastic Container Service deployment:

- a private Amazon Simple Storage Service bucket for uploads, with versioning
  explicitly suspended and incomplete multipart uploads removed after one day.
  Hierarchical application names are normalized to an Amazon S3-safe bucket
  prefix while remaining unchanged for secrets and tags;
- distinct AWS Secrets Manager values for the API token, browser session token,
  and OpenID Connect authorization-state key; and
- the least-privilege task-role policy needed to access the bucket.

The caller owns the generic HTTP service, network, DNS, certificate, and EFS
mount. This separation keeps SameOldChat portable while allowing an environment
to use its own Amazon Elastic Container Service ingress module. Pass the
`environment`, `secrets`, and `task_policy_json` outputs into that service.

It is a sibling of [`deploy/ecs-scale-zero`](../../deploy/ecs-scale-zero), not a
replacement: this module owns the durable application resources, that one owns
request-triggered activation. Both pin the same exact AWS provider version so a
single root configuration can consume them together.

Every variable below has no default and is therefore required.

```hcl
module "chat_runtime" {
  source = "./terraform/ecs-runtime"

  name                   = "sameoldchat"
  store                  = "postgresql"
  chat_mode              = "local"
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

`bootstrap_admin_email` reaches the store through the process that owns it:
`sameoldchat` in local composition and `sameoldchat-chatd` in distributed
composition. In distributed composition the server rejects the setting rather
than accepting and dropping it.

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
