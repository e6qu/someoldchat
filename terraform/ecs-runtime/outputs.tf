output "blob_bucket_name" {
  description = "Private Amazon Simple Storage Service bucket used for durable uploads."
  value       = aws_s3_bucket.blobs.bucket
}

# A task configured from this output alone used to exit 2 at "invalid chat
# composition": -chat-mode, -store, -auth-workspace, -auth-lookup-user, and
# -auth-public-url were not exported and had no environment form, so following
# the README produced a crash-looping service. The blob settings are exported
# too, so the task's -blob-s3-prefix can never diverge from the prefix the
# task-role policy actually grants.
#
# SAMEOLDCHAT_SOCKET_TLS is "1" because auth_public_url is validated as HTTPS,
# so every client reaches the task through TLS. Socket Mode connection URLs
# otherwise follow X-Forwarded-Proto, which a caller-owned ingress may not send;
# forcing wss:// keeps apps.connections.open from handing out ws:// URLs the
# ingress refuses. The host still follows the request.
output "environment" {
  description = "Non-secret SameOldChat environment configuration for an ECS task."
  value = {
    SAMEOLDCHAT_AUTH_LOOKUP_USER      = var.auth_lookup_user
    SAMEOLDCHAT_AUTH_PUBLIC_URL       = var.auth_public_url
    SAMEOLDCHAT_AUTH_WORKSPACE        = var.auth_workspace
    SAMEOLDCHAT_BLOB_S3_BUCKET        = aws_s3_bucket.blobs.bucket
    SAMEOLDCHAT_BLOB_S3_PREFIX        = var.blob_prefix
    SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL = var.bootstrap_admin_email
    SAMEOLDCHAT_CHAT_MODE             = "local"
    SAMEOLDCHAT_OIDC_CLIENT_ID        = var.oidc_client_id
    SAMEOLDCHAT_OIDC_ISSUER           = var.oidc_issuer
    SAMEOLDCHAT_RELEASE_REVISION      = var.release_revision
    SAMEOLDCHAT_SOCKET_TLS            = "1"
    SAMEOLDCHAT_STORE                 = var.store
  }
}

# SAMEOLDCHAT_SESSION_TOKEN is deliberately absent; see the comment above
# aws_secretsmanager_secret.auth_state_key in main.tf. Exporting it made the
# task exit 2 on every start because this module always configures an OpenID
# Connect issuer.
output "secrets" {
  description = "SameOldChat secret environment variables mapped to AWS Secrets Manager ARNs."
  value = {
    SAMEOLDCHAT_API_TOKEN              = aws_secretsmanager_secret.api_token.arn
    SAMEOLDCHAT_APP_CREDENTIAL_KEY_HEX = aws_secretsmanager_secret.app_credential_key.arn
    SAMEOLDCHAT_AUTH_STATE_KEY_HEX     = aws_secretsmanager_secret.auth_state_key.arn
    SAMEOLDCHAT_OIDC_CLIENT_SECRET     = var.oidc_client_secret_arn
  }
}

output "task_policy_json" {
  description = "Least-privilege Amazon Elastic Container Service task-role policy for the blob bucket."
  value       = data.aws_iam_policy_document.task.json
}
