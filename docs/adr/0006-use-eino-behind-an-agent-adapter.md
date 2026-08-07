# ADR-0006: Use Eino Behind an Agent Adapter

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot needs a bounded Agent loop with streamed model responses and structured
Tool selections. Implementing every model-stream and Tool-calling integration
primitive locally would add transport and orchestration work, while allowing a
framework to define domain values or use cases would make security policy depend
on vendor behavior.

Eino is the accepted Agent framework direction. Its current version, model
component APIs, streaming ownership, Tool schema APIs, and error behavior have
not been validated in this repository.

## Decision

KuPilot will use Eino only inside `internal/agent/einoadapter` to implement the
Application-owned `AgentRunner` and the Agent-owned neutral `Model` boundary.

The adapter may use Eino for:

- Mapping neutral messages to one model request.
- Receiving bounded streaming text and structured Tool-selection events.
- Mapping the fixed KuPilot Tool specifications to the model transport.
- Translating cancellation, completion, usage, and errors into project-owned
  events and stable safe error classes.

KuPilot, not Eino, owns the Agent loop limits, Tool authorization, scope
generation checks, Tool dispatch, Evidence creation, Diagnosis validation,
persistence intent, and Application event acceptance. Eino types do not cross
the adapter. The S14 spike may choose the smallest stable ChatModelAgent, ReAct,
or Graph implementation path inside the adapter. KuPilot will not expose a
general graph workflow or adopt RAG, retrievers, Multi-Agent orchestration,
dynamic Tool registration, memory persistence, or a framework checkpoint/resume
mechanism in `v0.1`.

An AgentRun is never resumed as an Eino execution after process restart. Safe
Session history is reconstructed into neutral input for a new run.

This ADR selects Eino as an isolated dependency; it does not select or claim to
have verified an exact version or API path.

## Consequences

Positive consequences:

- Model and Tool protocol churn is localized to one adapter.
- Agent policy can be tested without a live model or Eino runtime.
- KuPilot can use framework streaming primitives without making them domain
  contracts.
- Removing or replacing the framework remains possible behind neutral ports.

Costs and constraints:

- Every message, Tool, stream, callback, usage value, and error requires an
  explicit mapping.
- The adapter must guard against duplicate, malformed, out-of-order, and
  oversized framework events.
- KuPilot cannot rely on framework memory, retry, or orchestration defaults
  unless each behavior is explicitly admitted by runtime policy.

## Alternatives considered

- Calling a model HTTP API directly was rejected for the initial implementation
  because it would duplicate structured streaming and Tool protocol work before
  the product contract is evaluated.
- Exposing Eino message and Tool types throughout the codebase was rejected
  because vendor contracts would leak into Application, Domain, Tools, and TUI.
- Using Eino to own the whole Agent loop was rejected because budgets, scope,
  Evidence, persistence, and policy must remain deterministic KuPilot controls.
- Adopting general graph workflows, RAG, or Multi-Agent features was rejected as
  outside `v0.1`; this does not preselect the minimal internal API path evaluated
  in S14.

## Security and privacy impact

Eino is not a security boundary. Model output remains untrusted, and only
runtime code can dispatch a fixed Tool. Prompt instructions and framework Tool
registration cannot widen ClusterScope, Kind allowlists, budgets, endpoint, or
write authority.

The adapter must close model streams, bound buffers, remove raw vendor error
bodies, and prevent model requests or responses from entering SQLite or ordinary
logs. Framework callbacks cannot receive credentials or a Kubernetes client.

## Validation gate

S04 must record a candidate locked Eino version and its official minimum Go
requirement so the repository can select one Go lower bound. No later than S14
and before building the Eino Agent adapter, a repository spike must verify from
official module metadata, documentation, and compile tests:

1. The current stable module and its minimum supported Go version.
2. Streaming text and structured Tool-call event ordering, chunk ownership, and
   closure on success, error, timeout, and cancellation.
3. Strict Tool schema representation without a prompt-parsed fallback.
4. Whether framework retries, callbacks, tracing, or persistence are enabled by
   default and how they are disabled or bounded.
5. Neutral error and usage mapping without retaining raw request or response
   bodies.
6. Compatibility with the single OpenAI-compatible adapter contract in
   ADR-0022.

The selected version and exact API mappings must be recorded after the spike.
This ADR makes no claim that a specific Eino release has passed.

## Revisit triggers

- The S14 gate cannot provide strict structured Tool events or bounded stream
  ownership.
- Eino requires policy, persistence, or vendor types to escape the adapter.
- Measured maintenance cost of the adapter exceeds the avoided protocol work.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0009: Use Fixed Structured Tools](0009-use-fixed-structured-tools.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
