# KuPilot Glossary

These terms are the canonical product language. Product documentation and future
implementation work should use them consistently rather than introducing near
synonyms.

## AgentRun

One bounded execution created when a user submits one diagnostic question. An
AgentRun has a beginning and terminal state, allows only one active execution at
a time in `v0.1`, and is bound to an immutable ClusterScope. Cancelling or
changing scope terminates the active AgentRun; it is not silently resumed.

## ClusterScope

The verified Kubernetes Context and Namespace boundary, together with the scope
generation used to reject stale work. Every AgentRun receives an immutable
ClusterScope. A ClusterScope never means all Namespaces, and the model cannot
change it through a ToolInvocation.

## Diagnosis

The structured result of an AgentRun. A Diagnosis separates confirmed facts,
hypotheses, missing information, and recommended actions, and identifies the
ClusterScope and observation time. A Diagnosis is not a guaranteed root cause and
is not proof that a recommended action was executed.

## Evidence

A safe, bounded observation produced from a ToolInvocation and traceable to its
source ResourceRef, ClusterScope, and observation time. Evidence records what was
observed; it is not model inference. Only Evidence from the current AgentRun can
support its confirmed facts.

## ResourceRef

A bounded reference to a Kubernetes resource, using its kind and name within the
active ClusterScope and stronger identity information when safely available. A
user-selected ResourceRef is a requested target, not proof that the resource
exists; a read-only Tool must verify it. Direct `v0.1` targets are limited to Pod,
Deployment, ReplicaSet, Job, and Service.

## Session

The local conversation container that can hold messages and multiple completed
AgentRuns across application launches. A Session is not a ClusterScope. Starting
KuPilot without an explicit resume action creates a new Session; resuming history
does not replay an AgentRun or make old Evidence current.

## ToolInvocation

One audited use of one fixed, structured Tool during an AgentRun. A
ToolInvocation inherits the AgentRun ClusterScope, operates under local policy
and hard budgets, and produces a safe result that may become Evidence. It is not
a shell command, kubectl invocation, generic Kubernetes request, or write action
in `v0.1`.

## Agent-first

The product principle that the user's diagnostic intent and the Agent's bounded
Evidence collection remain the primary interaction. Interface features should
help the Agent or help the user supervise it, not replace it with resource
browsing or direct cluster management.

See the [Product Contract](./product.md), [Scope](./scope.md), and
[Privacy Overview](./privacy-overview.md) for the complete public baseline.
