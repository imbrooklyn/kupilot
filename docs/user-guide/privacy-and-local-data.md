# Privacy and Local Data

KuPilot orchestrates locally and connects directly to the selected Kubernetes
API and configured model endpoint. Local orchestration does not mean all
diagnostic data stays on the workstation. Review both cloud transfer and local
retention before using KuPilot with a cluster.

## Cloud model categories

Before the first model-content request, `/privacy` displays the canonical model
destination, consent-policy version, current decision, and the exact enabled
categories:

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

## Standard local persistence

The current public binary always creates standard-persistence Sessions. The
SQLite database may keep:

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
| Safe Session history and Diagnosis | Until user deletion of local state |
| ToolInvocation, Evidence, and model-request detail | 30 days |
| Read-only lifecycle audit | 90 days |
| Model-transfer consent | Until revoked, cleared, or invalidated |

When operational detail expires, historic Diagnosis text may remain, but the UI
must not present removed Evidence detail as current proof.

The storage contracts also enforce a minimal-persistence mode in which content
is memory-only and the Session is non-resumable. The current public CLI/TUI does
not expose a way to select that mode and must not be described as providing it
to ordinary users.

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

The current public CLI/TUI does not expose per-Session deletion, clear-history,
or delete-all. To remove local KuPilot history with the current binary:

1. Quit every KuPilot process so no database or log handle remains open.
2. Resolve the exact state and log directories from the effective typed
   configuration and the tables above. Do not infer them from the current
   working directory.
3. If retention is required, make an owner-protected backup under your own
   policy. A backup retains the same sensitive local metadata.
4. Remove only `kupilot.db` and the known `kupilot.db-journal`,
   `kupilot.db-wal`, and `kupilot.db-shm` files from that exact state directory.
   Removing the database also removes stored Session history, settings, and
   consent.
5. If local operational logs must also be removed, remove only `kupilot.log`,
   `kupilot.log.1`, and `kupilot.log.2` from the exact log directory.

Do not recursively remove a home, XDG base, Application Support, state, or log
root. On the next start, KuPilot creates new validated local state and requires
model-transfer consent again.

Logical row deletion and file removal do not guarantee forensic erasure from
SQLite free pages, WAL history, filesystem journals, snapshots, backups, swap,
or storage media. Use operating-system disk encryption and manage backups and
snapshots when stronger protection is required. KuPilot cannot delete data a
model provider retained under that provider's policy.

## No product telemetry

KuPilot has no product telemetry, analytics, remote crash reporting,
KuPilot-operated account, update checker, or KuPilot control plane. Normal
diagnosis uses only the selected Kubernetes API and configured model endpoint.
A kubeconfig exec credential program is launched only when declared by the
selected kubeconfig and permitted by configuration; it runs with the local
user's authority and may have its own network or filesystem behavior.
