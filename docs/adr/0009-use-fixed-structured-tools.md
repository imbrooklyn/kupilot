# ADR-0009: Use Fixed Structured Tools

- Status: Accepted
- Date: 2026-08-08

## Context

The model needs to request Kubernetes observations, but model text is not a safe
command or authorization format. A shell, kubectl wrapper, generic Kubernetes
request, dynamic Tool registry, or prompt-parsed convention would let untrusted
content influence resource type, scope, command construction, output size, or
future writes.

KuPilot also needs deterministic Evidence provenance: the runtime must know
which bounded operation produced each observation.

## Decision

KuPilot will expose exactly six versioned, structured, read-only model Tools in
`v0.1`:

- `get_resource`
- `list_resources`
- `get_events`
- `get_pod_logs`
- `get_previous_pod_logs`
- `get_related_resources`

Each Tool has a code-defined name, purpose, strict input schema, task-specific
result DTO, and deterministic Evidence mapping. Unknown fields, duplicate
fields, wrong types, overlong values, model-supplied scope, arbitrary Kind, raw
selector, endpoint, credential, deadline, and hard-limit fields are rejected.

The catalog and schema versions are frozen in RunInput. Runtime, not the model:

1. Decodes and canonicalizes the neutral structured selection.
2. Validates the fixed name, schema, Tool budget, repetition rule, and deadline.
3. Injects the immutable ClusterScope and hard limits into a BoundToolCall.
4. Checks current generation before and after the handler.
5. Accepts the event before Evidence, persistence, TUI, or model context changes.

Handlers depend on narrow Kubernetes reader ports and return a safe ToolResult.
They cannot call the model, TUI, Application repositories, SQLite, or a generic
Kubernetes client. Only deterministic local handling can create Evidence.

There is no text-to-Tool fallback. Prose that resembles a call, an invented Tool
name, or malformed structured output performs no operation.

## Consequences

Positive consequences:

- Tool authorization and argument validation are deterministic and testable.
- Scope cannot be selected or widened by model output.
- Every accepted observation has a fixed operation and provenance path.
- Model and Kubernetes adapters remain separated by neutral contracts.

Costs and constraints:

- Each Tool requires explicit schemas, canonicalization, projections, budgets,
  safe errors, and fixtures.
- Model endpoints that cannot produce reliable structured Tool events are
  unsupported.
- New diagnostic needs cannot use an arbitrary escape hatch; they require a
  product and security review.

## Alternatives considered

- Prompting the model to emit kubectl or shell commands was rejected because
  prompt compliance is not authorization and command parsing creates injection
  risk.
- A generic Kubernetes `get`, `list`, or discovery Tool was rejected because it
  would expose resource and field choice beyond the product allowlist.
- Dynamic Tool registration or plugins were rejected because they weaken the
  frozen per-run policy and are outside `v0.1`.
- Returning free-form Tool prose was rejected because it cannot support stable
  field projection, limits, or Evidence provenance.

## Security and privacy impact

Structured Tools reduce ambiguity but are not trusted merely because their
syntax is valid. Runtime scope, Kind, relationship, source, budget, endpoint,
and write controls remain independent. Kubernetes and model free text inside a
ToolResult is normalized, redacted or blocked, bounded, and marked as untrusted
data before model or terminal use.

No Tool reads Secret objects, ConfigMap data, arbitrary environment values, or
cross-Namespace resources. `v0.1` contains no mutation Tool.

## Validation

Deterministic tests of the neutral Tool specification, runtime dispatch, Eino
mapping, six handlers, and complete catalog must prove:

- Exact acceptance at every boundary and rejection of missing, duplicate,
  unknown, wrong-type, oversized, and extra fields.
- Model-supplied scope and limits never enter canonical arguments.
- Every denial occurs before external I/O when it is locally decidable.
- Fixed dispatch cannot call an invented or unregistered handler.
- Repetition, call, byte, item, log, and traversal budgets are atomically
  enforced.
- Malformed stream events and Tool-like prose produce zero Tool calls.
- Every Evidence item refers to the current run, invocation, scope, and
  observation time.

Endpoint-specific schema APIs and the Eino mapping must satisfy the same neutral
contract without leaking vendor types.

## Revisit triggers

- A named MVP diagnostic category cannot be served by the six Tools and a
  proposed addition passes the feature admission and threat-review gates.
- A model transport cannot preserve strict structured selection semantics.
- The product admits an extension mechanism through a separate Accepted ADR.

## References

- [Scope](../scope.md)
- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0015: Require the Evidence and Diagnosis Contract](0015-evidence-and-diagnosis-contract.md)
- [ADR-0016: Freeze Runtime Budgets](0016-freeze-runtime-budgets.md)
- [ADR-0024: Freeze Kubernetes Kind and Relationship Allowlists](0024-freeze-kubernetes-kind-and-relationship-allowlists.md)
