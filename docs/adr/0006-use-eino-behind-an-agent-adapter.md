# ADR-0006: Use Eino Behind an Agent Adapter

- Status: Accepted
- Date: 2026-08-08
- Amended: 2026-08-10
- Amended by: ADR-0036 and ADR-0037

## Context

Kupilot needs a bounded Agent loop with streamed model responses and structured
Tool selections. Eino provides both Agent-runtime primitives and a maintained
OpenAI Chat Completions component. The accepted model contract still requires
exact control over request serialization, raw stream byte accounting,
Authorization, redirects, error-body disposal, and response closure. Allowing
the framework or provider client to define domain values or transport policy
would make security depend on vendor behavior.

Agent-runtime and model-protocol integration are separate infrastructure
responsibilities. Their Eino, provider, HTTP, SSE, message, callback, stream,
and Tool types must remain inside their respective adapters.

## Decision

Kupilot pins `github.com/cloudwego/eino` v0.9.13 and
`github.com/cloudwego/eino-ext/components/model/openai` v0.1.13. The OpenAI
component is used only inside `internal/llm/openaicompat` to implement the
Agent-owned neutral `Model` port. A project-owned `net/http` wrapper remains the
transport security boundary.

The model component owns Chat Completions integration, provider JSON decoding,
and decoded text, indexed Tool-call fragments, finish reasons, and usage.
Kupilot supplies the exact fixed JSON body through the component's
request-payload modifier and validates raw choice envelopes through its response
modifier. The project transport exclusively owns:

- API-key injection after validation of the request origin, method, media type,
  and serialized-body limit.
- Canonical-origin enforcement, verified TLS, loopback-only HTTP, and redirect
  policy.
- Raw response byte, data-record, and record-size limits before component
  decoding.
- Error-body replacement, request-header metadata, safe error mapping, and
  synchronous response-body closure.

The component receives a fixed non-secret API-key placeholder. The real
credential exists only while the guarded RoundTripper performs an admitted
request, and the placeholder is restored before redirect handling. The adapter
replaces caller callback contexts, and Kupilot registers no Eino global
callbacks. It performs no automatic retry, fallback, tracing, or persistence
and closes both the returned Eino stream and its tracked HTTP response body.

Agent-runtime integration uses Eino only inside
`internal/agent/einoadapter` to implement the Application-owned `AgentRunner`.
It consumes the existing neutral `Model` port and does not construct another
OpenAI component, provider client, or HTTP transport. The locked runtime path
is `flow/agent/react.NewAgent` with a project-owned bridge implementing
`model.ToolCallingChatModel`. The bridge's immutable `WithTools` accepts only
the fixed catalog; each `Generate` or `Stream` operation maps to exactly one
neutral `Model.Stream` call.

The seven admitted specifications are exposed to ReAct as run-local
`InvokableTool` values. `compose.ToolsNodeConfig.ExecuteSequentially` is true,
and each wrapper uses `compose.GetToolCallID` to recover the structured call
identity. The complete selected batch is validated and bound before handler
execution. Kupilot reserves and accounts for every Tool call immediately
before synchronous dispatch. Neutral deltas are emitted through the Kupilot
EventSink while the model call is active; the bridge returns one complete Eino
message chunk only after the neutral completion is valid. Every Eino output
stream is drained and closed. The adapter latches the first Tool-wrapper error
so later entries visited by the sequential Tools node return before reservation
or handler dispatch.

The Agent adapter uses Eino for:

- Bounded single-Agent runtime composition over project-owned Model and Tool
  ports.
- Translation between Eino runtime values and project-owned run events.
- Mapping the fixed Kupilot Tool specifications into the isolated runtime.
- Propagating cancellation and terminal state without owning model transport
  policy.

The ReAct `MaxStep` setting is a secondary failsafe only. Kupilot budgets,
repeat and no-progress state, scope checks, and terminal ownership remain
authoritative. The adapter clears inherited Eino callback context and installs
no handler. It enables no process-global callback, model retry, fallback,
memory, checkpoint, resume, tracing, Tool-return-directly behavior, or dynamic
Tool option.

Kupilot, not Eino, owns Agent-loop limits, Tool authorization, scope-generation
checks, Tool dispatch, Evidence creation, Diagnosis validation, persistence
intent, and Application event acceptance. Eino types do not cross either
adapter. Kupilot will not expose a general graph workflow or adopt RAG,
retrievers, Multi-Agent orchestration, dynamic Tool registration, memory
persistence, or a framework checkpoint/resume mechanism.

An AgentRun is never resumed as an Eino execution after process restart. Safe
Session history is reconstructed into neutral input for a new run.

## Consequences

Positive consequences:

- Maintained Chat Completions and fragmented Tool-call decoding is reused
  without delegating credential, origin, or raw-wire policy.
- Agent-runtime churn and model-protocol churn remain localized to separate
  adapters.
- Agent policy can be tested without a live model or Eino runtime.
- The model compatibility contract enforces raw-wire limits without making
  framework streaming primitives domain contracts.

Costs and constraints:

- Eino runtime values and model component values each require an explicit
  project-owned mapping.
- The model adapter must guard against duplicate, malformed, out-of-order, and
  oversized provider events before they reach the Agent runtime.
- Kupilot cannot rely on framework memory, retry, callback, or orchestration
  defaults unless each behavior is explicitly admitted by runtime policy.
- Core and component versions must be validated together when either pin
  changes.

## Alternatives considered

- Maintaining a parallel direct Chat Completions and SSE decoder was rejected
  because the pinned Eino component already supplies the required provider JSON
  and fragmented Tool-call mapping. Its string API-key configuration and
  abstract stream do not become security boundaries: the component receives
  only a fixed placeholder, while the project transport injects the secret and
  bounds the raw response body before component decoding.
- Allowing the component's default request serializer to define the contract
was rejected because Kupilot requires an exact code-owned catalog payload, strict
  schemas, deterministic request bytes, and a pre-I/O request-size check.
- A generic direct HTTP or provider abstraction was rejected. The admitted
  transport is a single code-defined Chat Completions profile with no dynamic
  endpoint, provider, request shape, fallback, or extension surface.
- Exposing Eino message and Tool types throughout the codebase was rejected
  because vendor contracts would leak into Application, Domain, Tools, and TUI.
- Using Eino to own the whole Agent loop was rejected because budgets, scope,
  Evidence, persistence, and policy must remain deterministic Kupilot controls.
- Eino ADK `ChatModelAgent` was rejected because its session, checkpoint,
  resume, transfer, and asynchronous event surface exceeds the admitted
  single-run contract.
- A direct `compose.Graph` implementation was rejected because it would
  duplicate ReAct message accumulation, branching, and loop behavior without
  adding an admitted capability.

## Security and privacy impact

Eino is not a security boundary. Model output remains untrusted, and only
runtime code can dispatch a fixed Tool. Prompt instructions and framework Tool
registration cannot widen ClusterScope, Kind allowlists, budgets, endpoint, or
write authority.

The model adapter must close response streams and bodies, bound buffers,
discard provider error bodies in default logging mode, and prevent model
requests or responses from entering SQLite or default operational logs.
ADR-0036 permits only an explicitly enabled bounded failed-response prefix in
the local model-failure log. Kupilot must not install Eino global
callbacks; caller-provided callbacks are removed before model data enters the
component. Framework callbacks cannot receive model credentials, raw HTTP
values, or a Kubernetes client.

## Validation

Official module metadata, source, and adapter tests must verify:

1. The pinned Eino core and OpenAI component modules, their supported Go
   versions, and the exact APIs used at each adapter boundary.
2. Streaming text and structured Tool-call event ordering, raw byte ownership,
   and response closure on success, error, timeout, and cancellation.
3. Strict Tool schema representation without a prompt-parsed fallback.
4. Whether framework retries, callbacks, tracing, or persistence are enabled by
   default and how they are disabled or bounded.
5. Neutral error, usage, and request-metadata mapping without retaining raw
   request or response bodies.
6. Compatibility with the single OpenAI-compatible transport contract in
   ADR-0022, including confinement of Agent-runtime Eino types inside the Agent
   adapter and provider and HTTP types inside the model adapter.

The selected versions and exact API mappings remain recorded in dependency and
compatibility metadata.

## Revisit triggers

- The pinned Eino model component can no longer be wrapped without widening the
  accepted protocol or credential surface.
- Eino requires policy, persistence, or vendor types to escape either adapter.
- A dependency change enables retry, callback, tracing, persistence, or request
  behavior that cannot be deterministically disabled or bounded.
- Measured maintenance cost of either adapter exceeds the isolation benefit.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0009: Use Fixed Structured Tools](0009-use-fixed-structured-tools.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
