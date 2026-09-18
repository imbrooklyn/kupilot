# ADR-0001: Use Go and a Fixed Platform Toolchain

- Status: Accepted

## Context
A local Kubernetes terminal application needs the Go client ecosystem, explicit
cancellation, deterministic tests and a small CGO-free distribution matrix.

## Decision
Use Go 1.27.0 for the module, local gates, CI and release builds. Pin dependencies
and development tools to their newest compatible releases. Every transitive
module must support Go 1.27.0; do not accept a Go 1.27.1 requirement or automatic
toolchain replacement. Exact pins and exceptions belong in
[Dependency Compatibility](../compatibility.md).

Support macOS 13 or newer and Linux on amd64 and arm64. Windows is experimental
and outside the release gate. Use CGO_ENABLED=0 for production binaries; native
platform tests still verify filesystem, process, terminal and SQLite behavior.

## Consequences and alternatives
One language and four release targets keep composition and packaging direct.
Cross-compilation alone cannot establish native lifecycle correctness. A second
language, mandatory CGO or a broader platform promise needs demonstrated demand
and equivalent validation.

## Validation
Run focused tests, the fast and slow gates, native platform CI, CGO dependency
inspection and release configuration checks. Review exact module metadata,
licenses and vulnerability results. Version selection is not itself evidence
that these checks passed.
