# Model Compatibility Contract

This document defines the single model protocol profile accepted by Kupilot
`v0.4`. The profile is intentionally narrower than the broad and inconsistent
use of the term "OpenAI-compatible." Compatibility means passing this contract;
it does not follow from a product label or provider claim.

## Internal boundary

The Agent owns the neutral `Model` consumer port. Domain values contain no HTTP,
SSE, Eino, SDK, or provider types. One synchronous `Model.Stream` call receives
a Context, one bounded `ModelRequest`, and a synchronous event consumer.

A successful call emits ordered events with strictly increasing sequence
numbers and ends with exactly one `completed` event before returning success. A
failed call returns exactly one `ModelError` and emits no `completed` event.
Text or Tool fragments emitted before a failure are partial and cannot become a
durable assistant Message, Tool authorization, Evidence, or Diagnosis.

The neutral event kinds are:

- `metadata`: an optional, validated provider request identifier.
- `text_delta`: bounded assistant text.
- `tool_call_fragment`: one bounded, ordered fragment identified by Tool-call
  index. Identifier, function-name, and argument fragments remain separate.
- `usage`: optional non-negative input, output, and total token counts.
- `completed`: the sole successful terminal event, with `stop`, `tool_calls`, or
  `length` as its finish reason.

Fragments never authorize a Tool. A complete Tool call is accepted only after
ordered assembly, fixed-name validation, canonical strict arguments, the
`tool_calls` finish reason, and the independent Agent runtime checks for schema,
scope, generation, budgets, and dispatch.

## Transport implementation and lifecycle

The production model transport uses
`github.com/cloudwego/eino-ext/components/model/openai` v0.1.13 with
`github.com/cloudwego/eino` v0.9.13. Both the component and its hardened
`net/http` wrapper are contained in `internal/llm/openaicompat`; Eino, provider,
HTTP, SSE, and transport error types do not cross the neutral `Model` port.

The component owns Chat Completions request integration and stream decoding.
Kupilot supplies a fixed request-payload modifier so the serialized body remains
the exact seven-capability contract, and wraps the response body before the component
can read it to enforce raw wire-byte, data-record, and record-size limits. A
component scaffold leaves optional sampling and output fields unset so SDK
model-name heuristics cannot reject the project-owned payload before HTTP. A
response-chunk modifier rejects ambiguous choice envelopes, while the adapter
maps decoded text, indexed Tool-call fragments, finish reasons, and usage into
project-owned events. Kupilot does not maintain a second SSE or provider JSON
decoder.

Construction validates the effective profile and credential locally, clones an
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

## Model configuration

The ordinary typed `ModelConfiguration` contains only validated, non-sensitive
values:

- The fixed provider kind `openai_compatible`.
- One canonical endpoint base URL and its exact canonical origin.
- A user-configured model identifier.
- The fixed `runtime` credential-source marker, never the selected source or
  key value.
- Temperature from 0 through 0.2, a hard output-token limit from 1 through
  8,192, and a configured request timeout no greater than 300 seconds. The
  immutable run profile and remaining run time may impose a shorter deadline.
- Required streaming and structured Tool-calling capability flags.
- The fixed transport policy: normally verified HTTPS, HTTP only on an explicit
  loopback host, and same-origin redirects only.

The model request contains no endpoint, origin, credential, Context, Namespace,
deadline, redirect setting, arbitrary Tool, or hard-limit override. Every model
request carries the complete frozen catalog of exactly seven strict `v0.4` Tool
specifications.

## Chat Completions wire profile

The configured endpoint is a base URL. The accepted request target is:

```text
POST {configured-endpoint}/chat/completions
```

The request uses JSON and asks for one streamed choice. Its supported fields are
the configured `model`, neutral `messages`, seven strict function `tools`,
`stream: true`, `stream_options.include_usage: true`, the bounded
`temperature`, and `max_tokens`. When the typed configuration explicitly sets
`model.reasoning_effort: none`, the request also includes
`reasoning_effort: "none"`; otherwise that field is absent. The adapter never
infers it from a model name or retries based on an endpoint error body. Tool
definitions use `type: "function"`, a fixed name and description, a strict JSON
object schema, and `strict: true`.
Every property at each object level is included in `required`, and every object
sets `additionalProperties: false`. Model-visible fields that have local
defaults are required but nullable; `null` is canonicalized to the code-defined
default before runtime authorization. Schema keywords outside the accepted
Structured Outputs subset are not sent. Runtime validation independently
enforces length, value, duplicate-item, scope, and hard-budget constraints.

The response must have the `text/event-stream` media type and use single-line
SSE `data:` records. Empty lines and SSE comment lines are allowed. Each JSON
chunk may provide:

- Choice index zero with `delta.content` text.
- Choice index zero with indexed `delta.tool_calls` fragments. The only
  supported Tool-call type is `function`. Fragments for different bounded
  indexes may interleave; arrival order within each index is preserved.
- One supported `finish_reason`: `stop`, `tool_calls`, or `length`.
- An optional final usage chunk with empty `choices`.

A choice-index-zero assistant role marker or otherwise empty delta before the
finish reason is a bounded no-op. It emits no neutral event and cannot authorize
a Tool. Every Tool index must remain in range, the completed index set must be
contiguous from zero, and every per-index assembly must validate as one complete
fixed-catalog Tool call before the `tool_calls` finish state is accepted.

`data: [DONE]` is accepted after a supported finish reason. EOF is also accepted
after a supported finish reason, so usage and `[DONE]` are optional. EOF before
a finish reason, data after a finish reason other than one optional usage chunk,
duplicate usage, duplicate terminal state, mixed text and Tool selection,
an invalid or non-contiguous Tool index, a nonzero choice index, or malformed
JSON is rejected.

The response header `X-Request-ID` is optional. When present, its value is
validated and bounded before becoming neutral metadata. Other response headers
and raw response identifiers are not part of the internal contract. Additional
non-authoritative JSON envelope fields may be ignored only when the required
fields remain unambiguous and within all limits.

Only validated final usage and provider request identifiers may be projected
into the existing safe `ModelRequestMetadata`. Raw chunks, headers, request or
response bodies, partial output, and provider objects are never metadata.

## Capability validation strategy

Adapter construction is deliberately network-free and does not send a
speculative capability probe. After Application has admitted the transfer, the
first bounded `Model.Stream` request validates the complete required wire
profile. An incompatible media type, stream shape, Tool-call behavior, or finish
state returns a stable `unsupported` or `invalid_external_response` failure.
Kupilot does not retry that failure automatically, downgrade to non-streaming or
prose-parsed Tools, route to another origin, or retain partial output as a
successful result.

## Capability matrix

<!-- markdownlint-disable MD013 -->

| Behavior | Requirement |
| --- | --- |
| Chat Completions JSON request | Required |
| SSE streaming | Required |
| Structured function Tool calls | Required |
| Strict JSON object Tool schemas | Required |
| Indexed, fragmented Tool arguments | Required; distinct indexes may interleave |
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

## Fixed limits

All limits are measured locally. A server-side limit does not replace them, and
configuration may tighten but cannot increase them.

<!-- markdownlint-disable MD013 -->

| Boundary | Maximum |
| --- | ---: |
| Serialized JSON request body | 256 KiB |
| Messages per request | 322, derived from two initial messages plus the 64-model-call and 256-Tool-call hard ceilings |
| System, user, or Tool content in one neutral input message | 64 KiB |
| One assembled neutral assistant response | 128 KiB |
| Tool specifications per request | Exactly 7 |
| One strict Tool input schema | 16 KiB |
| One assembled Tool argument object | 8 KiB |
| One Tool-call identifier | 256 bytes |
| Total response-stream wire bytes | 256 KiB |
| One decoded transport or neutral event payload | 64 KiB |
| Data-bearing transport or emitted neutral events | 1,024, whichever is reached first |
| Discarded HTTP error-body read | 4 KiB |
| Configured output tokens | 8,192 |
| One model request | At most 300 seconds, further capped by the selected profile and owning AgentRun deadline |

<!-- markdownlint-enable MD013 -->

The request byte limit is checked after deterministic JSON serialization and
before HTTP I/O. Stream byte, event, fragment, and Tool-argument limits include
partial, malformed, error, and terminal paths. Reaching a fixed byte or event
limit returns `budget_exhausted`; discarded bytes are never included in an
error, log, event, or persistence value.

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
Private HTTPS endpoints are supported only when their certificate chain and
hostname validate against the process trust store. At most three same-origin
redirects are followed, and only when the redirect preserves the authenticated
POST and body; method-changing or otherwise ambiguous redirects are rejected.

The model API key is a transport-only credential extracted from masked TUI
input, an optional local plaintext file field, or the one-shot environment
override. It is never an ordinary typed configuration field. The Eino component
receives a fixed non-secret placeholder; the guarded HTTP transport replaces it
with the real Authorization value only after validating the exact origin,
request method, content type, and bounded body.
The placeholder is restored before redirect processing, and Authorization is
attached only to the validated origin. A dedicated extractor removes the
optional `model.api_key` before Viper and the ordinary configuration object see
it. The key is never a Domain value, model message, request body, Application
event, rendered TUI or history value, safe error, ordinary log field, callback,
audit field, SQLite value, or child environment entry. Request and logger
capture tests may inspect a generated synthetic value in memory, but logs never
record Authorization or request bodies.
The adapter also fails closed before a request or neutral event can carry the
transport credential as configuration, content, metadata, text, or Tool-call
data. Endpoint error bodies are read only to the fixed limit and are never
decoded into a safe error or metadata value. Default logs discard them.
Explicit sensitive diagnostics may retain only the credential-redacted prefix
documented by ADR-0036.

By default, the fixed local `model_request` log event records only the local
request ID, operation, phase, outcome, stable class and code, retryability,
observed HTTP status, fixed cause category, and a sink-generated project-
function call chain. The chain is limited to 32 names and 512 bytes and contains
no source file, line, argument, local value, dependency frame, or raw error.
Invalid neutral requests still fail before HTTP and before this log event.

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
| `normal.sse` | Text deltas, optional request metadata and usage, finish reason, `[DONE]` |
| `tool-call-fragments.sse` | Ordered identifier, name, and argument fragments |
| `no-usage-eof.sse` | Optional usage and terminal EOF after finish reason |
| `reasoning-none` route | Explicit `reasoning_effort: "none"` admission before a valid stream |
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

<!-- markdownlint-enable MD013 -->

These fixtures define protocol compatibility, not model quality, prompt
obedience, or suitability of any particular hosted service.
