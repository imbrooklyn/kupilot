# Model Compatibility Contract

## Native Eino ownership

[ADR-0061](adr/0061-use-eino-directly-in-application.md) defines the
current Eino ownership and protocol rules. Application directly composes
Eino ADK; native message types remain private to Application. OpenAI profiles
explicitly select `chat_completions` or `responses`; omission keeps the existing
Chat Completions behavior. There is one Agent/Runner per run, no automatic
protocol selection, retry, fallback, or additional conversation store.

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
selects Eino's native Chat Completions or Responses component explicitly. `ollama` uses Eino's native
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

Agent requests use native structured Tool calls. Chat Completions and Ollama
stream; Responses requires explicit `streaming: false` because the pinned Eino
stream converter loses encrypted reasoning at item completion (ADR-0061).
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

`internal/application` is the sole Eino and model-provider boundary.
Domain, Agent core, Tools, persistence, CLI, and TUI contain no
Eino or provider values. Application directly owns ADK and its native models. The composition root gives this
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
`github.com/cloudwego/eino-ext/components/model/ollama` v0.1.9, and native
`agenticopenai v0.2.2`, with `github.com/cloudwego/eino v0.9.19`. The components,
ReAct runtime, and the
hardened `net/http` wrapper are contained in `internal/application`.

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
`0ebab92e14f26088411dbc440a1ebdc904ccd8a1`; the current pinned ACL is
`v0.1.18-0.20260527084435-846f52bd97c6` and
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
- Structured Tool calling for `agent`; streaming for Chat Completions/Ollama
  and explicit non-streaming for Responses; fixed
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
the configured reasoning-effort field; omission leaves it absent. The adapter never
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
native options. Explicit zero must be present as `options.temperature: 0`.
The pinned SDK omits zero, so the guarded transport restores only this missing
configured scalar, preserves every other request byte, and rechecks the final
request ceiling. A present mismatched, null, or duplicate temperature, missing
nonzero temperature, or invalid options fails before network I/O. This fixed
request correction does not repair provider output or model Tool arguments;
see [ADR-0058](adr/0058-preserve-explicit-native-model-temperature.md).
ADR-0059 additionally restores only bound Tool parameter-schema byte spans
from the validated catalog. The exact bound Tool count, order, identities,
and descriptions must match before any replacement. Missing or invalid
envelopes and an oversized final request fail before I/O. All other bytes are
preserved; no provider output is repaired. See
[ADR-0059](adr/0059-preserve-bound-native-tool-schemas.md).
`json_object` selects Ollama's fixed JSON format; `prompt`
omits it. Unsupported model or format behavior fails the single request.
An omitted `reasoning_effort` also omits native `think`; explicit `none` sends
`think: false`; `low`, `medium`, and `high` send that native thinking level. Interactive switching to Ollama chooses the omitted form rather
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
v0.55.0, `x/term` v0.43.0, and `x/text` v0.39.0 requirements. All retain the
repository's Go 1.25 minimum. The project vulnerability gate must remain
clean; the fact that Kupilot does not configure an SSH transport is not used to
waive a reachable dependency finding.

### Native Tool schema fidelity limitation

The 2026-09-16 deterministic request audit found an independent request defect
in native Eino Ollama v0.1.9 with its v0.1.0 native client. All 14 catalog
schemas are intact before native conversion. The final serialized request
preserves Tool names, descriptions, root required fields, top-level types
(including null), and enums, but loses:

- array item schemas, including `list_resources.filters.items`;
- nested object properties and required fields, including `get_events.resource`;
- `additionalProperties: false`; and
- numeric, string, array-size, and pattern constraints.

Even the flat `get_cluster_overview` schema loses its limit bounds and purpose
length constraints. The audit reports 147 differing constraint paths per
request; this count includes missing parent structures and is not a count of
independent defects. Greeting and retained-query requests have the same losses.

The same fixture confirms explicit temperature 0.1, output limit 2048,
`format: "json"`, absent `think`, ordered retained history, and exactly one
current question. Request sizes are 51,133 and 51,391 bytes. Each audit run made
two scripted model calls and zero live model, Tool-handler, or Kubernetes calls. The
scripted second response confirms the existing safe
`model_invocation/provider_reported_failure` projection without retry; it does
not reproduce an Ollama generation failure.

This is a reproducible request-fidelity defect, not evidence that schema loss
caused an observed incomplete Tool-argument JSON response. Runtime Tool binding
still enforces the original strict schema and grants no additional authority.
The native component's converter copies only each top-level property's type,
description, and enum; the native client's property representation also lacks
nested object and constraint fields. Repairing only `items` would be incomplete.

There is also a provider limitation. Ollama 0.34.0's
[native API types](https://github.com/ollama/ollama/blob/v0.34.0/api/types.go)
represent array items, nested properties, and nested required fields, but do
not represent all catalog keywords such as numeric/length constraints or
`additionalProperties`. Preserving bytes in the SDK alone therefore does not
establish that all constraints reach the model. Runtime validation remains the
authority boundary regardless of what a provider exposes to its model.

ADR-0059 repairs the project request boundary using only the already bound
code-owned parameter-schema spans. The full Agent audit is now an ordinary
deterministic regression and passes for all 14 schemas, with zero differing
constraint paths. Corrected greeting and retained-query requests contain
53,599 and 53,857 bytes. Temperature, format, thinking omission, history, Tool
identities, and all unrelated bytes remain unchanged. Dependency versions are
unchanged. No complete stable upstream repair was available at verification;
the upstream main-branch array fix is neither complete nor a pinned release.

### Bounded native first-call comparison

On 2026-09-17, after that request audit passed, nine independent first-call
measurements used native Eino, Ollama **0.34.0**, and **gpt-oss:20b**, digest
`17052f91a42e97930aa6e28a6c6c06a983e6a58dbb00434885a0cf5313e376f7`.
The fixed settings were temperature **0.1**, output ceiling **2048**,
`format: "json"`, and omitted `think`. Synthetic retained greeting history
preceded one Namespace question. No Home configuration was read or changed.

Three interleaved rounds compared the full initial Agent request, a test-only
reduction to `get_cluster_overview`, and a test-only removal of the final
response protocol section. Mandatory authority instructions and retained
history remained. Each sample made exactly one model call, with no Tool or
Kubernetes execution, no persistence, and no retry. The selected Tool's strict
project binding had to succeed for a structural PASS. This does not validate
a subsequent Tool result, final answer, or complete AgentRun.

| Round | Request variant | Structural result | Exact stage/reason | Bytes | Measured input/output tokens | Wall seconds |
| --- | --- | --- | --- | ---: | --- | ---: |
| 1 | Full | FAIL | `model_invocation/provider_reported_failure` | 53,857 | unavailable | 25.102 |
| 1 | Single Tool | PASS | none | 42,111 | 8,370 / 125 | 16.819 |
| 1 | Without final protocol | FAIL | `tool_selection/tool_call_malformed` | 49,738 | 8,714 / 101 | 16.985 |
| 2 | Full | FAIL | `tool_selection/tool_call_malformed` | 53,857 | 9,500 / 95 | 2.194 |
| 2 | Single Tool | PASS | none | 42,111 | 8,370 / 123 | 3.034 |
| 2 | Without final protocol | PASS | none | 49,738 | 8,714 / 186 | 4.208 |
| 3 | Full | FAIL | `model_invocation/provider_reported_failure` | 53,857 | unavailable | 2.763 |
| 3 | Single Tool | PASS | none | 42,111 | 8,370 / 100 | 2.273 |
| 3 | Without final protocol | PASS | none | 49,738 | 8,714 / 163 | 3.717 |

The suite used **9 model calls, 0 Tool calls, 0 Kubernetes calls**, and
**437,118 request bytes**. Command wall time was **79.250 seconds**; the live
assertion gate was **FAIL** (five of nine selections passed). Both provider
failures were observed in memory as Tool parsing errors with unexpected JSON
end. Only fixed booleans and classifications were emitted; raw traffic and
arguments were not recorded. The two project parameter rejections were not
field-audited after the run, so this evidence cannot identify their exact bad
argument or establish that any missing intent was uniquely recoverable.

Request schema loss is proven independently and corrected, but these results
do not show it caused the earlier parsing failure. The tested provider/model
combination remains unreliable for the full request. Three samples per variant
do not prove that catalog size or protocol text is the cause, that a reduced
request is reliably compatible, or that the model alone caused server parsing
failure. Ollama generation and parser behavior remain inseparable in this
measurement. Provider-discarded schema constraints remain an explicit upstream
limitation, enforced locally rather than emulated.

The project therefore keeps the full production catalog, prompt, strict
arguments, and existing typed failure mapping. No output repair, guessed
parameter, retry, reduced-catalog fallback, configuration change, SDK fork, or
additional model probe follows this result. Deterministic fixtures establish
request correctness and safety; this local comparison establishes only the
listed structural observations. Real Kubernetes, model semantic quality,
remote CI, and release evidence were **Not run** during this local campaign.

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
| OpenAI JSON request | Explicit `chat_completions` or `responses` protocol |
| Native Ollama `/api/chat` request | Required for `ollama` |
| SSE streaming | Required for OpenAI Chat Completions; Responses streaming is not admitted |
| NDJSON streaming | Required for `ollama` |
| Structured function Tool calls | Required |
| Strict JSON object Tool schemas | Required |
| Indexed, fragmented Tool arguments | Chat Completions only; distinct indexes may interleave |
| Complete native Tool argument object | Required from `ollama` |
| Tool-call identifier and index | Provider-supplied for `openai`; deterministic request-bound adapter values for `ollama` |
| Provider-specific `strict` Tool flag | Not required and not used as an authority boundary |
| Tool-argument whitespace and key order | Valid bounded JSON object accepted; canonicalized after strict binding |
| Commentary accompanying a Tool selection | Accepted only with `tool_calls`; bounded and discarded |
| Eino-recognized reasoning | Chat/Ollama display-only metadata is checked and discarded; native Responses reasoning remains ephemeral in the same run |
| Usage chunk | Optional |
| `X-Request-ID` response header | Optional |
| `[DONE]` after a finish reason | Optional; terminal EOF is accepted |
| Response-format control | Not required and not used as a safety boundary |
| Non-streaming content response | Required for native Responses; unsupported for other Agent protocols |
| Multiple response choices | Unsupported |
| Multiline SSE data records or provider-specific event types | Unsupported |
| Tool selection encoded in prose or fenced JSON | Unsupported |
| Responses API | Native Eino AgenticMessage and ADK, explicit configuration, no hosted Tools or remote state |
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
| HTTP 400 or 422: request rejected (`model_request_rejected`) | `unsupported` | No; check the explicit model profile |
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

HTTP request rejection is reported as `model_invocation/provider_request_rejected`,
separately from unsupported response media or stream behavior. It cannot identify
the rejected parameter without endpoint-specific evidence. Kupilot never parses
error prose to change settings or resend a request; see [ADR-0060](adr/0060-distinguish-provider-request-rejection.md).

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

Parameter evidence correction: the campaigns below configured temperature zero,
but the pinned SDK omitted that field from the HTTP request. Effective
temperature depended on server/model defaults and was not measured. These
results do not establish explicit-zero behavior or a benefit from lowering
temperature. Their recorded counts and outcomes remain historical observations;
they are not post-ADR-0058 conformance evidence.

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

## Native Responses dependency and fidelity gate

ADR-0061 admits Application-owned native Eino composition with core `v0.9.19`
and `agenticopenai v0.2.2`. The latter brings OpenAI Go SDK `v3.35.0` and pins
ACL `v0.1.18-0.20260527084435-846f52bd97c6` transitively; this ACL revision is a
pseudo-version, not a stable ACL release. The module graph and checksums remain
explicit in `go.mod` and `go.sum`. Eino and its extensions use Apache-2.0;
OpenAI Go uses Apache-2.0 and the new Azure SDK/tidwall dependencies use MIT.
These dependencies do not enable Azure routing or hosted capabilities.

The recording fixture `TestNativeResponsesAgentToolAndFinal` sends native
reasoning plus a function call, executes one synthetic Tool, and proves that
Eino sends the encrypted reasoning and paired function result in the next
request. It also checks `store: false`, disabled truncation, explicit reasoning,
JSON mode and absence of `previous_response_id`. The separate pinned streaming
fixture demonstrates upstream loss of `encrypted_content`; it is evidence for
denying that configuration, not a passing streaming capability claim.

Responses uses `Generate` and therefore has no provisional text projection.
Failed/incomplete provider statuses, malformed Tools, unsupported content,
invalid Evidence, expired authority, budget exhaustion and persistence failure
remain distinct local denials. Model reasoning does not enter durable history.
The full composition matrix runs both native message protocols with real
SQLite, synthetic Tools and zero Kubernetes I/O, including resume, steer,
queue, cancellation, timeout and stale generations.

### Native Responses endpoint observation

A bounded opt-in run on 2026-09-17 used the configured compatible HTTPS origin
and requested model identifier `gpt-5.6-luna`, native Responses, JSON-object
output, a 2048-output-token ceiling, and omitted temperature. The first seven
cases kept reasoning effort omitted; the eighth case explicitly selected
`medium`. This final suite used the dependency graph with the required
`x/net` and `x/text` security updates. There was no Kubernetes or live Reviewer access. Every Tool
result was synthetic, and raw model/Tool/reasoning content was not retained.

| Scenario | Model / Tool calls | Request bytes | Measured input / output tokens | Wall seconds | Structural result |
| --- | --- | --- | --- | --- | --- |
| Greeting | 1 / 0 | 53,513 | 10,683 / 53 | 4.802 | PASS |
| Later-turn identity | 1 / 0 | 53,879 | 10,744 / 56 | 4.798 | PASS |
| One Tool and current observation | 2 / 1 | 110,245 | 21,748 / 173 | 9.366 | PASS |
| List, Inspect and final | 3 / 2 | 168,872 | 33,183 / 350 | 16.746 | PASS |
| Empty result | 2 / 1 | 109,818 | 21,637 / 251 | 10.548 | PASS |
| Partial result | 2 / 1 | 110,252 | 21,752 / 308 | 11.569 | PASS |
| Typed clarification | 1 / 0 | 53,597 | 10,699 / 139 | 4.976 | PASS |
| Explicit medium reasoning with a Tool | 2 / 1 | 110,679 | 21,802 / 394 | 14.786 | PASS |

The explicit reasoning case reported 52 reasoning tokens and two encrypted
reasoning items. The suite completed in 77.60 seconds: 14 model calls,
6 synthetic Tool calls, 770,855 request bytes, 152,248 input tokens and
1,724 output tokens. Estimated standard token cost was USD 0.032518 using the
[published model prices](https://developers.openai.com/api/docs/models/gpt-5.6-luna),
without cached-input discounts. This is an estimate for the configured service,
not its invoice or proof of its backend model identity.

An earlier corrected campaign also passed the same eight scenarios, with the
explicit reasoning case run separately. It made 14 model calls and 6 synthetic
Tool calls, with measured usage priced at USD 0.032638. The two successful
campaigns therefore total USD 0.065156 in estimated standard token cost. Each
scenario was an explicit independent test; the runtime performed no retry.

Before the configuration correction, two controlled requests with explicit
temperature 0.1 returned HTTP 400, classified as
`model_invocation/provider_request_rejected`. The second observation identified
only fixed parameter-category markers for unsupported temperature. Their usage
was unavailable. The correction made temperature optional; it did not disable
reasoning, retry automatically, or repair model output. These observations
establish compatibility for the tested settings and synthetic tasks, not broad
model quality, real cluster behavior, or release readiness.
