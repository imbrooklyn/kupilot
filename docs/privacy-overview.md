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

## Local standard persistence

The current public binary always starts standard-persistence Sessions. It may
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

Safe Session history and Diagnosis remain until local-state deletion. Detailed
Tool, Evidence, and model-request metadata expire after 30 days by default;
read-only lifecycle audit expires after 90 days. If Evidence detail expires
first, historic Diagnosis text remains history but cannot be presented as
current proof.

The repository enforces minimal-persistence semantics for compatible callers:
content remains in memory and the Session is non-resumable. The current public
CLI/TUI does not expose a minimal-persistence selector and must not be relied on
to start such a Session.

The current public CLI/TUI also does not expose per-Session deletion,
clear-history, or delete-all. [Privacy and Local Data](user-guide/privacy-and-local-data.md)
documents the exact database and log paths, known sidecars, retention, and the
safe manual cleanup boundary. File removal is not forensic erasure from backups,
snapshots, SQLite free pages, WAL history, swap, or storage media.

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
