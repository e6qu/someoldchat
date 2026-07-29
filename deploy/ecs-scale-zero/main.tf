# Exact versions, not ranges. specs/dependency-policy.md forbids wildcard ranges,
# and the two modules previously pinned mutually unsatisfiable AWS provider
# majors (">= 5.0, < 6.0" here against ">= 5.0, < 7.0" in terraform/ecs-runtime),
# so a root module consuming both could not `terraform init` at all.
terraform {
  required_version = "1.15.8"
  required_providers {
    aws     = { source = "hashicorp/aws", version = "6.56.0" }
    archive = { source = "hashicorp/archive", version = "2.7.1" }
  }
}

data "aws_partition" "current" {}
data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

resource "aws_ecs_cluster" "this" {
  name = var.name
  setting {
    name = "containerInsights"
    # Container Insights bills per ingested metric continuously, independent of
    # task count, so a module whose stated purpose is a near-zero idle baseline
    # must let an operator turn it off. It stays on by default because
    # aws_cloudwatch_dashboard.scale_zero reads ECS/ContainerInsights metrics;
    # the task-count widgets go blank when it is disabled.
    value = var.container_insights ? "enabled" : "disabled"
  }
}

resource "aws_cloudwatch_log_group" "application" {
  name              = "/ecs/${var.name}/application"
  retention_in_days = var.log_retention_days
}

resource "aws_security_group" "application" {
  name        = "${var.name}-application"
  description = "Application tasks; ingress is only from the activator security groups"
  vpc_id      = var.vpc_id
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
  dynamic "ingress" {
    for_each = var.lambda_security_group_ids
    content {
      protocol        = "tcp"
      from_port       = var.application_port
      to_port         = var.application_port
      security_groups = [ingress.value]
    }
  }
  tags = { Name = "${var.name}-application" }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-ecs-execution"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ecs-tasks.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}
resource "aws_iam_role_policy" "worker_secrets" {
  role = aws_iam_role.execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["secretsmanager:GetSecretValue"]
      Resource = values(var.worker_secrets)
    }]
  })
}

resource "aws_ecs_task_definition" "application" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.application_cpu
  memory                   = var.application_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = var.application_task_role_arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }
  # flag-contract: caller-supplied
  # The application command is a module input, so no gate can resolve it to a
  # binary. What the module *can* enforce is recorded on var.application_command
  # itself: the HTTP tier is stopped with ecs:StopTask and has no drain, so a
  # task-local store is refused at plan time.
  container_definitions = jsonencode([{ name = var.name, image = var.application_image, essential = true, command = var.application_command, environment = [for k, v in var.application_environment : { name = k, value = v }], portMappings = [{ containerPort = var.application_port, protocol = "tcp" }], logConfiguration = { logDriver = "awslogs", options = { awslogs-group = aws_cloudwatch_log_group.application.name, awslogs-region = data.aws_region.current.region, awslogs-stream-prefix = "application" } } }])
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/ecs/${var.name}/worker"
  retention_in_days = var.log_retention_days
}

# Scheduled messages and app-event delivery must continue while the request
# tier is at zero. This service is therefore an explicit always-on data-plane
# component, backed by the shared PostgreSQL store rather than task-local state.
resource "aws_ecs_task_definition" "worker" {
  family                   = "${var.name}-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.worker_cpu
  memory                   = var.worker_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = var.worker_task_role_arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }
  container_definitions = jsonencode([{
    name      = "${var.name}-worker"
    image     = var.worker_image
    essential = true
    # flag-contract: caller-supplied
    command     = var.worker_command
    environment = [for k, v in var.worker_environment : { name = k, value = v }]
    secrets     = [for k, v in var.worker_secrets : { name = k, valueFrom = v }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.worker.name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = "worker"
      }
    }
  }])
}

resource "aws_ecs_service" "worker" {
  name             = "${var.name}-worker"
  cluster          = aws_ecs_cluster.this.id
  task_definition  = aws_ecs_task_definition.worker.arn
  desired_count    = 1
  launch_type      = "FARGATE"
  platform_version = "1.4.0"
  # -owner is a lease identity. A rolling overlap would run two processes with
  # the same owner and defeat fencing, so replacement stops the old singleton
  # before starting the new one.
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = concat([aws_security_group.application.id], var.application_security_group_ids)
    assign_public_ip = false
  }
}

resource "aws_iam_role" "activator" {
  name               = "${var.name}-activator"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "lambda.amazonaws.com" }, Action = "sts:AssumeRole" }] })
}
resource "aws_iam_role_policy_attachment" "activator_vpc" {
  role       = aws_iam_role.activator.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}
resource "aws_iam_role_policy" "activator" {
  role = aws_iam_role.activator.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [
    # Scoped to this deployment's own log group. It used to be Resource = "*",
    # which let a compromised activator write into any log group in the account.
    # Note that aws_iam_role_policy_attachment.activator_vpc attaches the
    # AWS-managed VPC access policy, which still grants account-wide log writes;
    # replacing it with a scoped elastic-network-interface policy is recorded as
    # a follow-up in README.md rather than guessed at here.
    { Effect = "Allow", Action = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"], Resource = "${aws_cloudwatch_log_group.activator.arn}:*" },
    { Effect = "Allow", Action = ["ecs:RunTask"], Resource = aws_ecs_task_definition.application.arn, Condition = { ArnEquals = { "ecs:cluster" = aws_ecs_cluster.this.arn } } },
    { Effect = "Allow", Action = ["ecs:ListTasks"], Resource = "*", Condition = { ArnEquals = { "ecs:cluster" = aws_ecs_cluster.this.arn } } },
    # DescribeTasks and StopTask are scoped to this cluster's task ARNs. An
    # ArnEquals condition on a key the request context does not carry evaluates
    # false and denies, so the cluster condition is kept only on ListTasks,
    # where ECS documents `ecs:cluster`.
    { Effect = "Allow", Action = ["ecs:DescribeTasks", "ecs:StopTask"], Resource = "arn:${data.aws_partition.current.partition}:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task/${aws_ecs_cluster.this.name}/*" },
    { Effect = "Allow", Action = ["dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:DeleteItem", "dynamodb:Scan", "dynamodb:TransactWriteItems"], Resource = aws_dynamodb_table.state.arn },
    # Without the PassedToService condition this role could hand the task role —
    # which holds the object-storage and secret access — to any service that
    # accepts a passed role, which is a privilege-escalation primitive.
    { Effect = "Allow", Action = ["iam:PassRole"], Resource = [aws_iam_role.execution.arn, var.application_task_role_arn], Condition = { StringEquals = { "iam:PassedToService" = "ecs-tasks.amazonaws.com" } } }
  ] })
}

# Holds the wake lock, the scale-down lock, the idle-window marker, and every
# in-flight request and WebSocket lease. Because the name embeds var.name, a
# rename would otherwise replace the table in one apply and destroy every lock
# and lease at once, after which both activators believe nothing is held and can
# start a second wake and a simultaneous scale-down.
resource "aws_dynamodb_table" "state" {
  name                        = "${var.name}-activator"
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "id"
  deletion_protection_enabled = true
  attribute {
    name = "id"
    type = "S"
  }
  ttl {
    attribute_name = "expires"
    enabled        = true
  }
  point_in_time_recovery {
    enabled = true
  }
  lifecycle {
    prevent_destroy = true
  }
}

data "archive_file" "activator" {
  type        = "zip"
  source_file = "${path.module}/activator.py"
  # Written under .terraform so `make clean` and the module .gitignore already
  # cover it; it used to land next to the source as .activator.zip.
  output_path = "${path.module}/.terraform/tmp/activator.zip"
}
resource "aws_lambda_function" "activator" {
  function_name                  = var.name
  role                           = aws_iam_role.activator.arn
  runtime                        = "python3.12"
  handler                        = "activator.handler"
  filename                       = data.archive_file.activator.output_path
  source_code_hash               = data.archive_file.activator.output_base64sha256
  memory_size                    = var.lambda_memory_mb
  timeout                        = var.request_timeout_seconds
  reserved_concurrent_executions = var.lambda_reserved_concurrency
  vpc_config {
    subnet_ids         = var.lambda_subnet_ids
    security_group_ids = var.lambda_security_group_ids
  }
  environment { variables = { CLUSTER = aws_ecs_cluster.this.name, TASK_DEFINITION = aws_ecs_task_definition.application.arn, SUBNETS = join(",", var.private_subnet_ids), SECURITY_GROUPS = join(",", concat([aws_security_group.application.id], var.application_security_group_ids)), PORT = tostring(var.application_port), REPLICAS = tostring(var.application_replicas), STARTUP_TIMEOUT = tostring(var.startup_timeout_seconds), REQUEST_TIMEOUT = tostring(var.request_timeout_seconds), STATE_TABLE = aws_dynamodb_table.state.name, IDLE_AFTER = tostring(var.idle_after_seconds), MAX_BODY_BYTES = tostring(var.request_max_body_bytes), SCALE_DOWN_LOCK_SECONDS = tostring(var.scale_down_lock_seconds) } }
}

# The request that restarts the idle window can never be the invocation that
# observes it elapsed, so scale-down needs an invocation with no request in
# flight. Without this the activator stopped every task in each request's
# `finally`, which meant no idle window and a cold start per page load.
resource "aws_cloudwatch_event_rule" "activator_idle_sweep" {
  name                = "${var.name}-activator-idle-sweep"
  description         = "Stops application tasks once the configured idle window has elapsed"
  schedule_expression = "rate(${var.idle_sweep_minutes} ${var.idle_sweep_minutes == 1 ? "minute" : "minutes"})"
}
resource "aws_cloudwatch_event_target" "activator_idle_sweep" {
  rule  = aws_cloudwatch_event_rule.activator_idle_sweep.name
  arn   = aws_lambda_function.activator.arn
  input = jsonencode({ sameoldchat_maintenance = true })
}
resource "aws_lambda_permission" "activator_idle_sweep" {
  statement_id  = "AllowIdleSweep"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.activator.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.activator_idle_sweep.arn
}

resource "aws_apigatewayv2_api" "edge" {
  name          = var.name
  protocol_type = "HTTP"
}
resource "aws_apigatewayv2_integration" "edge" {
  api_id                 = aws_apigatewayv2_api.edge.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.activator.invoke_arn
  integration_method     = "POST"
  payload_format_version = "2.0"
  timeout_milliseconds   = var.request_timeout_seconds * 1000
}
resource "aws_apigatewayv2_route" "edge" {
  api_id    = aws_apigatewayv2_api.edge.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.edge.id}"
}
# The unauthenticated public front door of a scale-to-zero deployment, where
# each request can trigger ecs:RunTask. With only the account-level default an
# anonymous flood drove Lambda invocations and Fargate task starts without
# bound, and no request log existed to reconstruct it afterwards.
resource "aws_apigatewayv2_stage" "edge" {
  api_id      = aws_apigatewayv2_api.edge.id
  name        = "$default"
  auto_deploy = true
  default_route_settings {
    throttling_burst_limit = var.edge_throttling_burst_limit
    throttling_rate_limit  = var.edge_throttling_rate_limit
  }
  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.edge_access.arn
    format = jsonencode({
      requestId        = "$context.requestId"
      requestTime      = "$context.requestTime"
      httpMethod       = "$context.httpMethod"
      path             = "$context.path"
      status           = "$context.status"
      protocol         = "$context.protocol"
      responseLength   = "$context.responseLength"
      integrationError = "$context.integrationErrorMessage"
    })
  }
}
resource "aws_cloudwatch_log_group" "edge_access" {
  name              = "/aws/apigateway/${var.name}/access"
  retention_in_days = var.log_retention_days
}
resource "aws_lambda_permission" "api" {
  statement_id  = "AllowHttpApi"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.activator.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.edge.execution_arn}/*/*"
}

resource "aws_cloudwatch_log_group" "websocket_application" {
  name              = "/ecs/${var.name}/websocket-application"
  retention_in_days = var.log_retention_days
}
resource "aws_cloudwatch_log_group" "websocket_edge" {
  name              = "/ecs/${var.name}/websocket-edge"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "activator" {
  name              = "/aws/lambda/${var.name}"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_metric_alarm" "activator_errors" {
  alarm_name          = "${var.name}-activator-errors"
  alarm_description   = "The scale-to-zero HTTP activator returned or recorded Lambda errors"
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = aws_lambda_function.activator.function_name }
  statistic           = "Sum"
  period              = 60
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [var.alarm_topic_arn]
}

resource "aws_cloudwatch_metric_alarm" "websocket_edge_unhealthy" {
  alarm_name          = "${var.name}-websocket-edge-unhealthy"
  alarm_description   = "The WebSocket edge has no healthy NLB targets"
  namespace           = "AWS/NetworkELB"
  metric_name         = "HealthyHostCount"
  dimensions          = { LoadBalancer = aws_lb.websocket.arn_suffix, TargetGroup = aws_lb_target_group.websocket_edge.arn_suffix }
  statistic           = "Minimum"
  period              = 60
  evaluation_periods  = 2
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [var.alarm_topic_arn]
}

resource "aws_cloudwatch_dashboard" "scale_zero" {
  dashboard_name = "${var.name}-scale-zero"
  dashboard_body = jsonencode({
    widgets = [
      {
        type   = "metric"
        width  = 12
        height = 6
        properties = {
          title  = "HTTP activator"
          region = data.aws_region.current.region
          stat   = "Sum"
          period = 60
          metrics = [
            ["AWS/Lambda", "Invocations", "FunctionName", aws_lambda_function.activator.function_name],
            [".", "Errors", ".", "."],
            [".", "Throttles", ".", "."],
          ]
        }
      },
      {
        type   = "metric"
        width  = 12
        height = 6
        properties = {
          title  = "WebSocket edge and application tasks"
          region = data.aws_region.current.region
          stat   = "Average"
          period = 60
          metrics = [
            ["ECS/ContainerInsights", "RunningTaskCount", "ServiceName", aws_ecs_service.websocket_edge.name, "ClusterName", aws_ecs_cluster.this.name],
            [".", "RunningTaskCount", ".", aws_ecs_service.websocket_application.name, ".", "."],
            ["AWS/NetworkELB", "HealthyHostCount", "LoadBalancer", aws_lb.websocket.arn_suffix, "TargetGroup", aws_lb_target_group.websocket_edge.arn_suffix],
          ]
        }
      },
    ]
  })
}

resource "aws_security_group" "websocket_nlb" {
  name        = "${var.name}-websocket-nlb"
  description = "Public WebSocket NLB"
  vpc_id      = var.vpc_id
  ingress {
    protocol    = "tcp"
    from_port   = var.websocket_listener_port
    to_port     = var.websocket_listener_port
    cidr_blocks = ["0.0.0.0/0"]
  }
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}
resource "aws_security_group" "websocket_edge" {
  name        = "${var.name}-websocket-edge"
  description = "WebSocket activator tasks"
  vpc_id      = var.vpc_id
  ingress {
    protocol        = "tcp"
    from_port       = var.websocket_edge_port
    to_port         = var.websocket_edge_port
    security_groups = [aws_security_group.websocket_nlb.id]
  }
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}
resource "aws_security_group" "websocket_application" {
  name        = "${var.name}-websocket-application"
  description = "WebSocket application tasks; ingress is only from the activator"
  vpc_id      = var.vpc_id
  ingress {
    protocol        = "tcp"
    from_port       = var.websocket_application_port
    to_port         = var.websocket_application_port
    security_groups = [aws_security_group.websocket_edge.id]
  }
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_ecs_task_definition" "websocket_application" {
  family                   = "${var.name}-websocket"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.websocket_application_cpu
  memory                   = var.websocket_application_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = var.websocket_application_task_role_arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }
  container_definitions = jsonencode([{
    name      = "${var.name}-websocket"
    image     = var.websocket_application_image
    essential = true
    # flag-contract: caller-supplied
    command = var.websocket_application_command
    environment = [
      for k, v in var.websocket_application_environment : { name = k, value = v }
    ]
    portMappings = [{ containerPort = var.websocket_application_port, protocol = "tcp" }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.websocket_application.name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = "websocket-application"
      }
    }
  }])
}

resource "aws_ecs_service" "websocket_application" {
  name                               = "${var.name}-websocket"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.websocket_application.arn
  desired_count                      = 0
  launch_type                        = "FARGATE"
  platform_version                   = "1.4.0"
  deployment_minimum_healthy_percent = 0
  deployment_maximum_percent         = 100
  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.websocket_application.id]
    assign_public_ip = false
  }
  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_task_definition" "websocket_edge" {
  family                   = "${var.name}-websocket-edge"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.websocket_edge_cpu
  memory                   = var.websocket_edge_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = var.websocket_edge_task_role_arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }
  container_definitions = jsonencode([{
    name      = "${var.name}-websocket-edge"
    image     = var.websocket_edge_image
    essential = true
    # scripts/check-task-definition-flags.sh (make task-flags-check) resolves the
    # annotation below and fails when this list names a flag the binary does not
    # define, or omits one it requires. Every entry here must stay a literal so
    # the check can read it: `-subnets`, `-security-groups`, and `-replicas`
    # survived here for a whole release after the binary stopped accepting them,
    # and no gate could see it.
    # flag-contract: cmd/ecs-ws-activator
    command = [
      "-listen", ":${var.websocket_edge_port}",
      "-cluster", aws_ecs_cluster.this.name,
      "-service", aws_ecs_service.websocket_application.name,
      "-family", aws_ecs_task_definition.websocket_application.family,
      "-port", tostring(var.websocket_application_port),
      "-state-table", aws_dynamodb_table.state.name,
      "-startup-timeout", "${var.websocket_startup_timeout_seconds}s",
      "-idle-timeout", "${var.websocket_idle_timeout_seconds}s",
      "-lease-ttl", "${var.websocket_lease_ttl_seconds}s",
      # The edge no longer changes DesiredCount itself; it reports arrival and
      # idleness to the separately deployed lifecycle activator, which owns the
      # drain, snapshot, verify, and stop sequence.
      "-activator-url", var.websocket_lifecycle_activator_url,
      "-activator-token", var.websocket_lifecycle_activator_token,
      "-allowed-origin", var.websocket_allowed_origin,
    ]
    portMappings = [{ containerPort = var.websocket_edge_port, protocol = "tcp" }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.websocket_edge.name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = "websocket-edge"
      }
    }
  }])
}

resource "aws_ecs_service" "websocket_edge" {
  name             = "${var.name}-websocket-edge"
  cluster          = aws_ecs_cluster.this.id
  task_definition  = aws_ecs_task_definition.websocket_edge.arn
  desired_count    = var.websocket_edge_replicas
  launch_type      = "FARGATE"
  platform_version = "1.4.0"
  # The edge holds the client end of every proxied WebSocket, so ECS must never
  # be allowed to terminate a task before its replacement is healthy: at 50 with
  # two replicas, half of all connected clients were disconnected on every image
  # update, and a bad image had no rollback.
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = var.websocket_startup_timeout_seconds
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  network_configuration {
    subnets          = var.websocket_edge_subnet_ids
    security_groups  = [aws_security_group.websocket_edge.id]
    assign_public_ip = false
  }
  load_balancer {
    target_group_arn = aws_lb_target_group.websocket_edge.arn
    container_name   = "${var.name}-websocket-edge"
    container_port   = var.websocket_edge_port
  }
}

resource "aws_lb" "websocket" {
  name                             = substr("${var.name}-websocket", 0, 32)
  load_balancer_type               = "network"
  internal                         = var.websocket_nlb_internal
  subnets                          = var.websocket_nlb_subnet_ids
  security_groups                  = [aws_security_group.websocket_nlb.id]
  enable_cross_zone_load_balancing = true
  enable_deletion_protection       = var.websocket_nlb_deletion_protection
  dynamic "access_logs" {
    for_each = trimspace(var.websocket_nlb_access_logs_bucket) == "" ? [] : [1]
    content {
      bucket  = var.websocket_nlb_access_logs_bucket
      prefix  = var.websocket_nlb_access_logs_prefix
      enabled = true
    }
  }
}
resource "aws_lb_target_group" "websocket_edge" {
  name        = substr("${var.name}-ws-edge", 0, 32)
  port        = var.websocket_edge_port
  protocol    = "TCP"
  target_type = "ip"
  vpc_id      = var.vpc_id
  # cmd/ecs-ws-activator serves GET /healthz. A TCP probe kept a wedged task
  # healthy, so ECS never replaced it and the unhealthy-target alarm never fired.
  #
  # The matcher is the whole 2xx range, not "200". cmd/ecs-ws-activator answers
  # 204 while internal/activator answers 200 with a JSON body, and an exact "200"
  # made every edge target permanently unhealthy: with
  # deployment_minimum_healthy_percent = 100 and the deployment circuit breaker
  # the service never converged, no WebSocket traffic was ever served, and
  # aws_cloudwatch_metric_alarm.websocket_edge_unhealthy fired continuously. The
  # probe's contract is "the process answers this route successfully"; binding it
  # to one status code makes the module depend on an implementation detail the
  # binary never promised.
  health_check {
    protocol = "HTTP"
    port     = "traffic-port"
    path     = "/healthz"
    matcher  = "200-299"
  }
}
resource "aws_lb_listener" "websocket" {
  load_balancer_arn = aws_lb.websocket.arn
  port              = var.websocket_listener_port
  protocol          = "TLS"
  certificate_arn   = var.websocket_certificate_arn
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.websocket_edge.arn
  }
}
