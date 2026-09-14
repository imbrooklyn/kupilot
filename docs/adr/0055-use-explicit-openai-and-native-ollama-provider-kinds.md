# ADR-0055: Use Explicit OpenAI and Native Ollama Provider Kinds

- Status: Accepted
- Date: 2026-09-15
- Amends: ADR-0010, ADR-0022, ADR-0043, ADR-0046, and ADR-0054

## Context

Kupilot previously named its only model protocol `openai_compatible`. That
name was unnecessarily verbose for the OpenAI Chat Completions path and made a
local Ollama endpoint use Ollama's compatibility route instead of Eino's
stable native Ollama component. The native component has different concrete
transport semantics: it sends `/api/chat`, streams NDJSON, needs no bearer
credential for the admitted loopback deployment, reports usage differently,
and the Ollama protocol does not carry OpenAI Tool-call identifiers or delta
indexes.

Supporting that exact protocol does not justify provider discovery, routing,
fallback, cross-origin retry, a second Agent, or a second conversation loop.
The selected provider remains frozen configuration for one role and one
origin. Provider output remains untrusted and must satisfy the same local
Tool, Evidence, response, persistence, consent, and budget gates.

The source review selected stable
`github.com/cloudwego/eino-ext/components/model/ollama` v0.1.9, source commit
`9b7587b89863115eb89f172858fdd3b4de30c3e7`. It uses
`github.com/eino-contrib/ollama` v0.1.0 and Eino's
`ToolCallingChatModel` contract. Minimum Go requirements are compatible with
Kupilot's Go 1.25 baseline. Eino core remains pinned at v0.9.19.

## Decision

### Fixed provider kinds

Kupilot admits exactly two code-owned provider kinds:

- `openai` uses the existing Eino OpenAI Chat Completions component. This is
  the new name for `openai_compatible`; and
- `ollama` uses Eino's native Ollama component and the native `/api/chat`
  protocol.

Every configured role selects exactly one kind, endpoint, model, response
format, credential policy, and finite timeout. There is no auto-detection,
provider router, load balancer, fallback, downgrade, retry, or automatic
translation between the two protocols. The retired configuration value
`openai_compatible` is rejected rather than silently reinterpreted. Historical
model-request metadata is migrated deterministically from
`openai_compatible` to `openai`; it is audit metadata, not resumable transport
authority.

`openai` retains verified HTTPS by default and explicit loopback HTTP, a fixed
role-bound bearer credential, Chat Completions SSE for streaming, JSON for
non-streaming requests, and the explicit `prompt` or endpoint-proved
`json_object` response-format setting.

`ollama` is admitted only at an explicit loopback HTTP origin. It requires
`credential_ref: none`, accepts no API key, Authorization header, query,
userinfo, fragment, redirect, environment-selected authentication, or remote
Ollama service. The configured endpoint is the native server base, such as
`http://127.0.0.1:11434`; Eino appends `/api/chat`. Streaming responses must be
`application/x-ndjson`; non-streaming responses must be `application/json`.
The same request, response, chunk, message, wall-time, and cancellation bounds
apply.

### One Eino boundary and native normalization

Both components stay inside `internal/agent/einoadapter` and implement the one
Eino `ToolCallingChatModel` consumed by the existing `ChatModelAgent`, Runner,
ReAct iteration, message state, and summarization middleware. No Eino,
provider, HTTP, or vendor type crosses that boundary.

The native Ollama wire protocol has no Tool-call identifier or stream delta
index. After Eino decodes a native response, the adapter assigns each complete
native Tool call one deterministic, request-bound identifier and contiguous
ordinal before project validation. The identifier is derived only from the
current model-request identity and ordinal; it neither carries provider
authority nor survives as a continuation handle. Missing fragments,
duplicates, over-limit calls, malformed arguments, unexpected roles, data
after completion, or invalid finish state still fail closed. Tool results are
paired by the existing Eino message state; no Tool is retried or replayed.

The native component exposes a zero-valued usage container on intermediate
chunks. The adapter discards only those exact intermediate placeholders and
retains the terminal measured usage. It removes native reasoning content at
the same bounded untrusted-output boundary used by the OpenAI component. No
token estimate is invented when terminal usage is absent or invalid.

An omitted `reasoning_effort` leaves Ollama's native `think` member absent and
therefore preserves the selected model's own default. Explicit `none` sends
native `think: false`. Interactive provider setup has no separate reasoning
choice, so changing from OpenAI to Ollama clears a carried explicit value and
uses the omitted default. This matters for models whose native Tool protocol
requires their reasoning mode: the exact local `gpt-oss:20b` compatibility run
returned an empty Tool turn with `think: false` but completed the same bounded
Tool and final-response sequence when `think` was omitted. Kupilot does not
infer this behavior from the model name, retry the failed request, or rewrite
an explicitly authored configuration value.

OpenAI-specific request and response modifiers are used only with the OpenAI
component. Native Ollama serialization remains owned by Eino. The guarded
HTTP transport independently enforces exact origin, path, method, media type,
credential policy, body limits, redirects, cancellation, and safe error
projection for each selected protocol.

### Configuration, setup, and budgets

Configuration schema version 1 remains current. `provider_kind` is required
and is either `openai` or `ollama`. `openai` requires the fixed role-bound
credential reference and an effective credential. `ollama` requires
`credential_ref: none` and rejects file or environment API-key material.
Interactive model setup asks for the provider kind first, requests a masked
API key only for `openai`, and can persist a credential-free Ollama profile or
keep it process-local.

The immutable `compact`, `balanced`, and `extended` run profiles remain the
only budget profiles. Native Ollama does not gain an unlimited or hidden
provider budget. Because local model startup and generation can be slow, the
documented native Ollama configuration recommends `extended`: a 60-minute run,
at most 900 seconds per model request, and at most 180 seconds per Tool request.
The bounded local integration harness independently permits at most four
calls, ten minutes per request, and fifteen minutes for the suite. The ordinary
product default remains `balanced`; an explicit profile is visible in
`/status`, frozen per run, and never grants retry authority.

### Persistence and compatibility

A forward-only checksummed migration rebuilds only the `model_requests`
provider check and translates historical `openai_compatible` metadata to
`openai` in one transaction. All other model-request columns, indexes,
foreign keys, retention, graph deletion, and redacted export behavior remain
unchanged. Configuration is not silently migrated because replacing an
explicit protocol selector without endpoint verification could send content
under unintended assumptions.

Provider kind, model, and origin hash remain bounded non-secret metadata.
Credentials, prompts, raw requests, raw responses, NDJSON events, native
objects, Tool payloads, and continuation handles remain excluded from SQLite,
exports, default logs, and TUI content.

## Consequences

OpenAI Chat Completions configurations become shorter and explicit. Local
Ollama uses its native Eino component and API rather than depending on the
optional compatibility route. Both paths keep identical Application
authority, consent, Evidence, action, and no-blind-retry semantics.

Existing user configuration containing `openai_compatible` must be edited to
`openai`. An Ollama endpoint outside loopback, one that injects authentication,
or a model that does not support the fixed Tool and structured-response
contract is unsupported. Selecting `json_object` remains explicit and exact-
endpoint evidence is still required.

The native Ollama dependency has a larger transitive module graph than the
OpenAI component. Dependency, license, vulnerability, import, build, and
cross-platform gates therefore remain mandatory. The first production-call-
graph scan found GO-2026-5018 reachable through the native client's SSH helper,
so Kupilot explicitly pins the fixed `golang.org/x/crypto` v0.52.0 and its
minimum compatible `x/net`, `x/term`, and `x/text` module versions. These
modules require Go 1.25 and do not raise Kupilot's existing Go baseline.

## Security and privacy impact

Native Ollama sends model content only to the exact configured loopback origin
and sends no credential. Origin and role consent remain separate and are
invalidated by provider/origin changes. Environment-selected Ollama hosts and
authentication cannot broaden the configured route because the guarded
transport rejects the resulting request before network I/O.

Deterministic Tool-call identifiers restore only local pairing syntax. They do
not assert provider identity, response continuation, semantic correctness, or
permission. A disconnect remains unknown/recovered with no automatic retry;
neither admitted protocol exposes the stable same-response continuation
contract required by ADR-0049.

## Validation

Deterministic tests cover:

1. strict `openai` and `ollama` configuration, credential, endpoint, response-
   format, inheritance, write, setup, and retired-value rejection;
2. exact OpenAI `/chat/completions` SSE/JSON and native Ollama `/api/chat`
   NDJSON/JSON requests through the guarded transport;
3. no Authorization for Ollama, required Authorization for OpenAI, same-origin
   redirect handling, content types, bounds, cancellation, timeout, and safe
   error projection;
4. native streamed and non-streamed responses, omitted and explicitly disabled
   reasoning, terminal usage, reasoning removal, deterministic Tool-call
   identifier/index normalization, Eino Tool result pairing, and
   malformed/duplicate/out-of-order/one-over rejection;
5. one selected provider call with zero probe, fallback, cross-origin retry,
   duplicate Tool, action, or transcript content;
6. migration of historical metadata, fresh inserts for both kinds, rollback,
   foreign keys, retention, Session graph deletion, resume, and export;
7. an explicitly authorized bounded local run against an already installed
   Ollama server and model, reported as endpoint-specific integration evidence
   rather than deterministic or release evidence; and
8. the project vulnerability gate with the fixed cryptography dependency in
   the native client's reachable production call graph.

## References

- [Model Compatibility](../model-compatibility.md)
- [Configuration](../configuration.md)
- [Agent Runtime](../agent-runtime.md)
- [Security Threat Model](../security.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0054: Preserve Structured Response Compatibility Across Turns](0054-preserve-structured-response-compatibility-across-turns.md)
