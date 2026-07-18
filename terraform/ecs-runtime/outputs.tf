output "blob_bucket_name" {
  description = "Private Amazon Simple Storage Service bucket used for durable uploads."
  value       = aws_s3_bucket.blobs.bucket
}

output "environment" {
  description = "Non-secret SameOldChat environment configuration for an ECS task."
  value = {
    SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL = var.bootstrap_admin_email
    SAMEOLDCHAT_OIDC_CLIENT_ID        = var.oidc_client_id
    SAMEOLDCHAT_OIDC_ISSUER           = var.oidc_issuer
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
