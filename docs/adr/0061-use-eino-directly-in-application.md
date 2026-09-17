# ADR-0061: Use Eino Directly in Application

- Status: Accepted
- Date: 2026-09-17
- Amends: ADR-0013, ADR-0022, ADR-0043, ADR-0047, ADR-0055, and ADR-0060

## Context

The isolated runtime accumulated model wrappers, repeated message validation,
and a composition-root forwarding object. Its Chat Completions representation
also cannot retain native Responses reasoning items across local Tool calls.
Disabling reasoning is not an acceptable implicit compatibility correction.

## Decision

Application directly owns the Eino ADK composition. The separate
`internal/agent/einoadapter` package and composition-root runtime forwarder are
removed. Domain remains pure; delivery and SQLite receive only project-owned
safe projections. Agent contains the existing concrete catalog, budgets,
Evidence registry and final-response rules, not a framework-neutral Agent port.

Use Eino's native message types and model components. OpenAI profiles explicitly
select `chat_completions` (the existing default) or `responses` through
`api_protocol`. Native Ollama continues to use its native component. Selection
is frozen configuration, never model-name inference, probing, routing, retry,
or fallback. Exactly one ADK Agent and Runner execute a run. Eino owns request
serialization, message assembly, Tool pairing and ReAct iteration.

The Responses implementation uses stable `agenticopenai v0.2.2` and Eino
`v0.9.19`. It sets SDK retries to zero, storage to false, automatic response
caching off, and truncation disabled. Native reasoning items may remain in the
bounded current-run Eino state and return only to the same consented endpoint.
They are never shown, logged, persisted, exported, or restored by Session resume.
No hosted Tools, MCP, background response, checkpoint, or remote conversation
store is enabled. Reasoning effort is explicit; omission preserves the endpoint
default. Endpoint rejection cannot change the user's settings.

Run preflight, finite reservations, steer commitment, cancellation, scope and
policy generations, Tool authorization, Evidence ownership, final validation,
and persistence barriers remain Application policy. They are implemented at
native ADK hooks and the existing strict Tool handlers. A narrow HTTP guard
retains origin/credential, byte, timeout, closure and safe-error obligations.
Previously proved SDK defects retain only their bounded regression protection;
they do not justify a general protocol implementation.

There is still one durable SQLite safe conversation source. Historical messages
are translated once, without old Evidence or action authority. Summarization
continues to use Eino middleware and the same Agent profile's independent,
non-streaming, Tool-free, one-attempt budget.

## Optional sampling configuration

OpenAI temperature is optional. Omission delegates sampling to the explicitly
selected endpoint; an explicit zero or other admitted value is sent unchanged.
The runtime never infers sampling support from a model name, removes a supplied
value after rejection, or disables reasoning. Native Ollama continues to require
an explicit temperature under its existing request-fidelity contract. YAML null
is not an alternative spelling for omission. Existing complete profiles keep
their explicit values; a user-authorized configuration edit can remove one.

## Native Responses streaming admission

A recording fixture proves that v0.2.2 loses reasoning encrypted content when
it arrives in `response.output_item.done`. Native non-streaming Generate keeps
that item intact. Responses Agent profiles therefore require explicit
`streaming: false` until a stable component passes the same stream fidelity
fixture. This is a declared component limitation, not an automatic downgrade.
Chat Completions and native Ollama remain streamed. Responses still uses one
native ADK Agent/Runner and preserves reasoning; only provisional delivery is
unavailable. No raw-event repair, SDK fork, or second request is introduced.

## Validation

Recording HTTP fixtures must cover native Responses reasoning followed by local
Tools and a validated final, retained history, clarification, plan mode,
summary, cancel, timeout, stale generations, limits, malformed or incomplete
output, persistence denial, and zero unauthorized calls. Existing Chat
Completions and native Ollama regressions remain required. Import tests enforce
the new Application boundary and prevent Eino from reaching Domain, delivery,
Tools, Kubernetes or SQLite.

Dependency, platform, race, security and repository gates are required. Tagged
endpoint tests use synthetic data and report exact calls, usage and failures.
Source inspection establishes API availability, deterministic fixtures establish
local correctness, and a live endpoint result establishes compatibility only
for that exact configuration. None proves model semantic quality or release
readiness.

## Source evidence

- [Eino v0.9.19 Agentic ReAct](https://github.com/cloudwego/eino/blob/v0.9.19/adk/react.go)
- [Native Responses component](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_model.go)
- [Pinned streaming item conversion](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_event_convertor.go)
- [Pinned non-streaming conversion](https://github.com/cloudwego/eino-ext/blob/20b2a55477df4e2e078c51596deb019aa54ab133/components/model/agenticopenai/responses_convertor.go)

The stable Responses module transitively requires the exact ACL pseudo-version
`v0.1.18-0.20260527084435-846f52bd97c6`. Adoption does not relabel that indirect
revision as stable; the pinned graph must pass the complete dependency and
platform gates. No vendor fork or local replace directive is used.

The required vulnerability gate also upgrades `golang.org/x/net` to v0.55.0
and `golang.org/x/text` to v0.39.0 for reachable GO-2026-5026 and GO-2026-5970.
Both require Go 1.25.0 and retain the Go BSD license.
