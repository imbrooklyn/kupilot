# ADR-0039: Use Configurable Runtime Budget Profiles

- Status: Accepted
- Date: 2026-08-30
- Amended: 2026-09-01
- Supersedes: ADR-0016

## Context

The original 90-second, three-model-call, and ten-Tool-call ceilings routinely
stop legitimate multi-resource investigations before the Agent can correlate
workloads, Nodes, Events, and logs. Treating those MVP values as permanent
security maxima makes the runtime predictable but not operationally useful.

Unbounded execution would create a different failure: unexpected model cost,
API load, memory growth, and an Agent loop that cannot be supervised.

## Decision

Every run still receives an immutable local budget snapshot, but operators
choose one code-defined profile before the run starts:

| Budget | Compact | Balanced (default) | Extended | Hard ceiling |
| --- | ---: | ---: | ---: | ---: |
| Run wall clock | 2 min | 10 min | 30 min | 30 min |
| Agent steps | 12 | 32 | 64 | 128 |
| Tool calls | 16 | 48 | 128 | 256 |
| Model calls | 6 | 16 | 32 | 64 |
| Model request | 60 sec | 120 sec | 300 sec | 300 sec |
| Kubernetes request | 15 sec | 30 sec | 60 sec | 60 sec |
| Cumulative Tool results | 1 MiB | 4 MiB | 12 MiB | 16 MiB |
| Log calls | 4 | 12 | 32 | 32 |
| No-progress steps | 2 | 4 | 6 | 10 |

One ToolResult remains bounded, and each capability has independent item, log,
and traversal ceilings. Profiles may be tightened by code-defined adapter or
privacy policy. They cannot be changed during a run, raised by model output, or
set to unlimited.

The model request admits at most 322 Eino conversation messages, derived from
two initial messages plus the model-call and Tool-call hard ceilings; its
serialized body still cannot exceed 256 KiB. The ordered internal Agent event
stream is capped
at 32,768 events so the maximum Tool/Evidence profile fits without becoming an
unbounded event channel.

`model.max_output_tokens` defaults to the existing per-request hard ceiling of
8,192. A lower implicit default would add a second truncation budget without a
separate safety boundary and can cut off a Tool call or final diagnosis while
the bounded byte, request-time, call-count, and run budgets still have room.
Operators may explicitly tighten the token value for a chosen endpoint or cost
policy, but configuration cannot raise it above 8,192.

The independent model-stream hard ceilings are 8 MiB of raw wire data and
32,768 SSE data records or decoded chunks. Those values provide finite SSE and
JSON framing headroom at the output-token ceiling without treating tokens and
stream fragments as equivalent. One record remains capped at 64 KiB and one
assembled assistant response, including discarded reasoning, remains capped at
128 KiB.

Reservation remains atomic and occurs before external I/O. Per-request
deadlines are capped by the remaining run duration. Cancellation, generation
change, terminal state, repeated identical calls without an admitted retry
reason, and hard-ceiling exhaustion still stop new work.

`/status` reports the active profile, elapsed and remaining run time, and
used/maximum steps, Tool calls, model calls, Tool-result bytes, and log calls.
The transcript receives a concise reason when a limit stops work; the footer
does not permanently display all counters.

## Consequences

The default can complete ordinary multi-resource investigations, while compact
and extended profiles let operators choose latency/cost tradeoffs before a run.
The local hard ceiling keeps worst-case behavior finite.

Longer runs increase the importance of cancellation, stale-scope rejection,
status visibility, and deterministic accounting tests.

## Security and privacy impact

A larger budget authorizes more already-admitted reads; it does not authorize a
new Kind, Namespace mode, data field, model origin, or write. Consent,
projection, sensitive-data handling, RBAC, approval, and retention remain
independent gates.

## Validation

Fake-clock and counting tests must cover every profile, exact limits, one-over
values, remaining-time deadline caps, cancellation, stale scope, concurrent
reservation, byte accounting, and `/status` snapshots. Tests must also prove
that the output-token default equals its fixed ceiling, unknown profiles, and
any value above a hard ceiling fail before model or Kubernetes I/O.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [Configuration](../configuration.md)
