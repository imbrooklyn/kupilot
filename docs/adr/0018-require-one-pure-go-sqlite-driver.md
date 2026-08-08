# ADR-0018: Require One Pure-Go SQLite Driver

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot uses SQLite, but a Go SQLite driver affects cross-compilation, CGO
requirements, binary distribution, cancellation, locking, journal files, error
classification, time handling, filesystem permissions, and vulnerability
response. These behaviors are architecture and security concerns rather than an
adapter convenience.

## Decision

KuPilot will use exactly one pinned pure-Go SQLite driver through
`database/sql`, confined to `internal/persistence/sqlite`.

The selected driver is `modernc.org/sqlite`. The initial compatibility baseline
pins `modernc.org/sqlite` v1.56.0 with `github.com/jmoiron/sqlx` v1.4.0 and the
repository minimum Go version 1.25.0. The driver registers the fixed
`database/sql` name `sqlite`. Because sqlx v1.4.0 does not include that name in
its default bind table, the adapter explicitly and idempotently registers
`sqlite` as `sqlx.QUESTION` before constructing its private sqlx handle. Neither
the driver name nor the bind type is configurable.

The selected driver is a BSD-3-Clause, CGo-free SQLite port. Its matching
`modernc.org/libc` version remains pinned by the module graph because the
driver's generated SQLite code and libc runtime are jointly versioned. Driver
and libc upgrades are one compatibility change and must pass the complete
storage contract together.

sqlx's upstream module metadata lists several database drivers used by its own
compatibility tests, so Go checksum metadata may include those modules. KuPilot
does not import, register, or link any of them. Dependency guards must prove
that the package build and test closure contains `modernc.org/sqlite` and does
not contain another SQLite driver or `runtime/cgo`.

Pure Go is an accepted constraint. Adding a CGO driver requires a replacement
ADR that explicitly accepts the toolchain, platform, packaging, and security
consequences. There is no silent fallback to CGO and no second interchangeable
production driver.

Driver-specific DSNs, pragmas, errors, and connection hooks remain private to
the adapter and are normalized into project-owned configuration and safe error
classes. The selected dependency must remain maintained, license-compatible,
pinned, and compatible with the repository's Go and platform baselines.

## Consequences

Positive consequences:

- Distribution and storage behavior remain compatible with CGO-free builds.
- Driver churn stays behind one adapter and the `database/sql` boundary.
- The project avoids divergent driver, migration, and packaging behavior.
- Security review includes journals and failure modes, not only successful SQL.

Costs and constraints:

- A driver that fails any required behavior is rejected and can block a release.
- Supporting only one driver means a regression cannot use a dynamic fallback.
- Driver upgrades require compatibility, storage, and security regression tests.
- The generated SQLite implementation and its pure-Go runtime increase module
  and binary size compared with a system SQLite linkage.

## Alternatives considered

- Selecting a driver from familiarity or documentation claims alone was
  rejected because storage and platform behavior require reproducible tests.
- Selecting a CGO driver by default was rejected because it changes build and
  packaging assumptions.
- Shipping both pure-Go and CGO variants was rejected because it doubles
  behavior, migration, packaging, and test matrices.
- Hiding selection behind a generic storage abstraction was rejected because
  KuPilot requires SQLite semantics and a concrete validated implementation.

## Security and privacy impact

Driver behavior affects whether owner-only permissions, retention, rollback,
write-before-audit ordering, and deletion claims are true. A driver is
unacceptable if it logs SQL values, follows unsafe paths, silently changes
durability semantics, or prevents deterministic failure injection.

No driver enables database encryption by implication. Adding encryption would
require a key-management decision and separate ADR.

## Validation

The selected driver and every upgrade must satisfy:

1. Maintainer and release activity, license compatibility, published security
   posture, transitive dependencies, pinned stable version, and minimum Go
   version from official module metadata.
2. Supported-platform compile and test behavior for macOS and Linux on `amd64`
   and `arm64`, including documented binary characteristics.
3. Foreign-key enforcement, transaction isolation and rollback, busy and lock
   behavior, Context cancellation, connection closure, and bounded retry.
4. Schema migration, integrity-check, corruption, disk-full, read-only,
   permission-denied, and unknown-error behavior with stable classification.
5. Journal mode, WAL and shared-memory lifecycle, file and sidecar permissions,
   checkpoint behavior, and delete-all behavior.
6. UTC timestamp round-trips, bound parameters, binary and text limits, and no
   implicit logging or telemetry.
7. Race-enabled repository tests and interruption recovery through
   `database/sql` and sqlx.
8. A documented upgrade and vulnerability-response path.

Validation uses repository fixtures and temporary directories, never real user
history or credentials.

## Revisit triggers

- The chosen driver becomes unmaintained, vulnerable, license-incompatible, or
  incompatible with the supported Go or platform matrix.
- A driver upgrade changes journal, locking, cancellation, error, or permission
  behavior.
- Product topology introduces concurrent processes or shared storage.

## References

- [ADR-0008: Use SQLite for Local Persistence](0008-use-sqlite-for-local-persistence.md)
- [Data Retention Contract](../data-retention.md)
- [Security Threat Model](../security.md)
- [ADR-0028: Support macOS and Linux with Experimental Windows](0028-support-macos-and-linux-with-experimental-windows.md)
- [ADR-0030: Use sqlx Inside the SQLite Adapter](0030-use-sqlx-inside-the-sqlite-adapter.md)
