# ADR-0031: Require Explicit CLI Session Resume

- Status: Accepted
- Date: 2026-08-08

## Context

Automatically reopening history can disclose a previous conversation, confuse
historic ClusterScope with live authority, or make process restart look like
continuation of an AgentRun. Inferring a Session from the current working
directory would also pull KuPilot toward a repository workspace model that the
product explicitly rejects.

Users still need a deliberate way to continue eligible local conversation
history.

## Decision

A bare `kupilot` invocation always creates a new Session. History is queried only
through one of these explicit intents:

- `kupilot resume` opens a bounded picker of eligible standard-persistence
  Sessions.
- `kupilot resume <session-id>` requests one exact opaque Session identifier.
- `kupilot resume --last` requests the most recently active eligible Session.

Exact identifier and `--last` are mutually exclusive. Working directory,
repository path, terminal directory, Context name, Namespace, ResourceRef,
process environment, and previous crash state never imply a Session.

Before returning any candidate, Application runs mandatory retention cleanup and
filters deleted, expired, minimal-persistence, corrupt, and ineligible Sessions.
Picker rows contain only bounded safe title, last-activity time, privacy mode,
and safe historic scope candidate. They contain no message preview, Evidence,
raw error, credential, or live scope claim.

Resume reconstructs safe conversation history and candidates only. It never
resumes an Agent loop, model stream, ToolInvocation, pending Tool selection,
Kubernetes client, live ClusterScope generation, cancellation function,
approval wait, or write. A durably running run is marked `interrupted` during
startup recovery. The user must explicitly select and verify a current Context
and Namespace before a new question.

Minimal-persistence Sessions are non-resumable through picker, exact identifier,
and `--last`. Picker and `--last` exclude them. An exact identifier for a known
minimal-persistence Session returns the stable `session_not_resumable` error;
missing, deleted, expired, corrupt, and otherwise ineligible identifiers use a
safe non-disclosing error. No empty or failed resume path silently creates a new
Session.

Cancelling the top-level `kupilot resume` picker exits. Cancelling `/resume`
inside the TUI returns to the current Session. Both use the same Application
query and resume use cases. `version` and `help` short-circuit without opening the
business database or initializing Kubernetes or model adapters.

## Consequences

Positive consequences:

- Starting KuPilot has predictable privacy and state behavior.
- Historic scope and Evidence cannot become live authority through restart.
- Resume is independent of source repositories and current directories.
- Every resume path shares retention and eligibility checks.

Costs and constraints:

- Users must opt in to resume and may need to select a Session.
- Bare startup cannot provide automatic continuity.
- Minimal-persistence intentionally provides no cross-process conversation
  continuity.
- Safe picker metadata offers less context than message previews.

## Alternatives considered

- Automatically reopening the last Session was rejected because it can disclose
  history and blur new versus resumed state.
- Associating Sessions with a working directory was rejected because KuPilot is
  not a repository or IDE workflow and cluster diagnosis may have no repository.
- Resuming an interrupted AgentRun was rejected because external state, scope,
  credentials, budgets, and streams cannot be reconstructed safely.
- Allowing minimal-persistence resume from metadata was rejected because there is
  no durable content and it would misrepresent the privacy mode.

## Security and privacy impact

Explicit resume is a privacy boundary, not authorization for Kubernetes or model
transfer. Current scope verification and current endpoint/category consent are
still required. Historic Evidence can be displayed as history but cannot support
a confirmed fact in a new AgentRun.

CLI parsing must not accept an API key, kubeconfig content, question, raw scope,
or approval token through resume arguments. Except for the explicit
`session_not_resumable` result for a known minimal Session, safe errors do not
reveal why a specific identifier is ineligible.

## Validation

Deterministic CLI and Application tests must cover:

- Bare start always creates a new ID and performs no history query.
- Picker, exact identifier, and `--last` are the only history-query intents and
  cannot be combined ambiguously.
- Working-directory and environment changes never influence Session selection.
- Retention runs before candidate selection and expired, deleted, minimal, and
  corrupt Sessions are absent from all three paths.
- Resume restores only eligible safe content, marks running history
  `interrupted`, and requires new scope activation.
- No model, Kubernetes, Tool, approval, or executor call occurs merely because a
  Session is resumed.
- Safe errors do not distinguish expired, deleted, corrupt, otherwise
  ineligible, or never-existing identifiers.
- A known minimal-persistence identifier deterministically returns
  `session_not_resumable`, and no resume error falls back to a new Session.

S04 must freeze the CLI parse intents and `help`/`version` short circuit. S07
must implement retention-aware repository queries, S16 must implement the shared
picker cancellation states, and S23 must prove end-to-end new, picker, exact-ID,
`--last`, empty, minimal, and scope-conflict behavior without automatic external
calls.

## Revisit triggers

- A future explicit Session alias or search feature can preserve the same
  privacy, retention, and non-authority rules.
- The product adopts shared or remote history through a new topology ADR.
- Minimal-persistence semantics are deliberately changed through a retention
  ADR.

## References

- [Product Contract](../product.md)
- [Scope](../scope.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0023: Use a Single-Screen Agent-Supervision TUI](0023-use-a-single-screen-agent-supervision-tui.md)
- [ADR-0025: Enforce Data Retention and User Deletion](0025-enforce-data-retention-and-user-deletion.md)
