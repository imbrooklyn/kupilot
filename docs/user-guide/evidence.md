# Evidence Details

KuPilot conclusions cite Evidence collected by the fixed read-only Tools. An
Evidence reference explains which accepted observation supports a conclusion;
it is not a resource browser, log viewer, YAML viewer, or proof of causality.

## Open and close a detail

Completed Agent answers show each cited Evidence ID and its retained-detail
state below the answer.

- Press `Ctrl+E` to select cited Evidence in the transcript.
- Press `Up` or `Ctrl+P` and `Down` or `Ctrl+N` to move between references.
- Press `Enter` to request the selected safe detail.
- Press `Esc`, `Enter`, or `Ctrl+E` to close the detail. `Esc` also cancels a
  pending display request; a later result is discarded.

The root screen always retains its single composer. Evidence selection and the
detail overlay are non-editable keyboard surfaces.

## Displayed fields

An available detail contains only these bounded fields:

- Evidence ID and the exact AgentRun ID;
- Evidence category and a code-allowlisted projected source path;
- the historic Context, Namespace, and scope generation;
- API version, Kind, Namespace, and resource name;
- the UTC observation time;
- `available` or `partial` state, plus explicit partial and truncation flags;
- whether sensitive-value filtering applied replacements; and
- a locally revalidated concise projection of at most 512 UTF-8 bytes.

The resource summary excludes UID, resource version, annotations, addresses,
object bodies, and other non-allowlisted metadata.

## Detail states

| State | Meaning |
| --- | --- |
| `available` | The cited safe observation is retained and its displayed projection is complete. |
| `partial` | The accepted observation or its display projection was truncated. Treat it conservatively. |
| `expired` | Supporting detail was deleted, expired, or is no longer retained. The historic conclusion remains display-only. |
| `unavailable` | The request could not be matched to the cited run and scope safely. No detail is shown. |

Explicitly resumed history can restore machine-checked Evidence references from
the retained Diagnosis. It does not restore an AgentRun, Tool authority, live
scope, or resource verification. A scope-generation change closes any pending
detail request, and late results cannot replace detail in the current Context.

## Safety boundary

The detail query reads only accepted Domain Evidence and Diagnosis values. It
reuses the fixed source allowlist, text normalizer, sensitive-value filter, and
byte ceiling before creating the TUI ViewModel. KuPilot never reconstructs a
detail from a raw Kubernetes object or raw persistence payload.

Raw Tool results, complete container logs, YAML, Secret data, annotations, IP
addresses, credentials, kubeconfig content, complete model traffic, and model
responses are never eligible for this view. Missing data and identity mismatch
fail closed with a fixed safe state rather than exposing adapter errors.
