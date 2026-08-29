# ADR-0027: Use Stable Safe Error Classes

- Status: Accepted
- Date: 2026-08-08
- Amended by: ADR-0036

## Context

KuPilot receives errors from configuration, filesystems, child processes,
SQLite, Kubernetes, HTTP, model protocols, Eino, terminal lifecycle, and local
policy. Raw errors may contain paths, URLs, headers, credential-bearing output,
SQL, object fields, or remote bodies. Matching their text is also unstable and
can cause unsafe retry, widening, or UI behavior.

Application and the Agent need deterministic semantics without importing vendor
error types.

## Decision

Every adapter will translate failures at its boundary into a project-owned
`SafeError` shape containing:

- One stable class from the catalog below.
- A code-defined safe operation name.
- An explicit retryability value.
- A bounded user-safe message selected locally.
- A correlation identifier.
- Optional allowlisted ClusterScope or ResourceRef fields when needed to explain
  impact.
- An in-memory cause category for tests and metrics-free debugging, never a raw
  serialized vendor object or body.

The stable `v0.1` classes are:

<!-- markdownlint-disable MD013 -->

| Class | Meaning and default retry policy |
| --- | --- |
| `invalid_input` | User or local command failed deterministic validation; not retryable without changing input |
| `configuration_invalid` | A supplied typed setting, path, or credential is invalid; not retryable without changing the input |
| `consent_required` | Current endpoint/category policy lacks valid informed consent; retryable only after an explicit decision |
| `authentication_failed` | Kubernetes or model authentication failed; not automatically retried |
| `permission_denied` | The authenticated identity lacks an admitted operation; not automatically retried and never widens access |
| `not_found` | The exact admitted target or history record is absent; not automatically retried unless runtime explicitly revalidates state |
| `conflict` | Current target or durable state changed relative to a bound version; not automatically retried for writes |
| `unsupported` | Endpoint, API, capability, platform, or source is outside the verified contract; not retryable without a supported change |
| `policy_denied` | A fixed Tool, Kind, scope, endpoint, write, schema, or other runtime rule denied the request; not retryable with the same intent |
| `stale_scope` | Bound run scope or generation is no longer current; terminal for that AgentRun |
| `budget_exhausted` | A hard time, count, byte, item, traversal, repetition, or no-progress ceiling was reached; terminal or partial, never auto-expanded |
| `rate_limited` | An admitted external service requested throttling; retryable only when remaining budget and explicit runtime retry policy allow it |
| `unavailable` | A required admitted external service is temporarily unavailable; retryable only within remaining budget and fixed policy |
| `timeout` | A local deadline expired; not automatically retried unless the fixed Tool policy explicitly permits the one repeated call |
| `cancelled` | The owning Context was cancelled by user, shutdown, or scope transition; terminal for the operation |
| `sensitive_output_blocked` | High-risk content was blocked before a sink; not retryable by sending the original and represented as missing information |
| `invalid_external_response` | Kubernetes, model, exec credential, or storage data violated the bounded protocol contract; not automatically retried |
| `persistence_unavailable` | Required SQLite open, validation, migration, recovery, retention, transaction, or query failed; behavior follows the degraded-storage contract |
| `internal` | A KuPilot invariant failed or no safer specific class applies; not automatically retried and never exposes implementation details |

<!-- markdownlint-enable MD013 -->

Approval expiry, rejection, cancellation, invalidation, not-executed,
request-accepted, outcome-unknown, verification-timeout, and verified outcomes
are typed approval/execution states, not collapsed into free-form errors.

Runtime decisions use class and typed context only. They never use substring,
regular-expression, localized-message, HTTP-body, SQL-text, or vendor-type checks
outside the adapter. Retryability is conservative and is additionally bounded by
ADR-0016; a retryable class does not itself schedule a retry.

Raw causes remain within the adapter for mapping and never cross the safe error
boundary. AuditEvents and default operational logs receive only allowlisted
safe fields. ADR-0036 permits an explicitly enabled, bounded, credential-
redacted model-failure copy in the local rotating log; runtime decisions never
consume that copy.

## Consequences

Positive consequences:

- TUI, Agent, persistence, and tests share stable failure semantics.
- Credentials and untrusted remote text do not need to cross adapter boundaries.
- Retry, missing-information, degraded-storage, and terminal-state behavior are
  deterministic.
- Vendor upgrades affect mapping tests rather than product contracts.

Costs and constraints:

- Adapter authors must map each relevant failure explicitly and maintain
  fixtures across dependency upgrades.
- Safe user messages may contain less vendor-specific diagnostic detail.
- Adding or changing a stable class is a compatibility and documentation change.
- Correlation identifiers help local reasoning but do not provide remote
  telemetry or a hidden debug channel.

## Alternatives considered

- Passing raw Go errors upward was rejected because formatting can disclose
  sensitive inputs and callers would depend on vendor text or types.
- One generic error class was rejected because policy, retry, gaps, and storage
  behavior need distinct deterministic outcomes.
- Matching message text was rejected because it is unstable, locale-dependent,
  and attacker-influenced.
- Persisting raw errors for support was rejected because SQLite is not an
  unrestricted log or credential-safe store.

## Security and privacy impact

Safe errors are output projections. They follow the same allowlist, byte,
terminal, and retention rules as other external-facing data. A safe error never
includes kubeconfig or API-key material, auth headers, exec output, raw endpoint
bodies, raw Kubernetes data, SQL, arbitrary local paths, or stack dumps.
The opt-in local diagnostic record from ADR-0036 is not a SafeError and is never
shown in the TUI, sent to the model, audited, or stored in SQLite.

Permission, blocked-output, and unsupported failures become explicit gaps; they
never trigger broader access or transport of the original value.

## Validation

Table-driven adapter tests must map every documented success and failure fixture
to one class and retryability value. Generated distinct canaries must be placed
in nested, joined, wrapped, formatted, and malformed raw errors and shown absent
from:

- SafeError formatting and serialization.
- Application and Agent events.
- Model messages and Tool results.
- TUI frames.
- Default operational logs, AuditEvents, and every durable field. ADR-0036
  sensitive model-failure logs instead prove bounded admission and exact model
  credential removal.

Tests must also prove no runtime package outside an adapter branches on raw error
text or vendor error types and that retryable classes still obey remaining
budget, cancellation, generation, and one-repeat rules.

Each adapter must provide deterministic mapping tests, and the complete
six-Tool catalog must be covered by the cross-sink canary matrix. Vendor error
strings and unverified API behavior never become runtime contracts.

## Revisit triggers

- A repeated failure cannot be represented without unsafe free text or ambiguous
  runtime behavior.
- A provider or Kubernetes API introduces a stable semantic category needed by
  the product.
- A public API or localization layer requires versioned error-code compatibility.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0016: Freeze Runtime Budgets](0016-freeze-runtime-budgets.md)
- [ADR-0036: Record Bounded Model Failure Diagnostics](0036-record-bounded-safe-model-failure-diagnostics.md)
