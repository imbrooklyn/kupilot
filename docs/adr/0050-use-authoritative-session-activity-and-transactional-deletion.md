# ADR-0050: Use Authoritative Session Activity and Transactional Deletion

- Status: Accepted
- Date: 2026-09-07
- Amends: ADR-0025, ADR-0031, ADR-0038, ADR-0039, ADR-0040, ADR-0041, ADR-0043, ADR-0047, ADR-0048, ADR-0049

## Context

Kupilot already stores one local Session graph and supports explicit resume,
current-Session deletion from the privacy review, and whole-history deletion.
Those controls are not sufficiently discoverable, and `sessions.updated_at_ms`
mixes optimistic metadata changes with user-visible activity. It therefore
cannot prove the ordering and cutoff semantics required by a destructive
inactive-Session operation.

Session deletion also needs one authority boundary. Delivery may collect a
confirmation, but only Application can decide that a Session is inactive and
deletable, freeze a selection, and ask SQLite to commit an exact graph cascade.
Deleting an active current Session by cancelling work would make deletion an
implicit run-control operation and could erase input after a failed commit.

The interaction reference inspected for this decision is Codex CLI
`rust-v0.153.4`, peeled commit
`3d2ee51ca2d5db578f328aa75e20aa22c0197c9a`. Kupilot reuses only the general
ideas that current-conversation deletion is unavailable while work is running
and that deletion requires a preview and explicit confirmation. Name-based
deletion, archive, descendant threads, remote deletion, `--force`, `--yes`,
side conversations, and Codex product wording are not adopted.

## Decision

Kupilot adds fixed `/delete` and `/sessions` TUI commands plus
`kupilot sessions list` and `kupilot sessions delete`. Application owns their
typed commands, queries, eligibility, cutoff parsing, frozen plans, and
content-free digests. SQLite implements narrow query and transactional graph
deletion ports. TUI and CLI remain delivery adapters.

### Authoritative Last active

`last_activity_at` is the public activity field. A forward-only migration adds
`sessions.last_activity_at_ms`. Existing rows initialize conservatively from
their valid `updated_at_ms`; new rows initialize it from `created_at_ms`.
`updated_at_ms` remains internal optimistic metadata and must not be rendered
as activity.

Application advances Last active, monotonically, for:

- a committed initial ordinary user input or committed steer;
- a final AgentRun transition;
- an accepted Tool, Evidence, or Diagnosis lifecycle transition;
- an approval, action, execution, or verification transition; and
- an explicit Session rename.

Listing, filtering, picker movement, search, resume/view alone, `/status`,
`/doctor`, export, retention, startup maintenance, deletion preview, and failed
deletion do not advance it. Query order is
`last_activity_at DESC, session_id DESC`; batch selection order is
`last_activity_at ASC, session_id ASC`. A timestamp that is absent, corrupt,
before creation, or after the one frozen query time is `protected`, not silently
repaired or selected for deletion.

### Bounded discovery

Session discovery returns concrete, bounded metadata only: sanitized title,
exact Session ID, authoritative activity, privacy mode, current/resumable
state, protection reason, deletion eligibility, and a bounded opaque cursor.
It never returns Messages, summaries, Evidence payloads, credentials, scope
values, model content, or raw storage errors. Default page size is 20 and the
hard maximum is 100. Cursor schema 1 binds the last activity and Session ID;
malformed, oversized, or stale cursor input fails closed. There is no `--all`.

The TUI renders a bounded picker inside the single conversational surface.
Every row includes a relative activity label, exact local time with time-zone,
and textual state. Selected detail also includes exact UTC. `Enter` resumes an
eligible historical Session, `D` previews deletion, `B` collects an inactive
cutoff in the sole composer, and `Esc` closes. The existing resume picker uses
the same projection and delete path.

### Exact current and historical deletion

`/delete` accepts no argument and is exactly the current-Session deletion
entry. The privacy review's `D` control is labelled “same as `/delete`” and
enters the same Application-owned state machine. A current deletion preview
shows only sanitized title, exact ID, Last active, queue count and bytes, and a
fixed complete/incomplete deletion-scope explanation. It never renders queued,
pending, recovered, composer, or other uncommitted content.

Starting or active AgentRun work, a commit barrier, Reviewer work, an approval,
or action/execution/verification activity makes deletion unavailable. Deletion
does not cancel any of them. `Y` confirms in the TUI; `Esc`, `Enter`, and
`Ctrl+C` cancel. Application clears current-process queue, composer/context
projection, plan arm, and related display authority and exits only after the
SQLite transaction commits. Failure leaves the current Session and every
in-memory draft unchanged.

Deleting a historical row uses the same typed exact-Session command. A
committed deletion removes its picker row; failure preserves the row. Selecting
the current row redirects to the current deletion flow.

Deletion is logical graph deletion, not forensic erasure. The transaction
reuses the complete Session foreign-key cascade for Messages, summaries and
coverage, AgentRuns, model metadata, ToolInvocations, Evidence, Diagnoses,
approval/decision/action envelopes, execution/verification, and linked audit.
It does not remove exports, terminal scrollback, logs, backups, configuration,
credentials, cache entries, or SQLite free pages, and it never runs `VACUUM`.

### Cutoffs and frozen batch plans

A relative cutoff is a positive integer followed by `d` or `w`; `d` is exactly
24 hours and `w` is exactly 7 × 24 hours. It is resolved once against the
command's frozen UTC time. Absolute input must be RFC3339 with an explicit
time-zone. Zero, negative, decimal, month/year, date-only, natural-language,
overflowing, and over-policy durations are rejected. Eligibility uses strict
`last_activity_at < cutoff`; equality is retained.

A batch preview reports frozen local and UTC cutoff, matched, eligible,
protected, selected and remaining counts, oldest/newest selected activity,
the active limit, the exact graph scope and exclusions, and a content-free
selection digest. A dry run performs zero writes and states “No data was
deleted.” The default batch limit is 50 and the maximum is 100. If eligible
matches exceed the supplied limit, deletion performs zero writes and requires
a narrower cutoff or an explicit bounded limit.

Deletion plan schema 1 uses fixed-order, length-prefixed canonical bytes and
SHA-256. It binds plan kind, exact Session target or UTC cutoff, current-Session
exclusion, applied limit, matched/eligible/protected/selected/remaining counts,
over-limit state, ordered canonical UUIDv7 Session IDs, each Last active
timestamp and Session version, and the exact applied storage-schema revision.
The preview clock is intentionally not authority: non-TTY confirmation uses a
new process and must reproduce the same absolute selection. A plan is
current-command-only and is never persisted.

TTY batch confirmation requires the exact phrase `DELETE N SESSIONS`. A TTY
single deletion requires its fixed confirmation phrase. Non-TTY deletion is
rejected unless an earlier `--dry-run` result is supplied back as `--confirm`
with its exact digest. Batch automation must also replace the relative cutoff
with the resolved absolute cutoff. Confirmation re-reads and revalidates the
exact snapshot inside one transaction; changed candidates, activity,
protection, authority, Session version, ordering, count, schema revision, or
digest is stale and deletes nothing. A single plan follows the same rule.

### Process and active-authority isolation

Every database-using interactive Kupilot process holds a shared advisory lock
on one fixed state-directory lock file. A destructive batch CLI transaction
must obtain the exclusive non-blocking form for commit. Failure to prove
exclusive isolation rejects the entire batch. Exact single-Session deletion is
instead protected by its revalidated Session snapshot and active-authority
checks. The lock file carries no Session data or durable authority.

Batch deletion always excludes the process current Session and any Session
with starting, queued, or running AgentRun state; pending, approved, or
consuming approval/action state; or state whose safety cannot be proven.
It never cancels work. Selection and all graph deletes commit in one SQLite
transaction or roll back together. Concurrent run start, resume, rename,
retention, export, another deletion, or authority change either occurs before
the frozen snapshot or makes confirmation stale.

### CLI short circuit

`sessions list` and `sessions delete` short-circuit before new-Session
creation, TUI, model construction, Kubernetes, Tool, Reviewer, approval,
process, or executor setup. They load only the fixed safe configuration needed
to locate and validate SQLite, open/migrate storage, and obtain the appropriate
process lock. Plain output is bounded. `--json` uses a versioned typed schema;
generic maps and raw errors are prohibited.

## Retention and privacy impact

Session metadata is already local durable state; the dedicated activity column
adds no content. Listing is local and does not change retention. Logical
deletion follows the existing cascade and disclosure. Plans, cursors, filters,
cutoffs, confirmations, digests, picker state, and lock ownership are not
stored in SQLite, logs, exports, or model input.

## Rejected alternatives

- Continuing to label `updated_at_ms` as Last active without lifecycle proof.
- Deleting by title, accepting `--force`/`--yes`, or allowing unbounded lists.
- Cancelling live work as a side effect of deletion.
- Persisted deletion plans, tombstones, a second Session store, or a generic
  deletion log.
- Per-row best-effort batch deletion, automatic `VACUUM`, or deletion without
  cross-process isolation.

## Validation

Deterministic tests cover command routing and argument rejection; activity
update and non-update matrices; relative, absolute, DST and exact-cutoff
semantics; malformed/future/protected times; ordering and pagination; current,
historical and batch previews; all confirmation/cancellation paths; queue and
composer preservation on failure; digest tamper, reorder and stale snapshots;
default/max/one-over limits; active-state and process-lock exclusions;
transaction rollback and complete cascades; migration initialization and
checksum; non-TTY two-phase use; JSON bounds; retained external surfaces; and
zero model, Kubernetes, Tool, Reviewer, approval, process, and executor calls.

Live integration and release evidence are outside this decision.

## Consequences

Users can discover and remove local Session graphs without turning deletion
into run cancellation or exposing conversation content. A dedicated activity
field and frozen digest make inactive deletion explainable and race-safe, at
the cost of one migration, stricter process locking, and more conservative
protection when timestamps or active state cannot be proved.
