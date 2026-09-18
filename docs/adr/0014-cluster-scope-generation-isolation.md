# ADR-0014: Isolate Scope and Policy Generations

- Status: Accepted

## Context
A result can arrive after the user changes Context, Namespace, policy or consent.
Cancellation alone cannot stop that result from acquiring current authority.

## Decision
Application freezes the verified Context, working Namespace, namespace-access
policy, scope generation, policy generation, permission profile, model consent,
catalog and budgets into each run.

Advance scope generation before cancelling work on Context, Namespace or
namespace-policy changes. Advance policy generation and invalidate dependent
approvals, reviews, rules and actions before cancelling policy changes. Check
the complete relevant generations before I/O, after every return, at event
acceptance and immediately before an execution attempt.

Cancel and join old work, clear ResourceRef and dependent authority, rebuild or
dispose the client, and discard late results. Namespaced operations satisfy
current or all access within the same Context; cluster-scoped references have
no invented Namespace.

The last verified Context may be saved as a startup candidate. Saved or resumed
scope never restores verification, credentials, clients, generations or action
authority. Verify it independently before use; do not silently fall back.

## Consequences and validation
Boundary revalidation is intentional redundancy against stale asynchronous work.
Fake barriers test switches before, during and after reads, model calls,
approval, audit and execution. Denials assert zero downstream calls and stale
events cannot update delivery. See [Architecture](../architecture.md).
