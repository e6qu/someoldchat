locals {
  tags = merge(var.tags, { Name = var.name })

  # Application names may be hierarchical (for example, "sameoldchat/dev")
  # for secret and tag namespaces. Amazon Simple Storage Service (Amazon S3)
  # bucket prefixes cannot contain slashes, so map that separator to the
  # equivalent bucket-safe delimiter.
  blob_bucket_prefix = "${replace(var.name, "/", "-")}-blobs-"
}

# prevent_destroy matches aws_dynamodb_table.state in deploy/ecs-scale-zero: this
# bucket holds every uploaded file, versioning is suspended, and a rename of
# var.name changes bucket_prefix, so without it a single `terraform apply` could
# replace the bucket and lose every blob with no recovery path.
resource "aws_s3_bucket" "blobs" {
  bucket_prefix = local.blob_bucket_prefix
  force_destroy = false
  tags          = local.tags
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_public_access_block" "blobs" {
  bucket                  = aws_s3_bucket.blobs.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "blobs" {
  bucket = aws_s3_bucket.blobs.id
  versioning_configuration {
    status = "Suspended"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "blobs" {
  bucket = aws_s3_bucket.blobs.id

  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

resource "random_password" "api_token" {
  length  = 48
  special = false
}

resource "random_id" "auth_state_key" {
  byte_length = 48
}

resource "random_id" "app_credential_key" {
  byte_length = 32
}

resource "aws_secretsmanager_secret" "api_token" {
  name = "${var.name}/api-token"
  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "api_token" {
  secret_id     = aws_secretsmanager_secret.api_token.id
  secret_string = random_password.api_token.result
}

# There is deliberately no session-token secret. -session-token seeds one static
# browser session shared by every holder of the value, and cmd/server refuses it
# outright once an identity provider is configured. This module makes oidc_issuer
# a required variable, so a static session could never be legal here: exporting
# it made every task built from this module's own outputs exit 2 at startup with
# "-session-token ... cannot be combined with the configured identity provider".
# scripts/check-terraform-module-startup.sh now starts the binary with exactly
# the keys these outputs export, so the next such divergence fails a gate.
resource "aws_secretsmanager_secret" "auth_state_key" {
  name = "${var.name}/auth-state-key"
  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "auth_state_key" {
  secret_id     = aws_secretsmanager_secret.auth_state_key.id
  secret_string = random_id.auth_state_key.hex
}

resource "aws_secretsmanager_secret" "app_credential_key" {
  name = "${var.name}/app-credential-key"
  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "app_credential_key" {
  secret_id     = aws_secretsmanager_secret.app_credential_key.id
  secret_string = random_id.app_credential_key.hex
}

data "aws_iam_policy_document" "task" {
  statement {
    sid       = "SameOldChatBlobBucket"
    actions   = ["s3:GetBucketLocation", "s3:ListBucket"]
    resources = [aws_s3_bucket.blobs.arn]
  }

  statement {
    sid = "SameOldChatBlobObjects"
    actions = [
      "s3:AbortMultipartUpload",
      "s3:DeleteObject",
      "s3:GetObject",
      "s3:ListMultipartUploadParts",
      "s3:PutObject",
    ]
    resources = ["${aws_s3_bucket.blobs.arn}/${var.blob_prefix}*"]
  }
}
