# ADR-0022: Require a Chat Completions Streaming Tool Contract

- Status: Accepted
- Date: 2026-08-08
- Amended by: ADR-0036, ADR-0039, and ADR-0043

## Context

Kupilot's Agent-first interaction needs incremental user feedback and
machine-distinguishable Tool selections. A chat endpoint that returns only prose
cannot provide a safe Tool authorization boundary. Providers also differ in
stream framing, partial arguments, finish reasons, error bodies, usage, and
capability claims.

The policy contract must remain project-owned even though the sole Eino
boundary implements the OpenAI-compatible wire profile.

## Decision

A model endpoint is usable only if it implements the accepted Chat
Completions-style streaming and Tool-call profile:

- Bounded request messages with project-owned roles and content parts.
- One frozen set of versioned function Tool specifications with code-owned
  closed JSON Schemas per model request.
- One bounded Eino stream that assembles into exactly one supported assistant
  text or structured Tool-selection message.
- A supported finish reason and exactly one project terminal outcome. Usage and
  response-format features are optional and never required for core safety.
- Context cancellation and a profile-selected per-request ceiling of at most
  300 seconds, further bounded by remaining AgentRun time.

The Eino component owns provider serialization, SSE/JSON decoding, and
fragmented argument assembly. Tool-call identifier, type, and function name are
atomic for each index; argument fragments for distinct bounded indexes may
interleave. Kupilot independently bounds raw bytes, SSE records, decoded
chunks, and the assembled message; rejects ambiguous choices, malformed or
non-contiguous calls, unsupported finish reasons, and content after terminal
state; and validates every complete call through the fixed catalog and strict
local binder. Bounded empty deltas are inert.

Some compatible models emit commentary before or alongside a structured Tool
selection. After Eino assembles the response, Kupilot discards that text only
when the finish reason is `tool_calls`. A `stop` or `length` response containing
a Tool call remains invalid. Text that resembles JSON, a Tool name, approval,
command, or execution claim remains text and never dispatches a Tool.

Kupilot does not downgrade to prompt-parsed Tool calls, a prose-only diagnostic
mode, or an endpoint-selected Tool schema. Construction performs no network
probe. If the first Application-admitted request proves incompatible, the run
returns a safe unsupported or invalid-response classification without retry,
downgrade, or fallback.

The user must configure the model identifier; Kupilot does not embed a provider
default. Temperature is accepted only in the low range from 0 through 0.2, and a
hard output-token limit is mandatory. The concrete default must remain inside
that range and be documented with the Eino boundary configuration. An optional
typed reasoning-effort field may be omitted or set only to `none`; it is never
inferred from the model identifier or an endpoint error.

Raw provider request, response, stream, error, usage, and Eino message objects
stay inside `internal/agent/einoadapter`. Application and Agent core see only
project-owned run events, safe errors, Tool calls, and final outcomes. A final
model draft becomes a Diagnosis only after local structure and same-run
Evidence validation.

## Consequences

Positive consequences:

- Tool dispatch cannot be confused with model prose.
- Provider-specific streaming details remain isolated and fuzz-testable.
- The TUI can display progress without persisting partial assistant content.
- Unsupported endpoints fail explicitly rather than weakening security.

Costs and constraints:

- Basic chat-compatible endpoints without reliable structured Tools are not
  supported.
- Eino version behavior, local bounds, cancellation, and terminal ownership
  require careful compatibility tests.
- A provider protocol change can block model use until its mapping is updated.
- Streaming improves feedback but does not allow partial text to become a final
  durable Message.

## Alternatives considered

- Parsing Tool instructions from prose was rejected because model wording and
  untrusted cluster text could trigger ambiguous operations.
- Offering a prose-only fallback was rejected because the Agent could not gather
  runtime Evidence through the accepted contract.
- Letting Eino or provider types escape the sole adapter was rejected because
  it couples policy, Application, persistence, and TUI to a vendor API.
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

1. Eino-generated request fields and the required closed Tool schemas without
   relying on a provider-specific `strict` flag.
2. Cancellation and exactly one terminal outcome at every chunk boundary.
3. Bounded fragmented argument assembly with atomic Tool identities, inert
   empty deltas, interleaved distinct indexes, discard of non-authoritative
   commentary attached to a valid `tool_calls` response, and rejection of
   malformed, non-contiguous, unknown, oversized, and finish-inconsistent
   responses.
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
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0009: Use Fixed Structured Tools](0009-use-fixed-structured-tools.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0015: Require the Evidence and Diagnosis Contract](0015-evidence-and-diagnosis-contract.md)
- [ADR-0036: Record Bounded Model Failure Diagnostics](0036-record-bounded-safe-model-failure-diagnostics.md)
