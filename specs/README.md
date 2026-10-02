# Specifications

These files are normative. `MUST`, `MUST NOT`, `SHOULD`, and `MAY` have their
usual requirements-language meanings.

- [Product](product.md)
- [Slack user journeys](journeys/README.md)
- [Slack API and SDK compatibility](api-compatibility.md)
- [Slack app platform compatibility](slack-app-platform.md)
- [Persistence](persistence.md)
- [Application scale-to-zero](scale-to-zero.md)
- [Dependency admission](dependency-policy.md)
- [Hosting and deployment](hosting.md)

The [product gap audit](product-gap-audit.md) is the maintained inventory of
known gaps against these targets.

Machine-readable records:

- [`compatibility.yaml`](compatibility.yaml): Web API source inventory and
  per-method compatibility status (`make contract-check`,
  `make compatibility-report`);
- [`sdk-compatibility.yaml`](sdk-compatibility.yaml): pinned official SDK
  artifacts and suite results (`make sdk-inventory-check`);
- [`dependency-admission.yaml`](dependency-admission.yaml): dependency
  evidence and publication quarantine (`make dependency-check`); and
- [`upstream/`](upstream/): immutable copies of pinned upstream contract
  sources.

For implementation context, see the [architecture](../docs/architecture.md),
[module](../docs/modules.md), [deployment](../docs/deployment.md), and
[operations](../docs/operations.md) documents.
