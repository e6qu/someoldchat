# Specifications

These files are normative. `MUST`, `MUST NOT`, `SHOULD`, and `MAY` have their
usual requirements-language meanings.

- [Product](product.md)
- [Slack API and SDK compatibility](api-compatibility.md)
- [Persistence](persistence.md)
- [Application scale-to-zero](scale-to-zero.md)
- [Dependency admission](dependency-policy.md)
- [Hosting and deployment](hosting.md)

Machine-readable source inventories, dependency evidence, and compatibility status are recorded in
[`compatibility.yaml`](compatibility.yaml) and
[`sdk-compatibility.yaml`](sdk-compatibility.yaml). Immutable copies of pinned
upstream contract sources live under [`upstream/`](upstream/).
The dependency admission inventory is [`dependency-admission.yaml`](dependency-admission.yaml)
and `make dependency-check` validates its required evidence, immutable
references, and publication quarantine.

For implementation context, see the [architecture](../docs/architecture.md),
[module](../docs/modules.md), [deployment](../docs/deployment.md), and
[operations](../docs/operations.md) documents.
