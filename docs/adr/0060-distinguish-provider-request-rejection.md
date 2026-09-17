# ADR-0060: Distinguish Provider Request Rejection

- Status: Accepted
- Date: 2026-09-17
- Amends: ADR-0027, ADR-0055, and ADR-0057

## Context

An OpenAI-compatible endpoint can reject a configured combination of reasoning
and function calling with HTTP 400 before returning any model output. Mapping
that response to an unsupported stream hides the failing boundary. It does not
establish that the model generated malformed Tools or that final Evidence
validation rejected a valid answer.

## Decision

HTTP 400 and 422 map to the fixed model code `model_request_rejected`, the
existing `unsupported` class, and interaction reason
`provider_request_rejected` at `model_invocation`. The fixed safe message asks
the user to check model profile settings. Classification uses the observed
HTTP status, never provider error text. Authentication, permission, throttling,
server failures, unsupported media, and invalid streams keep their distinct
existing rules. Cancellation, deadlines, and local safety failures retain
precedence over HTTP status.

Reasoning effort remains explicit configuration: omitted or `none`. Kupilot
does not infer a setting from a model name, negotiate capabilities, change
configuration at runtime, or retry a rejected request. A user-authorized
configuration correction may be evaluated in an independent bounded test.
No model request, Tool, Evidence, action, approval, or executor gains authority.
Failure commits no successful assistant answer and never drains queued input.

The existing Eino ChatModelAgent, Runner, transport, and durable conversation
store remain the only implementations. Raw errors and responses stay out of
TUI, ordinary logs, SQLite, and exports. No new persistent field is required.

## Validation and evidence

Deterministic HTTP fixtures exercise success, 400, 422, adjacent status classes,
body disposal, cancellation, timeout, safe diagnostics, and exactly one request
with zero Tool calls after rejection. A configured OpenAI opt-in suite exercises
the full Agent with synthetic Evidence, retained history, and typed clarification.
It reports fixed outcomes, calls, request bytes, measured usage, and elapsed
time without persisting traffic or accessing Kubernetes or Reviewer services.

Live results apply only to the exact configured target and settings. They do not
prove general model quality or real-cluster behavior. Cost estimates use an
explicit reference tariff and provider usage; they are not a provider invoice.

## References

- [Model Compatibility](../model-compatibility.md)
- [Configuration](../configuration.md)
- [Interaction Conformance](../interaction-conformance.md)
