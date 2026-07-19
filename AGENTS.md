# Engineering rules

## The Boy Scout Rule

Leave every area touched by a change cleaner, safer, and easier to delete than
you found it. This is a hard rule for the whole repository. A defect does not
become out of scope because it sits in a different package, binary, storage
profile, document, test suite, deployment module, or user journey.

The system is one product. HTTP, gRPC, local composition, distributed
composition, storage, blobs, workers, lifecycle control, tooling, tests,
documentation, and user interface behavior affect one another. When a change
reveals an adjacent defect, fix it in the same cohesive change when the fix is
safe and understood. If it requires a separate architectural decision, record
the concrete finding and its boundary in the change description or an issue;
do not hide it by calling it unrelated.

The rule applies to more than executable code:

- remove dead code, duplicated code, stale generated output, and misleading
  comments;
- repair documentation links, terminology, examples, and instructions that
  the change exposes as wrong;
- strengthen tests when they omit a failure mode or assert an implementation
  detail instead of a contract;
- fix accessibility, error presentation, validation, and other usability
  problems encountered in the affected journey;
- update operational checks when a behavior change would otherwise escape
  deployment or recovery validation.

The rule does not authorize speculative rewrites. A fix must have a concrete
failure, inconsistency, deletion opportunity, or user impact behind it. Keep
the change cohesive, preserve explicit contracts, and make the smallest design
that removes the whole class of problem. Prefer a type or invariant that makes
the defect impossible over a new conditional that handles one observed case.

## Review discipline

Before declaring a change ready, inspect its callers, implementations,
generated adapters, storage backends, transport boundary, tests, operator
workflow, and documentation. Search for the old behavior and for duplicate
implementations. Run the narrow tests first, then the repository gates that
cover the affected profiles. A green test is evidence, not proof that an
unexamined adjacent contract is correct.

Do not suppress errors merely because recovery is inconvenient. Use retries,
backoff, protocol negotiation, and independent storage choices when their
contracts require them; do not use them to hide a broken primary path. Handled
errors must not become HTTP 500 responses. HTTP 500 is for unhandled
exceptions, unless the upstream Slack contract explicitly requires 500.

When context is limited, reduce the work into evidence-backed passes and keep
the discovered relationships visible in the branch and documentation. Context
limits are not a reason to discard a related finding.
