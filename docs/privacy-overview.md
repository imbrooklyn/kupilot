# Kupilot Privacy Overview

Kupilot runs locally, connects to the Kubernetes API selected by the user, and
uses explicitly configured named model profiles and optional data sources.
"Local" does not mean all operational data stays on the workstation. Eligible
bounded content is sent only to the destination bound to its fixed consumer
role after informed consent and local safety processing.

This document defines the accepted `v0.5` privacy target and distinguishes it
from current reachability. The checked-in implementation now has typed named
Agent and optional Reviewer profiles, role/origin/category consent, safe Session
context and summarization, and deterministic permission/Reviewer routing for the
existing supervised Deployment restart. Expanded data sources, public
permission controls, and new execution paths remain targets and are not claimed
reachable. Kupilot does not claim that redaction recognizes every sensitive
value or that a model or data provider follows its local retention schedule.

> [!IMPORTANT]
> Context and Namespace names, resource names, Node and workload status,
> topology, Events, user questions, and optional container output can be
> sensitive. Review the exact model origin, categories, namespace-access policy,
> provider terms, and organizational rules before accepting consent.

## Before the first model transfer

Kupilot displays:

- the fixed consumer role, named profile, and validated canonical origin;
- the consent-policy version and current decision;
- every code-defined content category and whether it is enabled;
- whether bounded container output is enabled; and
- categories that are never eligible.

Consent binds the exact policy version, model role, canonical origin hash, and
category set. A changed profile, role, origin, category, meaning, or policy
version invalidates it and any dependent pending authority. Reject, revoke,
cancel, and stale-review paths send zero model content. Agent consent cannot be
reused for a Reviewer at another origin.

Container output, metrics detail, an exact ConfigMap key or non-credential
container environment value, container-file content, optional Prometheus or
Loki results, remote diagnostic output, and local process output use separate
categories as applicable and remain disabled until their exact capability and
policy enable them. Changing a category returns the affected consent to pending
and cancels old work before another transfer.

Consent permits content transfer only. It does not authorize Kubernetes RBAC,
cross-Namespace policy, a new capability, raw persistence, approval, or a write.

## Data that may be sent

After consent, an AgentRun may send only bounded content from code-defined
categories, including:

- `user_question`: the current question after normalization,
  sensitive-value handling, and byte limits;
- `safe_conversation_context`: bounded committed same-Session user and final
  assistant context, plus a safe summary and recent tail when required;
- `resource_names_and_references`: the Context, working and explicitly targeted
  Namespaces, allowlisted resource names, Kinds, and safe identity fields;
- `projected_kubernetes_status`: reviewed status, conditions, counts,
  timestamps, fixed relationship summaries, and exact safe Secret metadata for
  allowlisted built-in resources;
- `projected_kubernetes_events`: bounded related Event fields after
  normalization, unsafe-control removal, redaction, and truncation;
- `redacted_container_output`: bounded current, previous, explicit all-container,
  or local-search result only when enabled and after the same safety pipeline;
- separately versioned exact ConfigMap-key or non-credential container-
  environment values only under `review`, category consent, and sink policy;
- separately versioned projected metrics, explicitly configured Prometheus or
  Loki results, container-file content, and remote/local diagnostic output only
  when the exact capability, sink, and consent category are enabled; and
- the minimum normalized `ActionEnvelope` and policy facts sent to an optional
  `approval_reviewer`, without raw cluster data or general Session history by
  default.

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
- Kubernetes Secret values or an unprojected Secret object;
- credential-bearing ConfigMap or environment values, ServiceAccount material,
  or another referenced credential value;
- raw Kubernetes objects, full YAML, managed fields, unrestricted labels or
  annotations, arbitrary APIs, or discovery bodies;
- Node addresses, provider IDs, image inventories, system information, taint
  values, or raw capacity/allocatable maps;
- EndpointSlice addresses, raw volume sources, storage credentials, or CSI
  attributes;
- raw or unbounded Events, logs, metrics, files, query results, remote output,
  or local process output;
- raw local database, configuration, log, prompt, model request/response,
  header, stream, ToolResult, or provider-error content; or
- arbitrary shell commands, unapproved kubectl/helm/argocd input or output,
  terminal control bytes, process environments, or generic executable payloads.

Each model key is used only to authenticate its selected role and origin. An
Agent key may come from masked TUI input, a one-shot Agent environment alias,
or optional plaintext `models.agent.api_key`. A Reviewer with its own
credential may use its one-shot role variable or optional plaintext
`models.approval_reviewer.api_key`; an inheriting Reviewer receives a distinct
opaque clone. Choosing to save discloses exactly which file-sourced plaintext
role keys will be written to the fixed Home configuration. No key enters
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
- Deterministic risk and permission routing plus a digest-bound immutable
  `ActionEnvelope` and durable pre-operation audit are required before every
  sensitive or effectful operation.
- A Reviewer recommendation is not authority and can cover only `review` work;
  malformed or failed review is fail-closed.
- Request acceptance and post-operation verification remain distinct durable
  and visible states.

Model output may still be incomplete or wrong. Evidence is a time-bounded
projection and not a guarantee that cluster state is unchanged.

Some compatible models emit commentary before or alongside a structured Tool
selection. Kupilot bounds and validates that content while resolving the
response mode, then discards it when the response terminates with
`tool_calls`. It is not shown, persisted, cited, or treated as Tool or action
authority. Ordinary text responses continue through the normal answer and
safety pipeline.

When a known, structurally safe Tool selection fails strict semantic binding,
the next bounded model decision may receive fixed local policy feedback. The
feedback states only code-owned schema and Namespace rules; it does not echo
the rejected arguments, live scope values, Kubernetes data, credentials, or a
claimed Tool result. The rejected batch creates no ToolInvocation, persistence
record, or Evidence.

Some compatible endpoints expose reasoning fragments separately from answer
text. Kupilot accepts only the matching bounded representation produced by the
pinned Eino component, checks it for credential reflection, counts it against
the assistant-response ceiling, and discards it before message assembly. It is
not shown, persisted, cited, logged, returned to the model, or treated as Tool
or action authority.

Final-answer content may be shown provisionally after Eino decodes and validates
each content chunk. Only the first top-level `answer_markdown` string is
eligible. Before a fragment reaches Application or the TUI, Kupilot checks the
exact model credential across chunk boundaries, normalizes split terminal
controls, applies the fixed sensitive-value policy, enforces byte and event
ceilings, and verifies current scope. Raw SSE, envelope syntax, Evidence
citations, proposed actions, reasoning and provider metadata remain excluded.
The complete decoded Diagnosis is checked again and only its final validated
answer may be persisted or committed to terminal scrollback.

## Terminal output and scrollback

Ordinary conversation starts in one cleared primary-screen live frame. Kupilot
removes each newly immutable, bounded terminal-safe history block from the live
projection, settles a compact frame, and inserts bounded row batches above it.
Each immutable block includes one inert trailing separator row and remains
pending until insertion is acknowledged. Composer drafts, placeholders, footer
and dialog state, the live Working row, provisional model output, and transient
layout spacer rows are excluded. Completed history may therefore remain in
terminal-emulator scrollback during and after Kupilot.

Provisional output exists only in the replaceable live Agent entry. A Tool
request clears any draft from that pre-Tool turn. Completion replaces the draft
with the validated answer; cancellation, timeout, stale scope, model failure,
and final-validation failure replace it with code-authored terminal text. None
of those transitions promotes a partial response into Session history.

If shutdown interrupts an insertion after its first batch may have reached the
terminal, Kupilot does not replay the entire ambiguous block. This prevents a
duplicate terminal disclosure; retained Session data continues to follow the
selected persistence mode.

Terminal scrollback is not SQLite or a second Kupilot-created history store;
its capture, lifetime, search, copy, and deletion behavior belong to the
terminal emulator, multiplexer, remote-session recorder, and operating system.
Minimal persistence prevents cross-process Session resume but does not retract
text already displayed. `/new`, Session deletion, clear-history, and
delete-all-local-state likewise do not clear terminal-owned scrollback. Users
handling sensitive operational data must use their terminal's own clearing and
retention controls.

Only the same normalized, bounded projection eligible for visible rendering
enters scrollback. Kupilot does not emit raw Kubernetes objects, credentials,
provider bodies, model-selected escape sequences, clipboard controls, or
device-control content through this path.

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
- one bounded safe Session summary, coverage metadata, and the eligible recent
  Message tail;
- Diagnosis metadata, Evidence citations, and typed proposed actions;
- sanitized capability invocation metadata, accepted Evidence, and bounded
  model-request metadata;
- lifecycle and consent audit; and
- versioned ActionEnvelope projection, permission decision, one-attempt outcome,
  cleanup, and verification audit for supervised actions.

It never stores assembled prompts, streaming deltas, raw model traffic, raw
Tool results, raw Kubernetes objects, raw Events, or raw container output.

By default, safe Session history and validated answers remain until explicit
deletion; capability, Evidence, and model-request detail expires after 30 days;
ordinary lifecycle audit after 90 days; and terminal approval/write audit after
180 days. `/privacy` may shorten operational-detail retention. Viewing
`/status` does not create a new content record.

Minimal persistence keeps conversation, answer, summary, coverage, Tool,
Evidence, and model detail in memory only and creates no resumable Session. It
still retains mandatory lifecycle and action audit. Privacy mode never weakens
the durable pre-operation gate.

In standard mode, explicit resume only reads eligible safe local history and
unverified candidates. It performs zero model, Kubernetes, Tool, Reviewer,
approval, process, or executor I/O. On the next explicit question, when retained
eligible history exists and current role/origin/category consent, scope, policy,
coverage, and budget checks pass, Application must transmit exactly one ordered,
bounded representation of that history. A failed gate causes zero model calls
and no current-question-only fallback. Historic scope, Evidence, permission
rules, reviews, ActionEnvelopes, approvals, and execution state are never
restored as authority.

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
user and assistant text, the bounded safe Session-context summary and its
content-free coverage explanation, free-form validated answer metadata, and
referenced Evidence summaries or expired markers.

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
network paths are the selected Kubernetes API, the explicit model origins, and
explicit optional Prometheus or Loki destinations. A kubeconfig exec credential
program, admitted local argv, Pod Exec, or diagnostic Pod may have behavior
outside Kupilot's full control and is separately disclosed and gated.

## References

- [Configuration](configuration.md)
- [Least-Privilege RBAC](rbac/README.md)
- [Security Threat Model](security.md)
- [Data Retention Contract](data-retention.md)
- [Scope](scope.md)
- [ADR-0026: Require Informed Consent Before Model Transfer](adr/0026-require-informed-consent-before-model-transfer.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](adr/0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045: Admit Controlled Execution and Remediation](adr/0045-admit-controlled-execution-and-remediation.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
