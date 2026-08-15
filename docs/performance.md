# Performance Baseline and Budgets

## Scope

KuPilot measures six maintenance-sensitive surfaces: no-I/O CLI startup,
process memory, local SQLite migration and Session operations, the synthetic
Diagnosis fixture matrix, bounded stream merge and rendering, and release
binary size. These measurements protect admitted behavior from regression.
They are not cross-machine service-level agreements, capacity claims, or a
reason to add product complexity.

The hard Agent, model, Kubernetes, Tool, byte, item, retention, traversal, and
timeout ceilings remain security limits. Performance work cannot relax them or
bypass scope checks, normalization, redaction, Evidence validation, audit,
approval, cancellation, or persistence safety.

## Environment classes and comparison method

Every comparison records the exact source revision and dirty state, Go version,
`GOOS`, `GOARCH`, `CGO_ENABLED`, operating-system version, CPU class and core
count, physical memory, power mode, terminal dimensions when relevant, and the
complete command line. Product binaries use Go 1.25.13, `CGO_ENABLED=0`,
read-only modules, `-trimpath`, and stripped symbols. All workloads use fixed
synthetic input, temporary SQLite files, and no real kubeconfig, model
credential, network service, cluster, or user state.

Measurements have three environment classes:

- A controlled physical host on a supported target is the only class that can
  accept a time or memory trend. Compare baseline and candidate in the same
  operating-system session and power mode.
- A hosted or virtual CI runner may execute semantic smoke and absolute safety
  gates. Its timing and resident-memory values can identify a result that needs
  controlled reproduction, but cannot reject a release by themselves.
- A cross-target build host may accept only target-specific artifact checks
  such as pure-Go dependency closure, repeatable byte size, and the absolute
  binary ceiling. It cannot represent target runtime performance.

Run the accepted baseline first and the candidate second, then reverse the
order. Discard no sample except a documented harness failure. Record the first
and second pass separately before pooling them. For time, report the median and
nearest-rank p95; for `B/op` and `allocs/op`, report medians; for resident and
in-use heap memory, report the median and maximum; for artifacts, report exact
bytes. Use `benchstat` when it is already available, or an equivalent sorted
sample calculation, and record the tool and version. Statistical significance
helps interpret noise but does not replace the fixed budgets.

When no comparable accepted output exists for the same versioned workload,
the result establishes a reviewable baseline rather than proving an
improvement or regression. Host-specific samples, profiles, and comparison
reports are local maintenance evidence and do not belong in this document.

macOS peak-process measurements use `/usr/bin/time -lp`; Linux uses
`/usr/bin/time -v`. Field meanings differ, so compare only the same operating
system and tool. A missing tool or benchmark target is a failed measurement,
never a zero result.

## Versioned workloads and stable budgets

<!-- markdownlint-disable MD013 -->

| Metric | Versioned harness and fixed workload | Samples and statistic | Accepted threshold |
| --- | --- | --- | --- |
| No-I/O startup | `BenchmarkCLIProcessStartupV1` executes an already-built native binary with `version` and `help` in an empty isolated environment. It verifies bounded output and zero filesystem creation. Existing CLI contract tests independently prove zero business-composition calls. | Three warm-ups followed by 30 one-process samples for each command; median and nearest-rank p95. | Candidate median wall time is at most 10% above the comparable accepted baseline and p95 is at most 20% above it. Both commands still cause zero database, log, model, Kubernetes, Tool, approval, or executor action. |
| Peak and stable memory | Measure native `version`, one `BenchmarkSQLiteDiagnosticLifecycleV1` operation, and one `BenchmarkStreamRenderV1/RetainedHistory100x1KiB` operation as separate precompiled processes. Record platform maximum RSS, Go `B/op` and `allocs/op`, and, for benchmark test processes, `inuse_space` after the workload completes. | Ten fresh process samples per workload; median and maximum RSS, median and maximum available in-use heap, plus the workload allocation statistics below. | Maximum RSS and maximum available in-use heap each grow by no more than 10% or 8 MiB over the comparable accepted baseline, whichever allowance is larger. Allocation trends must also satisfy their workload rows. |
| SQLite | `BenchmarkSQLiteMigrationFreshV1` opens, migrates, verifies, and closes one fresh real temporary database. `BenchmarkSQLiteResumeQueryV1` measures a 50-item picker, a 50-item literal search, and an exact 100-message history. `BenchmarkSQLiteDiagnosticLifecycleV1` stores one bounded Session/run/Tool/Evidence/Diagnosis lifecycle, reads resume and Diagnosis state, runs bounded retention, verifies migrations, foreign keys, permissions, and prohibited-data absence, and closes cleanly. | Ten independent outputs with `-benchmem`; one iteration per fresh migration or lifecycle output and 100 operations per reused resume-query output. Report medians. | Median `ns/op` is at most 15% above baseline. Median `B/op` and `allocs/op` are each at most 10% above baseline. Correctness, owner-only permissions, foreign keys, fixed migration history, and prohibited-data checks must pass in every sample. |
| Diagnosis | `BenchmarkDiagnosisFixtureMatrixV1` runs all eight admitted diagnostic categories in both sufficient- and limited-Evidence variants through scripted local model and Tool adapters and applies the fixed rubric. One operation is the complete 16-fixture matrix. | Ten one-operation outputs with `-benchmem`; median `ns/op`, `B/op`, and `allocs/op`. | Each median is at most 10% above the comparable accepted baseline. Every fixture and rubric assertion must still pass; no network, Kubernetes, or external model call is permitted. |
| Stream merge and render | `BenchmarkStreamDeltaMergeV1` merges one 64 KiB stream from 64 fixed 1 KiB deltas at the Application event bridge. `BenchmarkStreamRenderV1/BoundedStream64KiB` sends a 64 KiB stream, two Tool-step states, a stale event, a terminal result, and a post-terminal event through Bubble Tea `Update`, rendering after every event. `RetainedHistory100x1KiB` reconstructs and renders the maximum retained message count using fixed 1 KiB messages. | Ten outputs with `-benchmem`; 100 merge operations, 10 bounded-stream operations, and one retained-history operation per output. Report medians. | Median `ns/op`, `B/op`, and `allocs/op` are each at most 10% above baseline. Rendered state remains bounded and terminal-safe, and stale or post-terminal input cannot change accepted run state. |
| Binary size | `binary-size-check` builds each supported macOS/Linux `amd64`/`arm64` target twice with the fixed pure-Go performance flags and compares exact bytes. | Two builds per target in one gate; retain one exact byte count for each accepted revision. | No target grows more than 5% from its supplied accepted target baseline, repeated build sizes match, and no uncompressed binary exceeds 96 MiB. CGO-free SQLite and dependency checks remain mandatory. |

<!-- markdownlint-enable MD013 -->

For percentage checks, a change smaller than timer resolution or one
allocation is treated as no change. A threshold is fixed before candidate
measurement. It must not be raised merely because a candidate fails. A
candidate over budget requires controlled reproduction and attribution to an
admitted behavior before any production change is considered.

## Reproducible commands

The low-noise semantic smoke runs every versioned harness once. It checks
correctness and isolation, not timing:

```sh
GOTOOLCHAIN=go1.25.13 make test-performance
```

Build the native performance binary with the same fixed flags used by the size
gate:

```sh
GOTOOLCHAIN=go1.25.13 make build
```

Collect startup warm-ups and measured samples separately:

```sh
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkCLIProcessStartupV1/(Version|Help)$' \
  -benchtime=1x -count=3 ./cmd/kupilot
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkCLIProcessStartupV1/(Version|Help)$' \
  -benchtime=1x -count=30 ./cmd/kupilot
```

Collect SQLite and Diagnosis samples:

```sh
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^(BenchmarkSQLiteMigrationFreshV1|BenchmarkSQLiteDiagnosticLifecycleV1)$' \
  -benchmem -benchtime=1x -count=10 ./internal/persistence/sqlite
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkSQLiteResumeQueryV1' \
  -benchmem -benchtime=100x -count=10 ./internal/persistence/sqlite
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkDiagnosisFixtureMatrixV1$' \
  -benchmem -benchtime=1x -count=10 ./internal/agent
```

Collect stream merge and render samples:

```sh
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkStreamDeltaMergeV1$' \
  -benchmem -benchtime=100x -count=10 ./internal/application
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkStreamRenderV1/BoundedStream64KiB$' \
  -benchmem -benchtime=10x -count=10 ./internal/tui
GOTOOLCHAIN=go1.25.13 go test -run '^$' \
  -bench '^BenchmarkStreamRenderV1/RetainedHistory100x1KiB$' \
  -benchmem -benchtime=1x -count=10 ./internal/tui
```

The binary gate enforces the absolute ceiling and repeat-build agreement. A
reviewed directory containing `kupilot-<goos>-<goarch>` baseline binaries also
enables the fixed 5% trend gate:

```sh
GOTOOLCHAIN=go1.25.13 make binary-size-check
GOTOOLCHAIN=go1.25.13 make binary-size-check \
  BINARY_SIZE_BASELINE_DIR='<accepted-binaries>'
```

Benchmark names ending in `V1` define immutable workload inputs. If an input
must change, retain the old harness for comparison, add a new versioned name,
and review a new baseline before applying its trend threshold.

## Memory and profile boundaries

Compile the selected test package before process-level measurement so compiler
work is not charged to the workload. Execute each test binary in a new process
for each RSS sample. The Go test harness and runtime remain part of both sides
of the comparison:

```sh
mkdir -p bin/perf
GOTOOLCHAIN=go1.25.13 go test -c -o bin/perf/sqlite.test \
  ./internal/persistence/sqlite
/usr/bin/time -lp bin/perf/sqlite.test -test.run '^$' \
  -test.bench '^BenchmarkSQLiteDiagnosticLifecycleV1$' \
  -test.benchtime=1x -test.count=1
```

Use the equivalent precompiled command for the TUI retained-history workload.
For native startup memory, measure `bin/kupilot version` directly. Repeat each
command ten times without reusing the process.

Maximum RSS is a process peak and includes the Go runtime, loaded code, test
harness where applicable, SQLite driver, and transient allocations. It is not
a retained-heap measurement. `B/op` is total allocation traffic divided by the
fixed operation count and is not peak memory. A heap profile's `inuse_space`
after benchmark completion is the stable-memory proxy for precompiled test
workloads; the short-lived native `version` process has no equivalent retained
heap sample. `alloc_space` describes cumulative allocation pressure. Neither
establishes an interactive long-run steady state because the offline harness
has no background watch or live session.

CPU and heap profiles may be collected from a precompiled benchmark binary:

```sh
bin/perf/sqlite.test -test.run '^$' \
  -test.bench '^BenchmarkSQLiteDiagnosticLifecycleV1$' \
  -test.benchtime=10x -test.cpuprofile bin/perf/sqlite.cpu.pprof \
  -test.memprofile bin/perf/sqlite.heap.pprof
GOTOOLCHAIN=go1.25.13 go tool pprof -top -nodecount=20 \
  bin/perf/sqlite.heap.pprof
```

Profiles are diagnostic evidence, not gates. Profile collection changes
runtime cost, so profiled samples are never mixed with unprofiled timing or RSS
samples.

## Validity, safety, and known limitations

- Startup results are valid only when the isolated-directory assertion and
  independent zero-composition-call tests pass. A fast failed I/O attempt is
  not successful startup.
- SQLite uses real temporary files. Fixture content is synthetic, bounded, and
  safe; database and sidecars are checked without printing stored bodies or
  local paths.
- Diagnosis uses scripted local adapters. Stream inputs call only synchronous
  Application and TUI state transitions. No benchmark owns a goroutine or
  performs model, Kubernetes, credential-helper, or public-network I/O.
- The retained-history workload combines the maximum message count with a
  fixed 1 KiB per message; the separate stream workload exercises the 64 KiB
  single-message boundary. It does not claim that every retained message will
  simultaneously contain the maximum byte count.
- Binary equality in this gate means repeatable byte size, not byte-for-byte
  reproducibility or release provenance. Release metadata, archives, checksums,
  SBOMs, and sensitive-string scans remain the responsibility of release gates.
- CPU frequency scaling, thermal state, filesystem cache, antivirus activity,
  virtualized scheduling, and timer resolution remain sources of noise. The
  reversed run order and multiple samples expose but cannot eliminate them.
- Raw benchmark output, RSS samples, profiles, and machine-specific summaries
  are reviewed locally for paths and external text before sharing and are not
  committed by default.
- These workloads do not measure cloud cost, real model or cluster latency,
  maximum cluster size, live monitoring, or concurrent users. They make no
  latency, memory, or capacity promise across machines.
