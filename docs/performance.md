# Performance Baseline and Budgets

## Scope

KuPilot measures five maintenance-sensitive surfaces: no-I/O CLI startup,
process memory, local SQLite lifecycle operations, bounded stream rendering,
and release binary size. These measurements protect the existing product from
regression; they do not create a cross-machine service-level agreement or
justify broader runtime budgets.

The hard Agent, model, Kubernetes, Tool, byte, item, and traversal ceilings in
ADR-0016 remain security limits. Performance results cannot relax them.

## Measurement environment

Every comparison records the exact source commit, dirty state, Go version,
`GOOS`, `GOARCH`, `CGO_ENABLED`, operating-system version, CPU class, physical
memory, power mode, terminal dimensions when relevant, and command line. Use
Go 1.25.12, `CGO_ENABLED=0` for product binaries, fixed synthetic inputs, a
local temporary SQLite database, no real kubeconfig, no model credential, and
no network service.

Compare candidate and accepted baseline on the same host and operating-system
session. Run the baseline first, candidate second, then reverse the order to
expose thermal or cache bias. Discard no sample except for a documented harness
failure. Report median and p95 for time, median and maximum for resident memory,
and exact bytes for artifacts. Host-specific raw samples belong in local
release evidence, not public documentation.

macOS process measurements use `/usr/bin/time -lp`; Linux uses
`/usr/bin/time -v`. Those tools report different field names, so values are
compared only within the same operating system and tool. A missing measurement
tool or benchmark target is a failed baseline, never a zero result.

## Workloads and stable budgets

<!-- markdownlint-disable MD013 -->

| Surface | Fixed workload | Samples | Acceptance budget |
| --- | --- | ---: | --- |
| No-I/O startup | A release-mode native binary executes `kupilot version`; separately exercise `help` as a zero-business-I/O contract check | 30 measured invocations after 3 untimed warm-ups | Median wall time no more than 10% above the accepted same-host baseline and p95 no more than 20% above it. Both commands create zero database, log, model, Kubernetes, Tool, approval, or executor action. |
| Process memory | Measure native `version`, bounded TUI stream-render benchmark, and SQLite lifecycle benchmark with the platform process tool | 10 process samples per workload plus Go benchmark allocation data | Maximum RSS no more than 10% or 8 MiB above the accepted same-host baseline, whichever allowance is larger. Go benchmark `B/op` and `allocs/op` stay within their workload limits below. |
| SQLite | A benchmark opens a real temporary database, applies the accepted migrations, starts and completes bounded synthetic Session/run/Evidence/Diagnosis records, queries eligible resume metadata, enforces retention, and closes cleanly | `-count=10` with `-benchmem`; one operation per fresh temporary database unless the benchmark name states reuse | Median `ns/op` no more than 15% above baseline; `B/op` no more than 10% above baseline; `allocs/op` no more than 10% above baseline. Every iteration validates owner-only paths, foreign keys, fixed migrations, and prohibited-data absence. |
| Stream render | A benchmark applies fixed bounded stream deltas and Tool-step events to an 80x24 no-color TUI model, renders after each event, and includes the maximum admitted retained transcript input | `-count=10` with `-benchmem` | Median `ns/op`, `B/op`, and `allocs/op` each no more than 10% above baseline. Output remains bounded, contains no forbidden terminal control, and accepts no stale or post-terminal event. |
| Binary size | Build the four supported macOS/Linux `amd64`/`arm64` binaries with the release toolchain, `CGO_ENABLED=0`, `-trimpath`, and read-only modules | One exact byte count per target; repeat the release build twice | No target grows more than 5% from its accepted target baseline and no uncompressed binary exceeds 96 MiB. Archive membership, CGO-free SQLite, metadata, SBOM, checksum, and sensitive-string checks still pass. |

<!-- markdownlint-enable MD013 -->

For percentage checks, an observed change smaller than the timer resolution or
one allocation is treated as no change. A candidate that exceeds a budget must
identify the responsible admitted behavior and reduce or isolate it. Raising a
threshold solely to make a regression pass is not acceptance.

## Reproducible commands

Build and artifact measurements use the repository gates:

```sh
GOTOOLCHAIN=go1.25.12 make build cross-build
```

The native binary is measured with the platform process tool by repeatedly
executing:

```sh
./bin/kupilot version
```

SQLite and stream-render measurements use Go benchmark output with memory
accounting:

```sh
GOTOOLCHAIN=go1.25.12 go test -run '^$' -bench '^BenchmarkSQLite' \
  -benchmem -count=10 ./internal/persistence/sqlite
GOTOOLCHAIN=go1.25.12 go test -run '^$' -bench '^BenchmarkStreamRender' \
  -benchmem -count=10 ./internal/tui
```

Benchmark names beneath those prefixes identify one fixed workload and must not
silently change inputs. If an input changes, retain the old workload long enough
to compare it, introduce a versioned benchmark name, and record why the new
workload better represents an admitted product path.

## Validity and safety rules

- Startup and help measurements are valid only when business-I/O absence is
  independently asserted; a fast failing network or database call is not a
  startup success.
- SQLite measurements use real temporary files and fixed synthetic canaries.
  They never reuse a user's state directory and never persist raw model, Tool,
  Kubernetes, log, prompt, credential, or vendor-error content.
- Stream inputs are deterministic and bounded. Rendering benchmarks call only
  TUI state transitions and `View`; they perform no business I/O.
- CPU profiles, heap profiles, traces, and raw benchmark files are local
  maintenance artifacts. They are reviewed for paths and external text before
  sharing and are not committed by default.
- A single wall-clock sample, virtualized runner comparison, or cross-OS number
  cannot establish a regression or a public SLA. CI may detect large changes,
  but a release decision repeats the comparison on a controlled supported host.
