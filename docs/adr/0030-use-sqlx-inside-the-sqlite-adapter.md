# ADR-0030: Use sqlx Inside the SQLite Adapter

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot's SQLite adapter needs explicit row scanning, named fields, short
transactions, and fixed queries. Plain `database/sql` can implement all of this,
but repetitive manual scanning can obscure mappings and make omission tests
harder. A full ORM would introduce implicit schema, relationship, query, and
mutation behavior that conflicts with explicit retention and repository
contracts.

The current sqlx version and exact APIs have not been validated with the
eventual SQLite driver.

## Decision

KuPilot will use sqlx as a thin mapping and transaction helper only inside
`internal/persistence/sqlite`, on top of `database/sql` and the single selected
SQLite driver.

sqlx may be used for:

- Explicit struct-to-column mapping for adapter-private row types.
- Fixed named or positional parameter binding.
- Query and row scanning with required-column checks.
- Short transaction helpers whose lifetime is owned by one repository method.

sqlx will not define domain models, Application ports, schema migrations,
repository interfaces, retention policy, dynamic query builders, table names,
generic CRUD, or a unit-of-work framework. sqlx types, tags, handles, rows, and
transactions do not cross the adapter.

All SQL remains reviewable, fixed text colocated with adapter behavior. Dynamic
user, model, Kubernetes, or Session text is a bound value only. Optional sort or
filter choices are mapped from code enums to a closed statement set rather than
concatenated.

The adapter prohibits `SELECT *`, `Unsafe()`, `Must*` helpers, queries without a
`context.Context`, unbounded `Select`, and `map[string]any` write inputs. It uses
explicit column lists, short transactions, and the pure-Go driver selected under
ADR-0018.

The exact sqlx release and API usage are selected only after the S06 driver and
mapping spike. This ADR accepts the role, not an unverified version.

## Consequences

Positive consequences:

- Row mappings are concise while remaining explicit and adapter-local.
- Fixed SQL and transaction boundaries stay visible for security review.
- Tests can compare schema columns with allowlisted row fields.
- Application remains independent of both sqlx and `database/sql`.

Costs and constraints:

- sqlx adds a dependency and reflection-based mapping behavior that must be
  tested for missing, extra, null, and type-mismatched columns.
- Adapter-private rows still require deliberate conversion to domain values.
- Maintainers must resist growing sqlx use into an ORM or generic repository.
- Driver-specific placeholder, time, and error behavior still needs its own
  validation.

## Alternatives considered

- Using only `database/sql` was not selected because the accepted schema has
  enough explicit mappings that a narrow scanning helper reduces repetitive
  error-prone code.
- A full ORM was rejected because implicit queries, associations, migrations,
  and persistence hooks make data eligibility and transaction behavior harder
  to prove.
- Generated query code was deferred because the initial schema is small and a
  generation tool would add its own version and build workflow.
- Exposing sqlx rows to Application was rejected because storage shape is not a
  domain contract.

## Security and privacy impact

sqlx does not make a field eligible for storage. Adapter row types must have a
one-to-one review against the Data Retention Contract, and generic JSON, debug,
or raw-error columns remain prohibited.

Bound values prevent SQL syntax injection only when SQL identifiers and clauses
stay code-defined. Tests must prove adversarial external text cannot select a
statement, table, column, pragma, migration, or order expression.

## Validation gate

No later than S06 and before repository implementation, the chosen sqlx and
SQLite driver versions must jointly prove:

1. Minimum Go and license compatibility from official module metadata.
2. Context-aware query and transaction behavior through the selected driver.
3. Required-column, unknown-column, null, time, integer-boundary, and type-error
   behavior.
4. Correct rollback and no leaked rows, statements, or transactions on every
   injected failure.
5. Fixed named/positional binding for adversarial Unicode and SQL-shaped text.
6. Race-enabled behavior and connection closure on macOS and Linux.

The chosen version and API calls must be recorded after the spike. None is
claimed verified by this ADR.

## Revisit triggers

- sqlx becomes unmaintained or incompatible with the selected driver or Go
  lower bound.
- Reflection behavior prevents strict schema/field checks.
- Measured schema growth makes generated queries safer and more maintainable.
- sqlx begins leaking beyond the adapter boundary.

## References

- [ADR-0008: Use SQLite for Local Persistence](0008-use-sqlite-for-local-persistence.md)
- [ADR-0018: Select a Pure-Go SQLite Driver Through an S06 Gate](0018-select-a-pure-go-sqlite-driver-through-an-s06-gate.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
