# ADR-0008: Use SQLite for Local Persistence

- Status: Accepted
- Date: 2026-08-08
- Amended by: ADR-0035

## Context

KuPilot needs bounded local Session history, run lifecycle recovery, Evidence
provenance, retention cleanup, and future approval audit. The product is a local
single process and does not require a database server, shared tenancy, remote
synchronization, or high-availability storage.

The store must support atomic relationships and migrations while keeping SQL and
storage types outside Domain and Application. Local persistence also creates a
confidentiality and retention boundary that must be explicit.

## Decision

KuPilot will use one local SQLite database at the fixed `state/kupilot.db`
descendant of the resolved KuPilot Home. `internal/persistence/sqlite` owns the
database handle, SQL, migrations, row mappings, transaction mechanics, and
driver-specific behavior. Application owns transaction intent through focused
consumer ports.

The SQLite adapter will:

- Use an explicit schema with foreign keys and versioned forward migrations.
- Use fixed statements and bound values; external input never selects SQL
  identifiers, pragmas, migrations, or arbitrary ordering.
- Keep transactions short and never hold one across model, Kubernetes, terminal,
  or user interaction.
- Apply owner-only permissions to newly created directories, database files,
  and known sidecars on supported platforms. Preserve existing user-managed
  modes while continuing to validate file type and symlink safety.
- Reject unsafe symlinked paths and use no model-, Session-, or CLI-selected
  database path.
- Run schema, migration, interrupted-run recovery, and mandatory retention gates
  before history or new runs become available.
- Persist only the fields admitted by the
  [Data Retention Contract](../data-retention.md).

KuPilot will not claim SQLite encryption or tamper resistance. It will not store
credentials, raw transport bodies, raw Kubernetes objects, raw container
output, assembled prompts, stream deltas, or framework values. Unknown or
corrupt storage is not silently deleted or replaced.

The concrete SQLite driver is governed by ADR-0018 and must satisfy its
validation requirements. SQL mapping assistance is governed by ADR-0030.

## Consequences

Positive consequences:

- A single local file can provide transactions, relational integrity, indexes,
  and bounded cleanup without operating a service.
- Startup recovery and future approval audit can be durable and testable.
- Repository ports can remain use-case-specific while storage mapping stays in
  one adapter.
- Users can remove all KuPilot durable data locally.

Costs and constraints:

- Database, journal, WAL, and shared-memory files all require permission and
  lifecycle review.
- File deletion is not forensic erasure, and operating-system disk protection
  remains important.
- Migrations, locking, corruption, disk-full behavior, and driver packaging must
  be tested on every supported platform.
- A database failure needs visible degraded and fail-closed behavior.

## Alternatives considered

- JSON or line-oriented files were rejected because multi-entity updates,
  retention cascades, recovery, and future approval audit need reliable
  transactions and relationships.
- An embedded key-value store was rejected because the accepted data has clear
  relational ownership and query needs.
- PostgreSQL or another database server was rejected because it adds service
  installation, credentials, networking, and operations to a local single-user
  product.
- An encrypted database extension was rejected for `v0.1` because it adds key
  management and platform complexity and is not a substitute for minimizing
  stored data.

## Security and privacy impact

SQLite is a local confidentiality boundary, not a credential vault. Fixed
schema eligibility, filesystem permissions, bounded retention, explicit delete,
and sink-canary tests are mandatory. A generic JSON or debug column cannot
bypass the retention contract.

A failure to durably begin a run prevents the model call. A later read-only
write failure may let the current in-memory Diagnosis finish with visible
`persistence_degraded` state, but no new run starts until storage is healthy.
Every future pre-write audit failure prevents the Kubernetes write.

## Validation

The concrete driver, sqlx mapping, and schema must prove:

1. Foreign-key enforcement, transaction rollback, busy handling, cancellation,
   and supported journal behavior.
2. Owner-only creation modes plus unchanged wider existing user-managed modes
   for the directory, database, and sidecars on macOS and Linux.
3. Migration and integrity failure without silent recreation.
4. Startup interruption recovery and no automatic run or write replay.
5. Deterministic retention and deletion at all cutoff and failure boundaries.
6. Absence of every prohibited synthetic canary from all durable mappings.

Exact driver APIs, pragmas, and versions must be documented and satisfy these
requirements.

## Revisit triggers

- The product introduces multiple processes, shared writers, remote history, or
  multi-user state.
- No pure-Go driver satisfies the packaging, cancellation, permission, and
  locking requirements.
- A regulated use case requires an encrypted store and a reviewed key-management
  design.

## References

- [Architecture](../architecture.md)
- [Data Retention Contract](../data-retention.md)
- [Security Threat Model](../security.md)
- [ADR-0002: Use a Local Single Process with No KuPilot Server](0002-local-single-process-no-server.md)
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [ADR-0018: Require One Pure-Go SQLite Driver](0018-require-one-pure-go-sqlite-driver.md)
- [ADR-0030: Use sqlx Inside the SQLite Adapter](0030-use-sqlx-inside-the-sqlite-adapter.md)
