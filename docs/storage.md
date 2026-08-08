# Local Storage

KuPilot uses one local SQLite database for eligible Session history, lifecycle
metadata, Evidence provenance, audit records, and future approval metadata. The
database is a local confidentiality boundary, not an encrypted vault or a
tamper-resistant ledger.

## Selected stack

The storage adapter uses these pinned components:

- `modernc.org/sqlite` v1.56.0 as the only production SQLite driver. It is a
  CGo-free BSD-3-Clause implementation, embeds SQLite 3.53.3, and registers the
  fixed driver name `sqlite`.
- `github.com/jmoiron/sqlx` v1.4.0 as a thin adapter-private mapping and
  transaction helper.

sqlx v1.4.0 does not recognize `sqlite` in its built-in bind table. KuPilot
therefore registers that fixed name as `sqlx.QUESTION` in the SQLite adapter.
The registration is code-defined and cannot be selected by configuration,
Session data, Kubernetes data, or model output.

The driver, sqlx, Go minimum, and the driver's matching `modernc.org/libc`
runtime are one compatibility set. An upgrade must re-run migration, binding,
locking, cancellation, race, permission, and supported-target build contracts.
There is no alternate or CGo fallback driver.

sqlx's upstream module metadata covers compatibility tests for multiple
database drivers. Their checksum entries may therefore appear in `go.sum`, but
KuPilot does not import, register, or link those drivers. The build and test
package closure must contain only `modernc.org/sqlite` as a SQLite driver and
must not contain `runtime/cgo`.

## Path and file policy

The database is named `kupilot.db` under the validated per-user state directory.
The path comes from typed local configuration; database names and paths never
come from a Session, Tool, Kubernetes object, or model value.

On supported Unix platforms, KuPilot enforces mode `0700` on the state directory
and mode `0600` on the database and known journal, WAL, and shared-memory
sidecars. Existing symlinked path components, database files, and sidecars are
rejected before the driver opens the database. An unknown, incompatible, or
corrupt database is reported as unavailable and is not deleted, renamed,
overwritten, or recreated automatically.

These controls do not provide SQLite encryption or forensic deletion. Operating
system disk encryption, snapshots, backups, swap, and storage-media lifecycle
remain outside KuPilot's guarantees.

## Connection policy

The adapter uses one open and one idle connection. Connection-local settings
are encoded in the fixed driver DSN. WAL is activated only after the migration
history has been recognized, and startup verifies the following fixed policy on
the active connection:

| Setting | Value |
| --- | --- |
| `foreign_keys` | `ON` |
| `busy_timeout` | 5,000 milliseconds |
| `journal_mode` | `WAL` |
| `synchronous` | `NORMAL` |
| `secure_delete` | `FAST` |

The single connection serializes writes, while WAL preserves the accepted
journal and recovery behavior. The busy timeout is bounded and does not replace
caller-owned Context cancellation. `secure_delete=FAST` is a page-management
setting and is never described as secure erasure.

## Migrations

Migration SQL is embedded, forward-only, and named with a six-digit monotonic
version such as `000001_initial.sql`. Each applied migration records its version,
name, SHA-256 checksum, UTC Unix-millisecond application time, and application
version in `schema_migrations`.

Startup accepts an empty database or a recognized migration history. It applies
each pending migration in a short transaction, validates every recorded name and
checksum, and rejects gaps, changed migration content, and a schema version newer
than the running binary. A released migration is immutable; corrections use a
new forward migration.

The initial schema contains `sessions`, `messages`, `agent_runs`,
`model_requests`, `tool_invocations`, `evidence_items`, `diagnoses`, `approvals`,
`audit_events`, and `settings`. The `approvals` table is dormant metadata in a
read-only composition; its presence does not create an approval coordinator,
executor, write Tool, or Kubernetes mutation path.

## Data boundary

Schema columns are explicit, bounded where content is admitted, and use UUIDv7
text identities and UTC Unix-millisecond timestamps. SQL uses fixed statements,
explicit columns, Context-aware calls, and bound values. sqlx handles, rows,
transactions, tags, and driver values stay inside `internal/persistence/sqlite`.

The schema has no field for model API keys, kubeconfig content, authentication
tokens, certificates, private keys, raw container logs, assembled prompts, raw
model traffic, raw Tool results, arbitrary patches, or framework objects.
Token-count metadata is numeric usage information and never authentication
material. JSON columns are purpose-specific bounded projections; they do not
make an otherwise prohibited source eligible for storage.
ResourceRef projections are limited to the fixed `v0.1` direct-target
API-version and Kind pairs, and their Namespace must match the associated
historic scope snapshot.

## Repository contracts

Session-history ports are owned by the `internal/session` consumer and use only
project-owned Domain values and bounded request and result DTOs. The SQLite
adapter implements separate Session, Message, and AgentRun repositories. SQL,
sqlx handles, transactions, row mappings, `db` tags, nullable driver values, and
JSON encoding stay inside the adapter.

The Session repository supports create, exact-ID read, optimistic title rename,
and complete deletion. Rename changes only a standard-persistence Session,
requires the expected version, rejects a regressing update time, and advances
the version. Session deletion issues one parent delete in a short transaction;
foreign-key cascades remove the complete Session-owned graph or the transaction
rolls back.

The Message repository stores only bounded content that the caller has already
made eligible and safe. A Message write checks that its Session is active and
uses standard persistence, inserts the Message, and advances Session activity
in one short transaction. Minimal-persistence Sessions reject durable Message
content. Committed history is read in bounded ascending pages ordered by
`created_at_ms, id`.

The AgentRun repository atomically stores a committed user Message and its
`running` AgentRun before model or Tool work may begin. The same transaction
rejects a second durable `running` AgentRun. A terminal update checks the
stored immutable run identity and the `running` state; a final validated
assistant Message, terminal run metadata, and Session activity may be committed
together. No transaction spans model, Kubernetes, terminal, or user I/O.

Every repository statement has a fixed parameterized form and an explicit
column list. Reads use strict adapter-private mappings and reject invalid null
combinations, malformed bounded JSON projections, invalid Domain identifiers,
and invalid state values without returning a partial result. Repository errors
use code-defined safe text and never include SQL, bound values, driver text, or
local paths.

## Resume and startup recovery

Resume eligibility is derived rather than stored. A candidate must be active,
use standard persistence, and retain at least one committed safe Message.
Picker pages are global and use the exclusive descending keyset
`updated_at_ms, id`, with a maximum of 50 rows. `--last` is the first row under
the same ordering. There is no directory, repository, Context, Namespace, or
environment filter.

An exact resume first reads the Session metadata and then a bounded committed
Message page. A known minimal-persistence Session returns the stable
`session_not_resumable` outcome. Missing, archived, malformed, or otherwise
ineligible history uses a non-disclosing unavailable outcome. Resume returns
historic scope and ResourceRef values only as unverified candidates; it creates
no live generation and performs no model, Tool, Kubernetes, approval, or
executor operation.

The startup recovery operation validates every durable `running` AgentRun and
changes all valid rows to `interrupted` in one short transaction. It records a
stable termination reason and a UTC finish time no earlier than the stored
start time. Recovery is idempotent, honors Context cancellation, returns only a
count, and never reconstructs or replays Agent, model, Tool, scope, approval, or
write state. A validation or update failure rolls back the complete recovery
operation and prevents resume results from being treated as available.
