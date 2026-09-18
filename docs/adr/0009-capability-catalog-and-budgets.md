# ADR-0009: Use a Fixed Capability Catalog and Finite Budgets

- Status: Accepted

## Context
A model needs useful operational capabilities without selecting arbitrary APIs,
commands, output sources, budgets or network destinations.

## Decision
Use a compile-time code-owned version-1 catalog with strict typed Tool schemas.
Catalog admission names the current need, operation, sources/fields/sinks,
permissions, privacy, limits, partial/error behavior, Evidence and verification.
The runtime injects scope and authority; model arguments cannot broaden them.

Admit reviewed typed resource reads, bounded list/query/describe relationships,
Events, non-following logs and local log search, metrics, exact optional
Prometheus/Loki sources, separately controlled sensitive projections, remote
diagnostics, typed remediation and exact default-off local execution as specified
in [Operational Capabilities](../diagnostic-capabilities.md). Secret values,
generic HTTP, arbitrary patch/apply/edit/delete and raw object access remain denied.

Freeze finite budgets before I/O. Reserve atomically for Agent, Reviewer, summary,
Tool, Kubernetes, optional data source, remote/local execution, pages, items,
samples, lines, bytes, streams, repetition and wall/idle time. Per-capability
ceilings remain independent of run totals. Model and Tool output cannot select
or enlarge a profile. Exact token/cost/context limits require pinned component
and selected endpoint evidence, never character-to-token estimates.

Reuse accepted safe reads only when exact run, target, scope, policy, query and
freshness identity match. Reuse cannot restore historical Evidence authority.

## Consequences and validation
Closed capabilities make denial and exact-request tests meaningful. Every new
capability needs complete admission and bounded fixtures, rather than a plugin
or generic client. Test zero calls on denial, one-over limits, atomic reservations,
pagination, truncation, cancellation and stale generations. Budget values live
in [Agent Runtime](../agent-runtime.md), not duplicated ADR tables.
