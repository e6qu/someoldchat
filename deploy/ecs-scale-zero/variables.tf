variable "name" { type = string }
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
    condition     = trimspace(var.application_image) != ""
    error_message = "application_image must be an immutable image reference"
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
# docs/deployment.md documents `hibernation.idle_after: 30m` and
# docs/operations.md states that hibernation begins only after the configured
# idle window. Neither was implemented: the activator stopped every task in each
# request's `finally`.
variable "idle_after_seconds" {
  type    = number
  default = 1800
  validation {
    condition     = var.idle_after_seconds >= 0
    error_message = "idle_after_seconds must not be negative; 0 stops tasks at the first sweep after a request"
  }
}
variable "idle_sweep_minutes" {
  type        = number
  default     = 5
  description = "How often the activator is invoked with no request in flight to close an elapsed idle window."
  validation {
    condition     = var.idle_sweep_minutes >= 1 && floor(var.idle_sweep_minutes) == var.idle_sweep_minutes
    error_message = "idle_sweep_minutes must be a whole number of minutes and at least 1, because EventBridge rate() expressions take whole units"
  }
}
# Matches the Go activator's MaxBodyBytes (cmd/activator/main.go), so both
# activators enforce the same contract. This path previously had no cap at all.
variable "request_max_body_bytes" {
  type    = number
  default = 4194304
  validation {
    condition     = var.request_max_body_bytes > 0
    error_message = "request_max_body_bytes must be positive"
  }
}
variable "lambda_reserved_concurrency" {
  type        = number
  default     = 50
  description = "Ceiling on concurrent activator invocations, and therefore on the ecs:RunTask rate an anonymous flood can drive."
  validation {
    condition     = var.lambda_reserved_concurrency > 0
    error_message = "lambda_reserved_concurrency must be positive"
  }
}
variable "edge_throttling_burst_limit" {
  type    = number
  default = 200
  validation {
    condition     = var.edge_throttling_burst_limit > 0
    error_message = "edge_throttling_burst_limit must be positive"
  }
}
variable "edge_throttling_rate_limit" {
  type    = number
  default = 100
  validation {
    condition     = var.edge_throttling_rate_limit > 0
    error_message = "edge_throttling_rate_limit must be positive"
  }
}
variable "container_insights" {
  type        = bool
  default     = true
  description = "Amazon CloudWatch Container Insights. Bills per ingested metric continuously; the dashboard's task-count widgets are blank when disabled."
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
    condition     = trimspace(var.websocket_application_image) != ""
    error_message = "websocket_application_image must be an immutable image reference"
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
# The WebSocket application replica count is deliberately absent. It used to be
# passed to the edge as `-replicas`, but the edge no longer scales the service:
# cmd/ecs-ws-activator asks the lifecycle activator to wake the stack, and the
# replica count is part of that activator's own start-servers configuration.
# Keeping the input would have accepted an operator's value and ignored it.

# The lifecycle activator (cmd/activator) that owns wake and hibernate for the
# WebSocket application service. It is deployed separately — see README.md — so
# its address is an input, not a resource of this module. Without it the edge
# task exits 2 at startup.
variable "websocket_lifecycle_activator_url" {
  type        = string
  description = "Base URL of the lifecycle activator serving POST /activate and POST /hibernate, for example https://lifecycle.internal.example.com"
  validation {
    condition     = can(regex("^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$", var.websocket_lifecycle_activator_url))
    error_message = "websocket_lifecycle_activator_url must be an absolute https:// base URL, because the edge sends the lifecycle control token to it on every wake and hibernate request"
  }
}
# The same value as the lifecycle activator's own -control-token. cmd/activator
# rejects /activate, /hibernate, /recover, and /metrics without it.
variable "websocket_lifecycle_activator_token" {
  type        = string
  sensitive   = true
  description = "Control-plane bearer token the edge presents to the lifecycle activator; must equal that activator's -control-token."
  validation {
    condition     = length(var.websocket_lifecycle_activator_token) >= 32
    error_message = "websocket_lifecycle_activator_token must be at least 32 characters, because it authorizes hibernating and waking the whole stack"
  }
}
# Not var.idle_after_seconds: that variable governs the HTTP tier's Lambda sweep
# and explicitly permits 0, while cmd/ecs-ws-activator rejects a non-positive
# idle interval and would crash-loop the always-on edge service. The default
# matches idle_after_seconds so a deployment that changes neither keeps one idle
# window across both paths.
variable "websocket_idle_timeout_seconds" {
  type        = number
  default     = 1800
  description = "Idle interval with no live WebSocket streams before the edge asks the lifecycle activator to hibernate."
  validation {
    condition     = var.websocket_idle_timeout_seconds > 0 && floor(var.websocket_idle_timeout_seconds) == var.websocket_idle_timeout_seconds
    error_message = "websocket_idle_timeout_seconds must be a positive whole number of seconds; cmd/ecs-ws-activator exits 2 on a non-positive idle interval"
  }
}
# A connection lease is renewed at a third of this, so a task killed without
# running its deferred release cannot hold the application service awake for
# longer than this window.
variable "websocket_lease_ttl_seconds" {
  type        = number
  default     = 90
  description = "Expiry of a WebSocket connection lease in the shared lifecycle table, renewed at a third of it."
  validation {
    condition     = var.websocket_lease_ttl_seconds >= 3 && floor(var.websocket_lease_ttl_seconds) == var.websocket_lease_ttl_seconds
    error_message = "websocket_lease_ttl_seconds must be a whole number of seconds and at least 3, because the renewal interval is a third of it"
  }
}
variable "websocket_edge_image" {
  type = string
  validation {
    condition     = trimspace(var.websocket_edge_image) != ""
    error_message = "websocket_edge_image must be an immutable image reference"
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
# The always-on tier that owns every open WebSocket, and therefore the
# deployment's continuous baseline cost. These were hardcoded while every other
# tier was a variable.
variable "websocket_edge_cpu" {
  type    = number
  default = 512
}
variable "websocket_edge_memory" {
  type    = number
  default = 1024
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
variable "websocket_nlb_deletion_protection" {
  type        = bool
  default     = true
  description = "Deletion protection on the load balancer that terminates every client WebSocket."
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
# cmd/ecs-ws-activator admits a handshake when the Origin header is empty OR
# equal to this value. With the previous default of "" both disjuncts reduced to
# "no Origin header", so a default apply answered every browser upgrade with 403
# — browsers always send Origin — while admitting non-browser clients that omit
# it: the exact inverse of the intended policy. There is no safe default, so the
# variable is required and must name an absolute HTTPS origin.
variable "websocket_allowed_origin" {
  type        = string
  description = "Absolute https:// origin permitted to open a WebSocket, for example https://chat.example.com"
  validation {
    condition     = can(regex("^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$", var.websocket_allowed_origin))
    error_message = "websocket_allowed_origin must be an absolute https:// origin with no path, because browsers always send Origin and an empty value rejects every browser"
  }
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
