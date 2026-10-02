# Benchmarks and profiling

## Running

    make bench                          # every benchmark
    make bench BENCH=NormalizeBlocks    # one, by regexp
    make bench BENCHTIME=5s             # longer, for a steadier number
    make bench BENCH_PKG=./internal/domain

`BENCH` is a Go benchmark regexp, so `BENCH='Normalize|Cursor'` selects a
group. Benchmarks are not part of `make check`: they measure rather than
assert, and a machine-dependent number is not a gate.

## Profiling

    make profile PROFILE_PKG=./internal/domain BENCH=NormalizeBlocks

Writes `cpu.out`, `mem.out`, and the test binary to `.cache/profiles`, then
prints the `pprof` commands to read them. `PROFILE_PKG` must name a single
package; profiles from several would overwrite each other.

    go tool pprof -top -nodecount=20 .cache/profiles/bench.test .cache/profiles/cpu.out
    go tool pprof -top -nodecount=20 -sample_index=alloc_space .cache/profiles/bench.test .cache/profiles/mem.out

`alloc_space` is usually the more useful view. The hot paths are dominated by
allocation during JSON handling rather than by computation, so a change that
does not move allocation counts rarely moves wall time either.

## What is covered

`internal/domain` covers the per-request work on the message write and
pagination paths: Block Kit, attachment, unfurl, and scope normalization, and
list cursor encoding. `NormalizeBlocks` runs at 1, 10, and 100 blocks because
its cost scales with payload size.

`internal/store/sqlstore` covers message creation (text only, 10 blocks, and
100 blocks) and listing. Creation writes the message and enqueues its durable
outbox event in one transaction, so the benchmark measures that pair, which is
the cost a caller observes.

`tests/load` has one benchmark, `BenchmarkMemoryStoreCreateMessage`, for
message creation against the in-memory store. Its concurrency and recovery
tests assert behavior under contention and run as the `make test-load` gate.

## Interpreting a change

Report `-benchmem` numbers, and prefer allocation counts over wall time when
comparing runs on different machines. Compare several runs of each side with
`benchstat`; a single pair of numbers is rarely conclusive.
