locals {
  tags = merge(var.tags, { Name = var.name })
}

resource "aws_s3_bucket" "blobs" {
  bucket_prefix = "${var.name}-blobs-"
  force_destroy = false
  tags          = local.tags
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

resource "random_password" "session_token" {
  length  = 48
  special = false
}

resource "random_id" "auth_state_key" {
  byte_length = 48
}

resource "aws_secretsmanager_secret" "api_token" {
  name = "${var.name}/api-token"
  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "api_token" {
  secret_id     = aws_secretsmanager_secret.api_token.id
  secret_string = random_password.api_token.result
}

resource "aws_secretsmanager_secret" "session_token" {
  name = "${var.name}/session-token"
  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "session_token" {
  secret_id     = aws_secretsmanager_secret.session_token.id
  secret_string = random_password.session_token.result
}

resource "aws_secretsmanager_secret" "auth_state_key" {
  name = "${var.name}/auth-state-key"
  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "auth_state_key" {
  secret_id     = aws_secretsmanager_secret.auth_state_key.id
  secret_string = random_id.auth_state_key.hex
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
