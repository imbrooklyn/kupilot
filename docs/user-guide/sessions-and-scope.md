# Sessions and Scope

This page defines the implemented Session-memory and explicit-resume semantics
within the Accepted `v0.5` contract. The checked-in binary now uses eligible
persisted history as model context and directly uses Eino ADK summarization
with project-owned safe coverage metadata.

## A bare start is always new

`kupilot` without a subcommand creates a new Session. It does not inspect the
current working directory, repository, Context, Namespace, environment, or crash
state to select history. Scope selection is independent: Kupilot uses an
effective configured Context, then the last successfully verified local
Context, then kubeconfig `current-context`, and freshly verifies it with the
configured Namespace or `default`. A new empty Session is not eligible for
resume until it has at least one committed safe Message.

The remembered Context is only a local candidate. It does not restore a
Kubernetes client, scope generation, AgentRun, ResourceRef, approval, or
Session. If the name no longer exists, Kupilot falls back to kubeconfig
`current-context`. Use the existing Context and Namespace controls to change
the active scope; a Context is remembered only after activation succeeds.

Starting another new Session does not imply that the old one was deleted. Safe
standard-persistence history remains in the local database until the user
explicitly deletes its Session. Operational detail has a separate bounded
retention period.

## Explicit resume forms

History is queried only through these forms:

```sh
./bin/kupilot resume
./bin/kupilot resume 018f0f5c-7b89-7abc-8def-0123456789ab
./bin/kupilot resume --last
```

The identifier above is a synthetic UUIDv7 example.

- `resume` opens a global, bounded picker of eligible Sessions.
- `resume SESSION_ID` requests one exact UUIDv7 identifier.
- `resume --last` requests the first eligible Session in global last-activity
  order.

The global resume picker reuses the sole composer for a bounded local filter.
`/resume FILTER` matches only safe display metadata: sanitized title, canonical
UTC last-activity display time, and display-only Context or Namespace. Exact
title, title prefix, title substring, scope, and timestamp matches are ranked in
that order; ties use descending last activity and then descending Session ID.
The repository returns at most 50 stable results, and the existing picker shows
at most eight rows at once. Message, Diagnosis, Evidence, Tool, model, approval,
and audit content is never searched or previewed.

Exact ID and `--last` are mutually exclusive. There is no `--all`, `--cd`,
cwd/repository filter, Context filter, Namespace filter, or automatic last
resume. Picker cancellation exits the top-level resume flow. An empty,
cancelled, invalid, unavailable, or non-resumable result never creates a new
Session as a fallback.

`/privacy` displays the current persistence mode. Its mode control starts a new
empty Session in standard or minimal mode; it does not mutate an existing
Session. Minimal-persistence content and model memory are process-only. Picker
and `--last` exclude minimal Sessions, and exact resume returns
`session_not_resumable`.

## Discover and delete Sessions

`/sessions` opens the bounded Session picker inside the existing single screen.
Rows contain only sanitized title, Session ID, relative and exact local Last
active, persistence mode, and textual current/resumable/protected/deletion
state. Selected detail includes exact UTC. `Enter` resumes an eligible
historical Session, `D` previews the selected Session, `B` uses the sole
composer for an inactive cutoff, and `Esc` closes. The `/resume` picker uses
the same Last-active projection and `D` path.

`/delete` accepts no argument and previews the current Session. `D` in
`/privacy` is explicitly the same operation. A preview displays sanitized
title, exact ID, Last active, queue item/byte counts, complete graph scope, and
the external surfaces it cannot delete; it never displays queue, pending,
recovered, or composer text. `Y` confirms. `Esc`, `Enter`, or `Ctrl+C` cancels.

Deletion is unavailable while a starting/active AgentRun, commit barrier,
Reviewer, approval, action, execution, or verification is active. It never
cancels that work. The current queue, composer, context, and UI state clear only
after the whole SQLite graph transaction commits. Failure leaves them intact.
A committed historical deletion removes only the matching picker row.

The pre-TUI CLI offers the same bounded Application projection:

```text
kupilot sessions list [--limit N] [--cursor CURSOR] [--json]
kupilot sessions delete SESSION_ID
kupilot sessions delete --before 1d [--limit N] [--dry-run]
kupilot sessions delete --before 2026-09-01T00:00:00Z --confirm DIGEST
```

Exact deletion accepts only canonical UUIDv7, never a title, `--force`, or
`--yes`. TTY deletion previews and confirms interactively. Non-TTY automation
first uses `--dry-run`, then supplies the returned exact absolute cutoff and
digest. `d` means 24 hours and `w` means seven such days; absolute values must
be timezone-bearing RFC3339. Eligibility is strictly Last active before the
frozen cutoff, so equality remains. Batches choose oldest first, exclude the
current Session and every active, attempted, corrupt, future, or unproved row,
and abort if the bounded limit or process-isolation proof fails.

Authoritative Last active advances only for committed initial input or steer,
final run result, accepted Tool/Evidence/Diagnosis, approval/action/
verification transitions, and explicit rename. Listing, filtering, resume or
view, `/status`, `/doctor`, export, retention, startup maintenance, preview, and
failed deletion do not change it.

Deletion removes the Session-owned conversation, safe summary and coverage,
run, Tool, Evidence, Diagnosis, ActionEnvelope, approval, decision, execution,
and linked audit rows. It does not remove exports, terminal scrollback, logs,
backups, configuration, credentials, cache, or SQLite free pages and never runs
automatic `VACUUM`. It is logical deletion, not forensic erasure.

## Export a Session summary

The current standard-persistence Session can be exported from `/privacy` as the
versioned, redacted Markdown summary and coverage explanation documented in
[Privacy and Local Data](privacy-and-local-data.md). A historical Session must
first be resumed explicitly; export does not add a Session page, CLI command,
file browser, or second composer. It does not call the model or cluster and does
not make historic Evidence current. Minimal Sessions cannot be resumed or
exported across processes.

## What resume restores

Resume acceptance loads only allowlisted safe history and Session metadata:

- Committed, locally processed user Messages.
- Final validated assistant Messages, free-form answers, and safe Diagnosis
  metadata.
- A bounded safe summary, coverage metadata, and eligible recent committed tail
  when compaction has occurred.
- Safe Session metadata.

The picker may display an unverified historic Context and Namespace candidate
before acceptance. Choosing it is a separate explicit scope activation that
must complete first. Resume acceptance itself keeps the currently verified
scope and clears the selected ResourceRef; it does not install or revalidate a
historic resource candidate.

`scope_generation` and `policy_generation` are current-process safety epochs.
They are not Kupilot versions, database versions, migration versions, or
Session versions. Restarting or upgrading Kupilot does not by itself make
compatible history ineligible. The historic numbers remain display provenance
and never become current authority.

Resume does not restore or replay:

- An AgentRun, model stream, partial model output, or ToolInvocation.
- A Kubernetes client, active generation, or live ClusterScope.
- Current Evidence authority for a new run.
- A cancel function, permission profile authority, Session rule, Reviewer
  decision, privacy review, ActionEnvelope, approval, process, execution, or
  verification state.

Historic Evidence may explain an old Diagnosis, but it is display-only in a
resumed Session. A new confirmed fact must cite new Evidence from the new
AgentRun.

The command itself makes no model request, Tool call, Reviewer request,
Kubernetes read, local-process launch, approval, or executor call. A later
scope-picker selection performs normal scope verification. Supplying explicit
`--context` or `--namespace` startup overrides also requests separate scope
activation.

Every question after the first in a Session receives one ordered,
bounded representation of all retained eligible safe history when such history
exists. Application first checks the current named role/profile, canonical
origin, exact data categories and consent, verified current scope/generation,
coverage, and finite context/summary budget. A failed gate causes zero model
calls and no current-question-only fallback. The independent policy-generation
gate also invalidates prior permission, review, rule, and action state. The current question is included
exactly once. Partial streams, Tool calls/results, raw model traffic, command
output, and approval dialogs are never replayed as conversation history.

## Saved-scope conflict

A saved Context and Namespace are candidates, never live authority. If the
same Context and Namespace are already independently verified in the current
process, Kupilot accepts the resume under that current authority without
another scope choice or Kubernetes request. Otherwise an unavailable or
conflicting candidate opens the ordinary Context or Namespace picker. There is
no historic-authority shortcut and no keep-current confirmation that could
silently ignore the conflict.

If a question reaches Application with a stale delivery projection, Kupilot
does not silently change its target or send it. The draft is retained and the
safe notice always says that the question was not sent. A currently verified
scope is shown for review and can be used only by submitting again. An
unverified scope opens the Context/Namespace picker; a stale selected resource
opens the ordinary resource-selection recovery. If another run is already
active, the TUI resynchronizes to it and keeps the draft so `Enter` can steer or
`Tab` can queue only after another explicit key press.

Question-start notices distinguish Session unavailable, scope verification or
generation changes, stale selected resource, policy change or invalid policy,
active/starting run, another bounded operation, storage degradation or failed
precommit, missing model, consent review, local input rejection, and an unknown
safe failure. They never include the question, resource content, credential,
raw storage error, or path.

No scope action occurs until the user chooses a picker item. `Esc` cancels the
resume. A selection resolves its Context from local kubeconfig, creates a fresh
client bundle, and verifies the exact Namespace. Failure returns to the picker
with the attempted generation unavailable; Kupilot does not silently restore
the old client or create a new Session.

Explicit top-level `--context` or `--namespace` overrides take precedence over
the saved candidate. They still require verification and do not make historic
Evidence current.

## ResourceRef after resume or scope change

A scope change invalidates the old generation first, cancels the active run,
clears the selected ResourceRef and picker caches, closes or replaces the old
client, and rejects late results.

Resume never restores or automatically revalidates a saved ResourceRef. The
selection remains cleared even when a saved candidate used to match the scope.
The user may select a resource again through the ordinary `/resource` flow;
that input aid still creates no Evidence, and the next AgentRun must verify
current cluster facts through a typed bounded capability.

## Resume privacy behavior

Resume is not consent for a new model transfer. The configured model role,
canonical origin, policy version, and exact enabled data-category set must
still match an accepted consent record. A changed profile, role, destination,
category, or meaning requires a new review before the next content request.

Summarization reuses the `agent` model profile with an independent finite
non-streaming no-Tool budget. If coverage is corrupt or required compaction
fails, Kupilot preserves the last committed state and sends no oversized or
silently truncated request. Runner-managed durable Session support is not used
until a stable non-prerelease Eino tag passes ADR-0047's adoption gate.

`/compact` invokes that same summarization path explicitly. It preserves the
old committed summary and Messages on every failure and does not submit a
question or drain the queue. `/status` reports context pressure from exact safe
Message/byte/coverage limits, not estimated tokens. Plan arm, queue drafts,
search and clipboard state, title state, compaction intent, and active stream
state do not survive resume. A committed plan answer and safe claim coverage
follow the ordinary standard/minimal retention rules without restoring
Evidence or execution authority.

Session titles and historic scope names may themselves be sensitive local
metadata. Picker rows are deliberately limited to a safe title, last activity,
privacy mode, and historic scope candidate; they do not show Message previews,
Evidence, credentials, or live-scope claims.
