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
