# Supporting Observation Details

This interaction remains normative for the Accepted `v0.5` target. The checked-
in binary exposes built-in and exact-CRD reads, Events, logs, metrics, optional
data sources, and bounded remote-diagnostic Evidence projections. Local-process
and mutation results remain action state rather than raw persisted Evidence;
all displayed content passes the same provenance and safe-display boundary.

Internally, every confirmed fact remains bound to machine-checked Evidence from
the current diagnostic run. The normal interface presents these records as
supporting observations and does not expose their correlation identifiers. An
observation explains what supported a conclusion; it is not a resource browser,
log viewer, YAML viewer, or proof of causality.

## Open and close a detail

Completed answers do not show Evidence IDs, citation aliases, or repeated
reference rows. Those correlation values remain internal and machine-checked.
Pressing `Ctrl+E` explicitly enters observation inspection and shows only the
selected position and a friendly state such as `ready` or `partial` before the
safe detail is opened.

- Press `Ctrl+E` to open the newest committed final's claim index. Legacy
  retained answers without claim metadata open their supporting observations
  directly.
- Press `Left` and `Right` to move between declared claims. Press `Up` or
  `Ctrl+P` and `Down` or `Ctrl+N` to move only between the exact Evidence
  references cited by the selected claim.
- Press `Enter` to request the selected safe detail.
- Press `Enter` in an open detail to return to the exact claim selection.
  Press `Esc` or `Ctrl+E` to leave inspection. `Esc` also cancels a pending
  display request; a later result is discarded.

A final answer's claim index opens the exact Evidence IDs cited by that claim.
The Evidence detail includes a bounded “Referenced by claims” list with each
claim number and fixed claim type, so the same accepted relationship works in
both directions. It never searches raw payload text, and pending, queued,
recovered, failed, or streaming content cannot enter the index.

The root screen always retains its single composer. Observation selection and
the detail overlay are non-editable keyboard surfaces.

## Displayed fields

An available detail contains only these bounded fields:

- resource Kind, Namespace, and name;
- historic Context and Namespace;
- UTC observation time;
- a friendly observation type such as `Condition`, `Kubernetes event`, or
  `Service readiness`, `Metric sample`, or `Diagnostic result`;
- `complete` or `partial` status;
- a warning only when sensitive values were filtered; and
- a locally revalidated concise projection of at most 512 UTF-8 bytes.

Evidence ID, run ID, API version, scope generation, projected source-path enum,
and raw partial, truncation, or filtering booleans remain internal. The resource
summary also excludes UID, resource version, annotations, addresses, object
bodies, and other non-allowlisted metadata.

## Detail states

| State | Meaning |
| --- | --- |
| `ready` / `complete` | The cited safe observation is retained and its displayed projection is complete. |
| `partial` | The accepted observation or its display projection was truncated. Treat it conservatively. |
| `expired` | Supporting detail was deleted, expired, or is no longer retained. The historic conclusion remains display-only. |
| `unavailable` | The request could not be matched to the cited run and scope safely. No detail is shown. |

Explicitly resumed history can restore machine-checked links to retained
supporting observations. It does not restore an active diagnostic run,
cluster-read authority, live scope or policy generation, permission rule,
Reviewer decision, ActionEnvelope, approval, execution, or resource
verification. A scope or policy change
closes any pending detail request, and late results cannot replace detail in the
current Context.

## Safety boundary

The detail query reads only accepted Domain Evidence and Diagnosis values. It
reuses the fixed source allowlist, text normalizer, sensitive-value filter, and
byte ceiling before creating the TUI ViewModel. Kupilot never reconstructs a
detail from a raw Kubernetes object or raw persistence payload.

Raw Tool results, complete container logs, metrics series, files, process
output, optional-source responses, YAML, Secret values, unrestricted
annotations, IP addresses, credentials, kubeconfig content, complete model
traffic, and Reviewer responses are never eligible for this view. Missing data
and identity mismatch fail closed with a fixed safe state rather than exposing
adapter errors.

Action acceptance, ambiguous outcome, progress, cleanup, and verification are
typed runtime states, not Evidence fabricated from model prose. A later
verification observation cannot rewrite whether an external attempt may have
occurred.

## Claim coverage

New model answers classify each declared claim as a current observation,
inference, recommendation, uncertainty, or unsupported observation. A current
observation must cite accepted Evidence from the same run, scope, and policy
generation in acceptance order. The runtime checks the claim text hash,
identity, ordering, uniqueness, and bounds before committing the answer.

This proves reference integrity for the declared manifest. It does not prove
that the prose matches the manifest or that model reasoning is correct. The
offline synthetic quality scores exercise this validator only; no live model
quality evaluation is implied.

Each committed final also has a compact textual provenance strip: Evidence
count, observed-at range, frozen scope/policy generations, complete/partial/
truncated/unavailable state, inference/uncertainty and conflict/supersession
presence, checked/not-checked source counts, authoritative terminal reason, and
fixed safe next action. It displays no raw Evidence payload.

Source coverage distinguishes checked-and-absent, not checked, unavailable,
denied, partial, truncated, timed out, stale, and conflicting. An absent item in
a bounded list is not global nonexistence unless runtime proves that exact query
complete. Freshness is unknown unless a code-owned source policy supplies an
exact ceiling. Conflict and supersession require typed subject/field/revision
identity; model prose cannot create them.
