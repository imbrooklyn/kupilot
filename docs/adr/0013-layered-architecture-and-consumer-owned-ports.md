# ADR-0013: Use Layered Boundaries and Consumer-Owned Ports

- Status: Accepted
- Date: 2026-08-05
- Amended by: ADR-0037

## Context

Kupilot combines a terminal UI, a single-Agent runtime, structured Tools,
Kubernetes clients, a cloud model transport, local persistence, and safety
processing. Without explicit ownership, framework types and I/O concerns could
leak into the domain, the TUI could become a second cluster client, and
cross-package calls could form cycles.

The architecture must remain understandable and testable for a small project.
It must not create a large framework or a package for every concept merely to
look layered.

## Decision

Kupilot will use a small layered, ports-and-adapters style with the following
fixed ownership:

- `internal/domain` owns pure models and invariants.
- `internal/application` is the only use-case layer and owns orchestration,
  commands, queries, events, and outbound ports.
- `internal/agent` owns neutral single-Agent policy and its consumer ports.
- `internal/agent/einoadapter` is the Eino translation boundary. Eino types do
  not cross it.
- `internal/tools` owns the admitted structured handlers and the narrow Kubernetes
  read ports they consume.
- `internal/kube` and `internal/persistence/sqlite` are infrastructure adapters.
- `internal/cli` and `internal/tui` are delivery adapters that depend only on
  Application contracts for use cases.
- `cmd/kupilot` is the sole composition root and explicitly supplies concrete
  implementations to consumers.

Interfaces belong to the consumer that needs the capability and normally have
one to three methods. Infrastructure may import the consumer contract it
implements; the consumer does not import the infrastructure package. Contract
values are project-owned, task-specific DTOs.

The architecture will not introduce a generic repository, generic Kubernetes
gateway, service locator, reflection-based dependency injector, general event
bus, dynamic registry, or framework-wide unit of work. Every package and
interface must have an active consumer and a testable responsibility. Empty
package trees are prohibited.

## Consequences

Positive consequences:

- Domain invariants can be tested without framework or I/O dependencies.
- Application tests can replace Agent, Kubernetes, and persistence adapters
  with narrow fakes.
- Vendor churn is confined to adapter mappings.
- Import direction and the single composition root can be checked statically.
- TUI state remains a projection of Application state rather than a competing
  source of truth.

Costs and constraints:

- Explicit DTO mapping is required at framework, Kubernetes, and SQLite
  boundaries.
- Some adapter packages import consumer contracts, which can be unfamiliar when
  reading imports without considering port ownership.
- Maintainers must review new interfaces and packages for a real consumer and
  avoid speculative abstraction.

## Alternatives considered

- A package-by-technical-feature monolith was rejected because UI, model,
  Kubernetes, and SQL types would be difficult to isolate and test.
- A strict framework with generated dependency injection and generic
  repositories was rejected because it adds indirection and abstractions before
  concrete needs exist.
- Letting each feature coordinate its own adapters was rejected because it
  would create multiple use-case layers and composition roots.
- Exposing vendor SDK types through shared contracts was rejected because it
  couples unrelated packages to volatile external APIs.

## Security and privacy impact

The boundaries make safety controls independently enforceable:

- TUI input cannot directly invoke Kubernetes or persistence.
- The Agent can use Kubernetes only through structured Tool contracts.
- Kubernetes infrastructure has no Agent dependency and cannot interpret model
  content as a request.
- SQL rows, transport bodies, credentials, and framework callbacks cannot enter
  Domain or Application contracts.
- The composition root is the one place where an adapter can become reachable;
  the current composition includes only the admitted read adapters and the one
  supervised Deployment restart path.

Layering is not sufficient by itself. Runtime generation checks, allowlists,
projection, redaction, budgets, and tests remain required.

## Validation

The [Architecture](../architecture.md) records the compile-time graph, forbidden
edges, runtime owners, and consumer port table. Static import-boundary checks
and contract tests must fail if Eino escapes its adapter, delivery imports
infrastructure, Domain imports a framework, or a second composition root
appears.

## Revisit triggers

- A concrete use case cannot be expressed without a documented dependency
  cycle under these boundaries.
- Repeated mapping code creates measured maintenance risk that a narrower shared
  value package can solve without vendor leakage.
- A new process boundary or product topology is accepted through another ADR.

## References

- [Architecture](../architecture.md)
- [ADR-0002: Use a Local Single Process with No Kupilot Server](0002-local-single-process-no-server.md)
- [ADR-0003: Keep the Product Agent-First](0003-agent-first-interaction.md)
