# ECS scale-to-zero

This is the current Amazon Elastic Container Service (ECS) infrastructure
module. See the [deployment guide](../../docs/deployment.md),
[hosting specification](../../specs/hosting.md), and
[scale-to-zero specification](../../specs/scale-to-zero.md) for the
provider-neutral requirements.

This module exposes an Amazon API Gateway HTTP API backed by an AWS Lambda
activator connected to the VPC. The activator starts one or more AWS Fargate
tasks with `ecs:RunTask`, waits for the task ENI and `/readyz`, then forwards
the original HTTP request. When no request is active, the application has zero
running tasks.

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
  alarm_topic_arn = aws_sns_topic.operations.arn
}
```

There is deliberately no Application Load Balancer and no Amazon ECS service
managing the application task count. The task definition is launched
directly so the zero-task state is real. The activator is the always-available
HTTP entry point; the application is stateless and must keep durable state in
its configured store.

This module implements request-triggered ECS task activation and scale-down.
It does not deploy `sameoldchat-activator` or perform the provider-neutral
hibernation state machine, snapshot publication, or restore procedure. Those
operations require a separately deployed lifecycle activator configured with
the explicit `-snapshot-store s3` settings and permissions to use the selected
snapshot bucket. The Lambda activator is not a substitute for that lifecycle
component.

The module is HTTP-only. It does not pretend that an already-established WebSocket can be transferred between processes. Use a dedicated WebSocket gateway/activator when that transport is required.

The image should be immutable (prefer a digest), contain `/readyz`, and start the server without migrations or other work that is not required for serving requests. `application_task_role_arn` is deliberately required: the application’s AWS permissions must be explicit.

`alarm_topic_arn` is required and receives alarms for activator errors and
loss of all healthy WebSocket edge targets. The module also creates a
CloudWatch dashboard with activator, ECS task, and Network Load Balancer
metrics.

The Lambda subnets must have a route to the ECS and DynamoDB APIs through NAT or the corresponding VPC endpoints. The application subnets must have ECR, CloudWatch Logs, and any application-store connectivity needed by the task. Fargate task startup is bounded by API Gateway’s HTTP integration timeout; keep the image small and pin its digest to reduce pull and bootstrap latency.

## WebSockets

The WebSocket path is separate from the HTTP path:

```text
client → NLB TLS listener → websocket-edge ECS service (always on)
                         → websocket application ECS service (desired count 0/replicas)
```

`websocket_edge_image` must contain `cmd/ecs-ws-activator`; the repository includes `Dockerfile.websocket-edge`, built from the repository root with `docker build -f deploy/ecs-scale-zero/Dockerfile.websocket-edge .`. The edge accepts the upgrade, raises the application ECS service desired count, waits for `/readyz`, performs the backend WebSocket handshake, and proxies messages in both directions. When the last connection closes, it sets the application service desired count back to zero. The application service has `ignore_changes = [desired_count]` so Terraform does not undo runtime wake/sleep decisions.

The edge service cannot scale to zero while retaining an open public socket. It is the deliberately small always-on control-plane component; only the WebSocket application service scales to zero. The NLB is also always-on and does cost money through hourly and usage-based NLB/LCU charges, even while the application service has no tasks; see [AWS Elastic Load Balancing pricing](https://aws.amazon.com/elasticloadbalancing/pricing/). Set `websocket_nlb_internal = true` for private clients.

The role supplied through `websocket_edge_task_role_arn` must allow the edge to
read and update only the configured WebSocket application service, list and
describe that service's tasks, and use `PutItem`, `DeleteItem`, `Scan`, and
`TransactWriteItems` on the configured lifecycle table. The role supplied
through `application_task_role_arn` is separate and belongs to the application
tasks. This module does not attach policies to either externally supplied role.

The HTTP and WebSocket activators coordinate scale-down through the shared
DynamoDB state table. A short-lived scale-down lease excludes new request or
socket leases while the activator checks all paginated lease records and stops
the service. This prevents a concurrent request from being stopped between an
idle check and the ECS update.

The NLB terminates TLS, so `websocket_certificate_arn` is required. An empty `websocket_allowed_origin` permits non-browser clients without an `Origin` header only; set it to the exact browser origin for browser connections. NLB TCP listeners preserve individual connections and support WebSockets. [AWS NLB listeners](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-listeners.html), [ECS service desired count](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service_definition_parameters.html)
