# Privacy and Local Data

This page distinguishes current behavior from the Accepted `v0.5` privacy
target. The checked-in binary now exposes named model roles, role-scoped
consent, safe Session context and summarization, bounded data-source and remote-
diagnostic pipelines, and default-off local execution. Remote-diagnostic human/
Reviewer delivery remains fail-closed in this slice. Local process output has
no model-transfer category: bounded sanitized output may be shown only in the
local terminal and cannot become model content or Evidence.

Kupilot orchestrates locally and connects directly to the selected Kubernetes
API and configured model endpoint. Local orchestration does not mean all
diagnostic data stays on the workstation. Review both cloud transfer and local
retention before using Kupilot with a cluster.

## Cloud model categories

Before the first model-content request, `/privacy` displays the fixed model
role, named profile, canonical destination, policy version, current decision, and the exact included
categories using readable labels. The same dialog also displays local
persistence and retention; those controls do not grant model-transfer consent.

The protocol category identifiers are documented below for configuration and
audit interpretation. The TUI labels them `User question`, `Conversation
context`, `Resource references`, `Kubernetes status`, `Kubernetes events`, and
`Container output` rather than exposing the identifiers directly.

1. `user_question`: the question after local normalization,
   sensitive-value handling, and byte limits.
2. `safe_conversation_context`: bounded committed same-Session user/final-
   assistant context and, when required, one safe summary plus recent tail.
   Current-run structured Tool results stay inside Eino's run state; persisted
   Tool calls/results are never replayed as Session history.
3. `resource_names_and_references`: Context, Namespace, and admitted resource
   names or references.
4. `projected_kubernetes_status`: allowlisted status, conditions, counts,
   timestamps, and relationships.
5. `projected_kubernetes_events`: bounded Event reasons and messages after local
   safety processing.
6. `redacted_container_output`: bounded current, previous, or all-container
   log facts, bounded local log-search results, container-file content, Pod Exec
   output, and diagnostic-Pod output after normalization and redaction.
7. `projected_kubernetes_metrics`: bounded current Metrics API CPU and memory
   projections.
8. `projected_prometheus_results`: bounded samples from explicitly enabled,
   code-owned Prometheus query templates.
9. `redacted_loki_output`: bounded Loki lines from explicitly enabled,
   code-owned queries after multiline, terminal, and sensitive-value handling.

Safe Secret metadata uses the resource-reference and projected-status
categories. Exact ConfigMap-key and non-credential container-environment values
do not have a current transfer category and therefore remain fail-closed.
Local process output likewise remains terminal-only. A Reviewer receives only
the minimum normalized ActionEnvelope and policy facts by default, not raw
cluster output or general Session history. No new category is implied by an
existing broad label.

The container-output category is disabled by default. In the privacy dialog,
`L` toggles it. A toggle returns consent to pending and cancels an active run;
press `A` only after reviewing and accepting the new exact category set. `R`
rejects pending consent or revokes accepted consent. `Esc` cancels the review
without granting authority.

Consent binds the policy version, model role, a hash of the canonical origin,
and the whole enabled-category set. A profile, role, destination, category,
category meaning, or policy version change requires a new decision. Agent
consent cannot authorize a Reviewer at another origin. Consent does not broaden Kubernetes
RBAC, add a Tool, make a prohibited source eligible, authorize a write, or
guarantee that local redaction recognized every sensitive value.

## Data never eligible for model content

The model-content contract excludes:

- Kubeconfig contents, Kubernetes bearer tokens, client certificates, private
  keys, ServiceAccount token material, and exec credential output.
- Kubernetes Secret values, ServiceAccount tokens, credential-bearing
  ConfigMap or environment values, and referenced credential values.
- The model API key as content. It is used only as an authentication header for
  the validated configured origin.
- Raw Kubernetes objects, full YAML, managed fields, unrestricted annotations,
  EndpointSlice addresses, and arbitrary API types.
- Raw or unbounded Events, logs, metrics, files, optional data-source results,
  and remote diagnostic output, plus all local process output even after
  bounded sanitation.
- Raw configuration, SQLite, local application-log, prompt, request, response,
  stream, header, and endpoint-error contents.

Eligible resource names, Event messages, user text, and application output may
still be sensitive after processing. Kupilot treats them as cluster data and
blocks high-confidence sensitive values rather than sending originals for
diagnostic completeness.

## Local persistence and retention controls

`/privacy` displays the current Session storage as `history saved` or `memory
only` and shows the effective retention periods. `/status` reports the same
safe privacy and local-storage state without external I/O. A
standard-persistence Session may keep:

- Session and AgentRun metadata.
- Locally processed committed user Messages and final validated assistant
  Messages.
- A bounded safe Session summary and coverage metadata; eligible recent Message
  rows remain the tail rather than being copied into a generic payload.
- Structured Diagnoses.
- Sanitized ToolInvocation metadata, accepted Evidence, and bounded model
  request metadata.
- Allowlisted lifecycle audit, the consent tuple, and safe ActionEnvelope,
  permission-decision, one-attempt, cleanup, and verification metadata.

It does not persist raw logs, metrics, files, optional-source responses,
remote/local process output, raw Events, raw Tool results, raw Kubernetes
objects, assembled prompts, model streams, protocol bodies, framework values,
Reviewer response bytes, Session rules, credentials, or kubeconfig paths.

Default logical retention is:

| Data | Default lifetime |
| --- | --- |
| Safe Session history, summary/coverage, and Diagnosis | Until explicit deletion of that Session |
| ToolInvocation, Evidence, and model-request detail | 30 days |
| Run and lifecycle audit | 90 days |
| Terminal approval and decision records, and approval/write audit | 180 days |
| Model-transfer consent | Until revoked, cleared, or invalidated |

When operational detail expires, historic Diagnosis text may remain, but the UI
must not present removed Evidence detail as current proof.

In `/privacy`, `T` selects the next shorter operational-detail period from the
bounded `30`, `14`, `7`, and `0` day presets. The setting is compared with the
current durable value and committed atomically. It can never increase retention;
`0` keeps operational detail in process memory only. The 90- and 180-day audit
periods remain fixed, and explicit Session deletion may remove linked audit
records earlier.

`M` starts a new empty Session in the other persistence mode while no AgentRun
is active. It never changes an existing Session's mode. Minimal persistence
stores no user or assistant Message content, summary, coverage, Diagnosis, Tool
detail, Evidence, or model-request detail. Its necessary Session shell and
required lifecycle, consent, permission, action, and execution-audit records
remain subject to their own retention rules. A minimal Session is unavailable
to the resume picker, exact-ID resume, and `--last` across processes.

Standard-mode resume itself performs zero model, Kubernetes,
Tool, Reviewer, approval, process, or executor I/O. On the next explicit
question, when retained eligible safe history exists and current
role/origin/category consent, scope, policy, coverage, and budget checks pass,
Application must send exactly one ordered, bounded representation of that
history. A failed gate causes zero model calls and no current-question-only
fallback. Historic scope, Evidence, Session rules, Reviewer decisions,
ActionEnvelopes, approvals, and execution never regain authority.

## Export a redacted Session summary

A standard-persistence Session can be exported only from the existing
`/privacy` flow. Press `E`, enter one explicit absolute `.md` target in the
same composer used for questions and Slash commands, and review the displayed
target and data categories. Press `Y` to confirm; `Esc` or `Enter` cancels
without sending an export command. To export a historical Session, resume it
explicitly first and then use `/privacy`. Minimal Sessions have no retained
conversation to export and do not offer this action.

The deterministic `kupilot.export-summary.v2` Markdown projection may contain:

- The schema version, export and truncation state, Session ID, sanitized title,
  timestamps, standard persistence mode, and historic display-only Context and
  Namespace.
- Bounded, redacted committed user and final assistant text.
- The bounded safe summary and coverage explanation, without raw framework or
  model state.
- The escaped final free-form answer Markdown, citation-backed compatibility
  metadata, validation warnings, and typed proposed operation/target display
  fields. Proposed actions contain no execution authority.
- Bounded, redacted summaries of referenced accepted Evidence while retained,
  or an explicit expired marker after its detail was removed.

It never includes raw Tool inputs or results, raw or complete container logs,
raw Events, Kubernetes objects, full prompts, model requests, responses or
streams, framework payloads, credentials, Secrets, kubeconfig data or paths,
Reviewer response bytes, Session rules, approval nonces or digests, UID,
resource version, internal fingerprint,
execution authority, or arbitrary repository JSON.
Kupilot does not call the model, Kubernetes, a Tool, an approval path, or an
executor to create this summary.

The target parent must already exist as a safe owner-only directory on
supported platforms. Kupilot rejects relative or unclean targets, symlink path
components, directories, an existing target, and non-sticky ancestor
directories writable by group or others on supported Unix platforms. It writes
a `0600` temporary file in the same directory, synchronizes it, and publishes
without replacing another file.
A failed publication does not expose a partially written target and removes its
temporary file. An uncertain final directory synchronization may leave the
complete no-replace target present, so inspect the explicitly chosen target
before retrying.

The pre-export audit record contains only fixed operation metadata, the Session
ID, UTC time, outcome, and schema version. It contains neither the target path
nor exported content. Audit failure denies the filesystem write. The export is
a separate local copy: deleting its source Session later does not delete the
Markdown file.

Export redaction and limits reduce exposure but cannot guarantee that every
sensitive operational name or free-text value was recognized. The output is
not encrypted or tamper-resistant. Protect it with operating-system access
controls, disk encryption, and an appropriate backup policy. Deleting it is not
a forensic-erasure guarantee.

## Home, configuration, database, cache, and logs

Kupilot resolves one process-frozen Home from `KUPILOT_HOME`, or uses
`$HOME/.kupilot` by default:

| Local category | Fixed path below Home |
| --- | --- |
| Configuration | `config.yaml` |
| Database | `state/kupilot.db` |
| Cache | `cache/` |
| Current operational log | `logs/kupilot.log` |

Known SQLite sidecars use the database base name with `-journal`, `-wal`, or
`-shm`; bounded logs use `.1` and `.2` rotations. No version has been released
with another local layout, so Kupilot performs no legacy discovery or migration.

On supported Unix platforms, newly created Kupilot directories use `0700` and
new files use `0600`. Existing user-managed modes are respected and are not an
availability gate, even when wider. Kupilot may warn about a wider Home or
configuration file, but it does not chmod or chown it. Managed targets must
still have the expected file type and must not use a symbolic link below the
canonical Home.

Interactive model setup may save a model API key as disclosed plaintext in
`config.yaml`. That file is not an encrypted credential store and can be
exposed by its permissions, another same-user process, backups, or snapshots.
The extractor keeps the value out of ordinary typed configuration, TUI history,
SQLite, logs, audit, model content, and child environments. Choosing `session`
instead keeps it only in the current Kupilot process.

The default operational log is limited to code-defined startup, AgentRun
lifecycle, and admitted model-request lifecycle events with allowlisted scalar
fields. A model failure may retain its local request ID, stable class and code,
retryability, observed HTTP status, fixed cause category, and a bounded
function-name-only Kupilot call chain. It stores no Messages, Tool arguments,
resource names, cluster payloads, request or response bodies, headers,
credentials, raw errors, file paths, line numbers, local values, or database
rows.

For a short-lived model investigation, `logging.sensitive_diagnostics: true`
adds the configured endpoint and model, a bounded credential-redacted error
chain, the first 4 KiB of a failed provider response, and a bounded Go stack
with local paths and lines. Kupilot warns at startup. Provider errors may echo
operational content, and these local files are not encrypted. Disable the
setting after reproduction and delete the current log and numbered rotations
when they are no longer needed. Kupilot does not deliberately attach
Authorization, the model key, request bodies, successful responses, streams,
Tool data, or Kubernetes payloads, but an untrusted error may echo operational
content after fixed sensitive-value handling. Each file is at most 1 MiB; at
most three files are kept; rotation or pruning occurs at seven days.

Disable this local application log before startup with:

```yaml
logging:
  enabled: false
```

or set `KUPILOT_LOG_ENABLED=false`. This setting is independent from the
container-output category in `/privacy`: disabling the local application log
does not enable or disable Pod log Tools, and toggling container output does not
enable or disable the local application log.

## Remove local history

Press `D` in `/privacy` to request deletion of the current Session. In the
resume picker, select a historical Session and press `D`. Both paths reuse the
existing non-editable dialog and sole composer. Deletion requires a second,
explicit `Y`; `Esc` or `Enter` cancels without sending an Application command.

For the current Session, Application first cancels and waits for a starting,
active, or terminal-but-not-yet-quiesced AgentRun. Pending and
approved-but-not-executed approvals become terminal and non-executable before
deletion. A consuming approval, cancellation, or failure
to persist that safe state denies deletion. The SQLite adapter then removes the
Session-owned Messages, runs, model metadata, Tool detail, Evidence, Diagnoses,
approval and decision records, linked read/write audit, and Session row in one
transaction. The UI changes current or picker state only after a matching
committed result. A database failure rolls back the graph deletion and reports
the Session as not deleted; an approval already invalidated remains
non-executable.

Press `H` in `/privacy`, then `Y`, to clear all Session history. Application
cancels and awaits starting or active AgentRun work and durably makes every
pending or approved-but-not-executed approval non-executable. A consuming
approval or prerequisite failure denies the request. SQLite removes every
Session graph and associated audit in bounded transactions. Settings and still-
valid model-transfer consent remain, which the confirmation and result state
explicitly disclose. A transaction failure reports that history was not
cleared.

Press `X` in `/privacy`, then `Y`, to delete all local database state. After the
same run and approval gates, Kupilot validates the fixed Home state directory,
the exact `kupilot.db` path, and the known `-journal`, `-wal`, and `-shm`
sidecars. It rejects symlinks and non-regular targets before closing storage.
It then closes the database and removes only those exact files, including
stored Session history, settings, and consent. It never recursively removes a
directory or creates replacement state in the same operation.

A preflight denial leaves the database open and Kupilot running. If any exact
file cannot be removed after storage closes, the UI reports an incomplete
deletion before exit. A successful result also requires acknowledgement before
exit. On the next start, Kupilot creates new validated database state and
requires model-transfer consent again.

Clear-history and delete-all database state do not remove `config.yaml`, cache,
exported summaries, or the local operational log. `kupilot cache clear` removes
only entries below the fixed cache child and does not load or change the
configuration, database, or log. To remove a locally saved key, edit or remove
the exact Home configuration after Kupilot exits. To remove operational logs,
remove only `kupilot.log`, `kupilot.log.1`, and `kupilot.log.2` below the fixed
Home log directory. Do not recursively remove an unrelated parent or an
explicit export directory. User-managed backups retain the same sensitive local
data and remain outside Kupilot deletion.

Per-Session and clear-history deletion are logical operations. Neither logical
row deletion nor file removal guarantees forensic erasure from SQLite free pages, WAL history,
filesystem journals, snapshots, backups, swap, or storage media. Kupilot does
not run automatic `VACUUM` as a secure-delete claim. Use operating-system disk
encryption and manage backups and snapshots when stronger protection is
required. Kupilot cannot delete data a model provider retained under that
provider's policy.

## No product telemetry

Kupilot has no product telemetry, analytics, remote crash reporting,
Kupilot-operated account, update checker, or Kupilot control plane. Normal
diagnosis uses only the selected Kubernetes API and configured model endpoint.
A kubeconfig exec credential program is launched only when declared by the
selected kubeconfig and permitted by configuration; it runs with the local
user's authority and may have its own network or filesystem behavior.
