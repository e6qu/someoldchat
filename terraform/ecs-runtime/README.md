# SameOldChat Amazon ECS runtime

This module owns the durable, application-specific resources for a SameOldChat
Amazon Elastic Container Service deployment:

- a private Amazon Simple Storage Service bucket for uploads, with versioning
  explicitly suspended and incomplete multipart uploads removed after one day;
- distinct AWS Secrets Manager values for the API token, browser session token,
  and OpenID Connect authorization-state key; and
- the least-privilege task-role policy needed to access the bucket.

The caller owns the generic HTTP service, network, DNS, certificate, and EFS
mount. This separation keeps SameOldChat portable while allowing an environment
to use its own Amazon Elastic Container Service ingress module. Pass the
`environment`, `secrets`, and `task_policy_json` outputs into that service.

`bootstrap_admin_email` is deliberately required. SameOldChat resolves an OIDC
identity only to an existing workspace user, so it must equal the verified
email address issued by the configured OpenID Connect provider for the initial
administrator.
