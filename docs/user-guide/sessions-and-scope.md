# Sessions and Scope

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
Session. Minimal-persistence content is memory-only. Picker and `--last` exclude
minimal Sessions, and exact resume returns `session_not_resumable`.

## Delete a Session

The current Session can be selected for deletion with `D` in `/privacy`. A
historical standard Session can be selected with `D` in the existing resume
picker. There is no separate Session-management page or second input field.
Both paths show the exact target and require `Y`; `Esc` or `Enter` cancels with
no deletion command.

Deleting the current Session first cancels and waits for any starting, active,
or terminal-but-not-yet-quiesced AgentRun and invalidates pending or
approved-but-not-executed approval authority. A consuming approval denies
deletion. After the Session graph commits as one SQLite
transaction, the current Session or picker row is cleared. On database failure,
the graph remains and the UI reports that it was not deleted. A restart never
restores an AgentRun or approval authority; startup recovery makes persisted
pending or approved-but-not-executed approvals terminal before lifecycle
actions continue.

Deletion removes the Session-owned conversation, run, Tool, Evidence, Diagnosis,
approval, decision, and linked audit rows. It is logical deletion, not forensic
erasure of SQLite free pages, WAL, backups, snapshots, swap, or storage media.

## Export a Session summary

The current standard-persistence Session can be exported from `/privacy` as the
versioned, redacted Markdown summary documented in
[Privacy and Local Data](privacy-and-local-data.md). A historical Session must
first be resumed explicitly; export does not add a Session page, CLI command,
file browser, or second composer. It does not call the model or cluster and does
not make historic Evidence current. Minimal Sessions cannot be resumed or
exported across processes.

## What resume restores

Resume loads only allowlisted safe history and historic candidates:

- Committed, locally processed user Messages.
- Final validated assistant Messages, free-form answers, and safe Diagnosis
  metadata.
- Safe Session metadata.
- An unverified historic Context and Namespace candidate.
- An unverified historic ResourceRef candidate when eligible.

Resume does not restore or replay:

- An AgentRun, model stream, partial model output, or ToolInvocation.
- A Kubernetes client, active generation, or live ClusterScope.
- Current Evidence authority for a new run.
- A cancel function, privacy review, approval, or write state.

Historic Evidence may explain an old Diagnosis, but it is display-only in a
resumed Session. A new confirmed fact must cite new Evidence from the new
AgentRun.

The command itself makes no model request, Tool call, or Kubernetes read.
Selecting and accepting a saved scope is a later explicit action that performs
normal scope verification. Supplying explicit `--context` or `--namespace`
startup overrides also requests separate scope activation.

## Saved-scope conflict

A saved Context and Namespace are candidates, never live authority. When a
resumed Session's saved scope differs from the current scope, Kupilot opens
`Confirm Session scope` with two choices:

1. `Keep current scope`, selected by default.
2. `Use saved scope`.

No scope action occurs until the user confirms a choice. `Esc` cancels the
resume. Choosing the saved scope resolves that Context from local kubeconfig,
creates a fresh client bundle, and verifies the exact Namespace. Failure leaves
the attempted generation unavailable; Kupilot does not silently restore the old
client or create a new Session.

Explicit top-level `--context` or `--namespace` overrides take precedence over
the saved candidate. They still require verification and do not make historic
Evidence current.

## ResourceRef after resume or scope change

A scope change invalidates the old generation first, cancels the active run,
clears the selected ResourceRef and picker caches, closes or replaces the old
client, and rejects late results.

After resume, a saved ResourceRef is eligible for revalidation only when its
saved Namespace matches the newly verified ClusterScope. Kupilot reads the
exact object and checks stronger identity when available before selecting it.
If the object is absent, forbidden, changed, cross-Namespace, or otherwise
unavailable, the selection remains cleared. Selecting a candidate never creates
Evidence; the next AgentRun must verify it through a typed bounded capability.

## Resume privacy behavior

Resume is not consent for a new model transfer. The configured canonical model
origin, policy version, and exact enabled data-category set must still match an
accepted consent record. A changed destination or category requires a new
review before the next content request.

Session titles and historic scope names may themselves be sensitive local
metadata. Picker rows are deliberately limited to a safe title, last activity,
privacy mode, and historic scope candidate; they do not show Message previews,
Evidence, credentials, or live-scope claims.
