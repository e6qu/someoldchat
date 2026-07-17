# ECS scale-to-zero qualification

This qualification checks the request shapes used by the Python AWS Elastic
Container Service activator without contacting AWS. It verifies that the
activator starts tasks with an explicit `startedBy` value and uses that value
as the only `ListTasks` filter, as required by the Amazon Elastic Container
Service API.

Run it from the repository root:

```sh
make ecs-qualification
```

The suite uses only Python's standard library and substitutes the AWS clients
with test doubles. It does not qualify network, identity, or provider account
behavior.
