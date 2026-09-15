# Model Compatibility Contract

This document defines the two fixed model provider kinds and explicit named-
profile contract accepted for Kupilot `v0.5`. The checked-in runtime implements the
required `agent` profile, optional `approval_reviewer` transport seam, Eino ADK
composition, safe Session context, Agent-profile summarization, and the strict
Reviewer transport consumed by deterministic permission routing. Reviewer
output remains non-authoritative. The current Application action foundation
covers the supervised Deployment restart, six additional typed remediation
operations, exact local direct-argv and separate-shell actions, and the three
default-off remote-diagnostic handlers. Human and Reviewer delivery for those
remote diagnostics uses the same inline Application supervision and exact
ActionEnvelope authority. Review-class Pod logs and optional Prometheus/Loki
reads use that supervision too; model output cannot bypass their privacy,
catalog, permission, target, generation, or audit checks.

Each protocol is intentionally narrower than a broad compatibility label.
Compatibility means passing this contract for one exact provider kind, role,
model, origin, dependency tag, and configuration; it does not follow from a
product label or provider claim.

## Named profiles and evidence boundary

Kupilot admits exactly two provider kinds, `openai` and `ollama`, while allowing
the two fixed explicitly configured profiles and canonical origins. `openai`
uses Eino's OpenAI Chat Completions component. `ollama` uses Eino's native
Ollama component at an explicit loopback origin. The fixed consumer roles are
required `agent` and optional `approval_reviewer`.
Summarization reuses `agent` with an independent reserved budget; there is no
predeclared `context_compactor` role.

Each call is bound to the one profile selected by its code-owned consumer role.
There is no provider auto-detection, protocol translation, fallback, router,
load balancing, cross-origin retry, or model-selected endpoint. Provider kind
is explicit, validated configuration and is frozen with each role binding.

Each profile explicitly selects `response_format: prompt` or
`response_format: json_object`. `prompt` is the compatibility default.
`json_object` is selected only with exact endpoint evidence and asks the
selected Eino component for its JSON-object mode. It is not inferred from the
origin or model name and never triggers a probe, downgrade, fallback, or retry.

Agent requests remain streamed Chat Completions with structured Tool calls.
Reviewer requests are separate strict non-streaming, no-Tool requests that may
return only `approve`, `deny`, or `escalate_to_user` plus a bounded rationale.
Reviewer failure authorizes nothing and never falls back to the Agent or
another origin.

Credentials are opaque and profile-specific. Consent binds the model role,
policy version, canonical origin hash, and exact data categories. Changing a
profile, role, origin, category, or meaning invalidates the affected consent
and pending action before another content request.

Exact context windows, input/output tokens, request/stream limits, concurrency,
latency, cost ceilings, and summary thresholds require tagged pinned dependency
source/tests plus selected-endpoint fixtures and, where authorized, tagged live
integration. No model name, marketing page, Eino example default, character-
to-token estimate, or old global `8192` value is sufficient evidence.

## Internal boundary

`internal/agent/einoadapter` is the sole Eino and model-provider boundary.
Domain, Agent core, Application, Tools, persistence, CLI, and TUI contain no
HTTP, SSE, Eino, SDK, or provider values. The composition root gives this
boundary explicit role-bound model dependencies, opaque credentials, fixed
Tool handlers, and project-owned run-policy services. It creates no service
locator or provider router.

At run start, Application selects the required ordered, bounded representation
of all retained eligible prior same-Session context and the adapter maps the
project-owned prompt, messages, current question exactly once, and fixed Tool
catalog into Eino values. When prior eligible context exists, omitting it or
falling back to a current-question-only call fails closed before model I/O.
Stable ADK `ChatModelAgent` and `Runner` own the in-run message state,
Tool-message pairing, ReAct iteration, and events. Before each bounded model
call, project policy validates the Eino-owned conversation in place and invokes
the one concrete Eino ChatModel selected by the frozen provider kind.
Eino v0.9.19 runs the summarization handler first and then the run-local steer
handler's `BeforeModelRewriteState`; the returned Messages are persisted before
the same handler's `WrapModel` executes the Application commit barrier around
real model I/O. A failed barrier does not delegate to the model.
There is no parallel neutral request/message protocol or custom conversation
loop. Eino serializes the request, decodes the selected protocol's SSE or
NDJSON response, and assembles its message stream. The adapter then validates
and locally canonicalizes the one
assembled assistant message before any Tool reservation, Tool dispatch,
durable assistant Message, Evidence reference, or Diagnosis can result.

Eino summarization middleware is reused directly with a project-owned safe
finalizer, coverage metadata, recent-tail selection, and evidence-based budget.
Kupilot does not implement a `MemoryManager`, tokenizer, summary engine,
generic checkpoint/event store, raw framework transcript, or framework-neutral
Agent or memory facade.

The only pre-assembly delivery projection observes already decoded and
chunk-validated Eino `Content` strings. It incrementally recognizes a first
top-level `answer_markdown` JSON string, applies cross-chunk output safety, and
emits project-owned provisional text. It does not decode SSE, reconstruct a
provider payload, assemble a message or Tool call, decide a finish reason, or
create runtime authority. The validated assembled message remains the only
source of final text and structured actions.

Provider fragments never authorize a Tool. A complete Tool call must have a
contiguous bounded index, fixed name, bounded identifier, valid JSON argument
object, and an admitted Tool terminal state. OpenAI supplies its wire ID and
delta index. Native Ollama supplies neither, so the adapter assigns one
request-bound deterministic identifier and ordinal to each complete decoded
call before the same project validation. The project-owned strict binder then
rejects duplicate, unknown, wrong-type, overlong, sensitive, scope-bearing, and
runtime-authority fields, injects immutable scope and ceilings, and produces
canonical arguments before budget reservation or dispatch. Non-canonical JSON
whitespace and key order remain valid. Commentary that precedes or accompanies
a Tool selection is bounded and discarded after the assembled response proves
`tool_calls`; ordinary commentary is not recognized by the provisional answer
projector and cannot become Tool authority, Evidence, or Diagnosis. A requested
Tool clears any final-envelope-shaped provisional prose from that model turn.

When every selection has a known Tool name and a structurally safe argument
object but strict semantic binding denies any member, the adapter keeps the
batch atomic and returns fixed code-authored local policy feedback through the
ordinary correlated Tool-message path. The feedback contains neither rejected
arguments nor live scope values. It authorizes no ToolInvocation, budget
reservation, handler, Kubernetes request, persistence, or Evidence. Eino may
then request another bounded model decision. Unknown Tools, malformed argument
objects, injected authority fields, and sensitive model text remain terminal.

## Transport implementation and lifecycle

The production model transport uses
`github.com/cloudwego/eino-ext/components/model/openai` v0.1.13 and
`github.com/cloudwego/eino-ext/components/model/ollama` v0.1.9 with
`github.com/cloudwego/eino` v0.9.19. Both components, the ReAct runtime, and the
hardened `net/http` wrapper are contained in `internal/agent/einoadapter`.

Those pins are the implemented runtime and the exact tagged source reviewed for
this slice. Eino v0.9.19 is the exact stable target at commit
`9d983b36a5112a1c233056b1a099825298fafb8f`. Its handler, wrapper, and
chat-model sources are unchanged from v0.9.13; its summarization change keeps
the current context on two error returns. It provides stable ADK
`ChatModelAgent`, `Runner`, message state, Tool pairing, model-boundary
handlers, and summarization middleware, but no accepted/committed/recovered
product steering protocol and no suitable runner-managed durable Session
contract. Kupilot keeps the existing SQLite safe Messages through the thin
project-owned selection bridge and does not adopt `TurnLoop`, a prerelease
Session API, or a second runtime.

The reviewed OpenAI extension v0.1.13 source revision is
`0ebab92e14f26088411dbc440a1ebdc904ccd8a1`; the pinned ACL is v0.1.17 and
`go-openai` is v0.1.2. The reviewed stable native Ollama extension v0.1.9
source revision is `9b7587b89863115eb89f172858fdd3b4de30c3e7`; it uses
`github.com/eino-contrib/ollama` v0.1.0 at revision
`d83ec400201e36f50556dfbec25cf2bbadd0b4c9`. These are compatibility identities,
not evidence for a particular live endpoint. Eino core and the OpenAI
extension were not upgraded to admit Ollama.

The selected Eino component is the only request serializer and stream decoder.
Kupilot does not replace or reconstruct its JSON request. When a profile
selects `json_object`, a response-constrained Eino model value sharing the same
role, origin, credential policy, and guarded HTTP client serializes the
provider's fixed JSON mode for Agent streaming or Reviewer generation.
Agent-summary generation
uses the unconstrained value because its result is bounded plain text. This is
still one model boundary and one Agent/Runner/ReAct loop. A payload observer
rejects an oversized request or exact credential reflection and otherwise
returns the Eino-generated bytes unchanged. The guarded transport wraps the
response body before Eino reads it to enforce raw wire-byte, data-record, and
record-size limits. A response-chunk observer rejects ambiguous choice
envelopes without decoding a second provider protocol. Before assembly,
Kupilot accepts only Eino's paired request-ID and reasoning metadata, bounds
and credential-checks those values, and discards them. Native Ollama's exact
zero-valued intermediate usage containers are removed before this common
validation; terminal measured usage remains. After Eino assembles the stream,
Kupilot validates the message shape and finish reason and applies
project policy. During that same read-once drain, the passive answer projector
may receive validated `Content` strings. It exposes neither raw provider chunks
nor Eino values outside the adapter.

Construction validates the effective profile and provider credential policy locally, clones an
independent HTTP transport, and performs no network request. A successfully
constructed adapter owns the opaque credential. Application owns interactive
replacement: it cancels and joins an active run, builds one replacement before
swapping, and then closes the prior adapter. Close rejects new requests,
releases idle connections, and destroys the owned credential. There is no
package-global client or credential.

Kupilot installs no Eino global callbacks. Each model call replaces any
caller-provided callback context before giving data to the component. The
component performs no application retry, fallback, tracing, or persistence.
Every returned Eino stream and its tracked HTTP response body are closed on all
terminal paths.

## Implemented named model configuration

The current ordinary typed `ModelConfiguration` contains only validated,
non-sensitive values:

- The fixed selected provider kind `openai` or `ollama`.
- One explicit profile name and fixed consumer role: required `agent` or
  optional `approval_reviewer`.
- One canonical endpoint base URL and its exact canonical origin.
- A user-configured model identifier.
- The fixed `runtime` credential-source marker for `openai`, or `none` for
  native `ollama`; never the selected source or key value.
- Temperature from 0 through 0.2, an optional positive endpoint-evidenced
  output-token limit, and a configured request timeout no greater than 900
  seconds. Without endpoint evidence, Kupilot omits the token parameter and
  relies on independent output-byte, stream, call, time, and cost-unit safety
  limits. The immutable run profile and remaining run time may impose a shorter
  deadline.
- A fixed response format of `prompt` or explicitly endpoint-proved
  `json_object`. The latter constrains structured Agent or Reviewer output but
  never Agent-summary prose.
- Required streaming and structured Tool-calling flags for `agent`; fixed
  non-streaming and Tool-free flags for `approval_reviewer`.
- The fixed transport policy: verified HTTPS or explicit loopback HTTP for
  `openai`; explicit loopback HTTP and no redirect for `ollama`.

The model request contains no endpoint, origin, credential, Context, Namespace,
deadline, redirect setting, arbitrary Tool, or hard-limit override. Every model
request made for Agent investigation carries the complete current frozen
catalog of exactly fourteen strict Tool specifications. Summary and Reviewer
requests are non-streaming and Tool-free. Capabilities outside this versioned,
code-owned catalog remain unavailable until their exact schema, policy, wiring,
and tests are implemented. "Strict" describes Kupilot's closed JSON Schemas and
local binder; it does not require a provider-specific strict-output flag.

## Implemented OpenAI Chat Completions wire profile

For `provider_kind: openai`, the configured endpoint is a base URL. The
accepted request target is:

```text
POST {configured-endpoint}/chat/completions
```

The request uses the fields emitted by the pinned Eino ChatModel and asks for
one streamed choice. The required effective values are the configured `model`,
bounded conversation `messages`, fourteen function `tools`, `stream: true`,
`stream_options.include_usage: true`, and the bounded `temperature`.
When the exact profile selects `json_object`, the request also contains
`response_format: {"type":"json_object"}`. Prompt-only profiles omit that
field. The selection is frozen configuration rather than endpoint inference.
`max_tokens` is present only when typed configuration explicitly sets
`models.agent.max_output_tokens` from endpoint evidence. When configuration sets
`models.agent.reasoning_effort: none`, Eino also emits
`reasoning_effort: "none"`; otherwise that field is absent. The adapter never
infers it from a model name or retries based on endpoint error text. Temperature
is the one fixed, adapter-owned Eino `ExtraFields` entry: its value is the
validated configuration scalar, not user-provided extension data. This avoids
the pinned downstream client applying OpenAI-specific restrictions based only
on a `gpt-5` identifier before an OpenAI-compatible endpoint can evaluate the
request. Eino still serializes the body, and Kupilot does not rewrite it. Tool
definitions use `type: "function"`, a fixed name and description, and the
code-owned closed JSON object schema. The pinned Eino API does not emit the
provider-specific `strict` member, and compatibility does not depend on it.
Every property at each object level is included in `required`, and every object
sets `additionalProperties: false`. Model-visible fields that have local
defaults are required but nullable; `null` is canonicalized to the code-defined
default before runtime authorization. Schema keywords outside the accepted
Structured Outputs subset are not sent. Runtime validation independently
enforces length, value, duplicate-item, scope, and hard-budget constraints.

The response must have the `text/event-stream` media type and use single-line
SSE `data:` records. Empty lines and SSE comment lines are allowed. Each JSON
chunk may provide:

- Choice index zero with `delta.content` text. Content may precede or accompany
  indexed Tool fragments in the same response as described below.
- Choice index zero with bounded reasoning text that the pinned Eino component
  exposes through matching `ReasoningContent` and `reasoning-content` metadata.
  Kupilot validates and discards it before assembly; it is never answer text,
  conversation history, Tool authority, Evidence, Diagnosis, or diagnostics.
- Choice index zero with indexed `delta.tool_calls` fragments. The only
  supported Tool-call type is `function`. The identifier, type, and function
  name are atomic for an index; function arguments may be fragmented.
  Fragments for different bounded indexes may interleave, and Eino preserves
  argument arrival order within each index.
- One supported `finish_reason`: `stop`, `tool_calls`, or `length`.
- An optional final usage chunk with empty `choices`.

A choice-index-zero assistant role marker or otherwise empty delta before the
finish reason is a bounded no-op and cannot authorize a Tool. A reasoning
fragment is also inert: its paired Eino values must match, it counts toward the
combined assistant byte ceiling, and it is cleared before Eino assembles the
response. A `stop` response yields validated final text; `length` yields a
local budget stop. For a `tool_calls` response, any assembled commentary is
discarded and only complete indexed Tool calls proceed to strict binding.
Every Tool index must remain in range and the completed index set must be
contiguous from zero.

For visible provisional output, the final protocol places `answer_markdown`
first. Its incremental JSON string decoder supports escaped characters and
UTF-16 surrogate pairs split across content chunks. Before each provisional
event, the adapter checks the exact model credential across chunk boundaries,
normalizes split terminal controls, applies the fixed sensitive-value policy,
enforces answer and run-wide event ceilings, and verifies the immutable run
scope. Raw envelope syntax, citation metadata, proposed actions, reasoning
content, and provider metadata are never provisional answer text.

`data: [DONE]` is accepted after a supported finish reason. EOF is also accepted
after a supported finish reason, so usage and `[DONE]` are optional. EOF before
a finish reason, data after a finish reason other than one optional usage chunk,
duplicate usage, duplicate terminal state, an invalid or non-contiguous Tool
index, a nonzero choice index, or malformed JSON is rejected. A response
containing Tool fragments but terminating with `stop` or `length` is also
rejected.

The response header `X-Request-ID` and Eino's decoded response identifier are
optional. When present, they are bounded and credential-checked inside the
adapter, then discarded from the policy-bearing message. Other response
headers and raw response identifiers are not part of the internal contract.
Raw chunks, headers, request or response bodies, usage objects, and provider
objects never become Application or Domain metadata. Only the project-owned
safe provisional text event may cross into Application, and it is never Domain
state, a successful result, or durable content.

## Implemented native Ollama wire profile

For `provider_kind: ollama`, the configured endpoint is the explicit loopback
server base. It contains no `/v1` compatibility suffix and the accepted request
target is exactly:

```text
POST {configured-endpoint}/api/chat
```

Eino's native Ollama component owns serialization and decoding. The request
contains the configured model, bounded messages, fixed Tool schemas, native
`stream`, native `format`, and bounded `options`. Kupilot maps the configured
temperature and an explicitly endpoint-evidenced positive output limit to the
native options. `json_object` selects Ollama's fixed JSON format; `prompt`
omits it. Unsupported model or format behavior fails the single request.
An omitted `reasoning_effort` also omits native `think`; explicit `none` sends
`think: false`. Interactive switching to Ollama chooses the omitted form rather
than carrying an OpenAI-specific explicit disable value into the new provider.
The adapter does not select a value from the model name or retry a response
whose Tool behavior is incompatible with the explicit setting.

Streaming requires `application/x-ndjson` with one bounded JSON record per
line. Non-streaming summary or Reviewer generation requires
`application/json`. Each native response must decode to an assistant message;
intermediate records may contain content, reasoning, or complete Tool calls,
and the terminal record must carry an admitted done reason. The native
component exposes usage containers on every record. Kupilot removes only an
exact all-zero container before terminal state and accepts measured usage only
at terminal state.

Ollama Tool calls carry a function name and complete JSON argument object but
no OpenAI ID or delta index. Within the current model request, Kupilot assigns
each decoded call its contiguous arrival ordinal and a deterministic bounded
identifier derived from the request ID and ordinal. Duplicate ordinals,
malformed arguments, partial calls, over-limit calls, a Tool after terminal
state, and mismatched finish state remain invalid. The synthetic identifier is
used only for the existing Eino/project Tool-result pairing; it creates no
Evidence, permission, execution, replay, or continuation authority.

Native Ollama is restricted to explicit loopback HTTP and
`credential_ref: none`. The transport rejects Authorization, URL query,
userinfo, fragment, redirect, an endpoint-selected through `OLLAMA_HOST`, or
an authentication query introduced through ambient Ollama settings before
network I/O. Kupilot performs no model discovery, server start, model pull,
protocol fallback, or automatic resend.

The initial production-call-graph scan found GO-2026-5018 reachable through
the native client's imported SSH helper at `golang.org/x/crypto` v0.44.0.
Kupilot therefore pins fixed v0.52.0 plus its minimum compatible `x/net`
v0.54.0, `x/term` v0.43.0, and `x/text` v0.37.0 requirements. All retain the
repository's Go 1.25 minimum. The project vulnerability gate must remain
clean; the fact that Kupilot does not configure an SSH transport is not used to
waive a reachable dependency finding.

## Implemented Agent capability validation strategy

Adapter construction is deliberately network-free and does not send a
speculative capability probe. After Application admits a transfer, the first
bounded Eino model request validates the complete required wire profile. An
incompatible media type, stream shape, Tool-call behavior, or finish
state returns a stable `unsupported` or `invalid_external_response` failure.
Kupilot does not retry that failure automatically, downgrade to non-streaming or
prose-parsed Tools, route to another origin, or retain partial output as a
successful result.

## Implemented Agent capability matrix

<!-- markdownlint-disable MD013 -->

| Behavior | Requirement |
| --- | --- |
| OpenAI Chat Completions JSON request | Required for `openai` |
| Native Ollama `/api/chat` request | Required for `ollama` |
| SSE streaming | Required for `openai` |
| NDJSON streaming | Required for `ollama` |
| Structured function Tool calls | Required |
| Strict JSON object Tool schemas | Required |
| Indexed, fragmented Tool arguments | Required from `openai`; distinct indexes may interleave |
| Complete native Tool argument object | Required from `ollama` |
| Tool-call identifier and index | Provider-supplied for `openai`; deterministic request-bound adapter values for `ollama` |
| Provider-specific `strict` Tool flag | Not required and not used as an authority boundary |
| Tool-argument whitespace and key order | Valid bounded JSON object accepted; canonicalized after strict binding |
| Commentary accompanying a Tool selection | Accepted only with `tool_calls`; bounded and discarded |
| Eino-recognized reasoning content | Accepted only as matching, bounded metadata; credential-checked and discarded before assembly |
| Usage chunk | Optional |
| `X-Request-ID` response header | Optional |
| `[DONE]` after a finish reason | Optional; terminal EOF is accepted |
| Response-format control | Not required and not used as a safety boundary |
| Non-streaming content response | Unsupported for Agent requests |
| Multiple response choices | Unsupported |
| Multiline SSE data records or provider-specific event types | Unsupported |
| Tool selection encoded in prose or fenced JSON | Unsupported |
| Responses API | Unsupported |
| Provider auto-detection, fallback, or routing | Unsupported |
| Anthropic-, Gemini-, or other provider-specific protocols | Unsupported |

<!-- markdownlint-enable MD013 -->

Indexed Tool-call fragments do not imply concurrent Tool execution. Agent
runtime policy remains serial and applies the fixed call and step budgets.

## Implemented fixed safety limits

All values in this table are measured locally by the current implementation. A
server-side limit does not replace them, and current configuration may tighten
but cannot increase them. They remain useful regression evidence but are not
automatically the `v0.5` limits for another role, model, endpoint, capability,
or summarization call.

<!-- markdownlint-disable MD013 -->

| Boundary | Maximum |
| --- | ---: |
| Serialized JSON request body | 256 KiB |
| Structurally admitted messages per Agent conversation | 4,418; Eino summarization triggers much earlier when context exceeds 160 messages or the 128 KiB content-resource threshold |
| Eligible durable Session messages selected before translation | 4,096 and 4 MiB in committed order |
| Eligible durable recent tail after summarization | At least 16 user/assistant Messages and at most 25 so the cut remains on a complete-run boundary; current-run Tool-call/Tool-result pairs remain Eino-managed after the cut and outside durable coverage |
| Retained final assistant answer representation | Complete current strict response schema 4 envelope containing the validated Markdown answer, empty Evidence/action/limitation/question arrays, and the current outcome members; raw model traffic is never replayed |
| Durable safe summary | 16 KiB plus exact coverage metadata; no raw Eino state or Tool transcript |
| System, user, or Tool content in one input message | 64 KiB |
| One assembled assistant response, including discarded reasoning | 128 KiB |
| Tool specifications per Agent investigation request | Exactly 14; summary and Reviewer requests carry none |
| One strict Tool input schema | 16 KiB |
| One assembled Tool argument object | 8 KiB |
| One Tool-call identifier | 256 bytes |
| Total response-stream wire bytes | 8 MiB |
| One SSE data record, NDJSON record, or decoded Eino chunk | 64 KiB |
| SSE or NDJSON records or decoded Eino chunks | 32,768, whichever is reached first |
| Discarded HTTP error-body read | 4 KiB |
| Configured output tokens | No universal default; the request field is omitted unless the selected endpoint has exact evidence |
| One model request | At most 900 seconds, further capped by the selected profile and owning AgentRun deadline |

<!-- markdownlint-enable MD013 -->

The request byte limit is checked on Eino's serialized JSON before HTTP I/O;
OpenAI also uses its Eino request modifier for an earlier identical check.
Stream byte, record, chunk, and Tool-argument limits include
partial, malformed, error, and terminal paths. Reaching a fixed byte or event
limit returns `budget_exhausted`; discarded bytes are never included in an
error, log, event, or persistence value.

The stream ceilings are independent finite local resource bounds. They are not
derived from a token-to-byte or token-to-record estimate and do not promise any
provider fragmentation behavior. The 64 KiB record ceiling and 128 KiB
assembled-assistant ceiling remain independent and narrower.

Finite request, stream, message, response, time, call, byte, and cost ceilings
are reserved independently for Agent, Reviewer, and Agent-summary calls.
Endpoint-exact token and monetary cost evidence is still not available, so the
implementation makes no context-window or precise-token claim. Missing token
evidence does not permit an unlimited request; conservative byte, call, output,
and wall-time limits apply.

## Safe error mapping

Raw HTTP, SSE, SDK, Eino, redirect, and response-body errors end at the adapter
boundary. `ModelError` exposes only a stable class, code-defined operation and
message, conservative retryability, and a local bounded correlation identifier.
Retryability is classification metadata; it never schedules a retry or expands
the remaining run budget.

<!-- markdownlint-disable MD013 -->

| Condition | Stable class | Retryable classification |
| --- | --- | --- |
| HTTP 401 | `authentication_failed` | No |
| HTTP 403 | `permission_denied` | No |
| HTTP 429 | `rate_limited` | Yes, subject to fixed runtime policy |
| HTTP 5xx or transport unavailability | `unavailable` | Yes, subject to fixed runtime policy |
| Other HTTP status or unsupported media/finish behavior | `unsupported` | No |
| Malformed, ambiguous, incomplete, or reordered stream | `invalid_external_response` | No |
| Request, stream, event, or argument byte limit | `budget_exhausted` | No |
| Owning Context cancellation | `cancelled` | No |
| Owning or per-request deadline | `timeout` | No |
| Cross-origin redirect | `policy_denied` | No |

<!-- markdownlint-enable MD013 -->

An endpoint error body is bounded and discarded. Its text, headers, URL, and raw
cause cannot enter the safe error. The guarded transport records an observed
HTTP status before SDK decoding. Cancellation and fixed policy failures take
precedence; otherwise that status controls classification even if the SDK loses
its typed error wrapper. A generic SDK failure after an accepted HTTP 200 SSE
response is `invalid_external_response`, not `unavailable`.

## Endpoint, credential, and logging rules

The endpoint comes only from typed user configuration. Model output, Tool
arguments, messages, resumed history, and Kubernetes content cannot change it.
HTTPS uses normal certificate and hostname verification. Plain HTTP is accepted
only for an explicit loopback endpoint. User information, query parameters,
fragments, insecure TLS overrides, and cross-origin redirects are rejected.
Private HTTPS endpoints are supported only for `openai` and only when their
certificate chain and hostname validate against the process trust store. At
most three same-origin OpenAI redirects are followed, and only when the
redirect preserves the authenticated POST and body; method-changing or
otherwise ambiguous redirects are rejected. Native Ollama is loopback HTTP
only and rejects every redirect.

Each OpenAI model API key is a role-owned transport-only credential extracted from
masked Agent setup, an optional named-profile plaintext file field, or the
role's one-shot environment source. It is never an ordinary typed configuration
field. The Eino component
receives a fixed non-secret placeholder; the guarded HTTP transport replaces it
with the real Authorization value only after validating the exact origin,
request method, content type, and bounded body.
The placeholder is restored before redirect processing, and Authorization is
attached only to the validated origin. A dedicated extractor removes the
optional role `api_key` fields before the strict typed decoder and ordinary
configuration object see them. A key is never a Domain value, model message, request body, Application
event, rendered TUI or history value, safe error, ordinary log field, callback,
audit field, SQLite value, or child environment entry. Request and logger
capture tests may inspect a generated synthetic value in memory, but logs never
record Authorization or request bodies.
The adapter also fails closed before a request or decoded Eino message can
carry the transport credential as configuration, content, metadata, text, or
Tool-call data. Endpoint error bodies are read only to the fixed limit and are
never decoded into a safe error or metadata value. Default logs discard them.
Explicit sensitive diagnostics may retain only the credential-redacted prefix
documented by ADR-0036.

Native Ollama requires `credential_ref: none`. Any file or environment API key
conflicts with that profile, and the transport rejects Authorization before
network I/O. The guarded boundary still applies the same bounded sensitive-
value processing to native content and safe diagnostics without inventing a
credential.

By default, the fixed local `model_request` log event records only the local
request ID, operation, phase, outcome, stable class and code, retryability,
observed HTTP status, fixed cause category, and a sink-generated project-
function call chain. The chain is limited to 32 names and 512 bytes and contains
no source file, line, argument, local value, dependency frame, or raw error.
Invalid project request snapshots still fail before HTTP and before this log
event.

With `logging.sensitive_diagnostics: true`, terminal model failures may also
record the endpoint, model, bounded formatted error chain, bounded failed-
response prefix, and bounded Go call stack. These fields never affect the safe
classification or runtime policy and remain excluded from Application, Agent,
TUI, audit, SQLite, and model content.

## Deterministic compatibility fixtures

The fixtures under `testdata/model` are the compatibility oracle. They use only
synthetic English content and loopback `httptest` servers.

<!-- markdownlint-disable MD013 -->

| Fixture or route | Contract exercised |
| --- | --- |
| `normal.sse` | Ordered decoded-content observation, optional request metadata and usage, finish reason, `[DONE]` |
| `tool-call-fragments.sse` | Atomic Tool identity with ordered argument fragments |
| `interleaved-tool-calls.sse` | Atomic identities with interleaved indexed argument fragments |
| `noncanonical-tool-arguments.sse` | Valid non-canonical Tool JSON passed to strict local binding |
| `commentary-tool-call.sse` | Bounded commentary accompanying a Tool selection |
| `reasoning-content.sse` | Bounded Eino reasoning metadata checked and discarded before final text assembly |
| `no-usage-eof.sse` | Optional usage and terminal EOF after finish reason |
| `reasoning-none` route | Explicit `reasoning_effort: "none"` admission before a valid stream |
| Response-format request recorder | Explicit `json_object` serialization for structured output, omission for `prompt` and Agent-summary requests, and one request with no fallback |
| `error-400.json` | Unsupported compatibility classification and generic-SDK status fallback |
| `error-401.json` | Authentication classification and error-body confinement |
| `error-429.json` | Rate-limit classification and bounded retry metadata |
| `error-500.json` | Unavailable classification and error-body confinement |
| `malformed.sse` | Partial text followed by one invalid-response terminal error |
| `stream-limits.json` | One-over event, total-stream, and event-count limits |
| Blocking cancel and timeout routes | Context propagation and bounded termination |
| Cross-origin redirect route | Zero target requests and no Authorization forwarding |
| Verified private HTTPS and same-origin redirect routes | Normal certificate verification and authenticated POST preservation |
| Generated credential, endpoint-error, and caller-callback canaries | Header-only credential use and safe sink confinement |
| Tracking response bodies and first-use incompatibility | Closure on every terminal path and no probe, retry, downgrade, or fallback |
| Fragmented final-envelope integration route | Incremental answer-only projection, UI coalescing, final replacement, and no envelope metadata disclosure |
| Scripted steer boundary routes | Summary-before-steer order, durable commit before model I/O, exact-once active input, intact Tool pairs, and zero model calls after a failed barrier |
| Current-schema history routes | Complete schema 4 retained assistant representation and a later strict final without retired three-member imitation |
| Native Ollama request recorder | Exact `/api/chat`, no Authorization, NDJSON/JSON media, bounded native format/options, deterministic Tool-call pairing, usage normalization, and no redirect or fallback |

<!-- markdownlint-enable MD013 -->

These fixtures define protocol compatibility, not model quality, prompt
obedience, or suitability of any particular hosted service.

The deterministic fixture matrix now covers role selection, same- and
different-origin profiles, independent credentials and consent, strict
non-streaming no-Tool Reviewer responses, malformed/timeout/cancelled review,
safe Session context ordering, current-question-once, ADK summarization,
coverage/recent-tail integrity, and proof of no fallback or cross-origin retry.
They also cover pending, committing, committed, rejected, unknown, and
recovered input, with no automatic transport retry and no Eino `TurnLoop`.
The current fixtures also cover Reviewer permission routing, durable decisions,
pre-operation audit, and one-attempt execution for the supervised Deployment
restart, all six additional typed remediation operations, exact local
direct-argv and separate-shell actions, and the three default-off
remote-diagnostic handlers. The same fixtures cover review-class Pod-log and
optional data-source supervision, including preflight denial, target mutation,
outcome-audit failure, and zero source calls without consumed authority. Human
and Reviewer delivery is reachable through the inline supervision path;
deterministic tests establish the zero-call denial boundary but do not claim
universal endpoint or cluster compatibility.

Deterministic CI remains the required protocol and safety proof. Opt-in tagged
live integration may establish compatibility only for the exact endpoint,
model, dependency, and profile tested. Model evaluation separately measures
Agent answer quality and Reviewer approval, denial, escalation, latency, and
cost; neither evidence level replaces deterministic CI.

## Manual compaction, plan mode, and continuation evidence

Manual compaction calls the same Eino v0.9.19 summarization handler used by
`BeforeModelRewriteState`; it remains non-streaming, Tool-free, one-attempt,
and bound to the `agent` profile's independent summary budget. Plan-only mode
freezes a run mode but uses the same `ChatModelAgent`, `Runner`, ReAct state,
Tool pairing, summary-before-steer handler ordering, and streaming transport.
The adapter supplies only a fixed safe-read Tool subset and validates a strict
bounded plan response.

Before each real Agent endpoint entry, the run-bound Application preflight
verifies exact input sequencing, scope/policy/profile/origin/consent, context
coverage, Tool catalog, storage, budgets, sink, mode, and recovery state, then
emits its content-free projection. New model final output must use strict
response schema 4 and choose exactly one `answer` or `needs_user_input`
outcome. The completeness manifest, clarification bounds, and authoritative
stop reason are validated after Eino assembly without adding another model
role or answer critic.

**Protocol continuation unavailable.** The pinned OpenAI
extension `v0.1.13` and ACL `v0.1.17` issue a complete Chat Completions POST;
the `go-openai` `v0.1.2` SSE reader consumes `data:` records and `[DONE]` but
has no replay event ID, sequence/offset, `Last-Event-ID`, or same-response
reattach operation. Chunk response IDs and `X-Request-ID` do not establish
replay order. Kupilot therefore retains unknown/recovered handling and adds no
continuation, retry, polling, or checkpoint. The native Ollama component
v0.1.9 likewise issues one complete `/api/chat` request and exposes no stable
same-response replay identity or reattach offset.

The deterministic loopback conformance suite covers structured finals, Tool
call identities, event order, usage, cancellation, timeout, malformed,
duplicate and out-of-order stream data, unknown outcome, and no cross-origin
retry. It runs only under tests.

On 2026-09-15, the explicitly authorized compatibility-route suite passed
against Ollama `0.34.0`, `gpt-oss:20b`, and
`http://127.0.0.1:11434/v1` with
`response_format: json_object`. Four bounded calls covered streaming, one fixed
Tool call, a complete schema 2 final, and a later complete schema 2 final after
the current retained-assistant envelope under the full production policy
prompt. The run sent 94,363 request bytes and reported 13,944 input, 506 output,
and 14,450 total tokens. This proves only that exact local protocol observation;
live model-quality evaluation and live cluster integration were not run. This
historical result does not establish the newly admitted native `/api/chat`
path.

On the same date, the separately authorized native suite passed against
Ollama `0.34.0`, `gpt-oss:20b`, and `http://127.0.0.1:11434/api/chat` with
`response_format: json_object` and native `think` omitted. Four bounded calls
covered an unconstrained streaming text response, one native Tool call with a
request-bound local ID, the matching Tool-result turn and complete schema 2
final, and a later complete schema 2 final after the current retained-assistant
envelope under the full production policy prompt. The run sent 74,178 request
bytes and reported 12,714 input, 499 output, and 13,213 total tokens. A separate
single-request observation with explicit `think: false` ended with empty
content and no Tool call; it was not retried and is not counted as a pass. This
evidence is exact-version compatibility only. Live Reviewer evaluation and
live cluster integration were not run.

After ADR-0056, a separately authorized bounded native run on the same date
used Ollama `0.34.0`, `gpt-oss:20b`, and the loopback `/api/chat` route with
synthetic Tool data and zero Kubernetes access. The four-call protocol suite
sent 74,223 request bytes and reported 12,716 input, 462 output, and 13,178
total tokens while validating a Tool result, schema 3 final, and later-turn
schema 3 final. The full Agent then completed schema 3 after one Tool execution;
two model requests sent 105,125 aggregate request bytes. Its final contained a
verified current observation, an exact current-run Evidence reference, and a
runtime-derived claim hash. No repair request, fallback, or retry followed
validation. This is exact local compatibility evidence, not live cluster or
general model-quality evidence.

## References

- [Configuration](configuration.md)
- [Agent Runtime](agent-runtime.md)
- [ADR-0043: Use One Eino Runtime Boundary](adr/0043-use-one-eino-runtime-boundary.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0048: Own Run Steering and Queued Follow-Up Input](adr/0048-own-run-steering-and-queued-follow-up-input.md)
- [ADR-0049: Bound TUI Observability, Planning, Compaction, and Evidence Coverage](adr/0049-bound-tui-observability-planning-compaction-and-evidence-coverage.md)
- [ADR-0052: Use Typed Agent Outcomes, Evidence Integrity, and Preflight](adr/0052-use-typed-agent-outcomes-evidence-integrity-and-preflight.md)
- [ADR-0053: Scale Bounded Runtime Time Profiles for Local Models](adr/0053-scale-bounded-runtime-time-profiles-for-local-models.md)
- [ADR-0054: Preserve Structured Response Compatibility Across Turns](adr/0054-preserve-structured-response-compatibility-across-turns.md)
- [ADR-0055: Use Explicit OpenAI and Native Ollama Provider Kinds](adr/0055-use-explicit-openai-and-native-ollama-provider-kinds.md)
- [Eino releases](https://github.com/cloudwego/eino/releases)

## Deterministic response metadata and failure diagnostics

New final output uses schema 4. Citations contain only claim, claim_type, and
evidence_ids. Runtime derives ordinal, hash, and structural support. Questions
contain kind, prompt, and choices of label-only objects; code supplies local
choice IDs and the visible rendering. Plan wire schema 2 contains
description-only steps. Unknown or retired fields and old wire schemas fail
closed. Unique accepted Evidence references may be reordered locally; duplicate
or unknown references may not be repaired.

See [ADR-0057](adr/0057-derive-response-metadata-and-classify-interaction-failures.md)
and [Interaction Conformance](interaction-conformance.md) for the exact contract
and verification boundaries.

## Observed local structural conformance (2026-09-16)

The tested local target was Ollama **0.34.0**, model **gpt-oss:20b**, using the
pinned native Eino adapter. Each campaign scheduled ten scenarios in each of
three independent rounds, with a three-minute scenario deadline, at most four
model calls and three synthetic Tool calls. Failed scenarios were not repaired
or retried inside a run. No Kubernetes, Reviewer, external model, executor, or
durable conversation store was used by this tagged fixture. Application, real
temporary SQLite, resume, queue and TUI behavior have separate deterministic
composition evidence in [Interaction Conformance](interaction-conformance.md).

The latest campaign completed **24 of 30** scenario assertions. Six runs were
rejected at fixed typed boundaries, with no subsequent invocation. This is not
a three-round all-pass result and is not evidence of semantic answer quality.
It measured 49 model calls, 19 synthetic Tool calls, zero Kubernetes calls, and
2,542,287 request bytes. The provider supplied usage for 48 responses: 461,928
input and 12,638 output tokens. The provider-error response supplied no usage;
its tokens are unavailable, not measured zero. Total command wall time was
340.588 seconds; scenario times below exclude command overhead.

| Round | Scenario | M/T/K | Request bytes | Measured input/output tokens | Seconds | Structural result / exact reason |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | greeting | 1/0/0 | 51,158 | 9460/81 | 19.232 | PASS |
| 1 | later_turn_identity | 1/0/0 | 51,475 | 9521/105 | 3.102 | PASS |
| 1 | one_tool | 2/1/0 | 103,927 | 19282/674 | 18.659 | PASS |
| 1 | list_final | 2/1/0 | 103,925 | 19281/263 | 7.581 | PASS |
| 1 | list_inspect_final | 1/0/0 | 51,332 | 9486/1814 | 42.903 | FAIL / `evidence_reference_unknown` |
| 1 | empty_result | 2/1/0 | 103,483 | 19169/574 | 15.468 | PASS |
| 1 | partial_result | 2/1/0 | 103,923 | 19283/752 | 17.012 | PASS |
| 1 | verified_observation | 2/1/0 | 103,899 | 19277/621 | 14.498 | PASS |
| 1 | retained_history | 2/1/0 | 104,452 | 19386/527 | 12.525 | PASS |
| 1 | clarification | 1/0/0 | 51,242 | 9476/497 | 10.967 | PASS |
| 2 | greeting | 1/0/0 | 51,158 | 9460/84 | 2.123 | PASS |
| 2 | later_turn_identity | 1/0/0 | 51,475 | 9521/99 | 2.567 | PASS |
| 2 | one_tool | 2/1/0 | 103,921 | 19281/276 | 7.181 | PASS |
| 2 | list_final | 1/0/0 | 51,277 | unavailable | 2.656 | FAIL / `provider_reported_failure` |
| 2 | list_inspect_final | 3/2/0 | 158,109 | 29427/591 | 14.678 | PASS |
| 2 | empty_result | 1/0/0 | 51,276 | 9480/67 | 1.791 | FAIL / `tool_call_malformed` |
| 2 | partial_result | 2/1/0 | 103,923 | 19283/546 | 12.598 | PASS |
| 2 | verified_observation | 2/1/0 | 103,899 | 19277/363 | 8.922 | PASS |
| 2 | retained_history | 2/1/0 | 104,454 | 19386/321 | 8.301 | PASS |
| 2 | clarification | 1/0/0 | 51,242 | 9476/417 | 9.284 | PASS |
| 3 | greeting | 1/0/0 | 51,158 | 9460/95 | 2.316 | PASS |
| 3 | later_turn_identity | 1/0/0 | 51,475 | 9521/135 | 3.364 | PASS |
| 3 | one_tool | 2/1/0 | 103,928 | 19282/372 | 9.201 | PASS |
| 3 | list_final | 2/1/0 | 103,933 | 19282/464 | 11.131 | PASS |
| 3 | list_inspect_final | 3/2/0 | 158,130 | 29430/1021 | 25.569 | PASS |
| 3 | empty_result | 2/1/0 | 103,473 | 19168/351 | 11.274 | FAIL / `final_shape_invalid` |
| 3 | partial_result | 2/1/0 | 103,951 | 19287/753 | 21.140 | PASS |
| 3 | verified_observation | 2/1/0 | 103,911 | 19279/464 | 12.906 | PASS |
| 3 | retained_history | 1/0/0 | 51,536 | 9531/143 | 4.293 | FAIL / `tool_call_malformed` |
| 3 | clarification | 1/0/0 | 51,242 | 9476/168 | 5.129 | FAIL / `final_field_unknown` |

Failure stages were `claim_binding` for `evidence_reference_unknown`,
`model_invocation` for `provider_reported_failure`, `tool_selection` for both
`tool_call_malformed` results, and `final_decode` for `final_shape_invalid` and
`final_field_unknown`. The unknown Evidence was declared before any Tool read.
The provider failure was a non-empty native error record under HTTP 200. The
malformed Tool calls invoked no handler. The invalid final after an empty read
and the unknown clarification field supplied no uniquely recoverable accepted
structure. Runtime did not guess missing intent, references, or fields. Actual
response bytes were not retained, so these observations do not identify a raw
field value or explain the provider's internal failure.

Two earlier fixed campaigns are retained as distinct observations, not hidden
retries or replacement results:

| Campaign | Scenario assertions | M/T/K | Request bytes | Wall seconds | Measurement limits |
| --- | --- | --- | --- | --- | --- |
| Initial | 25/30 PASS | 50/19/0 | 2,594,336 | 388.878 | Usage observer missed the structured-model instance; usage unavailable. Two safe insufficient-Evidence responses did not perform the scenario's requested Tool work. |
| Instrumented | 25/30 PASS | 50/20/0 | 2,593,001 | 390.896 | 48 measured responses: 461,412 input and 15,189 output tokens. Two generic stream failures predated the native error-record observer; their cause cannot be retrospectively attributed. |

Later campaigns followed explicit fixture measurement and typed classification
changes; none changed authority or added runtime retries. The three campaigns
together made 149 model calls and 58 synthetic Tool calls, with zero Kubernetes
calls. The deterministic regression suite is the correctness authority. Real
cluster integration, Reviewer evaluation, broad model quality, and release
evidence: **Not run**.
