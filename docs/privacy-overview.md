# Kupilot Privacy Overview

Kupilot runs locally, connects to the Kubernetes API selected by the user, and
uses one user-configured model endpoint. "Local" does not mean all operational
data stays on the workstation. Eligible bounded content is sent to that endpoint
only after informed consent and local safety processing.

This document defines the `v0.4` privacy boundary. It does not claim that
redaction recognizes every sensitive value or that a model provider follows
Kupilot's local retention schedule.

> [!IMPORTANT]
> Context and Namespace names, resource names, Node and workload status,
> topology, Events, user questions, and optional container output can be
> sensitive. Review the exact model origin, categories, namespace-access policy,
> provider terms, and organizational rules before accepting consent.

## Before the first model transfer

Kupilot displays:

- the validated canonical origin of the model endpoint;
- the consent-policy version and current decision;
- every code-defined content category and whether it is enabled;
- whether bounded container output is enabled; and
- categories that are never eligible.

Consent binds the exact policy version, canonical origin hash, and category set.
A changed origin, category, meaning, or policy version invalidates it. Reject,
revoke, cancel, and stale-review paths send zero model content.

Container output is disabled by default and requires its separate category.
Changing it returns consent to pending and cancels the active run before another
model or Pod-log request.

Consent permits content transfer only. It does not authorize Kubernetes RBAC,
cross-Namespace policy, a new capability, raw persistence, approval, or a write.

## Data that may be sent

After consent, an AgentRun may send only bounded content from these categories:

- `user_question`: the current question after normalization,
  sensitive-value handling, and byte limits;
- `safe_conversation_context`: bounded committed conversation context and
  validated free-form answers needed for the current turn;
- `resource_names_and_references`: the Context, working and explicitly targeted
  Namespaces, allowlisted resource names, Kinds, and safe identity fields;
- `projected_kubernetes_status`: reviewed status, conditions, counts,
  timestamps, and fixed relationship summaries for allowlisted built-in
  resources;
- `projected_kubernetes_events`: bounded related Event fields after
  normalization, unsafe-control removal, redaction, and truncation; and
- `redacted_container_output`: bounded current or previous Pod output only when
  enabled and after the same safety pipeline.

Under namespace-access policy `all`, an answer may include projected metadata
from more than the working Namespace. Each item retains its actual Namespace.
Cluster-scoped Namespace, Node, and PersistentVolume references are identified
as cluster-scoped rather than assigned a fake Namespace.

Resource names and operational text may themselves be sensitive. Redaction
reduces risk but cannot guarantee recognition of every private value.

## Data excluded from model content

Kupilot never includes:

- raw kubeconfig or Kubernetes credentials, certificates, keys, bearer tokens,
  ServiceAccount tokens, or exec credential output;
- the model API key or Authorization header as content;
- Kubernetes Secret objects or data;
- ConfigMap `data` or `binaryData`, container environment values, or referenced
  credential values;
- raw Kubernetes objects, full YAML, managed fields, unrestricted labels or
  annotations, arbitrary APIs, or discovery bodies;
- Node addresses, provider IDs, image inventories, system information, taint
  values, or raw capacity/allocatable maps;
- EndpointSlice addresses, raw volume sources, storage credentials, or CSI
  attributes;
- raw or unbounded Events and container output;
- raw local database, configuration, log, prompt, model request/response,
  header, stream, ToolResult, or provider-error content; or
- shell commands, kubectl input/output, terminal control bytes, or executable
  action payloads.

The model key is used only to authenticate to the selected origin. It may come
from masked TUI input, the one-shot environment override, or the optional
plaintext `model.api_key`. Choosing to save it writes only the fixed Home
configuration after an explicit not-encrypted disclosure. It never enters
SQLite, Session content, logs, audit, export, model content, or child
environments.

If an otherwise eligible field appears to contain a high-risk value, Kupilot
redacts or blocks it. It does not send the original merely to complete an
investigation.

## Answers, Evidence, and actions

The visible result is bounded free-form Markdown. Its internal metadata may
reference current-run Evidence and propose a typed action. Neither Markdown nor
an action phrase is authority.

- Only deterministic local capability handling creates Evidence.
- Invalid Evidence references are removed and produce a warning.
- A proposal does not mean approved, attempted, accepted, or verified.
- Local digest-bound approval and durable pre-operation audit are required
  before every mutation.
- Request acceptance and post-operation verification remain distinct durable
  and visible states.

Model output may still be incomplete or wrong. Evidence is a time-bounded
projection and not a guarantee that cluster state is unchanged.

## Local persistence and deletion

Kupilot manages configuration, SQLite state, cache, and operational logs below
`${KUPILOT_HOME:-$HOME/.kupilot}`. SQLite is not encrypted. Database deletion
does not remove configuration, cache, logs, exports, backups, snapshots, or a
user-saved plaintext model key.

SQLite may store one global last-successfully-verified Kubernetes Context
display name as a startup preference, without Namespace, kubeconfig, endpoint,
credential, client, generation, or Session identity. This preference is
independent of Session privacy mode and remains until a later successful
Context activation replaces it or delete-all-local-state removes the database.
It does not restore live authority or become model content by itself.

Standard persistence may additionally store:

- safe Session and AgentRun metadata;
- processed committed user Messages and validated final Markdown Messages;
- Diagnosis metadata, Evidence citations, and typed proposed actions;
- sanitized capability invocation metadata, accepted Evidence, and bounded
  model-request metadata;
- lifecycle and consent audit; and
- approval, write-attempt, and verification audit for supervised actions.

It never stores assembled prompts, streaming deltas, raw model traffic, raw
Tool results, raw Kubernetes objects, raw Events, or raw container output.

By default, safe Session history and validated answers remain until explicit
deletion; capability, Evidence, and model-request detail expires after 30 days;
ordinary lifecycle audit after 90 days; and terminal approval/write audit after
180 days. `/privacy` may shorten operational-detail retention. Viewing
`/status` does not create a new content record.

Minimal persistence keeps conversation, answer, Tool, Evidence, and model detail
in memory only and creates no resumable Session. It still retains mandatory
lifecycle and write-approval audit. Privacy mode never weakens the durable
pre-write gate.

The user can delete the current or a selected historical Session after explicit
confirmation. Active work is cancelled and joined, and unexecuted approval is
made terminal before one transactional graph deletion. A consuming approval or
storage failure denies deletion without a partial-success claim.

Clear-history removes Session graphs in bounded transactions while preserving
the disclosed configuration and, when explicitly stated, valid consent.
Delete-all-local-state closes storage and removes only the validated database
and known sidecars; it does not recursively delete Home. Logical deletion and
file removal are not forensic erasure from free pages, WAL, snapshots, backups,
swap, or storage media.

## Redacted summary export

A current standard Session may be exported only through `/privacy` as
`kupilot.export-summary.v2`, with an
explicit absolute Markdown destination and second confirmation. The versioned
allowlist contains safe Session display metadata, bounded processed committed
user and assistant text, free-form validated answer metadata, and referenced
Evidence summaries or expired markers.

It excludes raw Tool input/output, raw logs or Events, Kubernetes objects, full
prompts, model traffic, credentials, Secrets, kubeconfig data, approval nonce,
and execution authority. Minimal Sessions cannot be exported.

The file is created with owner-only permissions and atomic no-replace
publication. It is not encrypted and survives later Session deletion; the user
controls its lifecycle.

## Local operational logging

The default bounded JSON log contains code-defined startup, run, and
model-request lifecycle fields. It excludes conversation content, capability
arguments, resource names, cluster payloads, bodies, credentials, raw errors,
paths, and local values.

Explicit `logging.sensitive_diagnostics: true` may add the bounded provider
failure details documented by ADR-0036. Provider text may reflect user or
cluster data, so the setting is for short-lived local troubleshooting. Logs are
limited to three files of at most 1 MiB each and seven days. Logging can be
disabled independently from the container-output consent category.

## No product telemetry

Kupilot has no usage analytics, remote crash reporting, Kupilot-operated
account, update checker, hosted control plane, or telemetry endpoint. Normal
network paths are the selected Kubernetes API and configured model origin. A
kubeconfig exec credential program may have independent behavior outside
Kupilot's control.

## References

- [Configuration](configuration.md)
- [Least-Privilege RBAC](rbac/README.md)
- [Security Threat Model](security.md)
- [Data Retention Contract](data-retention.md)
- [Scope](scope.md)
- [ADR-0026: Require Informed Consent Before Model Transfer](adr/0026-require-informed-consent-before-model-transfer.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](adr/0038-use-free-form-answers-with-verified-evidence-metadata.md)
