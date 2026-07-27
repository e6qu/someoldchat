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

Every variable below has no default and is therefore required; the WebSocket
tier is part of this module, not an optional add-on, so a call that omits those
variables fails `terraform plan` with "No value for required variable".

```hcl
provider "aws" { region = "eu-central-1" }

module "chat" {
  source = "./deploy/ecs-scale-zero"

  name                      = "sameoldchat"
  region                    = "eu-central-1"
  vpc_id                    = var.vpc_id
  private_subnet_ids        = var.private_subnet_ids
  lambda_subnet_ids         = var.lambda_subnet_ids
  lambda_security_group_ids = var.lambda_security_group_ids
  application_image         = var.application_image
  application_task_role_arn = aws_iam_role.chat_task.arn
  alarm_topic_arn           = aws_sns_topic.operations.arn

  # WebSocket tier; see WebSockets below.
  websocket_application_image         = var.websocket_application_image
  websocket_application_task_role_arn = aws_iam_role.chat_websocket_task.arn
  websocket_edge_image                = var.websocket_edge_image
  websocket_edge_task_role_arn        = aws_iam_role.chat_websocket_edge.arn
  websocket_edge_subnet_ids           = var.private_subnet_ids
  websocket_nlb_subnet_ids            = var.public_subnet_ids
  websocket_certificate_arn           = aws_acm_certificate.chat.arn
  websocket_allowed_origin            = "https://chat.example.com"

  # The separately deployed lifecycle activator that owns wake and hibernate.
  websocket_lifecycle_activator_url   = "https://lifecycle.internal.example.com"
  websocket_lifecycle_activator_token = var.lifecycle_control_token
}
```

Scale-down is not immediate. `idle_after_seconds` (default 1800, matching
`hibernation.idle_after: 30m` in the deployment guide) keeps the tasks running
after the last request, and `aws_cloudwatch_event_rule.activator_idle_sweep`
invokes the activator every `idle_sweep_minutes` with no request in flight to
close an elapsed window. The request path itself never stops a task, so a user
browsing the application does not pay a cold start per page load.

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

The WebSocket tier is not merely compatible with that lifecycle activator, it
**requires** one: the edge delegates every wake and hibernate decision to it
through `websocket_lifecycle_activator_url`. Deploy `sameoldchat-activator`
first, then apply this module with its base URL and control token.

The HTTP API Gateway path and the WebSocket Network Load Balancer path are two
separate deployment paths inside this module. Neither transfers an
already-established WebSocket between processes: the edge task terminates the
client socket for its whole lifetime. See [WebSockets](#websockets) below.

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
                         → POST /activate, /hibernate → lifecycle activator
                                                        (deployed separately)
                                                        → websocket application
                                                          ECS service
                         → proxied WebSocket → websocket application task
```

`websocket_edge_image` must contain `cmd/ecs-ws-activator`; the repository includes `Dockerfile.websocket-edge`, built from the repository root with
`docker buildx build --platform linux/amd64 -f deploy/ecs-scale-zero/Dockerfile.websocket-edge .`.
The `--platform` argument matters: the task definitions declare
`cpu_architecture = "X86_64"`, and the `scale-zero-artifacts` job in
`.github/workflows/ci.yml` builds this file for both published architectures and
asserts the resulting binary's machine type so a host-architecture binary cannot
reach an image again. The edge accepts the upgrade, asks the lifecycle activator
to wake the stack with `POST /activate`, waits for `/readyz` on a running task,
performs the backend WebSocket handshake, and proxies messages in both
directions. When the last connection closes and
`websocket_idle_timeout_seconds` has elapsed with no live stream lease, it asks
the lifecycle activator to hibernate with `POST /hibernate`. The application
service has `ignore_changes = [desired_count]` so Terraform does not undo
runtime wake/sleep decisions.

The edge deliberately never changes `DesiredCount` itself. Scaling the
application service is one step of a fenced hibernate/wake protocol that drains
ingress, drains the outbox, snapshots, verifies, and publishes a manifest;
driving the desired count directly is the blind shutdown
[scale-to-zero](../../specs/scale-to-zero.md) forbids, and on a profile where
the task carries the database volume it is unrecoverable data loss. This is why
`websocket_lifecycle_activator_url` and `websocket_lifecycle_activator_token`
are required, and why there is no WebSocket replica count input: the replica
count belongs to the lifecycle activator's own start-servers configuration.

`websocket_lifecycle_activator_url` must be reachable from the edge subnets over
`https`. The edge presents `websocket_lifecycle_activator_token` as a bearer
token, so the transport, not IAM, is what protects it; a plain-`http` activator
address is rejected by validation.

### Known boundary: the control token is in the task definition

`cmd/ecs-ws-activator` accepts the control token only as the `-activator-token`
command-line flag, so this module has to place it in the container command of
`aws_ecs_task_definition.websocket_edge`. Anyone who can call
`ecs:DescribeTaskDefinition` in the account can read it, and it is recorded in
every task-definition revision. Restrict that permission and rotate the token
with the lifecycle activator's `-control-token`. Teaching the binary to read the
token from a file or environment variable — so it could use the container
definition's `secrets` block and AWS Secrets Manager — is a change to
`cmd/ecs-ws-activator`, not to this module.

The edge service cannot scale to zero while retaining an open public socket. It is the deliberately small always-on control-plane component; only the WebSocket application service scales to zero. The NLB is also always-on and does cost money through hourly and usage-based NLB/LCU charges, even while the application service has no tasks; see [AWS Elastic Load Balancing pricing](https://aws.amazon.com/elasticloadbalancing/pricing/). Set `websocket_nlb_internal = true` for private clients.

The role supplied through `websocket_edge_task_role_arn` must allow the edge to
`ecs:ListTasks` and `ecs:DescribeTasks` for the configured WebSocket application
service, and to use `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Scan`, and
`TransactWriteItems` on the configured lifecycle table. It must **not** be
granted `ecs:UpdateService`: the edge no longer changes the desired count, and
the permission would only restore the blind-shutdown path. There is no IAM
action for reaching the lifecycle activator; that call is authorized by the
bearer token and needs only network reachability from the edge subnets. The role
supplied through `application_task_role_arn` is separate and belongs to the
application tasks. This module does not attach policies to either externally
supplied role.

### Known boundary

`aws_iam_role_policy.activator` is scoped to this deployment's log group, task
ARNs, and lifecycle table, and its `iam:PassRole` statement is conditioned on
`iam:PassedToService = ecs-tasks.amazonaws.com`. The role still attaches the
AWS-managed `AWSLambdaVPCAccessExecutionRole`, which grants account-wide
CloudWatch Logs writes. Replacing it with a scoped elastic-network-interface
policy is a separate change: getting those permissions wrong makes the function
unable to attach to the VPC at all, which cannot be validated without applying.

The HTTP and WebSocket activators coordinate scale-down through the shared
DynamoDB state table. A short-lived scale-down lease excludes new request or
socket leases while the activator checks all paginated lease records; the HTTP
Lambda then stops its tasks and the WebSocket edge then requests hibernation.
This prevents a concurrent request from being stopped between an idle check and
the stop decision. The edge also polls for idleness rather than deciding only on
disconnect, because the last disconnect is not the moment the idle interval
elapses.

The NLB terminates TLS, so `websocket_certificate_arn` is required. `websocket_allowed_origin` is required and must be the exact absolute
`https://` origin browsers will connect from. `cmd/ecs-ws-activator` admits a
handshake when `Origin` is absent **or** equal to this value, so an empty value
admits only clients that send no `Origin` at all — which rejects every browser,
since browsers always send it. That is why the variable has no default. NLB TCP listeners preserve individual connections and support WebSockets. [AWS NLB listeners](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-listeners.html), [ECS service desired count](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service_definition_parameters.html)
