# ADR-0006: Use One Safe SQLite Store

- Status: Accepted

## Context
Session history needs local durability without persisting raw model traffic,
credentials or resumable execution authority.

## Decision
Use one SQLite database through database/sql and modernc.org/sqlite. sqlx remains
a thin helper inside the adapter. Keep SQL, rows, transactions and db tags inside
that boundary. Use explicit bound statements, columns, context-aware operations,
foreign keys and short transactions; no ORM, generic CRUD, payload escape hatch,
SELECT *, Unsafe or Must operations.

The safe messages table is the sole durable conversation source. Standard mode
retains admitted safe messages, Diagnosis, Evidence, invocation and audit data;
minimal mode retains no model memory. Follow [Data Retention](../data-retention.md)
for exact fields, cutoffs, summaries, exports and degraded-storage behavior.
Raw prompts, traffic, Tool results, credentials, objects and framework state are
prohibited. IDs are project-owned UUIDv7 text and timestamps UTC Unix milliseconds.

Bare startup creates a new Session without history lookup. Only explicit resume
may query eligible history, with no model, cluster, Reviewer, Tool or executor I/O
during resume itself. History is display/context data, never restored authority.

Maintain authoritative monotonic last_activity_at_ms only on admitted lifecycle
writes. Deletion uses exact IDs or a bounded digest-bound snapshot reselected
inside one transaction. Protect current, active, consuming, future, corrupt or
unproved Sessions. Export only safe version-1 free-form summaries; export does
not claim complete raw records or operational authority.

Use owner-only creation modes and safe fixed paths, respecting existing
user-managed permissions. No encryption, tamper resistance or forensic erasure
is claimed. Before publication, correct one initial checksummed schema in place;
after publication, released migrations are immutable and upgrades forward-only.

## Consequences and validation
SQLite provides the required transaction barrier without another durable log.
Storage failure before an action attempt denies execution. Corrupt or incompatible
storage is never silently recreated. Test real temporary files for transactions,
checksum/corruption rejection, cancellation, deletion cascades, retention,
sensitive-data absence, explicit resume, export and minimal mode.
