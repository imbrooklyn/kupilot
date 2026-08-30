# ADR-0026: Require Informed Consent Before Model Transfer

- Status: Accepted
- Date: 2026-08-08

## Context

Kupilot sends selected local questions and projected Kubernetes observations to
a user-configured cloud model endpoint. Even after credentials and Secret data
are excluded, resource names, status fields, Events, and optional redacted
container output may be sensitive operational data.

A one-time generic acceptance would not tell the user which origin receives
which categories, and it could silently authorize categories added by a later
release. Prompt text cannot provide consent because model output and external
content have no policy authority.

## Decision

Kupilot requires explicit informed consent before the first model-content
transfer. Consent is enforced by Application and the model-egress gate, not by
the TUI, Agent, Prompt, Tool result, or model adapter alone.

Before accepting the decision, the TUI must display:

- The validated canonical endpoint origin or host that will receive content,
  without credentials, user information, query parameters, or headers.
- The exact enabled data-category set and a plain-language description of each
  category, including the user question, safe conversation context, resource
  names and references, projected Kubernetes status fields, projected Events,
  and optional redacted container-output facts when enabled.
- Categories that are never eligible, including kubeconfig material, tokens,
  certificates, private keys, Kubernetes Secret data, model API keys, raw
  objects, raw container output, raw prompts, and raw protocol bodies.
- The consent-policy version and controls for accept, cancel or reject, review,
  and later revocation.

The enabled category catalog is code-defined and versioned. Model, Tool,
Kubernetes, Session, and endpoint content cannot add, rename, or enable a
category.

An accepted consent record binds exactly:

- The consent-policy version.
- A hash of the validated canonical endpoint origin.
- The complete enabled eligible-category set.
- The local decision time and accepted state.

The record contains no endpoint credential, API key, header, user question,
cluster data, model body, or raw endpoint body. The endpoint origin itself
remains typed local configuration rather than consent history.

Any change to the canonical origin, enabled category set, category meaning, or
policy version invalidates the prior consent and requires a new decision before
another model-content request. Adding a category is never covered by an older
broader phrase. Revocation, clearing local state, or deleting the applicable
consent record also stops transfer until consent is granted again.

The final ordered egress check immediately before transport verifies a current
matching consent snapshot. Missing or stale consent returns
`consent_required` and produces zero model-content requests. Rejecting or
cancelling consent has the same zero-request property and never silently creates
a new Session or changes endpoint configuration.

Endpoint validation and a capability probe may occur before consent only when
they contain no user question, conversation, Kubernetes, Tool, Evidence, or
other cluster content. Authentication remains origin-bound under ADR-0010 and
ADR-0035. A redirect never transfers consent or authentication to another
origin.

Valid consent may be persisted under both standard and minimal-persistence
according to the Data Retention Contract. Resuming a Session does not itself
grant consent; Application must use only a current stored tuple that still
matches the active configuration and categories.

## Consequences

Positive consequences:

- The user knows the destination and data classes before local content leaves
  the process.
- New categories and endpoint changes fail closed instead of inheriting stale
  consent.
- Consent behavior is deterministic and independent of model cooperation.
- One typed tuple supports TUI, persistence, egress, and test contracts.

Costs and constraints:

- First use and every relevant policy change require an additional interaction.
- Narrowing or otherwise changing the category set invalidates the old tuple
  and may require confirmation again.
- The project must maintain clear English category descriptions and deterministic
  consent-state snapshots.
- Consent confirms user intent but cannot determine whether organizational
  policy permits the configured external service.

## Alternatives considered

- Permanent global acceptance was rejected because it cannot represent endpoint
  or category changes.
- Consent bound only to a provider label was rejected because one
  OpenAI-compatible profile may target different trust boundaries.
- Per-request confirmation was rejected because an exact versioned category
  tuple provides informed control with less interruption.
- Treating configuration as consent was rejected because entering an endpoint
  does not explain which local data will be sent.
- Asking the Agent to request permission in prose was rejected because text is
  not typed authority and can be influenced by prompt injection.
- Sending the first request and asking afterward was rejected because disclosure
  would already have occurred.

## Security and privacy impact

Consent is necessary but not sufficient for transfer. Source allowlists,
ClusterScope, Tool authorization, projection, normalization, sensitive-value
blocking, size budgets, endpoint validation, same-origin authentication, and
safe errors remain mandatory. Consent never authorizes Secret reads, broader
Kubernetes access, raw persistence, a new Tool, or a write.

The stored origin hash reduces unnecessary endpoint disclosure in SQLite but is
not an anonymization guarantee. The visible endpoint remains in typed local
configuration, and category choices may reveal that certain diagnostic data is
enabled.

## Validation

Deterministic TUI states and English copy must cover review, accept, reject,
cancel, stale, and revoked consent without making the TUI the policy owner.
Using a fake store, fake clock, and request-recording model endpoint, tests must
also cover:

1. First use, accept, reject, cancel, review, revocation, clear-local-state,
   standard persistence, minimal-persistence, and process restart.
2. Individual changes to origin, every enabled category, category meaning, and
   policy version, with zero content requests before renewed consent.
3. Exact reuse of an unchanged valid tuple without manufacturing a new broader
   decision.
4. Capability probes before consent containing no conversation or cluster
   content.
5. Cross-origin redirects, malformed origins, missing configuration, stale
   Session history, and forged TUI, Agent, Tool, or model events.
6. Consent serialization containing only the allowlisted tuple and no endpoint
   credential, raw origin, question, cluster field, model body, or arbitrary
   metadata.
7. Final egress-gate races in which consent is revoked or configuration changes
   after proposal but before transport.

Every deny, stale, reject, cancel, and race case must observe exactly zero
model-content requests and a stable safe state. Every concrete TUI and model
transport integration must preserve these properties.

## Revisit triggers

- A new model provider, endpoint mode, telemetry path, or remote service is
  proposed.
- A new data category or materially different category meaning is proposed.
- Consent must be scoped per Session, per run, per organization, or per
  destination account.
- Legal or organizational requirements demand a different consent record or
  non-user authorization workflow.

## References

- [Security Threat Model](../security.md)
- [Data Retention Contract](../data-retention.md)
- [Privacy Overview](../privacy-overview.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [ADR-0023: Use a Single-Screen Agent-Supervision TUI](0023-use-a-single-screen-agent-supervision-tui.md)
