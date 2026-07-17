terraform {
  required_version = "= 1.15.8"
  required_providers {
    aws     = { source = "hashicorp/aws", version = "= 6.55.0" }
    archive = { source = "hashicorp/archive", version = "= 2.8.0" }
  }
}

resource "aws_ecs_cluster" "this" {
  name = var.name
  setting {
    name  = "containerInsights"
    value = "enabled"
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
  container_definitions = jsonencode([{ name = var.name, image = var.application_image, essential = true, command = var.application_command, environment = [for k, v in var.application_environment : { name = k, value = v }], portMappings = [{ containerPort = var.application_port, protocol = "tcp" }], logConfiguration = { logDriver = "awslogs", options = { awslogs-group = aws_cloudwatch_log_group.application.name, awslogs-region = var.region, awslogs-stream-prefix = "application" } } }])
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
    { Effect = "Allow", Action = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"], Resource = "*" },
    { Effect = "Allow", Action = ["ecs:RunTask"], Resource = aws_ecs_task_definition.application.arn, Condition = { ArnEquals = { "ecs:cluster" = aws_ecs_cluster.this.arn } } },
    { Effect = "Allow", Action = ["ecs:DescribeTasks", "ecs:ListTasks", "ecs:StopTask"], Resource = "*", Condition = { ArnEquals = { "ecs:cluster" = aws_ecs_cluster.this.arn } } },
    { Effect = "Allow", Action = ["dynamodb:PutItem", "dynamodb:DeleteItem", "dynamodb:Scan", "dynamodb:TransactWriteItems"], Resource = aws_dynamodb_table.state.arn },
    { Effect = "Allow", Action = ["iam:PassRole"], Resource = [aws_iam_role.execution.arn, var.application_task_role_arn] }
  ] })
}

resource "aws_dynamodb_table" "state" {
  name         = "${var.name}-activator"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"
  attribute {
    name = "id"
    type = "S"
  }
  ttl {
    attribute_name = "expires"
    enabled        = true
  }
}

data "archive_file" "activator" {
  type        = "zip"
  source_file = "${path.module}/activator.py"
  output_path = "${path.module}/.activator.zip"
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
  reserved_concurrent_executions = 50
  vpc_config {
    subnet_ids         = var.lambda_subnet_ids
    security_group_ids = var.lambda_security_group_ids
  }
  environment { variables = { CLUSTER = aws_ecs_cluster.this.name, TASK_DEFINITION = aws_ecs_task_definition.application.arn, STARTED_BY = "${var.name}-scale-zero", SUBNETS = join(",", var.private_subnet_ids), SECURITY_GROUPS = join(",", concat([aws_security_group.application.id], var.application_security_group_ids)), PORT = tostring(var.application_port), REPLICAS = tostring(var.application_replicas), STARTUP_TIMEOUT = tostring(var.startup_timeout_seconds), REQUEST_TIMEOUT = tostring(var.request_timeout_seconds), STATE_TABLE = aws_dynamodb_table.state.name } }
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
resource "aws_apigatewayv2_stage" "edge" {
  api_id      = aws_apigatewayv2_api.edge.id
  name        = "$default"
  auto_deploy = true
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
          region = var.region
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
          region = var.region
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
    command   = var.websocket_application_command
    environment = [
      for k, v in var.websocket_application_environment : { name = k, value = v }
    ]
    portMappings = [{ containerPort = var.websocket_application_port, protocol = "tcp" }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.websocket_application.name
        awslogs-region        = var.region
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
  cpu                      = 512
  memory                   = 1024
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
    command = [
      "-listen", ":${var.websocket_edge_port}",
      "-cluster", aws_ecs_cluster.this.name,
      "-service", aws_ecs_service.websocket_application.name,
      "-family", aws_ecs_task_definition.websocket_application.family,
      "-port", tostring(var.websocket_application_port),
      "-subnets", join(",", var.private_subnet_ids),
      "-security-groups", aws_security_group.websocket_application.id,
      "-state-table", aws_dynamodb_table.state.name,
      "-replicas", tostring(var.websocket_application_replicas),
      "-startup-timeout", "${var.websocket_startup_timeout_seconds}s",
      "-allowed-origin", var.websocket_allowed_origin,
    ]
    portMappings = [{ containerPort = var.websocket_edge_port, protocol = "tcp" }]
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.websocket_edge.name
        awslogs-region        = var.region
        awslogs-stream-prefix = "websocket-edge"
      }
    }
  }])
}

resource "aws_ecs_service" "websocket_edge" {
  name                               = "${var.name}-websocket-edge"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.websocket_edge.arn
  desired_count                      = var.websocket_edge_replicas
  launch_type                        = "FARGATE"
  platform_version                   = "1.4.0"
  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200
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
}
resource "aws_lb_target_group" "websocket_edge" {
  name        = substr("${var.name}-ws-edge", 0, 32)
  port        = var.websocket_edge_port
  protocol    = "TCP"
  target_type = "ip"
  vpc_id      = var.vpc_id
  health_check {
    protocol = "TCP"
    port     = "traffic-port"
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
