# ADR-0046: Use Named Model Roles and Optional Auto-Review

- Status: Accepted
- Date: 2026-09-03
- Supersedes: ADR-0010's single configured origin restriction
- Amends: ADR-0026, ADR-0035, and ADR-0043

## Context

One model origin and one model runtime simplified the first implementation, but
the `v0.5` permission model needs a distinct optional Reviewer. The Agent and
Reviewer can have different quality, latency, cost, credential, and data-
transfer requirements. Treating those calls as fallback choices behind a
provider router would obscure their authority and consent boundaries.

Kupilot needs multiple explicit model profiles without becoming a multi-
provider routing product. It also needs to state clearly that a model review is
an input to deterministic policy, never the source of permission.

This ADR defines the accepted `v0.5` target. The exact configuration migration
and endpoint-specific limits remain evidence-gated and are not implemented by
this documentation change.

## Decision

### Named profiles and consumer roles

Kupilot supports one provider protocol kind, `openai_compatible`, and these
named consumer roles:

- required `agent`, used for the conversational Agent;
- optional `approval_reviewer`, used only for `review`-class permission
  decisions; and
- summarization, which uses a separately budgeted invocation of the `agent`
  profile rather than a predeclared `context_compactor` role.

Each named model profile explicitly binds one consumer role, canonical origin,
model identifier, non-secret protocol settings, finite limits, and an opaque
credential reference. Several profiles may name several explicit origins in
one configuration. Each call uses the one profile selected by its fixed
consumer role; the Agent and Reviewer do not choose endpoints.

There is no provider auto-detection, fallback, provider router, load balancer,
cross-origin retry, quality-based selection, or prompt-parsed Tool call. An
unavailable role fails closed. Ollama may be documented and tested as an
OpenAI-compatible integration target, not as a second provider kind.

`cmd/kupilot` explicitly constructs the Agent and optional Reviewer
dependencies. All Eino, provider, message, stream, Tool, callback, and transport
types remain inside `internal/agent/einoadapter`. Multiple profiles do not
create multiple framework boundaries or a framework-neutral provider facade.

### Reviewer delegation

`approval_reviewer` is available only when configured, consented, within its
independent budget, and admitted by project evaluation. It receives the
smallest bounded projection of explicit user intent, the normalized
`ActionEnvelope`, and code-owned policy facts. By default it receives no raw
Kubernetes object, log, command output, general Session history, or credential.

The Reviewer call is non-streaming, has no Tools, and returns one strict result:
`approve`, `deny`, or `escalate_to_user`, plus a bounded safe rationale.
Malformed output, extra fields, Tool calls, prose-only output, timeout,
cancellation, transport failure, missing consent, stale policy, or exhausted
budget grants no authority and performs no action.

The Reviewer may route only `review` work under `auto-review` or an exact
`custom` rule. It never routes `critical` work, creates Session rules, enables a
capability, changes risk, expands scope or RBAC, bypasses consent, approves a
hard denial, or mints an approval token. A denial cannot be worked around by
the Agent. A human may explicitly approve the same still-fresh envelope once,
or may change the permission profile and create a fresh envelope; stale or
changed input always requires a new decision.

### Credentials and consent

Every profile uses the existing opaque, non-renderable credential boundary.
Credentials never enter ordinary typed configuration values, CLI arguments,
prompts, history, TUI, errors, logs, audit, SQLite, model content, or child
environments. The exact `v0.5` YAML layout, environment names, reference
syntax, one-shot loading, and configuration migration must be derived and
tested in the configuration work; this ADR does not invent a usable schema.

Consent binds the policy version, canonical origin hash, exact model role, and
exact data categories. Consent for the Agent at one origin does not authorize a
Reviewer at another origin. Changing a profile, role binding, origin, category,
or meaning invalidates the affected consent and pending action before any new
content request. HTTPS verification, loopback-only HTTP, userinfo/query denial,
same-origin redirect handling, and authorization confinement apply separately
to every origin.

### Independent finite budgets

Agent calls, Reviewer calls, and Agent-profile summary calls have independent
reservations and `/status` accounting. At minimum they bound input/context and
output tokens, request and stream bytes, stream chunks, calls, wall time, idle
time, per-capability items, estimated or known cost, and the summary or review
sub-budget. A Reviewer or summary call cannot consume or expand investigation
authority.

Exact context windows, input/output token maxima, token-to-byte assumptions,
stream constraints, concurrency, latency, cost ceilings, and compaction
thresholds may be published only for the exact pinned Eino/OpenAI component and
the selected endpoint based on tagged source/tests, protocol fixtures, live
integration where authorized, and model evaluation. `v0.5` does not inherit
the `v0.4` value `8192` as a universal model ceiling. Conservative byte, call,
time, and cost bounds still apply when exact token evidence is unavailable.

## Consequences

Operators may use one model for investigation and another for low-risk review
without implicit routing. Each data transfer has a visible role, destination,
credential, consent tuple, and budget. Reusing one endpoint for both roles is
also explicit rather than a hidden fallback.

Configuration, setup, status, logging, compatibility fixtures, and evaluation
must become role-aware. Auto-review remains unavailable when its role or
evidence is missing; that is a visible fail-closed state, not a reason to use
the Agent as Reviewer.

## Security and privacy impact

Multiple origins increase credential and disclosure risk. Origin canonicalization,
late authorization injection, cross-origin denial, independent consent, exact
role composition, and source minimization remain mandatory. Reviewer rationale
is untrusted, bounded, normalized, and safe for its declared sinks; it cannot
contain or convey execution authority.

`full-access` and human approval derive from local deterministic state, not
from Reviewer confidence. A smaller or cheaper model is acceptable only after
evaluation shows that its use does not weaken required fail-closed behavior.

## Validation

Deterministic tests must cover same-origin and distinct-origin role composition,
credential confinement, independent consent, profile replacement, stale
generation, role-specific request shape, budget isolation, missing Reviewer,
malformed result, Tool-call denial, timeout, cancellation, redirect, and no
fallback. Every denial asserts zero executor calls and, where locally
decidable, zero model calls.

Compatibility uses three evidence levels:

1. deterministic CI fixtures for protocol, policy, cancellation, and sink
   behavior;
2. opt-in tagged live integration for one exact endpoint/profile; and
3. separately reported model evaluation for Agent quality, Reviewer decisions,
   false approval/denial, escalation, latency, and cost.

Live integration and model evaluation do not replace deterministic CI and must
not be generalized to an untested endpoint or model identifier.

## Revisit triggers

- A new consumer role, provider protocol kind, fallback, router, load balancer,
  or cross-origin retry is proposed.
- Reviewer input needs a new data category or raw operational content.
- A Reviewer is proposed for `critical` work or as the authority for a policy
  decision.
- A credential manager, remote configuration service, or durable secret store
  is proposed.
- Tagged dependency or endpoint evidence changes a role's exact limits or wire
  contract.

## References

- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0026: Require Informed Consent Before Model Transfer](0026-require-informed-consent-before-model-transfer.md)
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [Model Compatibility Contract](../model-compatibility.md)
- [OpenAI auto-review](https://learn.chatgpt.com/docs/sandboxing/auto-review)
- [OpenAI agent approvals and security](https://learn.chatgpt.com/docs/agent-approvals-security)
