variable "name" {
  description = "Stable application and secret-name prefix."
  type        = string
}

variable "oidc_issuer" {
  description = "HTTPS issuer URL for the application's OpenID Connect provider."
  type        = string

  validation {
    condition     = can(regex("^https://", var.oidc_issuer))
    error_message = "oidc_issuer must be an HTTPS URL."
  }
}

variable "oidc_client_id" {
  description = "Confidential OpenID Connect client identifier registered with the issuer."
  type        = string
}

variable "oidc_client_secret_arn" {
  description = "AWS Secrets Manager ARN containing the confidential OpenID Connect client secret."
  type        = string
}

variable "release_revision" {
  description = "Immutable SameOldChat commit or image digest exposed to Shauth validation."
  type        = string

  validation {
    condition     = can(regex("^([0-9a-f]{12}|sha256:[0-9a-f]{64})$", var.release_revision))
    error_message = "release_revision must be a 12-character lowercase hexadecimal commit tag or a sha256 digest."
  }
}

variable "bootstrap_admin_email" {
  description = "Verified OIDC email address that owns the initial SameOldChat workspace user."
  type        = string

  validation {
    condition     = can(regex("^[^@[:space:]]+@[^@[:space:]]+\\.[^@[:space:]]+$", var.bootstrap_admin_email))
    error_message = "bootstrap_admin_email must be a valid email address returned by the OpenID Connect provider."
  }
}

# Every other variable in this module validates; this one did not, and it is
# interpolated straight into the task policy as "${bucket}/${prefix}*". An empty
# value produced "bucket/*" and a "*" produced "bucket/**", granting the task
# delete access to every object in the bucket including any future snapshot
# prefix.
variable "blob_prefix" {
  description = "Amazon Simple Storage Service object-key prefix for uploaded blobs."
  type        = string
  default     = "blobs/"

  validation {
    condition     = can(regex("^[A-Za-z0-9!._-][A-Za-z0-9!._/-]*/$", var.blob_prefix))
    error_message = "blob_prefix must be a non-empty key prefix ending in a slash, with no wildcard, so the task policy cannot widen to the whole bucket."
  }
}

# There is deliberately no `chat_mode` variable. It accepted "grpc", and in grpc
# composition cmd/server refuses every one of SAMEOLDCHAT_STORE,
# SAMEOLDCHAT_BLOB_S3_BUCKET, SAMEOLDCHAT_BLOB_S3_PREFIX and
# SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL — which this module's `environment` output
# always exports — because cmd/chatd owns the store there. A task built from
# these outputs with chat_mode = "grpc" therefore exited 2 on every start, the
# same defect class as the session token. This module also provisions nothing a
# distributed deployment needs: no chat gRPC address, no certificate authority,
# and no client certificate or key. It is a local-composition module and now says
# so, and scripts/check-terraform-module-startup.sh proves the binary accepts
# what it exports.

variable "store" {
  description = "Storage backend the task selects: memory, sqlite, postgresql, or dqlite."
  type        = string

  validation {
    condition     = contains(["memory", "sqlite", "postgresql", "dqlite"], var.store)
    error_message = "store must be memory, sqlite, postgresql, or dqlite; storage selection is mandatory and never inferred."
  }
}

variable "auth_workspace" {
  description = "Workspace identifier used for external authorization."
  type        = string

  validation {
    condition     = trimspace(var.auth_workspace) != ""
    error_message = "auth_workspace is required whenever an OpenID Connect provider is configured."
  }
}

variable "auth_lookup_user" {
  description = "Existing user identifier that authorizes external identity lookup."
  type        = string

  validation {
    condition     = trimspace(var.auth_lookup_user) != ""
    error_message = "auth_lookup_user is required whenever an OpenID Connect provider is configured."
  }
}

variable "auth_public_url" {
  description = "Public HTTPS URL used for authorization callbacks."
  type        = string

  validation {
    condition     = can(regex("^https://", var.auth_public_url))
    error_message = "auth_public_url must be an HTTPS URL."
  }
}

variable "tags" {
  description = "Tags applied to application-owned AWS resources."
  type        = map(string)
  default     = {}
}
