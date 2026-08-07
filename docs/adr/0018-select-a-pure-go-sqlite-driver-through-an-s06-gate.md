# ADR-0018: Select a Pure-Go SQLite Driver Through an S06 Gate

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot has accepted SQLite, but a Go SQLite driver affects cross-compilation,
CGO requirements, binary distribution, cancellation, locking, journal files,
error classification, time handling, filesystem permissions, and vulnerability
response. Choosing a driver from familiarity or a README claim would turn these
behaviors into untested architecture.

No concrete driver or driver version has been validated in this repository.

## Decision

KuPilot will use exactly one pure-Go SQLite driver through `database/sql`,
confined to `internal/persistence/sqlite`. The concrete package is selected by an
evidence-producing S06 gate before schema implementation or dependency pinning.

Pure Go is an accepted constraint, not a preference that the spike may reselect.
If no pure-Go candidate passes every gate below, S06 stops and a replacement ADR
must explicitly accept any proposed CGO, toolchain, platform, packaging, and
security consequences before a CGO driver is added. There is no silent fallback.

KuPilot will not support two interchangeable production drivers. Driver-specific
DSNs, pragmas, errors, and connection hooks remain private to the adapter and
are normalized into project-owned configuration and safe error classes.

The selected dependency is pinned only after the spike records evidence. This
ADR intentionally does not name a winning driver, exact version, DSN, or API and
does not claim that a candidate already works.

## Required S06 evidence

The gate must record, for each serious candidate:

1. Maintainer and release activity, license compatibility, published security
   posture, transitive dependencies, current stable version, and minimum Go
   version from official module metadata.
2. Successful supported-platform compile and test behavior for macOS and Linux
   on `amd64` and `arm64`, including the intended static or otherwise documented
   binary characteristics.
3. Foreign-key enforcement, transaction isolation and rollback, busy/lock
   behavior, Context cancellation, connection closure, and bounded retry.
4. Schema migration, integrity-check, corruption, disk-full, read-only,
   permission-denied, and unknown-error behavior with stable classification.
5. Journal mode, WAL and shared-memory lifecycle, file and sidecar permissions,
   checkpoint behavior, and delete-all behavior.
6. UTC timestamp round-trips, bound parameters, binary/text limits, and no
   implicit logging or telemetry.
7. Race-enabled repository tests and interruption recovery through
   `database/sql` and sqlx.
8. A documented upgrade and vulnerability-response path.

The gate uses repository fixtures and temporary directories. It must not use
real user history or credentials.

## Consequences

Positive consequences:

- Distribution and storage behavior are chosen from reproducible evidence.
- Driver churn remains behind one adapter and `database/sql` boundary.
- The project avoids an accidental CGO or unsupported-platform commitment.
- Security review includes journals and failure modes, not only successful SQL.

Costs and constraints:

- S06 cannot begin schema implementation until the spike completes.
- A preferred pure-Go candidate may be rejected and force an explicit platform
  or packaging decision.
- Supporting only one driver means a driver regression can block a release
  rather than falling back dynamically.

## Alternatives considered

- Naming a driver now was rejected because no version or behavior spike has been
  run.
- Selecting a CGO driver by default was rejected because it changes build and
  packaging assumptions before evidence exists.
- Shipping both pure-Go and CGO variants was rejected because it doubles
  behavior, migration, packaging, and test matrices.
- Hiding selection behind a generic storage abstraction was rejected because
  the accepted product needs SQLite semantics and still must validate a concrete
  implementation.

## Security and privacy impact

Driver behavior affects whether owner-only permissions, retention, rollback,
write-before-audit ordering, and deletion claims are true. A driver cannot be
accepted if it logs SQL values, follows unsafe paths, silently changes durability
semantics, or prevents deterministic failure injection.

No driver enables database encryption by implication. Adding encryption would
require a key-management decision and separate ADR.

## Validation

Completion evidence is a checked-in, English S06 spike report or test output
reference that identifies the chosen version and every gate result. Until then,
the status of the strategy is Accepted but the driver selection is explicitly
unverified and implementation is blocked at that dependency boundary.

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
