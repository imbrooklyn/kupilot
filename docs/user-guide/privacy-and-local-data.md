# Privacy and Local Data

KuPilot orchestrates locally and connects directly to the selected Kubernetes
API and configured model endpoint. Local orchestration does not mean all
diagnostic data stays on the workstation. Review both cloud transfer and local
retention before using KuPilot with a cluster.

## Cloud model categories

Before the first model-content request, `/privacy` displays the canonical model
destination, consent-policy version, current decision, and the exact enabled
categories. The same dialog also displays local persistence and retention; those
controls do not grant model-transfer consent.

1. `user_question`: the question after local normalization,
   sensitive-value handling, and byte limits.
2. `safe_conversation_context`: bounded safe context for the current run,
   including structured Tool results and Evidence when needed.
3. `resource_names_and_references`: Context, Namespace, and admitted resource
   names or references.
4. `projected_kubernetes_status`: allowlisted status, conditions, counts,
   timestamps, and relationships.
5. `projected_kubernetes_events`: bounded Event reasons and messages after local
   safety processing.
6. `redacted_container_output`: bounded current or previous container-output
   facts after normalization and redaction.

The container-output category is disabled by default. In the privacy dialog,
`L` toggles it. A toggle returns consent to pending and cancels an active run;
press `A` only after reviewing and accepting the new exact category set. `R`
rejects pending consent or revokes accepted consent. `Esc` cancels the review
without granting authority.

Consent binds the policy version, a hash of the canonical origin, and the whole
enabled-category set. A destination, category, category meaning, or policy
version change requires a new decision. Consent does not broaden Kubernetes
RBAC, add a Tool, make a prohibited source eligible, authorize a write, or
guarantee that local redaction recognized every sensitive value.

## Data never eligible for model content

The model-content contract excludes:

- Kubeconfig contents, Kubernetes bearer tokens, client certificates, private
  keys, ServiceAccount token material, and exec credential output.
- Kubernetes Secret objects and data, ConfigMap data, container environment
  values, and referenced credential values.
- The model API key as content. It is used only as an authentication header for
  the validated configured origin.
- Raw Kubernetes objects, full YAML, managed fields, unrestricted annotations,
  EndpointSlice addresses, and arbitrary API types.
- Raw or unbounded Events and container output.
- Raw configuration, SQLite, local application-log, prompt, request, response,
  stream, header, and endpoint-error contents.

Eligible resource names, Event messages, user text, and application output may
still be sensitive after processing. KuPilot treats them as cluster data and
blocks high-confidence sensitive values rather than sending originals for
diagnostic completeness.

## Local persistence and retention controls

`/privacy` displays the current Session's persistence mode and the effective
retention periods. A standard-persistence Session may keep:

- Session and AgentRun metadata.
- Locally processed committed user Messages and final validated assistant
  Messages.
- Structured Diagnoses.
- Sanitized ToolInvocation metadata, accepted Evidence, and bounded model
  request metadata.
- Allowlisted lifecycle audit and the consent tuple.

It does not persist raw container output, raw Events, raw Tool results, raw
Kubernetes objects, assembled prompts, model streams, protocol bodies,
credentials, or kubeconfig paths.

Default logical retention is:

| Data | Default lifetime |
| --- | --- |
| Safe Session history and Diagnosis | Until explicit deletion of that Session |
| ToolInvocation, Evidence, and model-request detail | 30 days |
| Read-only lifecycle audit | 90 days |
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
stores no user or assistant Message content, Diagnosis, Tool detail, Evidence,
or model-request detail. Its necessary Session shell and required lifecycle,
consent, approval, and write-audit records remain subject to their own retention
rules. A minimal Session is unavailable to the resume picker, exact-ID resume,
and `--last` across processes.

## Export a redacted Session summary

A standard-persistence Session can be exported only from the existing
`/privacy` flow. Press `E`, enter one explicit absolute `.md` target in the
same composer used for questions and Slash commands, and review the displayed
target and data categories. Press `Y` to confirm; `Esc` or `Enter` cancels
without sending an export command. To export a historical Session, resume it
explicitly first and then use `/privacy`. Minimal Sessions have no retained
conversation to export and do not offer this action.

The deterministic `kupilot.export-summary.v1` Markdown projection may contain:

- The schema version, export and truncation state, Session ID, sanitized title,
  timestamps, standard persistence mode, and historic display-only Context and
  Namespace.
- Bounded, redacted committed user and final assistant text.
- The four structured Diagnosis sections: confirmed facts, hypotheses, missing
  information, and recommended actions, including their bounded supporting
  fields and Evidence references.
- Bounded, redacted summaries of referenced accepted Evidence while retained,
  or an explicit expired marker after its detail was removed.

It never includes raw Tool inputs or results, raw or complete container logs,
raw Events, Kubernetes objects, full prompts, model requests, responses or
streams, framework payloads, credentials, Secrets, kubeconfig data or paths,
approval nonces or digests, execution authority, or arbitrary repository JSON.
KuPilot does not call the model, Kubernetes, a Tool, an approval path, or an
executor to create this summary.

The target parent must already exist as a safe owner-only directory on
supported platforms. KuPilot rejects relative or unclean targets, symlink path
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

## Database and log paths

The database is `kupilot.db` in the resolved state directory:

| Platform | Database |
| --- | --- |
| Linux | `${XDG_STATE_HOME:-$HOME/.local/state}/kupilot/kupilot.db` |
| macOS | `~/Library/Application Support/KuPilot/kupilot.db` |

Known sidecars use the same base name with `-journal`, `-wal`, or `-shm`.
Configured `paths.state_dir` or `KUPILOT_STATE_DIR` replaces the state
directory. The directory must be an absolute, normalized, non-root,
non-symlink path. On supported Unix platforms it uses mode `0700`, while the
database and sidecars use `0600`.

KuPilot also writes a small JSON operational log by default:

| Platform | Current log |
| --- | --- |
| Linux | `${XDG_STATE_HOME:-$HOME/.local/state}/kupilot/logs/kupilot.log` |
| macOS | `~/Library/Logs/KuPilot/kupilot.log` |

`paths.log_dir` or `KUPILOT_LOG_DIR` replaces that directory. The log is limited
to code-defined startup and AgentRun lifecycle events and allowlisted scalar
fields. It stores no Messages, Tool arguments, resource names, cluster payloads,
request or response bodies, headers, credentials, raw errors, or database rows.
Each file is at most 1 MiB; at most three files are kept; rotation or pruning
occurs at seven days.

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
same run and approval gates, KuPilot validates the configured state directory,
the exact `kupilot.db` path, and the known `-journal`, `-wal`, and `-shm`
sidecars. It rejects symlinks and non-regular targets before closing storage.
It then closes the database and removes only those exact files, including
stored Session history, settings, and consent. It never recursively removes a
directory or creates replacement state in the same operation.

A preflight denial leaves the database open and KuPilot running. If any exact
file cannot be removed after storage closes, the UI reports an incomplete
deletion before exit. A successful result also requires acknowledgement before
exit. On the next start, KuPilot creates new validated database state and
requires model-transfer consent again.

Clear-history and delete-all do not remove exported summaries or the local
operational log. To remove operational logs after KuPilot exits, resolve the
exact configured log directory and remove only `kupilot.log`, `kupilot.log.1`,
and `kupilot.log.2`. Do not recursively remove a home, XDG base, Application
Support, state, or log root. User-managed backups retain the same sensitive
local metadata and remain outside KuPilot deletion.

Per-Session and clear-history deletion are logical operations. Neither logical
row deletion nor file removal guarantees forensic erasure from SQLite free pages, WAL history,
filesystem journals, snapshots, backups, swap, or storage media. KuPilot does
not run automatic `VACUUM` as a secure-delete claim. Use operating-system disk
encryption and manage backups and snapshots when stronger protection is
required. KuPilot cannot delete data a model provider retained under that
provider's policy.

## No product telemetry

KuPilot has no product telemetry, analytics, remote crash reporting,
KuPilot-operated account, update checker, or KuPilot control plane. Normal
diagnosis uses only the selected Kubernetes API and configured model endpoint.
A kubeconfig exec credential program is launched only when declared by the
selected kubeconfig and permitted by configuration; it runs with the local
user's authority and may have its own network or filesystem behavior.
