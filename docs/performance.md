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

`alloc_space` is usually the more useful view here. The hot paths are dominated
by allocation during JSON handling rather than by computation, so a change that
does not move allocation counts rarely moves wall time either.

## What is covered and why

`internal/domain` covers the per-request work on the message write and
pagination paths: Block Kit and attachment normalisation, unfurl
normalisation, cursor encoding, and scope normalisation. The array normalisers
are benchmarked at one, ten, and a hundred items because their cost scales with
payload size, and a single number would hide that.

`internal/store/sqlstore` covers message creation and listing. Creation writes
the message and enqueues its durable outbox event in one transaction, so the
benchmark measures that pair rather than either half; splitting them would
report a number no caller can observe.

`tests/load` holds concurrency and recovery tests. Those assert behaviour under
contention and are part of `make test-load`, which is a gate.

## Interpreting a change

Report `-benchmem` numbers, and prefer allocation counts over wall time when
comparing runs on different machines. `benchstat` over several runs of each
side is the honest comparison; a single pair of numbers usually is not.

Two examples from the change that introduced these benchmarks, both verified by
fuzzing before and after:

- `normalizeJSONArrayObjects` decoded every array element into a map purely to
  learn whether it was an object. The enclosing decode had already rejected
  malformed JSON, so checking the leading byte answers the same question:
  100 blocks went from 1415 allocations to 115.
- `NormalizeUnfurls` called `json.Valid` and then `json.Compact` on the same
  bytes. Compaction already reports malformed input, so the validation pass was
  a second scan of every value.

Both were found by reading a benchmark, not by reading the code.
