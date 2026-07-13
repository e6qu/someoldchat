# ECS scale-to-zero

This module exposes an API Gateway HTTP API backed by a VPC Lambda activator. The activator starts one or more Fargate tasks with `ecs:RunTask`, waits for the task ENI and `/readyz`, then forwards the original HTTP request. When no request is active, the application has zero running tasks.

Configure the AWS provider in the parent configuration and call this directory as a child module. The module intentionally does not configure a provider itself.

```hcl
provider "aws" { region = "eu-central-1" }

module "chat" {
  source = "./deploy/ecs-scale-zero"
  name = "sameoldchat"
  region = "eu-central-1"
  vpc_id = var.vpc_id
  private_subnet_ids = var.private_subnet_ids
  lambda_subnet_ids = var.lambda_subnet_ids
  lambda_security_group_ids = var.lambda_security_group_ids
  application_image = var.application_image
  application_task_role_arn = aws_iam_role.chat_task.arn
}
```

There is deliberately no ALB and no ECS service managing the application task count. The task definition is launched directly so the zero-task state is real. The activator is the always-available socket; the application is stateless and must keep durable state in its configured store.

The module is HTTP-only. It does not pretend that an already-established WebSocket can be transferred between processes. Use a dedicated WebSocket gateway/activator when that transport is required.

The image should be immutable (prefer a digest), contain `/readyz`, and start the server without migrations or other work that is not required for serving requests. `application_task_role_arn` is deliberately required: the application’s AWS permissions must be explicit.

The Lambda subnets must have a route to the ECS and DynamoDB APIs through NAT or the corresponding VPC endpoints. The application subnets must have ECR, CloudWatch Logs, and any application-store connectivity needed by the task. Fargate task startup is bounded by API Gateway’s HTTP integration timeout; keep the image small and pin its digest to reduce pull and bootstrap latency.

## WebSockets

The WebSocket path is separate from the HTTP path:

```text
client → NLB TLS listener → websocket-edge ECS service (always on)
                         → websocket application ECS service (desired count 0/replicas)
```

`websocket_edge_image` must contain `cmd/ecs-ws-activator`; the repository includes `Dockerfile.websocket-edge`, built from the repository root with `docker build -f deploy/ecs-scale-zero/Dockerfile.websocket-edge .`. The edge accepts the upgrade, raises the application ECS service desired count, waits for `/readyz`, performs the backend WebSocket handshake, and proxies messages in both directions. When the last connection closes, it sets the application service desired count back to zero. The application service has `ignore_changes = [desired_count]` so Terraform does not undo runtime wake/sleep decisions.

The edge service cannot scale to zero while retaining an open public socket. It is the deliberately small always-on control-plane component; only the WebSocket application service scales to zero. The NLB is also always-on and does cost money through hourly and usage-based NLB/LCU charges, even while the application service has no tasks; see [AWS Elastic Load Balancing pricing](https://aws.amazon.com/elasticloadbalancing/pricing/). Set `websocket_nlb_internal = true` for private clients.

The NLB terminates TLS, so `websocket_certificate_arn` is required. An empty `websocket_allowed_origin` permits non-browser clients without an `Origin` header only; set it to the exact browser origin for browser connections. NLB TCP listeners preserve individual connections and support WebSockets. [AWS NLB listeners](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-listeners.html), [ECS service desired count](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service_definition_parameters.html)
