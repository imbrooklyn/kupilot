# ADR-0043: Use One Eino Runtime Boundary

- Status: Accepted
- Date: 2026-09-01
- Amended: 2026-09-02
- Supersedes: ADR-0006
- Amends: ADR-0010, ADR-0022, and ADR-0036
- Amended by: ADR-0046 and ADR-0047

ADR-0046 composes explicit `agent` and optional `approval_reviewer` consumers
through this same boundary without adding a provider router or a second Eino
adapter. ADR-0047 replaces the hand-written ReAct ownership described below
with direct stable ADK `ChatModelAgent`, `Runner`, message state, and
summarization middleware. Eino types remain confined here, and the current
SQLite safe-message bridge remains the durable Session source until a stable
runner-managed Session contract passes the adoption gate.

ADR-0047 also narrows this ADR's earlier "no memory/checkpoint" constraint
precisely: the boundary must not enable a provider-global or durable memory
plug-in, checkpoint recovery, or a parallel persistence abstraction. It must
directly use stable ADK in-run message state and summarization. Completed-turn
Session persistence remains project-owned and does not make a checkpoint
operational authority.

## Context

Kupilot originally isolated the Eino OpenAI component in
`internal/llm/openaicompat` and the Eino ReAct runtime in
`internal/agent/einoadapter`. The model adapter converted Eino stream chunks
into project-owned fragment events. The Agent adapter immediately reassembled
those events into an Eino assistant message so that ReAct could consume it.

That double translation made one provider response pass through two independent
state machines for Tool-call fragments, finish reasons, usage, cancellation,
and terminal ownership. It also replaced the complete request payload after the
Eino component serialized it. Compatibility fixes therefore accumulated in a
parallel Chat Completions implementation instead of remaining aligned with the
pinned Eino component.

The project still needs exact local ownership of credentials, endpoint origin,
raw byte limits, safe errors, budgets, Tool authority, Evidence, and scope. None
of those controls requires a second model protocol abstraction.

## Decision

`internal/agent/einoadapter` is the single Eino and model-provider boundary. It
owns both the pinned Eino ReAct runtime and the pinned Eino OpenAI ChatModel.
The `internal/llm/openaicompat` package, the Agent-owned neutral `Model` port,
and the neutral model-fragment event hierarchy are removed.

The production path is:

1. Agent core creates project-owned immutable run input, prompt text, Tool
   specifications, budgets, and policy services.
2. The Eino adapter maps the initial project messages and fixed Tool catalog to
   Eino values once.
3. ReAct calls the guarded Eino OpenAI ChatModel directly.
4. The Eino component owns Chat Completions request serialization and SSE/JSON
   conversion. The adapter drains and closes one Eino stream, and Eino's schema
   API assembles its indexed Tool-argument fragments and finish metadata once.
5. The adapter validates the one assembled Eino assistant message, binds every
   complete Tool call through the project-owned strict catalog, and maps the
   final answer into the project-owned Diagnosis contract.

Visible answer streaming does not restore the removed neutral model protocol.
While the adapter drains the same read-once Eino stream, one passive observer
receives already decoded and chunk-validated `Content` strings. It recognizes
only a first top-level `answer_markdown` JSON string, applies project-owned
cross-chunk output safety, and publishes authority-free provisional text. Eino
still performs the only provider SSE decoding and final message and Tool-call
assembly. The observer neither reconstructs provider events nor produces a
message consumed by ReAct. The assembled finish reason and strict local
validation remain authoritative; a requested Tool or any terminal failure
discards the applicable draft.

Kupilot does not replace or reconstruct the Eino-generated request payload. A
request payload observer may reject an oversized body or an exact credential
reflection, but it returns admitted bytes unchanged. The configured model,
temperature, output limit, optional reasoning effort, messages, stream usage,
and Tool definitions are supplied through typed Eino configuration and values.
The sole exception to Eino's typed request fields is one adapter-owned,
constant-key `temperature` entry in Eino's `ExtraFields`. Its value is still
the validated scalar configuration value. This prevents the pinned downstream
OpenAI client from rejecting a compatible endpoint before transport merely
because a configured model identifier begins with `gpt-5`. No user-controlled
extension map is admitted, and the adapter neither examines the identifier nor
rewrites the serialized payload.

Provider-side `strict: true` is not a compatibility requirement or an authority
boundary. The model-visible Tool parameters remain code-owned JSON Schemas with
closed object shapes. Every returned argument object is decoded with the fixed
project schema, rejects unknown, duplicate, wrong-type, overlong, sensitive,
scope-bearing, or runtime-authority fields, and is canonicalized before budget
reservation or Tool dispatch. A provider accepting a looser output does not
widen what Kupilot can execute.

A complete batch whose Tool names and neutral JSON objects are structurally
safe may still contain a correctable semantic policy error, such as assigning a
Namespace to a cluster-scoped Kind or selecting a Namespace outside the frozen
policy. If any member has such an error, the adapter rejects the entire batch
before ToolInvocation creation, budget reservation, handler dispatch, or
Kubernetes I/O. It returns one fixed code-authored policy-feedback Tool message
for each correlated selection so Eino can make a new bounded model decision.
The feedback contains no rejected arguments or live scope values. Unknown
Tools, malformed strict object shapes, injected authority fields, and sensitive
model text remain terminal failures. This feedback round is not an automatic
Tool or Kubernetes retry, and existing model, step, and no-progress budgets
bound correction attempts.

The project-owned guarded HTTP transport remains inside the same adapter and
continues to own:

- canonical-origin, HTTPS verification, loopback-only HTTP, and redirect
  enforcement;
- late Authorization injection from the opaque credential and immediate
  placeholder restoration;
- request size and raw SSE byte, record, and record-size limits;
- content-type checks, non-success body disposal, optional bounded sensitive
  diagnostics, observed HTTP status, and stable safe error classification; and
- response-body closure and cancellation propagation.

The adapter accepts only the fixed Eino model and Tool options needed by the
current run. It clears inherited callback state and enables no provider retry,
fallback, tracing, provider-global or durable memory plug-in, checkpoint
recovery, dynamic Tool, or global callback. Under ADR-0047, this does not
prohibit directly composed ADK in-run message state or summarization
middleware; neither is resumable checkpoint authority. Eino types remain
private to `internal/agent/einoadapter` and do not enter Domain, Application,
Tools, Kubernetes, persistence, CLI, or TUI.

Some compatible endpoints expose bounded reasoning fragments through Eino's
paired `ReasoningContent` field and `reasoning-content` metadata even when the
configured request asks for no reasoning. Those fragments are untrusted and
non-authoritative. The boundary requires the paired values to match, validates
UTF-8, checks credential reflection and the combined assistant byte ceiling,
then clears both values before message assembly. Missing, mismatched, late,
oversized, credential-bearing, or additional provider metadata remains a
failed response. Reasoning content never reaches ReAct history, Tool binding,
Application, the TUI, persistence, or logs.

Domain does not define a parallel model request, message, role, Tool-call, or
Tool-specification protocol. The strict `ToolSelection` and
`ToolSpecification` values are Agent-owned catalog inputs without endpoint,
credential, scope, budget, transport, or execution authority. The adapter
validates the Eino-owned ReAct conversation in place before each model call.

## Consequences

One component still interprets provider streaming semantics, and ReAct consumes
the resulting message without a neutral-fragment round trip. The passive answer
projection improves delivery latency without becoming a provider or Agent
protocol. Ordinary
OpenAI-compatible variations that the pinned Eino component already supports,
including non-canonical whitespace or key order in Tool arguments, reach the
local strict binder instead of being rejected by a parallel serializer.

Agent tests that need scripted model behavior use Eino fakes inside the Eino
adapter package. Tests outside that package observe only `AgentRunner`, run
events, and project-owned outcomes. There is no exported provider or Eino test
seam.

The guarded transport and assembled-message validator remain non-trivial. They
are security controls, not a second provider client. An Eino dependency update
must still pass the request, stream, Tool, cancellation, error, credential, and
body-closure fixtures before it is accepted.

## Security and privacy impact

The change removes an authority-neutral translation layer; it does not move
authority into Eino. Tool dispatch still requires immutable scope, current
generation, local strict binding, atomic budget reservation, fixed dispatch,
projected Tool results, and deterministic Evidence creation.

The model credential remains opaque and transport-only. Raw request and
response bodies, provider errors, Eino messages, callback values, and streams
remain excluded from Application, TUI, SQLite, AuditEvents, and default logs.
The ADR-0036 sensitive failure projection remains opt-in, bounded, locally
redacted, and failure-only.

## Validation

Deterministic tests must prove:

1. Eino serializes the configured model request once and Kupilot does not
   replace its admitted payload.
2. Text, discarded bounded reasoning fragments, atomic Tool identities with
   fragmented and interleaved arguments, non-canonical valid Tool JSON, Tool
   commentary, optional usage, and supported finish reasons produce one
   validated Agent transition.
3. Unknown or malformed Tool calls, duplicate identities or JSON keys,
   non-contiguous indexes, unsupported output fields, missing or conflicting
   terminal state, and size limits cause zero unauthorized Tool calls.
4. A mixed batch containing one correctable semantic policy denial produces
   fixed correlated feedback for the whole batch, zero handler and Kubernetes
   calls, and admits a later corrected batch only within the existing budgets.
5. Cancellation, model deadline, stale scope, response closure, and Adapter
   closure have one owner and one terminal outcome.
6. HTTP status, redirect, TLS, media type, malformed stream, and transport
   failures map to stable safe classes without leaking bodies or credentials.
7. Static import tests permit Eino and Eino OpenAI imports only inside
   `internal/agent/einoadapter` and prove that its exported surface contains no
   Eino type.
8. Provisional answer tests cover real SSE content callbacks, fragmented JSON
   and Unicode decoding, cross-chunk output safety, scope and terminal races,
   cancellation and timeout, Tool-turn clearing, bounded event delivery, and
   final answer replacement without persistence or scrollback commitment.

## References

- [Architecture](../architecture.md)
- [Model Compatibility](../model-compatibility.md)
- [Security Threat Model](../security.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
- [ADR-0036: Record Bounded Model Failure Diagnostics](0036-record-bounded-safe-model-failure-diagnostics.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](0047-reuse-eino-adk-for-session-context-and-summarization.md)
