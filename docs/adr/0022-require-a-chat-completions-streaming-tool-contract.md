# ADR-0022: Require a Chat Completions Streaming Tool Contract

- Status: Accepted
- Date: 2026-08-08
- Amended by: ADR-0036

## Context

KuPilot's Agent-first interaction needs incremental user feedback and
machine-distinguishable Tool selections. A chat endpoint that returns only prose
cannot provide a safe Tool authorization boundary. Providers also differ in
stream framing, partial arguments, finish reasons, error bodies, usage, and
capability claims.

The internal contract must be stable and project-owned even though one Eino and
one OpenAI-compatible adapter implement it.

## Decision

A model endpoint is usable only if it implements the accepted Chat
Completions-style streaming and Tool-call profile and satisfies KuPilot's neutral
Model contract:

- Bounded request messages with project-owned roles and content parts.
- One frozen set of strict, versioned structured Tool specifications per model
  request.
- Streaming text deltas and structured Tool selections represented as distinct
  typed events.
- Deterministic request completion, finish reason, and one terminal success or
  classified failure. Usage and response-format features are optional and never
  required for core safety.
- Context cancellation and a 45-second per-request ceiling further bounded by
  remaining AgentRun time.

The adapter must assemble fragmented structured arguments under fixed byte and
event limits, reject unknown or duplicate fields according to the Tool schema,
and reject ambiguous, malformed, reordered, duplicate-terminal, or unsupported
events. Text that resembles JSON, a Tool name, approval, command, or execution
claim remains text and never dispatches a Tool.

KuPilot does not downgrade to prompt-parsed Tool calls, a prose-only diagnostic
mode, or an endpoint-selected Tool schema. If required capabilities are absent,
the run stops before cluster data is transferred or returns a safe unsupported
classification from a content-free capability check.

The user must configure the model identifier; KuPilot does not embed a provider
default. Temperature is accepted only in the low range from 0 through 0.2, and a
hard output-token limit is mandatory. The concrete default must remain inside
that range and be documented with the model adapter configuration. An optional
typed reasoning-effort field may be omitted or set only to `none`; it is never
inferred from the model identifier or an endpoint error.

Raw provider request, response, stream, error, and usage objects stay in the
adapter. Application and Agent core see only neutral bounded events and safe
errors. A final model draft becomes a Diagnosis only after local structure and
same-run Evidence validation.

## Consequences

Positive consequences:

- Tool dispatch cannot be confused with model prose.
- Provider-specific streaming details remain isolated and fuzz-testable.
- The TUI can display progress without persisting partial assistant content.
- Unsupported endpoints fail explicitly rather than weakening security.

Costs and constraints:

- Basic chat-compatible endpoints without reliable structured Tools are not
  supported.
- Fragment assembly, duplicate detection, cancellation, and terminal ownership
  require careful adapter code.
- A provider protocol change can block model use until its mapping is updated.
- Streaming improves feedback but does not allow partial text to become a final
  durable Message.

## Alternatives considered

- Parsing Tool instructions from prose was rejected because model wording and
  untrusted cluster text could trigger ambiguous operations.
- Offering a prose-only fallback was rejected because the Agent could not gather
  runtime Evidence through the accepted contract.
- Letting Eino or a provider type become the internal contract was rejected
  because it couples policy, tests, and TUI to a vendor API.
- Buffering an unbounded complete response before validation was rejected for
  latency and resource-exhaustion reasons.

## Security and privacy impact

Structured capability does not authorize a Tool; fixed runtime dispatch, scope,
allowlists, budgets, and generation checks still decide. Model output cannot
create Evidence, consent, endpoint changes, approval, or execution results.

Raw streams and successful response bodies are never persisted or logged.
Provider error bodies are absent from default logs; ADR-0036 permits only an
explicitly enabled, bounded failed-response prefix in the local rotating log.
Stream buffers and text deltas pass byte, control-character, and terminal-
safety limits before any sink.

## Validation

Local fake-endpoint and Eino adapter tests must prove:

1. Required structured Tool schema and streaming event representation.
2. Cancellation and exactly one terminal outcome at every chunk boundary.
3. Bounded fragmented argument assembly and rejection of malformed, duplicate,
   reordered, unknown, oversized, and mixed text/Tool events.
4. No Tool dispatch from prose or unsupported fallback behavior.
5. Safe mapping of finish reasons, optional usage, authentication, permission,
   throttling, timeout, unavailable, and malformed responses.
6. Required model configuration, the optional exact `none` reasoning effort,
   the 0 through 0.2 temperature range, and hard output bounds without a hard-
   coded provider model default.
7. Absence of raw provider objects and bodies from Application, TUI, default
   logs, and SQLite, plus bounded credential-redacted admission in the explicit
   ADR-0036 sensitive logging mode.

## Revisit triggers

- The accepted provider profile changes its Tool or streaming semantics.
- A second provider with different neutral needs is admitted.
- Evaluation shows streaming is unnecessary; removal must preserve structured
  Tools, cancellation, and bounded response behavior.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0006: Use Eino Behind an Agent Adapter](0006-use-eino-behind-an-agent-adapter.md)
- [ADR-0009: Use Fixed Structured Tools](0009-use-fixed-structured-tools.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0015: Require the Evidence and Diagnosis Contract](0015-evidence-and-diagnosis-contract.md)
- [ADR-0036: Record Bounded Model Failure Diagnostics](0036-record-bounded-safe-model-failure-diagnostics.md)
