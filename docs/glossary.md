# Kupilot Glossary

These terms are canonical public product language.

The checked-in implementation includes the named-model and safe Session-context
runtime slice. Other terms marked as `v0.5` targets describe the Accepted
implementation contract, not current reachability.

## AgentRun

One bounded execution created for an operational question. Only one AgentRun is
active at a time. It is bound to an immutable ClusterScope, capability catalog,
consent tuple, permission policy generation, and budget profile and is never
silently resumed.

## ClusterScope

One verified Kubernetes Context, one visible working Namespace, one immutable
`current` or `all` namespace-access policy, activation time, and generation.
The model cannot change it. `all` permits explicit cross-Namespace reads in the
same Context; it never means another cluster.

## Working Namespace

The Namespace used for default namespaced intent, resource selection, action
targets, and persistent footer display. It is not an implicit all-Namespace
marker and does not falsely label cluster-scoped or explicit cross-Namespace
Evidence.

## Diagnosis

The durable compatibility name for a validated terminal answer and its safe
metadata. The visible answer is free-form Markdown. Citation-backed facts,
gaps, warnings, and typed proposed actions remain separately bounded. A
Diagnosis is not guaranteed causal truth and does not prove an action ran.

## Evidence

A safe bounded observation produced by deterministic Tool handling and bound to
one run, invocation, scope generation, exact ResourceRef, source, and time.
Evidence records what was observed; model prose is interpretation.

## ResourceRef

A bounded reference to an allowlisted Kubernetes resource with API version,
Kind, exact name, and real Namespace semantics. A selected reference is an
input candidate, not proof of existence; a run capability must verify it.

## Session

The local conversation container for messages and completed AgentRuns across
launches. A Session is not live Kubernetes authority. Bare startup creates a
new Session; explicit resume restores safe history and unverified candidates
only. Under the `v0.5` target, every later question receives one ordered,
bounded representation of retained eligible prior same-Session turns; resume
itself performs no external I/O and historic state restores no authority.

## ToolInvocation

One audited use of one code-owned structured read capability. It inherits the
AgentRun scope and ceilings, uses strict canonical arguments, and may create
Evidence. It is not a shell command, kubectl invocation, generic API request,
or mutation.

## Proposed action

Descriptive typed output from the Agent. It carries no nonce, digest, target
fingerprint, or executor authority. The current `v0.4` catalog admits only an
exact Deployment restart proposal. The `v0.5` target admits only the typed P0
operations in the Product Contract, each of which must first become a locally
validated ActionEnvelope.

## Permission profile

A deterministic local routing policy for `safe`, `review`, `critical`, and
`deny` operations. `ask` is the default. A profile does not create technical
capability, grant Kubernetes RBAC, widen scope or consent, lower risk, or
override a hard denial.

## Reviewer

The optional `approval_reviewer` model role. It may return a bounded decision
input only for delegated `review` work. It is not permission authority, cannot
review `critical` work, and receives no executor.

## ActionEnvelope

The immutable versioned local representation of one sensitive or effectful
operation. Its canonical digest binds exact scope, policy, target, typed
parameters or policy-owned executable and argv, effects, limits, expiry, and
verification plan. Model prose, Reviewer rationale, and UI text cannot create
or modify it.

## Approval

A local, default-reject request whose approved state expires after 60 seconds
and is single-use. It is created only after fresh target or executable-policy
preparation and binds one ActionEnvelope through a versioned digest. Model,
Reviewer, or TUI prose cannot create it.

## Agent-first

The principle that conversational operational intent and visible bounded Agent
work remain the primary interface. Features help the Agent gather Evidence or
help the user supervise it; they do not create a parallel resource browser,
shell, dashboard, or controller.

See [Product Contract](product.md), [Scope](scope.md), and
[Privacy Overview](privacy-overview.md).
