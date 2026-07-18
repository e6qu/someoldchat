# Load tests

These tests exercise bounded concurrency against the in-memory repository.
They check ordering, pagination, and idempotency under concurrent writes.
They also check that session creation has one winner and that revocation is
visible through every replica using the shared store.
The outbox recovery test models a crashed worker by abandoning its lease and
checks that a replacement worker reclaims and acknowledges the event.
The scheduled-message recovery test applies the same rule to delayed message
execution and verifies that the idempotency key prevents a duplicate post.
They do not represent production capacity; use the benchmark to compare
changes and use a deployment-level load tool for capacity measurements.

Run the tests with:

```sh
make test-load
make test-load-race
go test ./tests/load -run '^$' -bench . -benchmem
```
