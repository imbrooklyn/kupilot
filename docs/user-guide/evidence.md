# Supporting Observation Details

This interaction remains normative for the Accepted `v0.5` target. The
checked-in binary still exposes only `v0.4` Evidence sources; future CRD,
metrics, optional data-source, file, remote diagnostic, and process projections
must pass the same deterministic provenance and safe-display boundary.

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

- Press `Ctrl+E` to select a supporting observation in the transcript.
- Press `Up` or `Ctrl+P` and `Down` or `Ctrl+N` to move between references.
- Press `Enter` to request the selected safe detail.
- Press `Esc`, `Enter`, or `Ctrl+E` to close the detail. `Esc` also cancels a
  pending display request; a later result is discarded.

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
