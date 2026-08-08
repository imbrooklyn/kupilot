# ADR-0010: Support One OpenAI-Compatible Model Origin

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot needs one cloud model for streamed Diagnosis generation and structured
Tool selection. Supporting several provider protocols, automatic failover, or a
broad compatibility matrix would multiply credential handling, capability
detection, error mapping, consent, testing, and data-transfer behavior before
the core product is validated.

The phrase "OpenAI-compatible" is not a precise standard. Endpoints differ in
streaming, Tool schemas, usage reporting, errors, model identifiers, and partial
protocol behavior. KuPilot must not claim compatibility based on a label alone.

## Decision

`v0.1` will support one configured model provider kind,
`openai_compatible`, and one canonical endpoint origin at a time. There is no
provider auto-detection, fallback, routing, load balancing, or simultaneous
multi-provider conversation.

The one compatibility profile uses Chat Completions-style streaming and
structured Tool calls. Response-format and usage fields are optional
capabilities, not requirements. The configuration contains a validated endpoint
origin, configured model identifier, non-secret capability and request settings,
and the API-key source category. The API key itself is not configuration data.
Endpoint and transport rules are:

- `https` with normal certificate and hostname verification is the default and
  may target an explicitly configured public or private endpoint.
- Plain `http` is allowed only for an explicit loopback endpoint.
- User information, query, insecure TLS override, and cross-origin redirect are
  rejected. Authentication is attached only for the validated origin.
- The user sees the endpoint host and eligible data categories before the first
  model-content transfer. Origin or category changes invalidate consent.
- Capability checks contain no cluster or conversation content.

An endpoint is supported only after it passes the model contract in ADR-0022.
"OpenAI-compatible" describes the one adapter profile; it is not a promise that
every endpoint using that description works.

The user must configure a model identifier; KuPilot does not hard-code a cloud
provider default. The precise wire paths, payload fields, streaming event types,
and concrete client API remain behind the adapter and must satisfy the
validation requirements below.

## Consequences

Positive consequences:

- Credential, consent, retry, error, and stream behavior have one reviewed path.
- Tests can define a finite capability contract and deterministic fixtures.
- Session history does not need cross-provider message or Tool semantics.
- Adding another provider remains an explicit product and privacy decision.

Costs and constraints:

- Users whose endpoint fails the required structured Tool or stream contract
  cannot use it in `v0.1` even if basic chat requests work.
- There is no automatic provider fallback during an outage.
- Endpoint-specific differences require adapter validation and safe errors.
- Non-loopback plaintext endpoints remain outside the supported product path.

## Alternatives considered

- Supporting multiple provider SDKs in `v0.1` was rejected because each adds a
  distinct data, credential, stream, and compatibility boundary.
- Automatic protocol detection was rejected because sending probes or content
  to guessed paths can disclose data and produces ambiguous behavior.
- Treating every nominally compatible endpoint as supported was rejected because
  Tool and streaming behavior is not uniform.
- Falling back from structured Tool calls to prompt-parsed text was rejected
  because it would weaken runtime dispatch integrity.

## Security and privacy impact

The endpoint is a user-selected external trust boundary. KuPilot binds informed
consent to its canonical origin and eligible categories, rejects cross-origin
redirects, and never lets model, Kubernetes, Session, or Tool content change it.
The API key is transport-only and governed by ADR-0021.

Eligible cluster data is still sensitive after projection and redaction. A
supported endpoint does not imply that it is appropriate under the user's
organizational policy.

## Validation

A local fake endpoint plus official protocol or SDK documentation must verify:

1. Exact request and stream APIs for text, structured Tool definitions, Tool
   selections, usage, finish reasons, cancellation, and errors.
2. Cross-origin redirect denial and absence of authentication on any different
   origin.
3. Strict decoding and bounded behavior for malformed, duplicated, reordered,
   oversized, and partial stream events.
4. A capability probe with no cluster or conversation content.
5. Safe timeout, throttling, unavailable, authentication, unsupported, and
   malformed-response classifications.
6. Chat Completions-style stream and Tool behavior, with response format and
   usage treated only as optional capabilities.
7. Compatibility with the selected Eino adapter contract without vendor types
   escaping its boundary.

Supported endpoint profiles and exact API mappings remain recorded in
compatibility documentation.

## Revisit triggers

- A second provider is required and has a complete credential, consent, error,
  streaming, Tool, and test contract.
- The accepted endpoint profile can no longer supply strict structured Tool
  events.
- Product requirements admit a protected local endpoint mode through a separate
  threat review.

## References

- [Privacy Overview](../privacy-overview.md)
- [Security Threat Model](../security.md)
- [ADR-0006: Use Eino Behind an Agent Adapter](0006-use-eino-behind-an-agent-adapter.md)
- [ADR-0021: Use Ephemeral Model API Key Sources](0021-use-ephemeral-model-api-key-sources.md)
- [ADR-0022: Require a Chat Completions Streaming Tool Contract](0022-require-a-chat-completions-streaming-tool-contract.md)
