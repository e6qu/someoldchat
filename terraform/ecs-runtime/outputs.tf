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
output "environment" {
  description = "Non-secret SameOldChat environment configuration for an ECS task."
  value = {
    SAMEOLDCHAT_AUTH_LOOKUP_USER      = var.auth_lookup_user
    SAMEOLDCHAT_AUTH_PUBLIC_URL       = var.auth_public_url
    SAMEOLDCHAT_AUTH_WORKSPACE        = var.auth_workspace
    SAMEOLDCHAT_BLOB_S3_BUCKET        = aws_s3_bucket.blobs.bucket
    SAMEOLDCHAT_BLOB_S3_PREFIX        = var.blob_prefix
    SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL = var.bootstrap_admin_email
    SAMEOLDCHAT_CHAT_MODE             = var.chat_mode
    SAMEOLDCHAT_OIDC_CLIENT_ID        = var.oidc_client_id
    SAMEOLDCHAT_OIDC_ISSUER           = var.oidc_issuer
    SAMEOLDCHAT_RELEASE_REVISION      = var.release_revision
    SAMEOLDCHAT_STORE                 = var.store
  }
}

output "secrets" {
  description = "SameOldChat secret environment variables mapped to AWS Secrets Manager ARNs."
  value = {
    SAMEOLDCHAT_API_TOKEN          = aws_secretsmanager_secret.api_token.arn
    SAMEOLDCHAT_AUTH_STATE_KEY_HEX = aws_secretsmanager_secret.auth_state_key.arn
    SAMEOLDCHAT_OIDC_CLIENT_SECRET = var.oidc_client_secret_arn
    SAMEOLDCHAT_SESSION_TOKEN      = aws_secretsmanager_secret.session_token.arn
  }
}

output "task_policy_json" {
  description = "Least-privilege Amazon Elastic Container Service task-role policy for the blob bucket."
  value       = data.aws_iam_policy_document.task.json
}
