# Sessions and Scope

## A bare start is always new

`kupilot` without a subcommand creates a new Session. It does not inspect the
current working directory, repository, previous Context, Namespace, environment,
or crash state to select history. A new empty Session is not eligible for resume
until it has at least one committed safe Message.

Starting another new Session does not imply that the old one was deleted. Under
the current standard-persistence composition, eligible history remains in the
local database until retention removes operational detail or the database is
removed by the user.

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

Exact ID and `--last` are mutually exclusive. There is no `--all`, `--cd`,
cwd/repository filter, Context filter, Namespace filter, or automatic last
resume. Picker cancellation exits the top-level resume flow. An empty,
cancelled, invalid, unavailable, or non-resumable result never creates a new
Session as a fallback.

The current public composition creates standard-persistence Sessions only. The
storage layer enforces non-resumable minimal-persistence semantics, but the
public CLI/TUI does not expose a minimal-persistence selector. If a database
contains a known minimal Session created by another compatible caller, picker
and `--last` exclude it, and exact resume returns `session_not_resumable`.

## What resume restores

Resume loads only allowlisted safe history and historic candidates:

- Committed, locally processed user Messages.
- Final validated assistant Messages and structured Diagnosis history.
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
resumed Session's saved scope differs from the current scope, KuPilot opens
`Confirm Session scope` with two choices:

1. `Keep current scope`, selected by default.
2. `Use saved scope`.

No scope action occurs until the user confirms a choice. `Esc` cancels the
resume. Choosing the saved scope resolves that Context from local kubeconfig,
creates a fresh client bundle, and verifies the exact Namespace. Failure leaves
the attempted generation unavailable; KuPilot does not silently restore the old
client or create a new Session.

Explicit top-level `--context` or `--namespace` overrides take precedence over
the saved candidate. They still require verification and do not make historic
Evidence current.

## ResourceRef after resume or scope change

A scope change invalidates the old generation first, cancels the active run,
clears the selected ResourceRef and picker caches, closes or replaces the old
client, and rejects late results.

After resume, a saved ResourceRef is eligible for revalidation only when its
saved Namespace matches the newly verified ClusterScope. KuPilot reads the
exact object and checks stronger identity when available before selecting it.
If the object is absent, forbidden, changed, cross-Namespace, or otherwise
unavailable, the selection remains cleared. Selecting a candidate never creates
Evidence; the next AgentRun must verify it through a fixed read-only Tool.

## Resume privacy behavior

Resume is not consent for a new model transfer. The configured canonical model
origin, policy version, and exact enabled data-category set must still match an
accepted consent record. A changed destination or category requires a new
review before the next content request.

Session titles and historic scope names may themselves be sensitive local
metadata. Picker rows are deliberately limited to a safe title, last activity,
privacy mode, and historic scope candidate; they do not show Message previews,
Evidence, credentials, or live-scope claims.
