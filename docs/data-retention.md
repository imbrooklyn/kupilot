# Kupilot Data Retention Contract

- Status: Accepted for `v0.4`
- Date: 2026-08-30

This document defines what Kupilot may persist, the default lifetime of each
eligible category, the exact meaning of minimal-persistence, deletion behavior,
and degraded SQLite behavior. It is a data-eligibility contract, not a database
schema. A schema may omit an eligible field, but it may not add a category that
this contract excludes.

Kupilot uses a local SQLite database. It does not claim that the database is
encrypted, tamper-resistant, a credential store, or capable of forensic
erasure. A separate fixed Home configuration may contain a model API key only
after the user chooses disclosed plaintext storage; that exception never makes
the key eligible for SQLite. Local controls are source exclusion, projection,
create-only owner modes on supported platforms, bounded detail retention, user
deletion, and visible failure. Existing user-managed modes, operating-system
disk encryption, and backup lifecycle remain the user's controls.

## 1. Normative principles

1. Durable data is allowlisted. A generic text, JSON, metadata, debug, error, or
   attachment column cannot make an excluded value eligible.
2. Source allowlisting, projection, normalization, sensitive-value handling, and
   byte limits run before an eligible value reaches a repository operation.
3. Standard persistence keeps safe conversation history. Detailed operational
   evidence has a shorter default lifetime than the conversation it explains.
4. Minimal-persistence keeps conversation content in process memory only. It
   creates no cross-process resumable Session.
5. Raw transport, framework, Kubernetes object, Event, and container-output data
   is never durable, even when a run or redactor fails.
6. Retention uses UTC timestamps. Durable IDs are application-generated UUIDv7
   text; SQLite time values are Unix milliseconds in UTC. TUI display may convert
   time to the user's local timezone.
7. User deletion is transactional and may remove records before their automatic
   retention deadline. A deletion is reported successful only after commit.
8. A storage failure can degrade an already-started investigation, but it
   cannot enable a model call before the required run-start transaction or a
   supervised write before durable pre-operation audit.
9. Primary-terminal scrollback is a terminal-owned display consequence, not a
   Kupilot-created durable store. Kupilot deletion controls cannot retract
   already displayed text from a terminal emulator, multiplexer, recorder,
   backup, or remote session.

## 2. Standard-persistence defaults

<!-- markdownlint-disable MD013 -->

| Category | Default lifetime | Authoritative clock and behavior |
| --- | --- | --- |
| Last successfully verified Kubernetes Context preference | Until it is replaced by a later successful activation or the user deletes all local state | The single global Context display name is not Session-owned. Clear-history preserves it; delete-all-local-state removes it with SQLite. |
| Session shell, safe Session summary, committed user Messages, final validated assistant Messages, and validated Diagnosis | Until the user deletes the Session or clears history | There is no automatic age expiry in the accepted default. Viewing or resuming does not create a second copy or restore live authority. |
| Sanitized ToolInvocation detail, accepted Evidence, and model-request metadata | 30 days | Measured from the owning invocation, observation, or request completion time. The public control may only shorten the current value, including to 0 days, which keeps detail only in process memory. |
| Ordinary read and lifecycle AuditEvents | 90 days | Measured from `occurred_at`; user Session deletion may remove them earlier through cascade. |
| Terminal approval and decision records, and approval, pre-write intent, write-attempt, and verification AuditEvents | 180 days | Measured from the relevant state or event time; user Session deletion or clear-all may remove them earlier because Kupilot is not a compliance ledger. Pending and approved requests are first made terminal by their owning lifecycle, never by retention cleanup. |
| Explicit `kupilot.export-summary.v2` Markdown file | Until the user removes the separately published file | This user-controlled copy is outside SQLite retention. Later Session deletion does not remove it. |
| Optional plaintext model profile in `KUPILOT_HOME/config.yaml` | Until the user overwrites or removes the local configuration | This user-selected credential copy is outside SQLite and Session retention. Process-only setup and environment loading do not create it. |
| Model-transfer consent | Until revoked, local state is cleared, or its exact tuple is invalidated | The stored record contains policy version, decision state and time, endpoint-origin hash, and the exact eligible-category set. Any origin, category, or policy-version change requires confirmation again. |
| Schema version, migration checksum, and maintenance metadata | Lifetime of the database | These records contain no user, model, or cluster content and disappear with delete-all local state. |

<!-- markdownlint-enable MD013 -->

The separate allowlisted local application log is not Session persistence. TUI
mode uses bounded `info` logging by default and lets the user disable it. Default
records contain no request or response body, Tool arguments, raw object, raw
container output, credential, or arbitrary error text. They may contain the
bounded safe model-failure projection from ADR-0036: local request ID, stable
error metadata, observed HTTP status, fixed cause category, and a sink-generated
project-function chain without files, lines, arguments, or values.

Explicit `logging.sensitive_diagnostics` adds only the ADR-0036 bounded model-
failure endpoint, model, error-chain, failed-response prefix, and Go stack
fields. These records remain outside SQLite and Session deletion. They use the
same three-file, 1 MiB-per-file, seven-day rotation and are not removed merely
by disabling the setting. The user controls earlier deletion of the log files.
The file sink remains disabled unless all file-count, byte, age, and sensitive-
field ceilings are fixed, documented, and tested.

The 60-second approval execution TTL is not a retention period. It limits when a
specific proposal may execute; its terminal audit record follows the 180-day
default unless the user deletes it earlier.

If 30-day Tool, Evidence, or model-request details expire while the Session and
Diagnosis remain, the UI must say that supporting detail was removed by policy.
It must not render a broken Evidence reference as current proof or silently
invent replacement detail.

The `/privacy` control may only lower the current operational-detail value; it
never raises it. A previously stored bounded value above the 30-day default is
read and displayed accurately until the user tightens it, but the public control
cannot create or increase such a value. Retention changes use an atomic
compare-and-tighten operation so a stale review cannot overwrite a stricter
concurrent value. Defaults remain 30, 90, and 180 days.

## 3. Eligible standard data

### 3.1 Session and Message

Standard persistence may store:

- One global startup preference containing only the last successfully verified
  Kubernetes Context display name. It contains no Namespace, kubeconfig,
  endpoint, credential, client, generation, ResourceRef, or Session identity.
- Opaque Session and Message identifiers, status, version, and UTC timestamps.
- A user-selected title or locally derived bounded safe title.
- Privacy mode and a safe last-scope candidate containing Context and working
  Namespace display values, never a live generation, namespace-access
  authority, or client.
- An optional safe ResourceRef candidate containing allowlisted Kind,
  Namespace, name, and optional UID or resource version.
- Committed user message content after sensitive-value replacement and explicit
  user opportunity to cancel the submission.
- Final locally validated assistant content. Partial streams and invalid model
  drafts are not committed Messages.
- A bounded safe Session summary.

Resume reconstructs a conversation container only. It never restores a client,
live ClusterScope generation, model stream, Agent loop, ToolInvocation,
cancel function, pending approval, or write. Saved scope and ResourceRef values
must be selected and revalidated before a new run.

### 3.2 AgentRun and model-request metadata

Eligible run data is limited to:

- Run and Session identity, request Message identity, state, safe termination
  reason, timestamps, safe working-scope snapshot, optional ResourceRef,
  counters, truncation flags, prompt and capability-catalog versions, and
  `persistence_degraded` state.
- Model-request identity, sequence, fixed provider kind, model identifier,
  endpoint-origin hash, status, stable error class, bounded validated provider
  request identifier, prompt and response fingerprints, optional token counts,
  and timing metadata.

There is no durable assembled prompt, raw request, raw response, stream, header,
or endpoint error body. The endpoint origin itself belongs to typed local
configuration; the database stores only the consent and request hashes needed by
the accepted contracts.

### 3.3 ToolInvocation and Evidence

Eligible operational detail is limited to:

- Tool name and schema version, sequence, bounded purpose, internally injected
  safe run scope, exact target Namespace where applicable, canonical sanitized
  arguments and digest, status, timestamps,
  stable error class, safe summary, byte and Evidence counters, and truncation
  metadata.
- Accepted Evidence identity and provenance, safe ResourceRef and source path,
  concise projected fact, observation time, optional resource version,
  deterministic severity, redaction and truncation metadata, and normalization
  fingerprint.

There is no generic Tool input or ToolResult body. A concise Evidence fact may be
derived from an eligible Event or bounded container output, but the source
payload and excerpt are not durable. Model-supplied scope, endpoint, credential,
deadline, arbitrary Kind, access policy, and hard limits are not model-supplied
canonical Tool arguments. An explicit target Namespace is canonical only after
runtime policy validation.

### 3.4 Diagnosis

An eligible Diagnosis contains the bounded validated free-form Markdown answer,
claim-to-Evidence citations, typed proposed actions, observed scope and time
range, and validation warnings. Legacy four-collection fields may remain
readable for historic rows but are not required or rendered for new answers.

The Diagnosis may outlive its 30-day Tool and Evidence detail. After detail
purge, historic answer text remains historic Session content and must show that
its supporting detail expired. It cannot become current Evidence for a new run.

### 3.5 Audit and consent

An AuditEvent contains only a stable event type, UTC time, safe outcome, actor
category, correlation identifier, optional Session or run identity, optional
safe scope or ResourceRef, and event-specific allowlisted scalar details. It is
not an arbitrary logging channel and is not claimed to be append-only or
tamper-resistant.

A consent record contains the policy version, decision time, endpoint-origin
hash, and eligible-category flags. It contains no endpoint credentials, API key,
headers, model body, user question, or cluster data. It authorizes only the exact
tuple and does not authorize a later category automatically.

### 3.6 Approval and write audit

Admitted action records may contain the fixed operation, immutable safe
scope, exact target identity, canonical safe parameters, template fingerprint,
operation digest, policy version, expiry, one-time state, bounded risk summary,
decision, pre-operation intent outcome, external request outcome, and bounded
verification outcome.

They contain no arbitrary patch, raw Deployment, client-go value, credential,
request body, or response body. Only a hash of the UI nonce is durable. Request
acceptance, observed rollout progress, timeout, failure, and verified completion
remain separate states.

## 4. Minimal-persistence

Minimal-persistence is selected before an AgentRun starts and is frozen in its
RunInput. It is not a silent fallback after a standard-persistence failure.

SQLite may store only:

- A necessary non-resumable Session shell: opaque ID, privacy mode, status, and
  creation and terminal timestamps.
- The minimum run-start and terminal fields required for interruption recovery:
  run, Session, and opaque request identity; safe scope snapshot; state;
  timestamps; stable termination reason; and `persistence_degraded` state.
- Minimal allowlisted AuditEvents needed to explain lifecycle, consent, scope,
  policy denial, or a supervised write.
- The consent tuple from Section 3.5.
- The complete allowlisted approval and write audit metadata from
  Section 3.6. Privacy mode never weakens the durable pre-write gate.

The following remains in memory only and is discarded at process exit:

- User and assistant Message content, including the final answer.
- Diagnosis content and rendered answer.
- ToolInvocation purpose, arguments, summaries, and detail.
- Evidence and model-request detail.
- Safe model context assembled for the current run.

The opaque request identity in a minimal run is lifecycle correlation, not a
retained Message. Its retained-Message relationship is absent, and no Message
row or content hash is created. Standard runs keep the same identity plus a
foreign-key relationship to the committed request Message.

A minimal Session never appears in the resume picker, exact-ID resume, or
`--last`. Exact-ID lookup returns the stable `session_not_resumable` outcome
rather than pretending an empty history was restored. Startup may mark a
durably recorded run `interrupted`; it never reconstructs content or continues
execution.

Minimal Session shells remain only while their required audit or recovery record
is retained: 90 days for read lifecycle or 180 days when linked to a supervised
write record, unless the user deletes them earlier. When the last required record
expires, cleanup removes the shell in the same bounded purge flow.

## 5. Data excluded from SQLite and ordinary sinks

Except for the explicitly selected plaintext model key in the fixed Home
configuration, the following has a retention period of zero and is never
eligible for SQLite, ordinary typed configuration values, an application log,
a crash bundle, or another Kupilot-created durable store:

- Kubeconfig contents; bearer tokens; ServiceAccount token material; client
  certificates; private keys; exec credential output; cookies; and
  authentication headers.
- Model API keys from every source outside the explicit Home configuration
  save. Even when locally saved, the key is never eligible for SQLite, Session
  content, audit, logs, exports, model content, or generic configuration values.
- Kubernetes Secret objects or data, ConfigMap data, container environment
  values, referenced credential values, and every source Kupilot is forbidden to
  read.
- Raw Kubernetes objects, full YAML, managed fields, unrestricted labels or
  annotations, discovery bodies, raw API request or response bodies, and
  client-go values.
- Raw or complete sanitized container output, raw Event payloads, raw Tool
  results, and adapter-local source DTOs.
- Assembled prompts, system instructions, raw model requests or responses, model
  SDK values, stream deltas, invalid drafts, partial assistant Messages, and
  capability-check bodies.
- Process environment snapshots, value-bearing CLI arguments, SQL bind values in
  debug output, raw database rows outside explicit mappings, and raw application
  logs. ADR-0036 admits only its safe function-name chain by default and its
  bounded model error, failed-response prefix, and current-goroutine stack when
  the user explicitly enables sensitive diagnostics.
- Terminal byte streams, escape sequences, clipboard or device-control content,
  and model-selected styling.
- Live clients, HTTP transports, database handles, transactions, Contexts,
  cancellation functions, callbacks, channels, goroutines, framework messages,
  and live scope authority.

The terminal-byte exclusion means Kupilot does not copy terminal output into
SQLite, logs, exports, crash bundles, or another generic durable sink. It does
not mean displayed text vanishes: the primary terminal, multiplexer, or session
recorder may keep safe rendered conversation in its own scrollback after
process exit. Session deletion, clear-history, delete-all-local-state, and
minimal persistence do not control that external retention.

Kubeconfig paths are not stored in SQLite or ordinary logs. A user-requested
local diagnostic view may show a safely resolved path without making it model or
history content. Sensitive data accidentally observed in an eligible free-text
source is replaced or blocked before the durable value is constructed; the
original is not kept for debugging.

## 6. Size limits

Persistence is not a blob store. The implementation must enforce at least these
per-record or aggregate maxima before a repository call:

- One committed user or system-notice Message: 64 KiB.
- One committed final assistant Message: 128 KiB.
- One complete validated Diagnosis, including Markdown and metadata: 128 KiB.
- One safe summary: 4 KiB.
- One Evidence fact: 2 KiB.
- One complete `kupilot.export-summary.v2` Markdown file: 2 MiB after the final
  redaction and output guard.

The capability and run byte budgets in ADR-0039 remain independent and may
impose a smaller bound. Oversized input is rejected or explicitly truncated
according to its field contract; it is never silently moved to a raw attachment.

### 6.1 User-controlled redacted summary export

ADR-0041 admits one durable output outside SQLite: an explicitly confirmed,
versioned Markdown summary of the current resumable standard-persistence
Session. Application projects a consistent SQLite snapshot through an explicit
allowlist. Only safe Session display metadata, committed user and final
assistant text, validated answer metadata and legacy compatible Diagnosis
fields, and referenced Evidence summaries or expired markers are eligible.
Every eligible free-text field is redacted and bounded before rendering, and
the complete document is processed and capped again.

Raw Tool input or output, raw or complete logs, raw Events, Kubernetes objects,
full prompts, model traffic, credentials, Secrets, kubeconfig data or paths,
approval authority, and arbitrary repository serialization remain ineligible.
Minimal Sessions have no retained content eligible for this export.

The content-free pre-export AuditEvent is ordinary read audit and follows its
90-day retention or earlier Session deletion. It records no output content or
target-path detail. The exported file has no automatic Kupilot retention: it is
a separate user-controlled copy and survives source Session deletion. Owner-only
permissions and atomic no-replace publication do not provide encryption,
tamper resistance, or forensic erasure.

## 7. Automatic retention enforcement

Retention uses an injected UTC clock so cutoff behavior is deterministic in
tests. Cleanup runs:

1. At validated database startup after migrations and interrupted-run recovery,
   before history is returned or a new durable run starts.
2. During idle periods in small batches with a Context, row, and elapsed-time
   limit.
3. Before a resume query when the last successful cleanup no longer satisfies
   the implementation's bounded freshness policy.

Cleanup uses category-specific timestamps and explicit relationships, not a
search through serialized content. In the same transaction, it reads the
current typed operational-detail setting and applies that cutoff to expired
ToolInvocations, Evidence, and model-request metadata; it removes ordinary read
AuditEvents at 90 days and approval/write AuditEvents at 180 days. After every
related AuditEvent has expired, it removes terminal approval and decision
records at the same 180-day boundary. It never removes pending or approved
authority; startup recovery first makes those requests terminal. Cleanup then
removes an otherwise empty minimal Session shell when no required retained
record needs it.

The remaining Diagnosis records that operational detail was removed by policy.
Cleanup never changes a historic item into current Evidence.

A cleanup transaction failure rolls back that batch, produces a safe storage
error, and does not report the affected rows as deleted. Kupilot does not raise
a retention value silently. Automatic frequent `VACUUM` is prohibited. An
explicit maintenance command or tested size threshold may be added when driver
behavior satisfies ADR-0018.

## 8. User-requested deletion

### 8.1 Delete one Session

Deleting a Session removes the complete Session-owned graph in one transaction:

- Messages and Session summary.
- AgentRuns and model-request metadata.
- ToolInvocations, Evidence, and Diagnoses.
- Session-linked approval and decision records.
- Session- and run-linked read, approval, write, and verification AuditEvents.
- The Session row and every resume index entry.

This cascade is intentional: Kupilot is a personal local application, not a
compliance ledger. A 90- or 180-day audit default does not override explicit
user deletion. If any required step fails, the transaction rolls back and the UI
states that the Session was not deleted.

The deletion command follows this fail-closed state machine:

<!-- markdownlint-disable MD013 -->

| State before deletion | Required transition | Result visible to the user |
| --- | --- | --- |
| Confirmation is cancelled | Send no Application deletion command and perform no repository or executor call. | The current `/privacy` review or resume picker remains open. |
| Current Session has a starting or active AgentRun | Cancel the exact run and wait for bounded termination before the graph transaction. | Failure or caller cancellation reports not deleted; no new run may start inside the deletion operation. |
| Session has a pending or approved-but-not-executed approval | Durably cancel each approval before the graph transaction. If that durable close fails, remove in-memory authority and deny deletion. | No approval left by the deletion path is executable, and the executor receives no call from deletion. |
| A matching approval is already consuming | Deny graph deletion; do not reinterpret or retry the already consumed operation. | The Session remains and deletion reports failure. |
| Repository deletion fails or the Context is cancelled | Roll back the Session graph transaction. Prerequisite run or approval cancellation that already committed remains terminal. | The UI keeps the Session and reports not deleted, never partial success. |
| Repository deletion commits | Accept only the matching request and Session identity, then clear current or picker state. | The UI reports logical deletion and no forensic-erasure claim. |
| Process restarts before a later deletion attempt | Normal startup recovery makes persisted running runs interrupted and pending or approved-but-not-executed approvals terminal. It restores no execution authority. | A new explicit confirmation is required. |
| Retention cleanup races with explicit deletion | SQLite serializes the transactions. If cleanup commits first and removes the target shell, the explicit delete cannot claim that it deleted one Session; if explicit deletion commits first, cleanup observes no graph. | Only a committed matching delete is reported as deletion success. |

<!-- markdownlint-enable MD013 -->

Deletion stores and logs no deleted content and creates no content-bearing
tombstone. Delivery and repository errors use stable content-free classes.

### 8.2 Clear history and delete all local state

Clear-history removes every Session graph and associated audit record through
bounded transactions. The fixed Home configuration, including a locally saved
plaintext model key, is outside that SQLite operation. The global remembered
Context preference is preserved. A still-valid consent record remains only
when the UI says so explicitly.

A separate delete-all-local-state operation closes database handles and removes
the resolved Kupilot database and known journal, WAL, and shared-memory sidecars.
It includes settings and consent. The operation targets only validated database
paths and never follows symlinks or recursively deletes an arbitrary directory.
It does not remove Home configuration, cache, logs, exports, or backups. Any
incomplete removal is reported; Kupilot does not create replacement state in
the same operation. `kupilot cache clear` is a separate fixed operation that
removes only cache entries.

### 8.3 Deletion limitations

Row deletion and file removal do not guarantee that old bytes are unrecoverable
from SQLite free pages, WAL, filesystem journals, snapshots, backups, swap, or
storage media. Driver-specific `secure_delete` and checkpoint behavior must not
be described as secure erasure. Users who require stronger protection must use
operating-system disk encryption and manage backups and snapshots.

## 9. Degraded SQLite policy

Storage state is explicit: `healthy`, `degraded`, or `unavailable`. Application,
not the SQLite adapter, decides whether a use case may continue.

<!-- markdownlint-disable MD013 -->

| Failure point | Required behavior |
| --- | --- |
| State-directory or file validation, database open, schema compatibility, migration, integrity check, or interrupted-run recovery | Mark storage unavailable. Do not return history or silently replace the database. Show a stable safe recovery error. |
| Mandatory startup cleanup | Mark storage unavailable for new durable work and resume results until a bounded cleanup succeeds; do not pretend expired detail was removed. |
| BeginRun transaction in standard or minimal mode | Abort before the first model request or Tool call, preserving the durable-start invariant. Do not silently change the selected privacy mode. |
| Standard-persistence write after a run started durably | Roll back that transaction, mark the run `persistence_degraded` where possible, and show a persistent warning. The already-started investigation may finish in memory; its missing durable content is not claimed resumable. |
| Minimal-persistence terminal or required audit write | Show degraded state and do not claim terminal metadata is durable. The already-started investigation may finish in memory. |
| History-only read | Fail the query safely; never return a partial or stale resume list. A new run is independent only if all mandatory startup and BeginRun gates still succeed. |
| Explicit Session deletion or clear-history | Roll back the failed transaction and report not deleted. Do not hide a resume entry independently. |
| Action proposal, decision, approved state, or pre-operation intent audit write | Fail closed with zero executor calls. |
| Action result audit after the one external request | Never retry the external write automatically. Show request outcome as unknown or verification unavailable as appropriate, retain the durable pre-operation record, and retry only the audit write a fixed bounded number of times. |

<!-- markdownlint-enable MD013 -->

After a mid-run persistence failure, Kupilot does not claim that it saved the
final answer or that the Session can resume the missing turn. A later new run is
allowed only after a bounded storage health check and the normal BeginRun
transaction succeed.

On restart, every durably `running` AgentRun becomes `interrupted`, and every
pending or approved-but-not-executed approval becomes terminal and non-executable.
Startup never resumes an Agent loop, ToolInvocation, model stream, Kubernetes
call, approval wait, or write request.

Kupilot does not automatically delete, rename, overwrite, or recreate a database
it cannot validate. Recovery that could discard data requires an explicit user
operation and a separately reviewed implementation.

## 10. Deterministic conformance tests

The persistence implementation must prove with a real temporary SQLite file,
fake clock, and failure injection:

- Standard mode retains safe Messages, final answer, and Diagnosis until user
  deletion while applying the configured operational-detail cutoff.
- Exact behavior immediately before, at, and after 30-, 90-, and 180-day
  cutoffs, including operational detail configured to zero and longer than the
  default.
- An expired Evidence detail leaves an explicit historic Diagnosis gap rather
  than a current or dangling proof claim.
- Minimal mode creates exactly the Session shell, recovery fields, consent, and
  minimum audit rows; no message, final answer, Diagnosis, Tool, Evidence, or
  model-request detail is durable or resumable.
- Picker, exact ID, and `--last` all exclude minimal Sessions according to their
  typed outcomes.
- Session deletion cascades through model, Tool, Evidence, Diagnosis, approval,
  and audit rows in one transaction; clear-history and delete-all target only
  validated Kupilot state.
- A failure injected at every migration, recovery, cleanup, begin, update,
  deletion, approval, and audit boundary produces the call counts in Section 9.
- Startup interruption recovery never restores live scope or external I/O and
  never replays a write.
- Owner-only directory, database, journal, WAL, and shared-memory permissions
  hold on supported platforms, and unsafe symlink fixtures are rejected.
- Adversarial text remains a bound SQL value and cannot select a query, table,
  column, pragma, migration, or order expression.
- Distinct generated canaries for every prohibited category are absent from all
  repository inputs, logical rows, database bytes, and WAL inspected by tests.

The pure-Go driver, PRAGMA, sqlx bind type, checkpoint, sidecar, and permission
behavior must satisfy ADR-0018 and ADR-0030.

## 11. Revisit triggers

An Accepted ADR and updates to the threat model and this contract are required
before:

- Changing a default lifetime, removing user cascade deletion, or persisting a
  new category.
- Expanding the accepted summary export with another format, source field,
  overwrite mode, automatic destination, multiple Sessions, or remote transfer;
  or adding backup, synchronization, telemetry, crash reporting, shared state,
  an encrypted database, or a server.
- Making minimal-persistence resumable or adding a no-database mode.
- Retaining raw prompts, raw model responses, raw Tool output, raw container
  output, or credentials for debugging.
- Selecting a storage driver whose journal, locking, deletion, or permission
  behavior cannot satisfy this contract.

## References

- [Security Threat Model](security.md)
- [Architecture](architecture.md)
- [Privacy Overview](privacy-overview.md)
- [Scope](scope.md)
- [ADR-0008: Use SQLite for Local Persistence](adr/0008-use-sqlite-for-local-persistence.md)
- [ADR-0018: Require One Pure-Go SQLite Driver](adr/0018-require-one-pure-go-sqlite-driver.md)
- [ADR-0025: Enforce Data Retention and User Deletion](adr/0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0030: Use sqlx Inside the SQLite Adapter](adr/0030-use-sqlx-inside-the-sqlite-adapter.md)
- [ADR-0041: Export Free-Form Session Summaries](adr/0041-export-free-form-session-summaries.md)
- [ADR-0042: Remember the Last Verified Kubernetes Context](adr/0042-remember-the-last-verified-kubernetes-context.md)
