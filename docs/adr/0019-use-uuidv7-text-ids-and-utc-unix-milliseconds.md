# ADR-0019: Use UUIDv7 Text IDs and UTC Unix Milliseconds

- Status: Accepted
- Date: 2026-08-08

## Context

Sessions, runs, messages, model requests, Tool invocations, Evidence, Diagnosis,
audit, and future approval records need identifiers and timestamps that remain
stable across process restart and SQLite migration. SQLite row identifiers would
couple domain identity to one adapter, while local-time strings would create
ambiguous ordering around timezone and daylight-saving changes.

The accepted product direction is UUIDv7 text for domain identity, UTC Unix
milliseconds in SQLite, and local-time presentation in the TUI. The exact Go
UUID implementation and API have not been validated.

## Decision

KuPilot will generate every durable domain identifier in application code as a
UUIDv7 value and persist its textual representation in an explicit SQLite
`TEXT` primary or foreign-key column. SQLite `rowid` is not a domain identifier,
is not exposed through Application ports, and is not used for resume, scope,
Evidence, audit, approval, or correlation semantics.

Identifiers are opaque values. Runtime policy never derives authorization,
ClusterScope, ownership, eligibility, retention, or approval from UUID ordering
or embedded time. External and CLI identifier input must pass strict UUIDv7
parsing before repository use, and safe errors must not disclose whether a
well-formed unknown identifier once existed.

Every durable timestamp is stored as a SQLite `INTEGER` containing Unix
milliseconds for a UTC instant. This includes creation, update, observation,
completion, expiry, decision, audit, retention, and recovery times. Domain and
Application code use time values rather than database-local time functions;
tests use a controlled clock. SQLite, model output, Kubernetes fields, and TUI
input do not choose an authoritative current time.

Persistence and protocol comparisons use UTC instants. Queries that need stable
ordering use an explicit timestamp plus a stable identifier tie-breaker; they do
not depend on insertion order, `rowid`, formatted local time, or an assumption
that UUIDv7 generation is strictly monotonic.

The TUI converts stored UTC instants to the user's current local timezone for
display. Conversion is presentation only: it never rewrites durable values,
retention cutoffs, audit order, approval TTLs, or model-visible observation
times. Where a displayed time could be ambiguous, the UI must preserve enough
date or zone context to avoid presenting two different instants as one event.

Migration versions remain monotonically increasing integers and are not UUIDs.
Protocol-owned identifiers received from Kubernetes or a model provider remain
typed external values and are not silently converted into KuPilot domain IDs.

This ADR selects representations and semantics, not a UUID library or API. The
concrete implementation must pass the S04/S06 gate before the initial migration
is treated as stable.

## Consequences

Positive consequences:

- Domain identity is created before persistence and remains independent of
  SQLite connection or insertion order.
- One UTC integer representation supports deterministic retention, audit,
  recovery, and approval calculations.
- Text IDs are portable across logs, CLI resume, fixtures, and future migration.
- Local display does not contaminate durable or security-sensitive time logic.

Costs and constraints:

- UUIDv7 text uses more storage than an SQLite integer key.
- UUIDv7 values disclose an approximate generation time when shared.
- Millisecond storage discards sub-millisecond precision.
- Stable ordering still requires an explicit tie-breaker for equal timestamps.
- UUID parsing, clock regression, concurrent generation, timezone, and
  daylight-saving behavior require dedicated tests.

## Alternatives considered

- SQLite integer identities were rejected because they bind domain identity to
  one database and complicate creation before a transaction.
- UUIDv4 was not selected because the accepted direction prefers time-oriented
  identifiers while keeping them opaque to policy.
- ULID was not selected because UUIDv7 provides the accepted UUID representation
  and avoids another project-specific identifier format.
- RFC 3339 text timestamps were rejected for SQLite storage because numeric
  cutoff queries and precision normalization are simpler with one integer unit.
- Storing local wall-clock time was rejected because timezone and
  daylight-saving transitions make comparison and retention ambiguous.
- Using UUID order as the only chronology was rejected because generation and
  clock behavior must not become an implicit audit or policy guarantee.

## Security and privacy impact

An identifier is not a secret, capability, or proof of authorization. Every
lookup still applies retention, eligibility, Session, scope, and approval rules.
Safe errors avoid turning exact-ID lookup into a history-enumeration channel.

Because UUIDv7 carries approximate time, KuPilot exposes identifiers only where
the product contract needs them. IDs contain no Context, Namespace, resource
name, user name, endpoint, model name, or credential. UTC storage also prevents
local timezone data from becoming an unnecessary durable attribute.

## Validation gate

S04 must record the candidate UUIDv7 implementation, its official module
metadata, minimum Go version, license, and exact dependency role if a dependency
is needed. No later than S06 and before the initial migration is frozen, tests
must prove:

1. Generated values have the required UUID version and variant and round-trip
   through the selected textual parser and SQLite `TEXT` columns.
2. Concurrent generation, multiple values in one millisecond, cancellation, and
   documented clock-regression behavior do not create duplicate domain IDs.
3. Malformed, non-v7, overlong, empty, and Unicode-confusable identifier input is
   rejected before repository lookup with stable safe errors.
4. Unix-millisecond conversion round-trips representative instants within the
   accepted millisecond precision and uses UTC for retention boundaries.
5. Equal-millisecond rows have deterministic query ordering independent of
   `rowid` and insertion plan.
6. TUI tests with fixed timezones and daylight-saving boundaries change only
   presentation, never stored values or policy calculations.
7. Schema and repository tests use explicit identifier and timestamp columns
   and do not expose SQLite `rowid` as domain state.

The chosen package version, parser/generator calls, and observed monotonic or
clock-regression behavior must be recorded after the gate. None is claimed
verified by this ADR.

## Revisit triggers

- The selected UUIDv7 implementation cannot satisfy concurrency or supported
  Go/platform requirements.
- Identifier timestamp leakage becomes unacceptable for a newly public or
  shared surface.
- A remote or multi-process topology requires coordinated identity semantics
  beyond locally generated UUIDv7 values.
- A retention, audit, or protocol requirement needs precision finer than
  milliseconds.

## References

- [Architecture](../architecture.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0008: Use SQLite for Local Persistence](0008-use-sqlite-for-local-persistence.md)
- [ADR-0025: Enforce Data Retention and User Deletion](0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
