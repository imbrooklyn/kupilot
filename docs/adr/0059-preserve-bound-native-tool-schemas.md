# ADR-0059: Preserve Bound Native Tool Schemas

- Status: Accepted
- Date: 2026-09-17
- Amends: ADR-0043, ADR-0054, ADR-0055, and ADR-0058

## Context

The native Eino Ollama v0.1.9 serializer drops array item schemas, nested
object fields, required fields, and other constraints from Kupilot's bound
Tool catalog. A recording transport reproduced this after full Agent request
assembly. The catalog is intact before native conversion. No newer stable
component or client tag is available. Upstream commit
`4290f5f79208a538d0c33d8f9744924e15bdc6ca` adds some array handling but does not
preserve the complete catalog. It is not a stable or sufficient repair.

Maintaining two SDK forks or copying a provider client is disproportionate to
this defect. The earlier investigation's preference for an upstream-only
repair is replaced by the bounded request correction below. This does not
implement missing Ollama/model capabilities or repair generated arguments.

## Decision

The existing native request guard restores each Tool's `function.parameters`
value from the exact code-owned catalog already validated at Tool binding.
An adapter-private bound Eino view supplies that immutable catalog through the
current call context. It only delegates Generate/Stream/WithTools to the same
native Eino component; it adds no model call, Agent, loop, or durable state.

The transport requires the exact bound Tool count, order, names, descriptions,
and function type. Missing, null, duplicate, unknown, reordered, or unbound
Tool envelopes are rejected before I/O. Each parameters slot must be a present
JSON object. Its contents carry no authority: the already validated catalog
is the only source of the complete replacement schema. No missing Tool or
Tool target is inferred. Tool-free requests cannot acquire a catalog.

Only those parameters byte spans and ADR-0058's explicit-zero scalar may be
replaced. All other request bytes remain unchanged, including model settings,
messages, history, current question, format, and thinking. There is no general
request serializer, provider router, dynamic schema registry, prompt schema
duplication, response correction, parser fallback, or SDK fork. The final
request must fit the original byte reservation, with exact HTTP framing and
context cancellation. Invalid bindings and oversized corrected requests cause
zero network calls. Errors preserve causes and use existing safe request
failure classes. The correction is removed when a pinned stable native
component passes the same full request-fidelity tests without it.

Ollama 0.34.0 represents array items and nested object fields but does not
represent every JSON Schema constraint in its API types. Sending the complete
catalog does not prove all constraints reach the model, and the application
does not emulate unsupported provider behavior. Runtime strict argument
binding, scope/policy generations, consent, budgets, Tool-result pairing,
Evidence ownership, action authority, and persistence barriers remain the
only authority. Invalid model output still fails closed with no automatic
retry, repair call, or continuation.

## Security and privacy

The restored schema is static code-owned metadata already admitted to the
same role and origin. This adds no source, data category, consent meaning,
operation, capability, or authority. Raw requests, responses, Tool arguments,
and Evidence remain ephemeral and absent from ordinary logs, TUI, SQLite, and
exports. Summary and Reviewer requests remain Tool-free. No credentials or
user configuration are changed.

## Validation

Tests must check complete equality of all catalog schemas at the final wire
boundary, including the plan-only subset, and preserve all unrelated bytes.
Both bound and Tool-free paths, rebinding, malformed/missing/null/duplicate/
unknown/out-of-order envelopes, exact/one-over byte limits, cancellation,
timeout, body closure, and zero-call denials must be covered. Full Agent
composition must retain current-schema history and current-question uniqueness.

Deterministic fixtures establish request fidelity and safety, not model
quality. A separately opted-in local first-call comparison uses at most nine
independent model calls, synthetic context, zero Tool/Kubernetes execution,
fixed settings, and no retry. It records fixed outcomes, bytes, measured usage
when available, and time. Persistent provider/model failures remain limitations
of the exact tested combination, not permission to add compatibility logic.

## References

- [Model Compatibility](../model-compatibility.md)
- [Architecture](../architecture.md)
- [ADR-0058](0058-preserve-explicit-native-model-temperature.md)
