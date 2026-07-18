# Load tests

These tests exercise bounded concurrency against the in-memory repository.
They check ordering, pagination, and idempotency under concurrent writes.
They do not represent production capacity; use the benchmark to compare
changes and use a deployment-level load tool for capacity measurements.

Run the tests with:

```sh
make test-load
make test-load-race
go test ./tests/load -run '^$' -bench . -benchmem
```
