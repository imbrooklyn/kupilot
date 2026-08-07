# ADR-0004: Do Not Fork k9s

- Status: Accepted
- Date: 2026-08-05

## Context

k9s is an established resource-oriented Kubernetes terminal application. A fork
would provide substantial existing UI and cluster-management behavior, but it
would also make those behaviors, state models, permissions, dependencies, and
upstream changes KuPilot's starting point.

KuPilot has a different product center: one natural-language question, bounded
structured Evidence gathering, and a cautious Diagnosis. Most of a general
resource management application would be outside the accepted scope and would
need to be disabled, removed, audited, or continuously reconciled with
upstream.

## Decision

KuPilot will not fork, embed, or use k9s as its application foundation. It will
build the smallest TUI and application flow required by the Agent-first product
contract.

This decision concerns architecture and product inheritance. It does not
prevent maintainers from studying public interaction patterns or independently
using compatible open-source dependencies under their licenses. Any copied
code would still require a separate need, provenance review, license compliance,
and scope review.

## Consequences

Positive consequences:

- KuPilot starts with the permissions, state, dependencies, and interaction
  surface it actually needs.
- Resource browsing and management behavior cannot become an accidental bypass
  around Application and Tool contracts.
- The project avoids a long-lived fork and the obligation to reconcile a large
  unrelated upstream surface.
- Architecture tests can reason about a small number of explicit dependency
  paths.

Costs and constraints:

- Terminal components, focus behavior, accessibility, and rendering must be
  implemented and tested by KuPilot.
- The project does not inherit mature resource navigation or cluster-management
  features, even where they might appear convenient.
- Maintainers must resist recreating the same broad surface incrementally.

## Alternatives considered

- Forking k9s and removing out-of-scope capabilities was rejected because the
  inherited code and upgrade surface would remain large and safety review would
  have to prove that every bypass was removed.
- Maintaining a thin Agent feature branch on top of k9s was rejected because
  upstream product and internal changes would control KuPilot's architecture.
- Launching or embedding k9s as a secondary interface was rejected because it
  would create a parallel cluster path outside KuPilot's use cases.

## Security and privacy impact

Starting from a minimal codebase narrows the set of cluster operations,
background behaviors, caches, and views that require review. All Kubernetes
reads can be routed through the documented application, Tool, and adapter
boundaries.

This choice is not a security guarantee. KuPilot must still test import
boundaries, fixed resource allowlists, generation invalidation, projection,
redaction, output limits, and zero-call denial paths.

## Validation

The [Architecture](../architecture.md) defines a minimal composition with no k9s
dependency or parallel resource-management path. The public
[Scope](../scope.md) rejects a general resource browser and direct management
surface.

Dependency review must verify that k9s has not entered the module graph or
source tree as an application foundation.

## Revisit triggers

- The product mission is explicitly changed to a general resource-management
  TUI.
- A narrowly scoped reusable component is proposed with documented provenance,
  license compatibility, dependency cost, and no expansion of cluster access.
- Maintaining the independent TUI becomes infeasible and evidence supports an
  ADR that preserves the Agent-first and safety boundaries.

## References

- [Architecture](../architecture.md)
- [Product Contract](../product.md)
- [Scope](../scope.md)
- [ADR-0003: Keep the Product Agent-First](0003-agent-first-interaction.md)
