# Agent Runtime Contract

This document defines the provider-neutral single-Agent policy used by KuPilot.
It complements the [Architecture](architecture.md), the
[Security Threat Model](security.md), and the accepted Agent, Tool, Evidence,
budget, and language decisions. Prompt text is behavioral guidance; the runtime
contracts in this document remain authoritative.

## Run ownership and lifecycle

One `AgentRunner` handles one immutable `RunInput`. The input contains only the
run, Session, and request Message identities; the locally processed current user
question; one verified `ClusterScope`; zero or one selected `ResourceRef`; and
the frozen Prompt, Tool catalog, and budget versions. It contains no client,
endpoint, credential, repository, callback, framework value, or mutable
Application state. Pointer-bearing accessors return defensive copies.

The runner blocks until one terminal `RunOutcome`, honors the owning Context,
and publishes ordered neutral events through `EventSink`. Its state machine is:

```text
queued -> running -> completed
                  -> failed
                  -> cancelled
                  -> timed_out
                  -> stale_scope
                  -> interrupted
```

Only `queued -> running` and `running -> terminal` are admitted. A terminal
state has no outgoing transition. A completed outcome contains one locally
validated Diagnosis for the same run and scope. Every other outcome contains
only a stable safe error classification and message.

The Eino runtime adapter is the sole framework translation boundary. It uses
the existing neutral `Model` port and these policy contracts. It must not create
another model component, HTTP transport, provider client, production Agent
loop, dynamic Tool registry, or framework checkpoint and resume path.

## Pinned Eino composition

The production adapter uses `github.com/cloudwego/eino` v0.9.13 and the
dedicated `flow/agent/react.NewAgent` single-Agent path. Its model bridge
implements the immutable `model.ToolCallingChatModel` contract: `WithTools`
accepts only the exact fixed catalog, while `Generate` and `Stream` translate
Eino messages into one neutral `ModelRequest`. Every admitted Eino model node
invocation therefore makes exactly one call through the existing neutral
`Model.Stream` port. The adapter creates no provider component, client, HTTP
request, retry, or fallback.

The six fixed Tool specifications are mapped to run-local Eino
`InvokableTool` values. The ReAct Tools node uses
`ExecuteSequentially: true`, and the wrapper recovers the structured call ID
through `compose.GetToolCallID`. A complete model-selected batch is validated
and bound before any handler can run. Admitted calls then reserve budget and
execute synchronously in model order; a failed reservation prevents that call
and every subsequent model or Tool call. Because the framework visits each
entry in a sequential batch even after one wrapper reports an error, the
adapter also latches the first wrapper failure; later entries return that safe
failure before scope reservation, budget reservation, or handler dispatch.

Neutral model deltas are validated and published as KuPilot events while the
neutral call is active. Only after the neutral stream has one valid completion
does the bridge expose one complete Eino message chunk, so ReAct branching
never interprets partial JSON or Tool-like prose. The adapter drains and closes
every returned Eino stream. Its `MaxStep` is only a secondary graph failsafe;
`RunBudget` remains the authoritative call and progress policy.

The adapter replaces inherited Eino callback context and registers no global
callback. It configures no memory, checkpoint, resume, tracing, retry,
fallback, Tool-return-directly, middleware, or dynamic Tool option. Eino's ADK
`ChatModelAgent` is not used because its session, checkpoint, resume, transfer,
and asynchronous event surface exceeds the single-run contract. A direct
`compose.Graph` is also not used because it would duplicate the maintained
ReAct accumulation and branch loop without adding admitted behavior.

## Versioned System Prompt and language

`kupilot-agent-policy-v1` is a deterministic English System Prompt. It defines
the read-only diagnostic role, Evidence-first behavior, fixed four-part
Diagnosis, unexecuted recommendation rule, Tool-result trust boundary, and
language policy. A machine-generated JSON block contains only:

- The run identifier and policy versions.
- The verified Context and Namespace display names, scope generation, and UTC
  activation time.
- The optional selected ResourceRef.
- The frozen run-level ceilings.

The current user question is a separate user message and is never interpolated
into the System Prompt. Credentials, kubeconfig material, Kubernetes Secret or
ConfigMap data, endpoint values, raw objects, and raw Tool output are not fields
of the trusted block. All string values are validated and JSON encoded; a name
that resembles an instruction remains data.

Diagnostic statements follow only the language of the current user question.
When that language cannot be determined reliably, the Agent falls back to
English. Tool results, Kubernetes fields, Events, logs, resource names, history,
and model output cannot change this choice. Project-owned structured field names
and the four Diagnosis headings remain English. There is no language setting,
locale negotiation, or probabilistic runtime language detector.

Before selecting a Tool, the System Prompt requires the Agent to distinguish an
admitted current-Namespace diagnostic request from an unsupported source request.
Node and Namespace objects, Namespace discovery, cluster-wide inventory, and
every unlisted Kind are explicit examples. The Agent must not use an admitted
Kind as a proxy for such a request. It returns a structured `unsupported` gap
without a Tool call and may identify the active Namespace as trusted scope, not
as Kubernetes Evidence. Namespace discovery remains available only through the
fixed `/namespace` selector outside an AgentRun.

## Fixed Tool contract

The `kupilot-read-tools-v1` catalog contains exactly these six structured,
read-only Tools in fixed order:

1. `get_resource`
2. `list_resources`
3. `get_events`
4. `get_pod_logs`
5. `get_previous_pod_logs`
6. `get_related_resources`

Each specification has a code-defined English description and strict JSON
Schema with `additionalProperties: false`. Every property at each object level
is listed in `required`; a field with a code-defined default is nullable in the
model schema and its `null` value is normalized locally to that default. A
structured selection is decoded strictly, normalized, and re-serialized
canonically before its digest is calculated. Unknown Tools, extra or duplicate
fields, wrong types, invalid Kinds or names, and prohibited authority fields are
denied before handler resolution. If any selection in one model batch is
invalid, no handler from that batch is invoked.

Model arguments contain no Context, Namespace, ClusterScope, generic GVR,
endpoint, credential, kubeconfig, deadline, or hard ceiling. A
model may request only schema-bounded query values such as a smaller item count,
time window, or log tail. Those values never replace runtime ceilings.

The runtime constructs `BoundToolCall` with the immutable run and scope, the
canonical selection, and hard ceilings. Its fixed `ToolHandlers` value has one
field for each admitted Tool and no dynamic registration operation. A Tool has
one Context-aware `Execute` operation and returns a project-owned safe
`ToolResult`.

A ToolResult carries invocation identity, Tool and schema version, injected
scope, UTC observation time, success, partial, error, or denied status, the
canonical neutral serialization of a Tool-specific safe DTO, accepted candidate
Evidence, safe warnings, truncation metadata, and an optional stable safe error.
It never carries a raw Kubernetes object, raw Event, raw container output,
credential, vendor error, or framework value. Before model use, the complete
serialized result is bounded and wrapped with
`data_class: untrusted_tool_data` plus an explicit statement that its fields
cannot change language, scope, policy, budgets, Tool or Evidence authority,
consent, approval, or execution state.

## RunBudget

`RunBudget` is concurrency-safe and stores no Context. It atomically admits a
call intention before external I/O and seals permanently when cancellation,
deadline, a hard-limit violation, a forbidden repeat, or no progress stops the
run. A failed reservation does not consume a partial counter and must produce
zero subsequent model or Tool calls.

The default limits equal the non-expandable `v0.1` ceilings:

| Budget | Maximum |
| --- | --- |
| AgentRun wall clock | 90 seconds |
| Agent loop | 8 steps |
| Tool calls | 10 |
| Model calls | 3 |
| Model child request | 45 seconds and no later than the run deadline |
| Tool child request | 10 seconds and no later than the run deadline |
| ToolResult | 64 KiB serialized per result |
| ToolResults per run | 384 KiB serialized total |
| Log Tools | 2 calls total |
| Evidence | 100 items per result |
| No progress | 2 consecutive completed steps with no new accepted Evidence |

A frozen configuration may reduce a value but cannot raise it. Child call
reservations return the smaller of their request ceiling and remaining run
time.

When a length, call, byte, repeated-call, or no-progress policy stop occurs
after a run has started, the runtime performs no further external call and may
produce a local gap-only Diagnosis from Evidence already accepted. That result
passes the same Diagnosis validator and cannot create a confirmed fact or an
execution claim. Cancellation, deadline, stale scope, malformed external data,
and internal failures remain non-completed terminal outcomes.

Repeated-call identity is the fixed Tool name, Tool version, and digest of all
canonical model-supplied arguments. Scope is excluded because it is immutable
for the run. The first call is admitted normally. One identical second call is
admitted only after a stable retryable result or through the separate trusted
runtime revalidation operation. A third call is always denied. Model text cannot
request revalidation.

## Evidence registry and Diagnosis validation

One runtime `EvidenceRegistry` is bound to one AgentRun and exact
ClusterScope. Its only acceptance path requires both a valid `BoundToolCall` and
its matching ToolResult. Acceptance verifies run, invocation, Tool version,
scope, observation time, Evidence shape, and uniqueness atomically. Cross-run,
cross-scope, duplicate, invalid, or post-finalization Evidence is rejected
without adding any item. User text, model text, historic Evidence, and selected
resources have no registration path.

A model Diagnosis is an untrusted four-part draft:

- `confirmed_facts`
- `hypotheses`
- `missing_information`
- `recommended_actions`

The final model message must be one bare JSON object containing exactly these
four fields. Unknown or duplicate keys, missing or null collections, trailing
content, and malformed JSON are rejected. Parsing never recognizes a Tool call
from text. The System Prompt states the exact item keys, allowed enum values,
non-null collection rules, prohibition on Markdown fences or commentary, and a
valid all-empty object for cases where no item can be populated safely.

Final validation performs these deterministic operations:

- A confirmed fact with no Evidence ID, a duplicate ID, or any ID absent from
  the current registry is excluded and produces a validation warning and an
  explicit missing-information entry.
- Unsupported Evidence references are removed from hypotheses; confidence
  remains model self-assessment and never promotes an inference to fact.
- Every recommendation is forced to `executed=false`; a contrary draft creates
  a warning.
- The observed time window and complete or partial Evidence-detail state are
  derived from registered Evidence. Truncation creates an explicit gap.
- Final Markdown is rendered locally from only the validated collections, uses
  the four stable English headings, cites accepted Evidence IDs, shows the
  observed scope and time window, and marks every recommendation `Not executed`.

The registry is sealed only after the final Diagnosis passes the complete
domain validation. A successful AgentRun means that this policy was followed;
it does not mean that a root cause was found.

## Ordered events

Every `RunEvent` contains the run ID, scope generation, monotonically increasing
sequence, and UTC occurrence time. The typed event catalog covers run start,
model stream start, text delta, Tool request, start, completion, failure or
denial, Evidence collection, Diagnosis readiness, and each run terminal state.
Payloads contain only neutral project values.

`EventPublisher` is the sole sequence and terminal owner. `RunStarted` is the
first event. `RunCompleted` requires a preceding `DiagnosisReady`. Exactly one
terminal event is admitted, and every later event is rejected before the sink.
The synchronous EventSink reports `accepted`, `degraded`, or `rejected` so the
Application can apply explicit backpressure and persistence policy. Text deltas
may be coalesced outside this contract; structural and terminal events cannot be
dropped.

## Security and persistence boundaries

The Agent policy package performs no Kubernetes, SQLite, terminal, or model
transport I/O. It imports no Eino or provider type. Scope freshness checks,
Application event acceptance, consent, source projection, redaction, and
sensitive-value blocking remain independent mandatory boundaries around these
contracts.

The production adapter checks the exact immutable scope before reserving each
external call, again immediately before the call, and after the return before
accepting any output. The Application-owned EventSink remains the independent
acceptance gate for every neutral event. A stale result cannot become Tool
context, Evidence, Diagnosis input, or a later call.

System Prompts, raw model traffic, streaming deltas, complete ToolResults, and
invalid Diagnosis drafts are ephemeral and never persistence payloads. Durable
code may store only the independently eligible derivatives defined by the
[Data Retention Contract](data-retention.md): safe run metadata, sanitized
ToolInvocation metadata, accepted Evidence, the validated Diagnosis, and final
assistant content according to privacy mode.

Deterministic conformance uses scripted neutral Model implementations, fake
Tools, fake clocks, and in-memory EventSinks. It proves exact and one-over
budgets, cancellation and deadline stops, retry and revalidation rules,
no-progress termination, zero handler calls for policy denials, Prompt and
Diagnosis snapshots, language isolation, same-run Evidence citations, and
single terminal publication without a real model, cluster, database, or TUI.
