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
