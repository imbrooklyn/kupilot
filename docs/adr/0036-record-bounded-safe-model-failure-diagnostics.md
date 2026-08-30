# ADR-0036: Record Bounded Model Failure Diagnostics

- Status: Accepted
- Date: 2026-08-29
- Amends: ADR-0006, ADR-0017, ADR-0022, ADR-0027

## Context

The TUI must keep model failures concise and safe, but a generic SDK error can
erase the HTTP status already observed by Kupilot's guarded transport. That can
misclassify an unsupported streaming-plus-Tools request as endpoint
unavailability. The previous operational-log allowlist also dropped every
model-adapter lifecycle record, leaving no durable, safe evidence for local
troubleshooting.

Raw provider errors and ordinary stack dumps can contain response bodies,
credentials, endpoint details, user or cluster content, and local filesystem
paths. They are not acceptable default operational data. Local users also need
an explicit, temporary way to retain enough detail to diagnose a provider or
SDK failure that the stable projection cannot explain.

## Decision

The Eino request scaffold leaves optional sampling and output fields unset;
Kupilot's fixed payload modifier remains the sole owner of their actual wire
values. This prevents SDK model-name heuristics from rejecting an otherwise
valid OpenAI-compatible request before HTTP. An optional reasoning-effort value
comes only from typed configuration and is never inferred from a model name or
raw endpoint error. The guarded model transport marks
when an HTTP round trip is entered and records any valid HTTP status from 100
through 599 in request-local state before the SDK maps the response. Context
cancellation and code-defined local policy failures retain precedence.
Otherwise, a non-200 observed status is classified from that status even when
the SDK returns only a generic error. A generic SDK failure after an accepted
`200` streaming response is an invalid external stream, not transport
unavailability. A generic failure before guarded transport entry is a local
compatibility or validation failure; a failure after transport entry but before
a response remains transport unavailability unless a fixed timeout or policy
error applies.

The local operational log admits one additional fixed `model_request` event.
An admitted request may emit an `info` start and terminal success record. A
terminal model failure emits `error`, except cancellation, which emits `warn`.
With the default `logging.sensitive_diagnostics: false`, the allowlisted
diagnostic projection is limited to:

- The local UUIDv7 model-request identifier, fixed operation, phase, and
  outcome.
- Stable safe error class and code, code-defined retryability, and one fixed
  cause category.
- The observed HTTP status when available.
- A sink-generated, function-name-only Kupilot call chain, with an explicit
  truncation flag.

The call chain contains at most 32 project function symbols and 512 bytes. It
contains no file name, line number, program counter, argument, local value,
error text, goroutine dump, runtime frame, or dependency frame. The logging sink
generates it synchronously from the current failure call chain; a caller cannot
supply or override it. Existing limits of 12 validated attributes, 1 MiB per
file, three files, and seven days remain unchanged.

Provider response bodies, SSE data, request bodies, headers, URLs, model or user
content, Kubernetes content, credentials, vendor errors, arbitrary error
chains, and conventional stack dumps remain prohibited in the default mode.

The explicit `logging.sensitive_diagnostics: true` setting adds a bounded
failure-only projection to the same local rotating log. It may contain:

- The configured endpoint and model identifier.
- The concrete Go error type and formatted error chain, capped at 16 KiB.
- At most the first 4 KiB of a non-success provider response body, with an
  explicit truncation flag.
- The current goroutine's Go call stack with file names and line numbers, capped
  at 64 KiB with an explicit truncation flag.

The adapter removes every exact occurrence of its opaque model credential,
normalizes external text, removes unsafe terminal controls, and applies the
fixed sensitive-value block or redaction policy before submitting sensitive
fields to the log handler. Kupilot never deliberately attaches Authorization or
other headers, request bodies, successful responses, SSE chunks, prompts, Tool
data, Kubernetes content, environment snapshots, SQL, or credentials to this
record. The untrusted provider error and error chain may still echo user or
cluster content after fixed sensitive-value handling; the exact model
credential must remain absent. The handler accepts the additional fields only
on terminal `model_request` failures, never from attached logger fields or
another event. Sensitive mode remains subject to the same 1 MiB, three-file,
seven-day rotation limits and is disabled when local logging is disabled.
Startup emits a visible warning while the mode is enabled.

## Consequences

An unsupported gateway, SDK preflight, or model profile is reported accurately
even when the SDK loses its typed error wrapper. In the default mode, local
users can correlate a TUI failure with the request, HTTP class, safe causal
stage, and Kupilot function path without exposing the provider payload or local
source paths.

The symbol-only chain is intentionally less detailed than a Go stack dump. It
cannot explain a provider's private rejection reason, and a chain longer than
the fixed ceiling is marked as truncated. Sensitive mode can explain more of
these failures but deliberately creates a higher-risk local artifact. Users
must enable it explicitly, protect the Home directory, disable it after
diagnosis, and remove retained rotations when they no longer need them.

## Security and privacy impact

The event adds local metadata but no remote telemetry or new network request.
Actual HTTP status and sensitive diagnostic content are not endpoint content
authority and cannot change retry, scope, consent, Tool, Evidence, or execution
policy beyond its fixed adapter classification. A status or symbol chain is
never sent to the model or stored in SQLite or AuditEvents.

The logging handler remains the final allowlist boundary. Unknown events,
fields, values, caller-provided stacks, and over-limit attributes are dropped.
Default-mode canaries must prove that external error text, response bodies, and
local paths do not enter the log. Sensitive-mode canaries must prove that only
the documented bounded fields enter, the exact model credential remains absent,
and no sensitive field changes any runtime decision.

## Validation

Deterministic tests must prove:

1. HTTP 400, authentication, throttling, and 5xx fixtures retain their exact
   stable classes while their bodies remain absent from every sink.
2. A generic SDK error is classified from an already observed HTTP status, a
   generic failure after HTTP 200 is an invalid stream, a pre-transport SDK
   validation failure is unsupported, cancellation wins, and no raw cause is
   retained in the stable failure value.
3. The fixed model event records its safe fields at the configured level and
   includes only a bounded project-symbol chain with an honest truncation flag.
4. Default mode excludes caller-provided errors, bodies, stacks, credentials,
   local paths, and unknown values, and invalid neutral requests still produce
   zero HTTP and log calls.
5. Sensitive mode is off by default, records the bounded endpoint, model,
   error, provider body and full stack fields only for terminal model failures,
   marks truncation, removes an exact reflected credential, applies general
   sensitive-value handling, and removes unsafe terminal controls.

## Revisit triggers

- A second model protocol needs different status or streaming semantics.
- The symbol-only chain is insufficient and a user-controlled diagnostic export
  is proposed.
- Log retention, record size, remote telemetry, or crash reporting changes.

## References

- [Model Compatibility](../model-compatibility.md)
- [Security Threat Model](../security.md)
- [Privacy Overview](../privacy-overview.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0017: Do Not Persist Full Prompts or Raw Outputs](0017-do-not-persist-full-prompts-or-raw-outputs.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
- [ADR-0027: Use Stable Safe Error Classes](0027-use-stable-safe-error-classes.md)
