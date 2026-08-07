# ADR-0016: Freeze Runtime Budgets

- Status: Accepted
- Date: 2026-08-08

## Context

Model streams, repeated Tool choices, Kubernetes lists, Events, container
output, and relationship traversal are controlled partly by external systems.
Without hard local ceilings, a single AgentRun could consume unbounded time,
memory, cluster API capacity, terminal space, or model spend. Prompt requests to
"be concise" are not enforceable budgets.

The same limits must apply across success, partial, retry, malformed, and error
paths and must not change because the model asks for more.

## Decision

`v0.1` freezes these code-defined maximums:

<!-- markdownlint-disable MD013 -->

| Budget | Maximum |
| --- | --- |
| AgentRun wall clock | 90 seconds |
| One Kubernetes request | 10 seconds |
| Kubernetes client throughput | QPS 5 and Burst 10 |
| One model request | 45 seconds |
| Agent loop | 8 steps, 10 Tool calls, and 3 model requests |
| Repeated call | The same Tool name and canonical arguments at most twice; the second call requires a retryable error or explicit state revalidation |
| ToolResult bytes | 64 KiB per result and 384 KiB cumulative per run |
| Result items | 50 resources, 50 Events, and 100 Evidence items per result |
| Log reads | 200 tail lines, a 15-minute window, 64 KiB per result, and 2 log calls per run |
| Related resources | 2 hops, 25 nodes, and 40 edges |
| No-progress stop | Stop collection after 2 consecutive Agent steps produce no new Evidence |

<!-- markdownlint-enable MD013 -->

Runtime owns and atomically reserves counters before external I/O. A failed
reservation produces zero external calls. Child request deadlines are capped by
both their per-request maximum and the remaining run time. Cancellation or a
terminal state prevents further reservation.

Canonical repeated-call identity includes the fixed Tool version and all
model-supplied canonical arguments, but excludes runtime-injected scope fields
that are already frozen per run. A second identical call is admitted only after
a stable retryable error or an explicit runtime state-revalidation need; model
prose alone is insufficient.

Adapters may apply smaller protocol or server limits. Configuration may tighten
any maximum for a run's frozen policy snapshot, but cannot raise it. Limit
exhaustion returns a safe partial result or terminal Diagnosis with explicit
missing information. It never causes a wider query, another Namespace, a longer
window, or a hidden retry.

## Consequences

Positive consequences:

- Worst-case time, request count, output, and traversal are reviewable.
- Cost and resource-exhaustion tests can assert exact boundary behavior.
- Partial Evidence and missing information remain preferable to policy bypass.
- Configuration cannot silently expand the supported security envelope.

Costs and constraints:

- Some incidents will exceed the limits and require the user to ask a narrower
  follow-up question or use another tool.
- Counters, byte accounting, and deadlines must be consistent across adapters.
- UTF-8, serialization, and truncation boundaries require deterministic
  definitions rather than approximate display length.
- Changing a ceiling is a product and security decision, not tuning during a
  run.

## Alternatives considered

- Provider- or server-side limits alone were rejected because they do not bound
  local buffers, retries, traversal, or cumulative work.
- Prompt-only limits were rejected because model compliance is probabilistic.
- User-configurable unlimited mode was rejected because it destroys the tested
  product boundary.
- Dynamic budget increases after partial results were rejected because an
  untrusted result could drive privilege and cost expansion.

## Security and privacy impact

Budgets reduce denial-of-service, unexpected spend, excessive cluster access,
and unnecessary data transfer. They do not make an in-scope field safe; source
allowlists, projection, sensitive-value controls, and consent still apply first.

Truncation metadata must not contain discarded bytes. The model and TUI receive
only a safe indication that information was limited and how that affects the
Diagnosis.

## Validation

S13 must implement and prove Agent/model/run counters; S17 through S19 must
complete Tool, log, item, and relationship budgets. Deterministic fake-clock and
counting tests must cover zero, exact maximum, and one-over values for every
budget. Tests must prove:

- Reservation precedes model and Kubernetes calls.
- Per-request deadlines never exceed remaining run time.
- Concurrent cancellation or scope invalidation cannot double-reserve or start
  another call.
- Partial, error, malformed-stream, and retry paths all consume the intended
  counters once.
- UTF-8 and serialized byte accounting cannot exceed the hard result or
  cumulative ceiling.
- Relationship cycles stop at node, edge, and hop limits.
- Two no-progress steps terminate without another external call.
- Tightened configuration works and every attempted increase is rejected.

## Revisit triggers

- Repeatable diagnostic evaluation shows a specific ceiling prevents an MVP
  category while a reviewed increase remains safe and bounded.
- Provider or Kubernetes constraints require a lower code-defined maximum.
- Concurrent AgentRuns are admitted through a new architecture decision and
  require a global budget in addition to per-run limits.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0009: Use Fixed Structured Tools](0009-use-fixed-structured-tools.md)
