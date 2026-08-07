# KuPilot Privacy Overview

KuPilot runs locally and connects directly to the Kubernetes API selected by the
user. It also uses a user-configured cloud model to support the Agent. "Local"
does not mean that all diagnostic data stays on the machine: eligible, bounded
data is sent to that model only after informed consent and local safety
processing.

This document states the `v0.1` privacy contract. It is a product boundary, not
a claim that redaction can eliminate every possible sensitive value.

## Before the first cloud transfer

Before sending model content, KuPilot must show:

- The configured model endpoint host.
- The categories of data that may be sent.
- Whether bounded container log excerpts are enabled.
- The categories that are excluded from model content.

The user must confirm this policy. A change to the endpoint origin or an eligible
data category requires confirmation again. The user remains responsible for
checking that the selected model provider, endpoint, and organizational policy
are appropriate for the cluster data involved.

## Data that may be sent to the cloud model

After consent, an AgentRun may send only the data needed for its bounded
Diagnosis, including:

- The user's question after local sensitive-value redaction.
- ClusterScope display names, such as the selected Context and Namespace, and the
  selected or discovered ResourceRef names.
- Safe resource projections, including relevant status, conditions, counts,
  reasons, timestamps, and bounded relationship summaries.
- Recent related Event reasons and messages after normalization, control-character
  removal, redaction, and truncation.
- Bounded current or previous Pod container log excerpts, only when log use is
  enabled, after normalization, control-character removal, redaction, and
  truncation.
- Bounded safe Session context, ToolInvocation results, and Evidence required to
  continue or explain the current AgentRun.

Resource names, Context names, Namespace names, Events, and application logs may
themselves be sensitive. KuPilot treats them as cluster data even after local
processing. Redaction lowers risk but cannot guarantee that every private value
has been recognized.

## Data excluded from model content

KuPilot must never include the following in prompts, Tool results, or other model
content:

- Raw kubeconfig contents.
- Kubernetes bearer tokens, client certificates, private keys, exec credential
  output, or ServiceAccount token material.
- Kubernetes Secret objects or Secret data.
- ConfigMap data, raw container environment values, or referenced credential
  values.
- The model API key as prompt or diagnostic content.
- Full raw Kubernetes objects, full YAML, managed fields, or unrestricted
  annotations.
- Raw, unbounded Event payloads or raw container log bytes.
- Raw local database, configuration, or application log contents.

The model API key is necessarily used as an authentication credential when
contacting the configured endpoint. It is restricted to that transport purpose:
it is not model content and must not be written to configuration, Session
history, logs, or the local database. Kubernetes credentials are used only by the
local Kubernetes client and are not sent to the model endpoint.

KuPilot does not provide a Tool that reads Kubernetes Secrets. If an Event, log,
resource field, or user question appears to contain a high-risk value, KuPilot
redacts or blocks that content. It never sends the original merely to preserve
diagnostic completeness.

## Local persistence

The `v0.1` persistence contract keeps only the data needed for safe Session
history and auditability:

- Session and AgentRun metadata.
- Redacted user messages and final assistant messages.
- Sanitized ToolInvocation parameters and summaries, Evidence, and usage or
  audit metadata.

It does not persist assembled full prompts, streaming deltas, raw model protocol
bodies, raw Tool results, raw Kubernetes objects, or raw container logs.

Local persistence is not an encrypted vault. The product relies on restrictive
file permissions, minimal storage, retention controls, and the user's operating
system disk protection; it must not claim that the local database is encrypted.
A minimal-persistence mode keeps message content, ToolInvocation details, and
Evidence in process memory rather than durable history, so that the Session cannot
be resumed across processes.

## No product telemetry

KuPilot has no product telemetry, usage analytics, remote crash reporting,
server-side account, or KuPilot-operated control plane in the planned `v0.1`.
Normal requests to the user-configured model endpoint and Kubernetes API are the
only product network paths required for a Diagnosis.

## Diagnosis and action safety

Evidence is a time-bounded observation, not a guarantee that the cluster remains
unchanged. A Diagnosis may be incomplete or wrong, and KuPilot may be unable to
identify a root cause. `v0.1` recommended actions are text for the user to
evaluate; KuPilot does not execute them and has no Approval Dialog.

See [Scope](./scope.md) for the fixed Tool and version boundaries and the
[Glossary](./glossary.md) for canonical terminology.
