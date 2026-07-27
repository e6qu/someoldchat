# There is deliberately no `region` variable. It was the sole source for every
# `awslogs-region` while the module already reads `data.aws_region.current`, so a
# root module whose provider is one region and which passed another planned and
# applied cleanly and then failed every task start with
# `ResourceInitializationError: failed to validate logger args`, because the
# awslogs driver targeted a region with no such log group. `terraform validate`
# cannot see that. The provider's own region is the only value that can be right.

# name is interpolated into resource names, into load-balancer names truncated to
# 32 characters, and into secret paths; vpc_id selects the network every security
# group is created in. Every other required variable in this file validates.
variable "name" {
  type = string
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$", var.name))
    error_message = "name must be 2-32 lowercase alphanumeric characters or hyphens, starting and ending alphanumeric, because it is truncated into load-balancer and target-group names"
  }
}
variable "vpc_id" {
  type = string
  validation {
    condition     = can(regex("^vpc-[0-9a-f]{8,17}$", var.vpc_id))
    error_message = "vpc_id must be an AWS VPC identifier of the form vpc-<hex>"
  }
}
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
# The HTTP tier has no Amazon ECS service and no drain: the activator Lambda
# launches tasks with ecs:RunTask and stops them with ecs:StopTask, which sends
# SIGTERM and then SIGKILL with no fence, no snapshot, and no manifest. README.md
# calls exactly that "the blind shutdown scale-to-zero forbids, and on a profile
# where the task carries the database volume it is unrecoverable data loss".
# Nothing enforced the statelessness escape hatch, so `-store sqlite -db
# file:/data/chat.db` was accepted here and every idle sweep destroyed the
# database. A local-volume store is refused at plan time instead.
variable "application_command" {
  type    = list(string)
  default = []
  validation {
    condition     = !can(regex("(^|[[:space:]])-(store[[:space:]=]+(sqlite|dqlite)|db[[:space:]=]|dqlite-[a-z]+[[:space:]=])", join(" ", var.application_command)))
    error_message = "application_command must not select a task-local store: the HTTP tier is stopped with ecs:StopTask and has no drain or snapshot, so -store sqlite, -store dqlite, -db, and -dqlite-* would be unrecoverable data loss. Use -store memory or -store postgresql."
  }
}
variable "application_environment" {
  type    = map(string)
  default = {}
  validation {
    condition     = !contains(["sqlite", "dqlite"], lower(trimspace(lookup(var.application_environment, "SAMEOLDCHAT_STORE", ""))))
    error_message = "application_environment must not set SAMEOLDCHAT_STORE to sqlite or dqlite: the HTTP tier is stopped with ecs:StopTask and has no drain or snapshot, so a task-local store would be unrecoverable data loss. Use memory or postgresql."
  }
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
# The `scale-down` item in aws_dynamodb_table.state is shared with
# cmd/ecs-ws-activator, whose scaleDownLockTTL is 15 seconds. While it is
# unexpired both activators refuse every new request lease and every new
# WebSocket lease, so this value is the deployment's worst-case unavailability
# after a sweep that is killed before it releases the lock. The Lambda used to
# write request_timeout_seconds + 60 on the same item, which made that window
# 85 seconds.
variable "scale_down_lock_seconds" {
  type        = number
  default     = 15
  description = "Expiry of the shared scale-down lock. Must equal cmd/ecs-ws-activator's scaleDownLockTTL; the idle sweep bounds its own work by it."
  validation {
    condition     = var.scale_down_lock_seconds >= 5 && var.scale_down_lock_seconds <= var.request_timeout_seconds && floor(var.scale_down_lock_seconds) == var.scale_down_lock_seconds
    error_message = "scale_down_lock_seconds must be a whole number between 5 and request_timeout_seconds: the sweep bounds itself by it and the invocation that runs the sweep is bounded by the Lambda timeout"
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
# The load balancer that terminates every client WebSocket is internet-facing by
# default and had no connection record at all, so an abuse or outage report could
# not be reconstructed. It is optional because access logging needs an Amazon S3
# bucket with an Elastic Load Balancing bucket policy, which this module does not
# own; an empty value leaves logging off exactly as before.
variable "websocket_nlb_access_logs_bucket" {
  type        = string
  default     = ""
  description = "Amazon S3 bucket receiving Network Load Balancer access logs; empty disables access logging."
}
variable "websocket_nlb_access_logs_prefix" {
  type        = string
  default     = ""
  description = "Key prefix for Network Load Balancer access logs."
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
