# Kupilot Glossary

These terms are canonical public product language.

## AgentRun

One bounded execution created for an operational question. Only one AgentRun is
active at a time. It is bound to an immutable ClusterScope, capability catalog,
consent tuple, and budget profile and is never silently resumed.

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
only.

## ToolInvocation

One audited use of one code-owned structured read capability. It inherits the
AgentRun scope and ceilings, uses strict canonical arguments, and may create
Evidence. It is not a shell command, kubectl invocation, generic API request,
or mutation.

## Proposed action

Descriptive typed output from the Agent. It carries no nonce, digest, target
fingerprint, or executor authority. The current catalog admits only an exact
Deployment restart proposal in the working Namespace.

## Approval

A local, default-reject, 60-second, single-use request created only after fresh
trusted target preparation. It binds the operation and target state through a
versioned digest. Model or TUI prose cannot create it.

## Agent-first

The principle that conversational operational intent and visible bounded Agent
work remain the primary interface. Features help the Agent gather Evidence or
help the user supervise it; they do not create a parallel resource browser,
shell, dashboard, or controller.

See [Product Contract](product.md), [Scope](scope.md), and
[Privacy Overview](privacy-overview.md).
