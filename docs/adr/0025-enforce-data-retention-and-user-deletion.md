# ADR-0025: Enforce Data Retention and User Deletion

- Status: Accepted
- Date: 2026-08-08

## Context

Safe local conversation history provides continuity, but it still contains user
questions, cluster names, projected observations, and model conclusions.
Operational detail, read audit, and future write audit have different purposes
and should not all live forever. Minimal-persistence and explicit deletion must
also have one meaning across Application, SQLite, CLI, and TUI.

## Decision

KuPilot adopts the normative
[Data Retention Contract](../data-retention.md) with these accepted defaults:

- Session metadata, sanitized user Messages, final validated assistant Messages,
  and Diagnosis remain until the user deletes the Session or clears history.
- Sanitized ToolInvocation detail, accepted Evidence, and model-request metadata
  remain for 30 days. The public control may only shorten the current value,
  including to zero days, without making any prohibited raw category eligible.
- Ordinary `v0.1` read and lifecycle AuditEvents remain for 90 days.
- Terminal `v0.2` approval and decision records, and approval, write-intent,
  write-attempt, and verification AuditEvents remain for 180 days. Retention
  cleanup never removes pending or approved authority; startup recovery first
  makes those requests terminal.
- Raw container output, full Kubernetes objects, assembled prompts, raw model
  streams or responses, raw Tool output, and credentials remain for zero days:
  they are never persisted.

The future 60-second approval execution TTL is independent of the 180-day audit
default.

If 30-day detail expires while the Session and Diagnosis remain, the historic
Diagnosis says that its supporting detail was removed by policy. Historic text
does not become current Evidence for another AgentRun.

Minimal-persistence stores only a necessary non-resumable Session shell, minimum
run lifecycle and audit fields, the consent tuple, and future approval/write
audit. It stores no user or assistant Message content, final answer, Diagnosis,
Tool detail, Evidence, or model-request detail. A minimal Session never appears
in a picker or `--last`; exact-ID resume returns `session_not_resumable`.
The run keeps an opaque request identity for lifecycle correlation but has no
retained-Message relationship and creates no Message row.

The existing `/privacy` dialog displays the current persistence mode, the
effective 30/90/180-day category periods, and minimal mode's non-resumable
effect. Its retention command is an atomic compare-and-tighten operation and
cannot increase the current value. Its mode command starts a new Session rather
than changing an existing Session's frozen mode. The current Session and a
resume-picker selection are the only per-Session deletion targets; KuPilot adds
no Session-management page or second composer.

Automatic purge runs at validated startup and during bounded idle batches. It
uses category timestamps and explicit relationships. A terminal approval is
removed only after every related AuditEvent has expired, and its decision then
cascades in the same transaction. It does not run frequent automatic `VACUUM`;
checkpoint, `secure_delete`, sidecar, and compaction behavior must satisfy
ADR-0018's driver requirements.

Deleting one Session cascades through Messages, runs, model metadata,
ToolInvocations, Evidence, Diagnoses, approval records, and linked read/write
audit. This deletion may occur before 90 or 180 days because KuPilot is not a
compliance ledger. Clear-history removes every Session graph. Delete-all local
state additionally removes settings and consent from the validated KuPilot data
paths. A failed deletion transaction is reported as not deleted.

Deletion requires an explicit target-bound confirmation. A starting, active, or
terminal-but-not-yet-quiesced run is cancelled and awaited first. Pending and
approved-but-not-executed approvals are durably cancelled; a consuming approval
or approval-persistence
failure denies graph deletion. If durable approval cancellation fails, its
in-memory authority is still removed so the failure leaves no executable
approval in the process. A graph commit is the only deletion success signal.
Cancellation, a database failure, or a restart cannot produce a partial-success
claim or restore run or approval authority.

Storage failure behavior remains asymmetric:

- A BeginRun transaction failure stops before model or Tool I/O, preserving the
  durable-start invariant.
- A later persistence failure may let the already-started read-only Diagnosis
  finish in memory with prominent `persistence_degraded` state and no false
  resume claim.
- A `v0.2` approval or pre-write intent-audit failure produces zero writes. A
  result-audit failure never triggers an automatic write retry.

Logical deletion and file removal are not described as forensic erasure from
SQLite free pages, WAL, backups, snapshots, swap, or storage media.

ADR-0034 separately admits one explicitly confirmed versioned redacted Markdown
summary outside SQLite. That user-controlled file does not change SQLite
eligibility or retention, is never available for minimal Sessions, and is not
removed when its source Session is later deleted.

## Consequences

Positive consequences:

- Conversation history, operational detail, read audit, and write audit have
  explicit and testable lifetimes.
- Minimal-persistence has a precise non-resumable meaning.
- Users can remove one Session or all locally owned history, including audit.
- Storage degradation cannot be hidden behind optimistic resume or write claims.

Costs and constraints:

- Safe conversation content remains indefinitely by default until user action.
- After 30 days, a Diagnosis may remain while its detailed Evidence is no longer
  inspectable.
- A previously stored bounded operational-detail value above the default remains
  visible until tightened, but the public control cannot create or increase it.
- Purge, cascade, WAL, clock, and failure behavior require real-file tests.

## Alternatives considered

- Expiring every Session after 30 days was rejected because the accepted product
  keeps safe conversation history until user deletion.
- Keeping every category until deletion was rejected because detailed Evidence
  and Tool/model metadata have higher volume and exposure.
- Using one audit period was rejected because supervised writes need a longer
  default history than read-only lifecycle events.
- Keeping write audit after explicit Session deletion was rejected because this
  personal tool is not a compliance ledger and the accepted schema cascades.
- Making minimal-persistence resumable was rejected because it has no durable
  conversation or Diagnosis content.
- Claiming secure erase was rejected because general SQLite and filesystems
  cannot provide that portable guarantee.

## Security and privacy impact

Retention never makes an excluded source eligible. Source denial, projection,
sensitive-value handling, and size limits happen before persistence. The SQLite
database remains unencrypted; owner-only permissions, operating-system disk
encryption, and user-controlled backup policy remain important.

Minimal-persistence cannot weaken future durable pre-write audit. Explicit user
deletion can remove that local audit afterward, and the confirmation must make
the loss clear.

## Validation

Deterministic driver, repository, integration, and user-interface tests must
cover:

- Exact 30-, 90-, and 180-day boundaries, zero-day detail, stale-setting
  conflicts, and zero writes for every attempted retention increase.
- Standard content retained until explicit deletion.
- Minimal rows, absence from picker and `--last`, and exact-resume rejection
  with `session_not_resumable`.
- Detail purge leaving an explicit historic Diagnosis marker.
- Transactional Session cascade through approval and audit rows.
- Every cleanup, deletion, begin, update, and pre/post-write audit failure, with
  the required model, Tool, and executor call counts.
- Database and WAL canary absence for every prohibited category.

Driver-specific PRAGMA and checkpoint behavior must satisfy ADR-0018.

## Revisit triggers

- A default lifetime changes or a new durable category is proposed.
- The summary export expands beyond ADR-0034, or backup, synchronization,
  telemetry, crash reporting, shared state, or encrypted storage is proposed.
- Minimal-persistence becomes resumable or a no-database mode is proposed.
- A compliance requirement would prevent user cascade deletion of write audit.

## References

- [Data Retention Contract](../data-retention.md)
- [Security Threat Model](../security.md)
- [Privacy Overview](../privacy-overview.md)
- [ADR-0008: Use SQLite for Local Persistence](0008-use-sqlite-for-local-persistence.md)
- [ADR-0012: Require Digest-Bound Approval for Writes](0012-require-digest-bound-write-approval.md)
- [ADR-0018: Require One Pure-Go SQLite Driver](0018-require-one-pure-go-sqlite-driver.md)
- [ADR-0034: Export Only Versioned Redacted Session Summaries](0034-export-only-versioned-redacted-session-summaries.md)
