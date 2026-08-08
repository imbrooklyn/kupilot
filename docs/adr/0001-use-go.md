# ADR-0001: Use Go

- Status: Accepted
- Date: 2026-08-05

## Context

KuPilot is a local terminal application that must coordinate Kubernetes reads,
streamed model responses, cancellation, bounded concurrent I/O, local SQLite
persistence, and cross-platform distribution. The implementation language must
support the Kubernetes client ecosystem, explicit context propagation,
deterministic tests, and a maintainable single-binary release process for a
small project.

## Decision

KuPilot will be implemented in Go and distributed as a local command-line
binary.

The implementation will use Go's explicit package dependencies and
`context.Context` cancellation model to enforce the boundaries in the
[architecture baseline](../architecture.md). Domain and application contracts
will use project-owned types rather than vendor SDK types.

The minimum supported Go version is Go 1.25.0, the newest stable common lower
bound required by the selected dependency versions. Any change to that lower
bound must be supported by official module metadata and verified by compile and
platform checks.

## Consequences

Positive consequences:

- The implementation can use the maintained Kubernetes Go client without a
  cross-language bridge.
- Context propagation, channels, and goroutines can express cancellation and
  streaming ownership directly.
- Static compilation and the Go test ecosystem support a small distribution and
  deterministic fake-adapter tests.
- Package imports can make architectural dependency rules mechanically
  checkable.

Costs and constraints:

- Goroutine, channel, and stream ownership must be designed explicitly to avoid
  leaks and races.
- Go interfaces are structural and easy to overproduce; KuPilot will define
  small interfaces at the consumer and prefer concrete types otherwise.
- Cross-platform behavior still requires validation for terminal, filesystem,
  Kubernetes authentication, and SQLite dependencies.

## Alternatives considered

- Rust could provide a similarly local binary and strong static guarantees, but
  it would add a less direct Kubernetes ecosystem path and higher implementation
  cost for this project.
- TypeScript with a JavaScript runtime could support terminal and model APIs,
  but would complicate the single-binary and Kubernetes client goals.
- A split-language implementation would add packaging and boundary complexity
  without a product requirement.

## Security and privacy impact

Go does not itself provide KuPilot's security boundary. Runtime scope checks,
fixed Tool schemas, safe projection, redaction, output limits, and credential
isolation remain application requirements. Context cancellation also does not
replace the post-result generation check.

The single-process design reduces cross-process credential movement, but code
must still prevent sensitive values from entering string representations,
errors, logs, model content, or persistence.

## Validation

The selected toolchain and dependencies must satisfy all of these checks:

1. Official module requirements support the selected Go lower bound.
2. The common supported Go lower bound is documented.
3. The supported platform matrix compiles successfully.
4. Applicable unit, race, and cross-build checks pass.

All four checks are release requirements.

## Revisit triggers

- A mandatory dependency cannot support the accepted release platforms or a
  maintainable Go toolchain.
- The single-binary requirement changes materially.
- Evidence shows that an essential product capability cannot be implemented or
  tested safely in Go.

## References

- [Architecture](../architecture.md)
- [Product Contract](../product.md)
- [Scope](../scope.md)
