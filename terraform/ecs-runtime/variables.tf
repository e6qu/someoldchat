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

variable "bootstrap_admin_email" {
  description = "Verified OIDC email address that owns the initial SameOldChat workspace user."
  type        = string

  validation {
    condition     = can(regex("^[^@[:space:]]+@[^@[:space:]]+\\.[^@[:space:]]+$", var.bootstrap_admin_email))
    error_message = "bootstrap_admin_email must be a valid email address returned by the OpenID Connect provider."
  }
}

variable "blob_prefix" {
  description = "Amazon Simple Storage Service object-key prefix for uploaded blobs."
  type        = string
  default     = "blobs/"
}

variable "tags" {
  description = "Tags applied to application-owned AWS resources."
  type        = map(string)
  default     = {}
}
