# Local Storage

Kupilot uses one local SQLite database for eligible Session history, lifecycle
metadata, Evidence provenance, approval state, and audit records. The
database is a local confidentiality boundary, not an encrypted vault or a
tamper-resistant ledger.

## Selected stack

The storage adapter uses these pinned components:

- `modernc.org/sqlite` v1.56.0 as the only production SQLite driver. It is a
  CGo-free BSD-3-Clause implementation, embeds SQLite 3.53.3, and registers the
  fixed driver name `sqlite`.
- `github.com/jmoiron/sqlx` v1.4.0 as a thin adapter-private mapping and
  transaction helper.

sqlx v1.4.0 does not recognize `sqlite` in its built-in bind table. Kupilot
therefore registers that fixed name as `sqlx.QUESTION` in the SQLite adapter.
The registration is code-defined and cannot be selected by configuration,
Session data, Kubernetes data, or model output.

The driver, sqlx, Go minimum, and the driver's matching `modernc.org/libc`
runtime are one compatibility set. An upgrade must re-run migration, binding,
locking, cancellation, race, permission, and supported-target build contracts.
There is no alternate or CGo fallback driver.

sqlx's upstream module metadata covers compatibility tests for multiple
database drivers. Their checksum entries may therefore appear in `go.sum`, but
Kupilot does not import, register, or link those drivers. The build and test
package closure must contain only `modernc.org/sqlite` as a SQLite driver and
must not contain `runtime/cgo`.

## Path and file policy

The database has the fixed path `state/kupilot.db` below the process-frozen
Kupilot Home. `KUPILOT_HOME` selects that root; otherwise Kupilot uses
`$HOME/.kupilot`. Database names and descendants never come from YAML, a
Session, Tool, Kubernetes object, working directory, or model value.

On supported Unix platforms, Kupilot assigns `0700` to a newly created state
directory and `0600` to a newly created database or known journal, WAL, and
shared-memory sidecar. Existing user-managed modes are preserved and do not
block storage solely for being wider. Symlinked managed descendants,
non-regular database files and sidecars, and path replacement remain rejected.
An unknown, incompatible, or corrupt database is reported as unavailable and
is not deleted, renamed, overwritten, or recreated automatically.

These controls do not provide SQLite encryption or forensic deletion. Operating
system disk encryption, snapshots, backups, swap, and storage-media lifecycle
remain outside Kupilot's guarantees.

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

The `v0.4` migration rebuilds the referenced `agent_runs` and `messages` tables
on one dedicated connection with foreign-key enforcement temporarily disabled,
then verifies the complete foreign-key graph before commit and restores
enforcement before connection reuse. It admits the code-defined runtime hard
ceilings and 128 KiB final assistant Messages while preserving the 64 KiB user
and system-notice limit. Any copy, graph, commit, or restoration failure leaves
the prior schema and migration ledger intact.

Migration 6 replaces the single-origin consent row with fixed `agent` and
`approval_reviewer` role keys, adds profile/role/invocation/cost metadata to
model requests, and adds the bounded `session_context_summaries` table. Legacy
consent and model-request rows receive conservative Agent-role metadata; loading
still applies the current policy/category checks before any transfer. The
migration stores no framework message, assembled prompt, Tool transcript,
credential, or generic payload.

Migration 7 makes every legacy pending or approved restart request terminal,
archives the legacy restart tables, and creates the generalized approval,
decision, and Reviewer-metadata tables. An approval stores explicit bounded
ActionEnvelope metadata plus a typed-parameter kind and SHA-256 digest; it does
not store raw parameters, executable, argv, command, environment, output, or a
generic payload. A Reviewer record stores only bounded safe disposition or
failure metadata. Startup recovery restores no authority from either table.
Terminal rows in the legacy archive participate in the same bounded 180-day
approval cleanup as generalized approvals.

Migration 8 extends accepted Evidence with nullable exact resource-type
identity, resource-policy version and generation, and explicit partial state.
It preserves legacy Evidence as the narrower historic form and adds no raw
object, discovery response, selector, continuation token, or generic payload.

Migration 9 extends accepted Evidence with the observability-policy version,
canonical source-origin hash, normalized series identity, and observation
window. It also binds optional Prometheus and Loki origin hashes into the
role-scoped consent row and returns migrated consent to pending. It stores no
raw Event, log, metric, PromQL/LogQL, response body, credential, or continuation
token.

Migration 10 rebuilds the ToolInvocation table to admit the complete fixed
14-entry Tool catalog and rebuilds the Evidence table to admit the one exact
`kupilot.remote-diagnostics-policy/v1` provenance value. It also rebuilds the
closed approval tables so the already-admitted remote-Pod network-effect bit
requires and accepts its destination hash. It preserves all prior rows and
other constraints. ToolInvocation retains only its existing bounded canonical
argument projection; action, approval, and audit rows receive only command
identity and parameter digests. No raw remote-command chunk, archive, container
file, diagnostic-Pod body or log, credential, or generic payload is stored.

Migration 11 rebuilds only the closed approval operation and parameter-kind
constraints to admit typed remediation, exact local direct argv, and the
separate shell operation. It adds no payload column. Executable/working-
directory identity, argv, shell command, environment, external origin, process
output, mutation bodies, and Kubernetes response bytes remain represented only
by bounded identities or digests where eligible and never by raw content.

Migration 12 rebuilds the same approval/decision/Reviewer foreign-key graph to
add only the closed `observation` parameter kind used by supervised Pod-log and
optional Prometheus/Loki actions. It adds no payload column. Search text,
query/window parameters, source responses, raw logs, and credentials are not
stored; the approval row retains only the existing parameter and origin
digests. All prior rows and constraints are preserved.

Migration 13 adds the bounded `run_sequence` column to committed Message rows.
It distinguishes the initial user question, committed in-run steers, and the
terminal assistant answer without persisting pending, queued, rejected, or
recovered drafts. Legacy rows retain their established ordering and remain
readable as historic conversation content without restoring active-run
authority.

Migration 14 adds bounded purpose-specific `claim_coverage_json` and
`plan_json` columns to Diagnosis rows. New writes decode these columns only
through the fixed claim/Evidence and plan DTOs; legacy `NULL` values remain
readable. The columns contain no Evidence payload, model response object,
queue state, executable input, ActionEnvelope, approval, or resumable plan
authority.

The initial schema contains `sessions`, `messages`, `agent_runs`,
`model_requests`, `tool_invocations`, `evidence_items`, `diagnoses`, `approvals`,
`approval_decisions`, `action_reviews`, `audit_events`, and `settings`. The
common approval schema remains bound to fixed typed state rather than a generic
payload or write command. Restart, typed remediation, and default-off local
process actions use its shared dispatcher. Remote-diagnostic handlers use the
same envelope schema and inline Application supervision; their Tool call stays
blocked until the exact approval is consumed or safely closed.

The `settings` table admits only code-owned typed records. In addition to the
retention setting, `scope.last_context` schema version 1 stores one strict,
bounded JSON object containing only the last successfully verified Kubernetes
Context display name. It contains no Namespace, kubeconfig, endpoint,
credential, live client, generation, or Session reference.

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
ResourceRef projections are limited to the code-owned built-in or exact
configured resource-policy catalog frozen for the run. Namespaced Evidence
records its exact observed Namespace, which may differ from the working
Namespace only for a run whose frozen policy allowed it; cluster-scoped
references contain no Namespace. Broad resource Evidence additionally stores
the exact API identity, policy version and generation, and partial state, never
the raw Kubernetes object or continuation token.

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
made eligible and safe: user and system-notice content is at most 64 KiB, while
a final assistant Message is at most 128 KiB. A Message write checks that its
Session is active and uses standard persistence, inserts the Message, and
advances Session activity in one short transaction. Minimal-persistence
Sessions reject durable Message content. Committed history is read in bounded
ascending pages ordered by `created_at_ms, id`.

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
| Model request | Lifecycle metadata, named profile, fixed role and invocation, reserved cost units, token counts when evidenced, latency, model identifier, origin hash, and prompt or response fingerprints only. No prompt, response body, header, stream, or provider object is accepted. Model, profile, and prompt-version text are at most 128 bytes, and a provider request identifier is at most 256 bytes. |
| Session context summary | One bounded safe summary with its hash, schema/policy versions, covered first/last Message IDs, ordered count/byte count/digest, generation time, Agent profile/origin hash, and degraded/truncation markers. Text is at most 16 KiB; coverage is at most 4,096 Messages and 4 MiB. Recent-tail text remains in Message rows. |
| Action approval | Explicit bounded ActionEnvelope identity, scope and policy generations, target facts, effect bitsets, limits, typed-parameter kind and digest, nonce hash, lifecycle state, and decision metadata. No raw typed parameter, executable, argv, command, environment, external output, or generic payload is accepted. Legacy unexecuted restart authority is made terminal by migration 7. |
| Reviewer recommendation | Approval and model-request identity, profile, origin hash, policy generation, disposition and time, plus either one bounded validated safe rationale or one stable error class. No prompt, response bytes, Tool call, credential, or authority payload is accepted. |
| ToolInvocation | One of the 14 admitted Tool names, version, safe purpose, canonical arguments projection and digest, lifecycle metadata, safe summary or safe error, byte count, and truncation state. Arguments are at most 8 KiB; purpose is at most 1 KiB; safe summary and safe error are each at most 4 KiB. Context, endpoint, credential, deadline, and hard-limit authority cannot be supplied through arguments; any Namespace field remains policy-validated. |
| Evidence | A project-owned ResourceRef projection, optional exact API identity and resource-, observability-, or remote-diagnostics-policy version/generation, category, concise fact, source path, severity, partial/redaction/truncation state, fingerprint, and observation time. A fact is at most 2 KiB, a source path at most 1 KiB, and one ToolInvocation may own at most 100 Evidence items. Raw remote output and remote-output excerpts are never stored; those facts contain only safe target metadata, counts, and a fingerprint. |
| Diagnosis | Validated free-form Markdown, typed claim/Evidence coverage, an optional inert bounded plan, typed not-executed proposed actions, validation warnings, compatibility metadata, and an exact historic Evidence window. The complete validated Domain record is at most 128 KiB. Every retained current observation and confirmed fact cites same-run Evidence when written. |
| AuditEvent | A fixed event type, actor, outcome, optional scope and subject, and a typed scalar detail object. Detail and subject projections are each at most 4 KiB, and a correlation identifier is at most 128 bytes. Minimal persistence admits only fixed lifecycle, consent, policy, degraded-storage, approval, and write-safety event types. |
| Setting | `retention.operational_detail_days` is a schema-version-1 integer from 0 through 3,650. `scope.last_context` is a schema-version-1 strict JSON object containing one Context display name. Both use injected UTC update time. Unknown and credential-shaped keys are rejected before SQL; the schema repeats the 64-byte key, 4 KiB JSON, and sensitive-key constraints. |

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
| Approval and supervised-action `write_*` AuditEvents | 180 days from `occurred_at_ms`. |

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
graph, including model metadata, ToolInvocations, Evidence, Diagnoses, terminal
approval rows, and Session- or AgentRun-linked AuditEvents, through foreign-key
cascade in one short transaction. Global typed settings are not Session-owned.

Physical-file conformance tests place distinct synthetic prohibited-content
canaries outside eligible DTOs, persist safe derivatives, and scan both the
database and WAL. Static guards keep sqlx imports inside the SQLite adapter and
reject `SELECT *`, unsafe or panic-style helpers, unbounded selection, generic
map boundaries, formatted SQL, and non-Context database calls in repositories.
Composition tests prove that only the Application approval coordinator can
reach the restart, typed-remediation, or local-process executors, and only after
durable single-use approval consumption and pre-operation audit. Static import
guards keep production `os/exec` in the local executor adapter, apart from the
separately documented kubeconfig exec-credential boundary.

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
