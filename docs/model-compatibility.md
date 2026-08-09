# Model Compatibility Contract

This document defines the single model protocol profile accepted by KuPilot
`v0.1`. The profile is intentionally narrower than the broad and inconsistent
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

## Model configuration

`ModelConfiguration` contains only validated, non-sensitive values:

- The fixed provider kind `openai_compatible`.
- One canonical endpoint base URL and its exact canonical origin.
- A user-configured model identifier.
- The API-key source category `environment`, never the API key value.
- Temperature from 0 through 0.2, a hard output-token limit from 1 through
  8,192, and a request timeout no greater than 45 seconds.
- Required streaming and structured Tool-calling capability flags.
- The fixed transport policy: normally verified HTTPS, HTTP only on an explicit
  loopback host, and same-origin redirects only.

The model request contains no endpoint, origin, credential, Context, Namespace,
deadline, redirect setting, arbitrary Tool, or hard-limit override. Every model
request carries the complete frozen catalog of exactly six strict `v0.1` Tool
specifications.

## Chat Completions wire profile

The configured endpoint is a base URL. The accepted request target is:

```text
POST {configured-endpoint}/chat/completions
```

The request uses JSON and asks for one streamed choice. Its supported fields are
the configured `model`, neutral `messages`, six strict function `tools`,
`stream: true`, `stream_options.include_usage: true`, the bounded
`temperature`, and `max_tokens`. Tool definitions use `type: "function"`, a
fixed name and description, a strict JSON object schema, and `strict: true`.

The response must have the `text/event-stream` media type and use single-line
SSE `data:` records. Empty lines and SSE comment lines are allowed. Each JSON
chunk may provide:

- Choice index zero with `delta.content` text.
- Choice index zero with indexed `delta.tool_calls` fragments. The only
  supported Tool-call type is `function`.
- One supported `finish_reason`: `stop`, `tool_calls`, or `length`.
- An optional final usage chunk with empty `choices`.

`data: [DONE]` is accepted after a supported finish reason. EOF is also accepted
after a supported finish reason, so usage and `[DONE]` are optional. EOF before
a finish reason, data after a finish reason other than one optional usage chunk,
duplicate usage, duplicate terminal state, mixed text and Tool selection,
reordered Tool indexes, a nonzero choice index, or malformed JSON is rejected.

The response header `X-Request-ID` is optional. When present, its value is
validated and bounded before becoming neutral metadata. Other response headers
and raw response identifiers are not part of the internal contract. Additional
non-authoritative JSON envelope fields may be ignored only when the required
fields remain unambiguous and within all limits.

Only validated final usage and provider request identifiers may be projected
into the existing safe `ModelRequestMetadata`. Raw chunks, headers, request or
response bodies, partial output, and provider objects are never metadata.

## Capability matrix

<!-- markdownlint-disable MD013 -->

| Behavior | Requirement |
| --- | --- |
| Chat Completions JSON request | Required |
| SSE streaming | Required |
| Structured function Tool calls | Required |
| Strict JSON object Tool schemas | Required |
| Indexed, fragmented Tool arguments | Required |
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
| Messages per request | 32 |
| Content in one neutral message | 64 KiB |
| Tool specifications per request | Exactly 6 |
| One strict Tool input schema | 16 KiB |
| One assembled Tool argument object | 8 KiB |
| One Tool-call identifier | 256 bytes |
| Total response-stream wire bytes | 256 KiB |
| One decoded transport or neutral event payload | 64 KiB |
| Data-bearing transport or emitted neutral events | 1,024, whichever is reached first |
| Discarded HTTP error-body read | 4 KiB |
| Configured output tokens | 8,192 |
| One model request | 45 seconds and no later than the owning AgentRun deadline |

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
cause cannot enter the safe error.

## Endpoint, credential, and logging rules

The endpoint comes only from typed user configuration. Model output, Tool
arguments, messages, resumed history, and Kubernetes content cannot change it.
HTTPS uses normal certificate and hostname verification. Plain HTTP is accepted
only for an explicit loopback endpoint. User information, query parameters,
fragments, insecure TLS overrides, and cross-origin redirects are rejected.

The model API key is a transport-only credential from the approved one-shot
source. Authorization is attached only to the validated origin. It is never a
configuration field, model message, request body, event, safe error, ordinary
log field, audit field, or persistence value. Request and logger capture tests
may inspect a generated synthetic value in memory, but logs never record
Authorization or request/response bodies.

## Deterministic compatibility fixtures

The fixtures under `testdata/model` are the compatibility oracle. They use only
synthetic English content and loopback `httptest` servers.

<!-- markdownlint-disable MD013 -->

| Fixture or route | Contract exercised |
| --- | --- |
| `normal.sse` | Text deltas, optional request metadata and usage, finish reason, `[DONE]` |
| `tool-call-fragments.sse` | Ordered identifier, name, and argument fragments |
| `no-usage-eof.sse` | Optional usage and terminal EOF after finish reason |
| `error-401.json` | Authentication classification and error-body confinement |
| `error-429.json` | Rate-limit classification and bounded retry metadata |
| `error-500.json` | Unavailable classification and error-body confinement |
| `malformed.sse` | Partial text followed by one invalid-response terminal error |
| `stream-limits.json` | One-over event, total-stream, and event-count limits |
| Blocking cancel and timeout routes | Context propagation and bounded termination |
| Cross-origin redirect route | Zero target requests and no Authorization forwarding |

<!-- markdownlint-enable MD013 -->

These fixtures define protocol compatibility, not model quality, prompt
obedience, or suitability of any particular hosted service.
