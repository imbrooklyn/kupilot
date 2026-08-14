# KuPilot Privacy Overview

KuPilot runs locally and connects directly to the Kubernetes API selected by the
user. It also uses one user-configured model endpoint to support the Agent.
"Local" does not mean that all diagnostic data stays on the machine: eligible,
bounded content is sent to that endpoint only after informed consent and local
safety processing.

This document states the `v0.1` privacy boundary. It is not a claim that
redaction can recognize every sensitive value or that a model provider deletes
data on KuPilot's schedule.

> [!IMPORTANT]
> Context, Namespace, resource names, status fields, Event messages, user
> questions, and optional container output can be sensitive operational data.
> Review the exact model destination, enabled categories, provider policy, and
> organizational rules before accepting consent.

## Before the first cloud transfer

Before sending model content, KuPilot displays:

- The validated canonical origin of the configured model endpoint.
- The consent-policy version and current decision.
- Every code-defined data category and whether it is enabled.
- Whether bounded container output is enabled.
- Categories that are never eligible for model content.

The user must accept the exact displayed tuple. A changed origin, enabled
category, category meaning, or policy version invalidates prior consent. Reject,
revoke, cancel, and stale-review paths send zero model content.

The container-output category is disabled by default. `/privacy` opens the
review; `L` toggles the category, `A` accepts the current tuple, `R` rejects or
revokes, and `Esc` cancels. Toggling the category returns consent to pending and
cancels an active AgentRun before another model or Pod log request.

Consent is necessary but not sufficient. It never authorizes a Secret read,
broader Kubernetes access, a new Tool, raw persistence, or a write.

## Data that may be sent to the model

After consent, an AgentRun may send only the bounded data needed for its
Diagnosis from these categories:

- `user_question`: the question after normalization, sensitive-value handling,
  and byte limits.
- `safe_conversation_context`: bounded safe context for the current run,
  including structured Tool results and Evidence when needed.
- `resource_names_and_references`: the selected Context and Namespace and
  admitted resource names or references.
- `projected_kubernetes_status`: allowlisted status, conditions, counts,
  timestamps, and bounded relationship summaries.
- `projected_kubernetes_events`: recent related Event reasons and messages after
  normalization, unsafe-control removal, redaction, and truncation.
- `redacted_container_output`: bounded current or previous Pod container-output
  facts, only when enabled, after normalization, unsafe-control removal,
  redaction, and truncation.

Resource names, Context names, Namespace names, Events, and application output
may themselves be sensitive. Redaction lowers risk but cannot guarantee that
every private value in an otherwise eligible field is detected.

## Data excluded from model content

KuPilot must never include the following as prompt, Tool result, or other model
content:

- Raw kubeconfig contents.
- Kubernetes bearer tokens, client certificates, private keys, exec credential
  output, or ServiceAccount token material.
- Kubernetes Secret objects or Secret data.
- ConfigMap data, raw container environment values, or referenced credential
  values.
- The model API key as content.
- Full raw Kubernetes objects, full YAML, managed fields, unrestricted
  annotations, EndpointSlice addresses, or arbitrary API types.
- Raw, unbounded Event payloads or raw container log bytes.
- Raw local database, configuration, application log, prompt, model protocol,
  header, or endpoint-error contents.

The model API key is necessarily used as an authentication credential for the
configured origin, but it is not model content. Kubernetes credentials are used
only by the local Kubernetes client. Neither credential category is written to
Session history, the local application log, or SQLite.

KuPilot has no Tool that reads Kubernetes Secrets. If an eligible Event, log,
resource field, user question, or model result appears to contain a high-risk
value, KuPilot redacts or blocks it. It does not send the original merely to
preserve diagnostic completeness.

## Local persistence and deletion

`/privacy` displays the current standard or minimal persistence mode, the
effective operational-detail period, the fixed read/lifecycle and approval/write
audit periods, and the effect on cross-process resume. A standard Session may
keep:

- Session and AgentRun metadata.
- Locally processed committed user Messages and final validated assistant
  Messages.
- Structured Diagnoses.
- Sanitized ToolInvocation metadata, accepted Evidence, and bounded model
  request metadata.
- Allowlisted lifecycle audit and the consent tuple.

It does not persist assembled prompts, streaming deltas, raw model traffic, raw
Tool results, raw Kubernetes objects, raw Events, or raw container output.

Safe Session history and Diagnosis remain until explicit Session deletion.
Detailed Tool, Evidence, and model-request metadata expire after 30 days by
default; read-only lifecycle audit expires after 90 days. Terminal approval and
decision records and approval/write audit expire after 180 days; retention
cleanup never removes pending or approved authority. `/privacy` can only shorten
the operational-detail period.
If Evidence detail expires first, historic Diagnosis text remains history but
cannot be presented as current proof.

The persistence-mode control starts a new empty Session; it does not mutate the
current Session. Minimal content remains in memory and the Session is
non-resumable across processes. The resume picker, exact-ID resume, and `--last`
exclude it.

`D` in `/privacy` deletes the current Session; `D` on a resume-picker row deletes
that historical Session. A second explicit confirmation is required. Starting,
active, and terminal-but-not-yet-quiesced runs are cancelled and awaited, and
pending or approved-but-not-executed approvals are made
non-executable before one transactional graph deletion. A consuming approval or
database failure denies deletion without a partial-success claim. There is no
separate Session-management surface, clear-history command, or delete-all UI.
[Privacy and Local Data](user-guide/privacy-and-local-data.md) documents the
exact database and log paths and the safe manual all-state cleanup boundary.
Logical deletion and file removal are not forensic erasure from backups,
snapshots, SQLite free pages, WAL history, swap, or storage media.

A current standard-persistence Session may be exported only through
`/privacy`. The existing composer accepts one explicit absolute Markdown target,
and a second view previews the fixed data categories and requires `Y`. The
versioned allowlist contains safe Session display metadata, bounded redacted
committed user and final assistant text, the four structured Diagnosis sections,
and referenced Evidence summaries or expired markers. It excludes raw Tool and
log data, full prompts and model traffic, credentials, Secrets, kubeconfig data,
and approval authority. Minimal Sessions cannot be exported.

Export writes a new user-controlled local copy with owner-only permissions,
same-directory temporary publication, atomic no-replace semantics, and a
content-free and path-free pre-export audit event. It calls no model, cluster,
Tool, approval, or executor path. Exported files are not encrypted by KuPilot
and are not removed by later Session deletion; users control their retention,
backup, and deletion after publication.

## Local operational logging

A bounded allowlisted JSON operational log is enabled by default. It contains
only code-defined startup and AgentRun lifecycle events with validated scalar
fields, not conversation content, Tool arguments, resource names, cluster
payloads, request or response bodies, credentials, or raw errors. It is limited
to three files of at most 1 MiB each and seven days.

Set `logging.enabled: false` or `KUPILOT_LOG_ENABLED=false` to disable this local
file sink. This is separate from the `/privacy` container-output category: one
controls local operational records, while the other controls whether bounded
Pod log Tools may read and transfer processed facts.

SQLite and local logs are not encrypted by KuPilot. Owner-only permissions,
operating-system account isolation, disk protection, and the user's backup and
snapshot policy are the relevant local controls.

## No product telemetry

KuPilot has no product telemetry, usage analytics, remote crash reporting,
KuPilot-operated account, update checker, or KuPilot control plane in `v0.1`.
Normal Diagnosis network paths are the selected Kubernetes API and configured
model endpoint. A kubeconfig exec credential program, when explicitly declared
and allowed, runs with the local user's authority and may have independent
network or filesystem behavior.

## Diagnosis and action safety

Evidence is a time-bounded observation, not a guarantee that the cluster remains
unchanged. A Diagnosis may be incomplete or wrong, and KuPilot may be unable to
identify a root cause. `v0.1` recommendations are text for the user to evaluate;
KuPilot does not execute them and has no approval dialog.

See [Configuration](configuration.md), [Least-Privilege RBAC](rbac/README.md),
[Security Threat Model](security.md), and [Scope](scope.md) for the complete
controls.
