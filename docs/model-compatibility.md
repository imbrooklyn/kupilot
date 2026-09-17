# Model Compatibility Contract

- Status: Accepted, unreleased v0.1.0
- Decision: [ADR-0063](adr/0063-establish-the-unreleased-openai-only-baseline.md)

## Native Eino ownership

Kupilot supports OpenAI through the pinned native Eino components only:
core `v0.9.19`, Chat Completions extension `v0.1.13`, and
`agenticopenai v0.2.2` for Responses. Application directly constructs one
`ChatModelAgent` and `Runner` and owns the sole ReAct loop under
[ADR-0061](adr/0061-use-eino-directly-in-application.md). There is no provider
facade, second Agent, conversation loop, router, repair request, or fallback.
Ollama support, dependencies, request rewriting, native Tool identity synthesis,
NDJSON parsing and live-test targets have been removed.

## Fixed roles and protocols

Configuration has a required `models.agent` slot and optional
`models.approval_reviewer`. Each binds its own endpoint, model and opaque
credential. Role and credential ownership come from the slot; no provider,
name, role, inheritance, credential-reference, streaming or Tool-required
switch is accepted. Reviewer settings and credentials never inherit from Agent.

`api_protocol: chat_completions` is the default and streams native function
calls and content. `api_protocol: responses` uses native non-streaming
Generate because the pinned streaming converter loses encrypted reasoning.
Agent Tools remain the fixed admitted catalog. Reviewer and summary requests
are non-streaming and Tool-free, with independent finite budgets. Summary
reuses the Agent profile and Eino summarization middleware.

Omitted `reasoning_effort` and `temperature` preserve endpoint defaults.
Explicit values are transmitted unchanged. Kupilot never disables reasoning,
changes protocol or sampling after an error, retries, or infers capabilities
from a model name. `response_format: prompt` is the default;
`json_object` is an explicit endpoint capability requirement.

## Runtime boundaries

Eino owns messages, Tool pairing, iteration and summarization. Application owns
Session, generations, consent, policy, budgets, cancellation, event acceptance,
Evidence and execution authority. Domain remains pure project-owned values;
delivery receives only safe Application events. Provider messages remain
ephemeral inside Application's Eino implementation.

Every later explicit question receives the complete eligible, ordered, bounded
safe SQLite history or current-process minimal-mode history. The current
question appears exactly once. Historical assistant text is reconstructed as
the current schema 1 envelope with empty authority-bearing arrays. No raw
traffic, reasoning, Tool transcript, old Evidence or action authority is replayed.
Missing eligible history, corrupt coverage, stale state or failed persistence
blocks model entry. Eino summarization replaces eligible history only after
its independent one-attempt validation and commit barrier.

Provider Tool identifiers and indexes must be supplied by the protocol. Bounded
argument fragments may interleave by index; Eino pairs the complete call and
result. Kupilot neither invents a missing Tool target nor repairs arguments.
Transport guards enforce origin, opaque credentials, cancellation and independent
request/response limits. Typed failure codes never branch on provider prose.

## Implemented OpenAI Chat Completions wire profile

The configured endpoint is a base URL. The
accepted request target is:

```text
POST {configured-endpoint}/chat/completions
```

The request uses the fields emitted by the pinned Eino ChatModel and asks for
one streamed choice. The required effective values are the configured `model`,
bounded conversation `messages`, fourteen function `tools`, `stream: true`,
`stream_options.include_usage: true`, and an explicitly configured `temperature` when present.
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
| SSE streaming | Required for Chat Completions; Responses streaming is not admitted |
| Structured function Tool calls | Required |
| Strict JSON object Tool schemas | Required |
| Indexed, fragmented Tool arguments | Chat Completions only; distinct indexes may interleave |
| Tool-call identifier and index | Provider-supplied and strictly paired by Eino |
| Provider-specific `strict` Tool flag | Not required and not used as an authority boundary |
| Tool-argument whitespace and key order | Valid bounded JSON object accepted; canonicalized after strict binding |
| Commentary accompanying a Tool selection | Accepted only with `tool_calls`; bounded and discarded |
| Eino-recognized reasoning | Chat Completions display-only metadata is checked and discarded; native Responses reasoning remains ephemeral in the same run |
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
automatically the `v0.1.0` limits for another role, model, endpoint, capability,
or summarization call.

<!-- markdownlint-disable MD013 -->

| Boundary | Maximum |
| --- | ---: |
| Serialized JSON request body | 256 KiB |
| Structurally admitted messages per Agent conversation | 4,418; Eino summarization triggers much earlier when context exceeds 160 messages or the 128 KiB content-resource threshold |
| Eligible durable Session messages selected before translation | 4,096 and 4 MiB in committed order |
| Eligible durable recent tail after summarization | At least 16 user/assistant Messages and at most 25 so the cut remains on a complete-run boundary; current-run Tool-call/Tool-result pairs remain Eino-managed after the cut and outside durable coverage |
| Retained final assistant answer representation | Complete current strict response schema 1 envelope containing the validated Markdown answer, empty Evidence/action/limitation/question arrays, and the current outcome members; raw model traffic is never replayed |
| Durable safe summary | 16 KiB plus exact coverage metadata; no raw Eino state or Tool transcript |
| System, user, or Tool content in one input message | 64 KiB |
| One assembled assistant response, including discarded reasoning | 128 KiB |
| Tool specifications per Agent investigation request | Exactly 14; summary and Reviewer requests carry none |
| One strict Tool input schema | 16 KiB |
| One assembled Tool argument object | 8 KiB |
| One Tool-call identifier | 256 bytes |
| Total response-stream wire bytes | 8 MiB |
| One SSE data record or decoded Eino chunk | 64 KiB |
| SSE records or decoded Eino chunks | 32,768, whichever is reached first |
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
Private HTTPS endpoints are supported only when their
certificate chain and hostname validate against the process trust store. At
most three same-origin OpenAI redirects are followed, and only when the
redirect preserves the authenticated POST and body; method-changing or
otherwise ambiguous redirects are rejected.

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
| Current-schema history routes | Complete schema 1 retained assistant representation and a later strict final without retired three-member imitation |

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

## Plan, compaction and evidence

Plan wire schema 1 contains a title, ordered step descriptions, limitations and
Evidence citations. Runtime derives step order. A plan creates no execution
authority. Final response schema 1 derives claim hashes and ordering, source
coverage, clarification identifiers and authoritative stop reasons locally.
Same-run Evidence ownership, freshness, completeness, scope/policy generations,
consent, budgets and persistence still fail closed. See [Agent Runtime](agent-runtime.md)
and the [decision matrix](interaction-conformance.md).

Deterministic fixtures establish request fidelity, boundary handling and local
invariants. Opt-in live endpoint conformance establishes structural behavior
only for the exact tested configuration. Neither proves model semantic quality,
real Kubernetes correctness or release readiness. Historical Ollama campaigns
are superseded development evidence and do not qualify this OpenAI baseline.

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
