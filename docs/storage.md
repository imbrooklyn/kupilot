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

Audit, typed-setting, and retention ports are owned by `internal/audit`. Each
port has only the operations required by its consumer. The remaining detail
repositories likewise expose concrete project-owned values rather than SQL,
driver types, generic records, or arbitrary payload maps.

The detail repositories enforce these storage boundaries again at their entry
points, after the caller has projected, normalized, redacted or blocked, and
bounded the source data:

<!-- markdownlint-disable MD013 -->

| Durable category | Stored representation and hard bound |
| --- | --- |
| Model request | Lifecycle metadata, token counts, latency, model identifier, origin hash, and prompt or response fingerprints only. No prompt, response body, header, stream, or provider object is accepted. Model and prompt-version text are at most 128 bytes, and a provider request identifier is at most 256 bytes. |
| ToolInvocation | One of the six fixed `v0.1` Tool names, version, safe purpose, canonical arguments projection and digest, lifecycle metadata, safe summary or safe error, byte count, and truncation state. Arguments are at most 8 KiB; purpose is at most 1 KiB; safe summary and safe error are each at most 4 KiB. Scope, endpoint, credential, deadline, and hard-limit authority cannot be supplied through arguments. |
| Evidence | A project-owned ResourceRef projection, category, concise fact, source path, severity, redaction and truncation state, fingerprint, and observation time. A fact is at most 2 KiB, a source path at most 1 KiB, and one ToolInvocation may own at most 100 Evidence items. |
| Diagnosis | Separate confirmed facts, hypotheses, missing information, not-executed recommended actions, answer text, validation warnings, and an exact historic Evidence window. The complete serialized record is at most 128 KiB. Every confirmed fact cites same-run Evidence when written. |
| AuditEvent | A fixed event type, actor, outcome, optional scope and subject, and a typed scalar detail object. Detail and subject projections are each at most 4 KiB, and a correlation identifier is at most 128 bytes. Minimal persistence admits only fixed lifecycle, consent, policy, degraded-storage, and future write-safety event types. |
| Setting | The current allowlist contains only `retention.operational_detail_days`, encoded as a schema-version-1 integer from 0 through 3,650 with a UTC update time. Unknown and credential-shaped keys are rejected before SQL; the schema repeats the 64-byte key, 4 KiB JSON, and sensitive-key constraints. |

<!-- markdownlint-enable MD013 -->

A ToolInvocation and its accepted Evidence are inserted atomically. Model
metadata, Tool detail, Evidence, and Diagnosis are rejected for
minimal-persistence Sessions. Diagnosis writes verify every cited Evidence ID,
owning AgentRun, scope, and observation window. When retention later removes
some or all cited Evidence rows, Diagnosis reads preserve the historic text and
identifiers but derive an explicit `partial` or `expired` detail state. They do
not turn missing detail into current Evidence.

AuditEvent writes validate their Session, AgentRun, and scope relationships.
Reads use exact IDs or descending Session-owned keyset pages ordered by
`occurred_at_ms, id`, with at most 100 events. Settings use strictly newer
injected update times, so an older writer cannot replace a newer value.

Every repository statement has a fixed parameterized form and an explicit
column list. Reads use strict adapter-private mappings and reject invalid null
combinations, malformed bounded JSON projections, invalid Domain identifiers,
and invalid state values without returning a partial result. Repository errors
use code-defined safe text and never include SQL, bound values, driver text, or
local paths.

## Retention and deletion

Retention cleanup is a caller-scheduled operation; the storage adapter owns no
clock, scheduler, background goroutine, or automatic `VACUUM`. A request carries
an exact UTC millisecond, the configured operational-detail lifetime, and a
batch size from 1 through 100. One short transaction deletes at most that many
rows from each category using these inclusive boundaries:

<!-- markdownlint-disable MD013 -->

| Category | Boundary |
| --- | --- |
| Evidence, ToolInvocations, and model-request metadata | The configured operational-detail cutoff, 30 days by default. Evidence uses `observed_at_ms`; Tool and model metadata use completion time when present and otherwise use start time as a conservative expiry fallback. |
| Ordinary read and lifecycle AuditEvents | 90 days from `occurred_at_ms`. |
| Dormant `approval_*` and future `write_*` AuditEvents | 180 days from `occurred_at_ms`. |

<!-- markdownlint-enable MD013 -->

Evidence is removed before its owning ToolInvocation. A ToolInvocation is
removed only when no retained Evidence row still refers to it. Audit retention
uses fixed code-defined event lists rather than interpreting arbitrary stored
text. If any statement, Context, or commit fails, the whole batch rolls back and
the returned counters remain zero. A conservative continuation flag tells the
caller to schedule another bounded batch when a category reached its limit.

After audit expiry, cleanup may remove a minimal-persistence Session shell when
it has no retained AuditEvent or approval and no queued or running AgentRun.
Standard-persistence Sessions, Messages, AgentRuns, and Diagnoses do not expire
automatically. Explicit Session deletion removes the complete Session-owned
graph, including model metadata, ToolInvocations, Evidence, Diagnoses, dormant
approval rows, and Session- or AgentRun-linked AuditEvents, through foreign-key
cascade in one short transaction. Global typed settings are not Session-owned.

Physical-file conformance tests place distinct synthetic prohibited-content
canaries outside eligible DTOs, persist safe derivatives, and scan both the
database and WAL. Static guards keep sqlx imports inside the SQLite adapter and
reject `SELECT *`, unsafe or panic-style helpers, unbounded selection, generic
map boundaries, formatted SQL, and non-Context database calls in repositories.
The read-only composition contains only the dormant approval schema DTO and no
approval service, approval coordinator, write executor, or restart path.

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
