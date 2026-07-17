variable "name" {
  type = string
  validation {
    condition     = trimspace(var.name) == var.name && trimspace(var.name) != "" && can(regex("^[A-Za-z0-9_-]+$", var.name)) && length("${var.name}-scale-zero") <= 128
    error_message = "name must contain only letters, numbers, hyphens, or underscores, and the derived ECS startedBy value must be at most 128 characters"
  }
}
variable "region" { type = string }
variable "vpc_id" { type = string }
variable "private_subnet_ids" {
  type = list(string)
  validation {
    condition     = length(var.private_subnet_ids) > 0
    error_message = "private_subnet_ids must contain at least one subnet"
  }
}
variable "application_image" {
  type = string
  validation {
    condition     = trimspace(var.application_image) == var.application_image && can(regex("^[^[:space:]]+@sha256:[0-9a-fA-F]{64}$", var.application_image))
    error_message = "application_image must be an immutable image reference ending in a 64-character SHA-256 digest"
  }
}
variable "application_task_role_arn" {
  type = string
  validation {
    condition     = trimspace(var.application_task_role_arn) != ""
    error_message = "application_task_role_arn must be explicit"
  }
}
variable "application_port" {
  type    = number
  default = 8080
}
variable "application_command" {
  type    = list(string)
  default = []
}
variable "application_environment" {
  type    = map(string)
  default = {}
}
variable "application_cpu" {
  type    = number
  default = 512
}
variable "application_memory" {
  type    = number
  default = 1024
}
variable "application_replicas" {
  type    = number
  default = 1
  validation {
    condition     = var.application_replicas > 0
    error_message = "application_replicas must be positive"
  }
}
variable "startup_timeout_seconds" {
  type    = number
  default = 20
  validation {
    condition     = var.startup_timeout_seconds > 0 && var.startup_timeout_seconds <= 25
    error_message = "startup_timeout_seconds must be between 1 and 25 because API Gateway HTTP API has a short integration timeout"
  }
}
variable "request_timeout_seconds" {
  type    = number
  default = 25
  validation {
    condition     = var.request_timeout_seconds > 0 && var.request_timeout_seconds <= 29
    error_message = "request_timeout_seconds must be between 1 and 29 seconds for API Gateway HTTP API"
  }
}
variable "lambda_memory_mb" {
  type    = number
  default = 1024
}
variable "lambda_subnet_ids" {
  type = list(string)
  validation {
    condition     = length(var.lambda_subnet_ids) > 0
    error_message = "lambda_subnet_ids must contain at least one subnet"
  }
}
variable "lambda_security_group_ids" {
  type = list(string)
  validation {
    condition     = length(var.lambda_security_group_ids) > 0
    error_message = "lambda_security_group_ids must contain at least one security group"
  }
}
variable "application_security_group_ids" {
  type    = list(string)
  default = []
}
variable "log_retention_days" {
  type    = number
  default = 30
}

variable "alarm_topic_arn" {
  type = string
  validation {
    condition     = trimspace(var.alarm_topic_arn) != ""
    error_message = "alarm_topic_arn must identify the SNS topic for deployment alarms"
  }
}

variable "websocket_application_image" {
  type = string
  validation {
    condition     = trimspace(var.websocket_application_image) == var.websocket_application_image && can(regex("^[^[:space:]]+@sha256:[0-9a-fA-F]{64}$", var.websocket_application_image))
    error_message = "websocket_application_image must be an immutable image reference ending in a 64-character SHA-256 digest"
  }
}
variable "websocket_application_task_role_arn" {
  type = string
  validation {
    condition     = trimspace(var.websocket_application_task_role_arn) != ""
    error_message = "websocket_application_task_role_arn must be explicit"
  }
}
variable "websocket_application_port" {
  type    = number
  default = 8081
}
variable "websocket_application_command" {
  type    = list(string)
  default = []
}
variable "websocket_application_environment" {
  type    = map(string)
  default = {}
}
variable "websocket_application_cpu" {
  type    = number
  default = 512
}
variable "websocket_application_memory" {
  type    = number
  default = 1024
}
variable "websocket_application_replicas" {
  type    = number
  default = 1
  validation {
    condition     = var.websocket_application_replicas > 0
    error_message = "websocket_application_replicas must be positive"
  }
}
variable "websocket_edge_image" {
  type = string
  validation {
    condition     = trimspace(var.websocket_edge_image) == var.websocket_edge_image && can(regex("^[^[:space:]]+@sha256:[0-9a-fA-F]{64}$", var.websocket_edge_image))
    error_message = "websocket_edge_image must be an immutable image reference ending in a 64-character SHA-256 digest"
  }
}
variable "websocket_edge_task_role_arn" {
  type = string
  validation {
    condition     = trimspace(var.websocket_edge_task_role_arn) != ""
    error_message = "websocket_edge_task_role_arn must be explicit"
  }
}
variable "websocket_edge_port" {
  type    = number
  default = 8080
}
variable "websocket_edge_subnet_ids" {
  type = list(string)
  validation {
    condition     = length(var.websocket_edge_subnet_ids) > 0
    error_message = "websocket_edge_subnet_ids must contain at least one subnet"
  }
}
variable "websocket_nlb_subnet_ids" {
  type = list(string)
  validation {
    condition     = length(var.websocket_nlb_subnet_ids) > 0
    error_message = "websocket_nlb_subnet_ids must contain at least one subnet"
  }
}
variable "websocket_nlb_internal" {
  type    = bool
  default = false
}
variable "websocket_listener_port" {
  type    = number
  default = 443
}
variable "websocket_certificate_arn" {
  type = string
  validation {
    condition     = trimspace(var.websocket_certificate_arn) != ""
    error_message = "websocket_certificate_arn must identify the TLS certificate used by the NLB listener"
  }
}
variable "websocket_allowed_origin" {
  type    = string
  default = ""
}
variable "websocket_startup_timeout_seconds" {
  type    = number
  default = 120
  validation {
    condition     = var.websocket_startup_timeout_seconds > 0
    error_message = "websocket_startup_timeout_seconds must be positive"
  }
}
variable "websocket_edge_replicas" {
  type    = number
  default = 2
  validation {
    condition     = var.websocket_edge_replicas > 0
    error_message = "websocket_edge_replicas must be positive because the edge owns the open socket"
  }
}
