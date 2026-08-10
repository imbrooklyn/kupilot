# Diagnostic Capabilities

KuPilot supports evidence-first diagnosis for the eight categories described
here. The [Product Contract](product.md) remains authoritative for the product
boundary. Support means that the Agent can select a bounded Evidence path and
produce a cautious, structured Diagnosis. It does not mean that every incident
has a discoverable root cause or that a particular model sentence is guaranteed.

## Common contract

Every evaluated Diagnosis contains four distinct collections: confirmed facts,
hypotheses, missing information, and recommended actions. Each confirmed fact
cites accepted Evidence from the same AgentRun. Hypotheses retain bounded
confidence and a falsifier. Forbidden, absent, stale, conflicting, sensitive-
blocked, partial, or truncated observations remain visible as missing
information. Every recommendation is marked as not executed.

The Agent uses only the six fixed read-only Tools. A scenario changes query
guidance and required caution, not ClusterScope, Tool schemas, permissions,
source allowlists, or budgets. Tool output remains untrusted data and cannot add
a Tool, request Secret data, change scope, or create execution authority.

## Capability matrix

<!-- markdownlint-disable MD013 -->

| Category | Minimum Evidence | Preferred Tool order | Allowed conclusion strength | Required caution |
| --- | --- | --- | --- | --- |
| CrashLoopBackOff | Projected waiting reason, restart and last-termination state; recent restart Events; a bounded previous or current log excerpt; owner context when needed | `get_resource` -> `get_events` -> `get_previous_pod_logs` -> `get_related_resources` for owners | Confirm the observed restart state, relevant Events, and bounded owner relationship; treat application startup, configuration, or dependency causes as hypotheses | One log line never proves the root cause. Forbidden or absent previous logs leave the exit explanation uncertain. |
| OOMKilled | Projected previous termination reason and exit code; restart count; bounded previous logs; owner context | `get_resource` -> `get_previous_pod_logs` -> `get_related_resources` for owners | Confirm OOMKilled only when Kubernetes reports that termination reason; memory limits, peaks, or leaks remain hypotheses without additional observations | A memory phrase in logs does not confirm OOMKilled. Conflicting state and log observations require a conflict gap and lower confidence. |
| ImagePullBackOff | Projected image waiting reason; recent FailedPull or BackOff Events | `get_resource` -> `get_events` | Confirm the waiting state and observed pull Events; image reference, registry reachability, and authorization remain bounded hypotheses | Event denial leaves the exact pull failure unknown. KuPilot never reads Secret data and must not assert that a registry credential is wrong. |
| Pod Pending | Projected phase and PodScheduled condition; recent scheduling Events; owner context | `get_resource` -> `get_events` -> `get_related_resources` for owners | Confirm the observed phase, scheduling condition, Event, and owner relationship; propose scheduling constraints only when the Evidence supports them | Missing Events do not prove capacity shortage. An older FailedScheduling Event that conflicts with a newer condition is stale context, not a current root cause. |
| Readiness probe failure | Projected Ready condition and container readiness; recent Unhealthy Events; bounded current logs | `get_resource` -> `get_events` -> `get_pod_logs` | Confirm readiness state and a probe failure only when a relevant Event was observed; startup timing, probe configuration, and application health remain hypotheses | Service unavailability alone does not prove a probe failure. Truncated logs cannot support a conclusion about omitted content. |
| Deployment unavailable | Projected desired, ready, available, updated, and unavailable replica counts with relevant conditions; a bounded Deployment-to-ReplicaSet-to-Pod owner graph; recent Deployment Events | `get_resource` -> `get_related_resources` for Pods and ReplicaSets -> `get_events` | Confirm zero availability, controller conditions, returned owner relationships, related workload status, and relevant Events; treat a stalled rollout or Pod startup contribution as a hypothesis | A Deployment condition is controller state, not the underlying root cause. A partial or forbidden relationship branch cannot support a Pod-specific cause. |
| Job failed | Projected active, succeeded, and failed counts with the Failed condition; a bounded Job-to-Pod owner relationship and Pod status; recent Job Events | `get_resource` -> `get_related_resources` for Pods -> `get_events` | Confirm the Job counts and condition, returned owner relationship, related Pod phase, and relevant Events; treat retry exhaustion or Pod execution failure as hypotheses | A failed count does not prove an application error or exit cause. Conflicting Job and Pod observations require an explicit conflict gap and lower confidence. |
| Service without ready Endpoint | Projected Service type and selector key count; bounded selector-match relationships and matching Pod readiness; address-free EndpointSlice ready and not-ready counts | `get_resource` -> `get_related_resources` for Pods and service endpoints | Confirm selector presence, returned matching Pods, their readiness summary, and zero ready endpoints only when those counts were observed; treat Pod readiness as a possible explanation | Service existence does not prove a backend. Missing, partial, or forbidden relationship data does not prove a zero count. EndpointSlice addresses and topology are never exposed. |

<!-- markdownlint-enable MD013 -->

## Deterministic evaluation

Scenario evaluation uses scripted model turns and synthetic Kubernetes
observation fixtures. The scripted conversation passes through the production
single-Agent adapter, fixed Tool binding, result envelope, Evidence registry,
and final Diagnosis validator. It never contacts a real model or cluster.

The evaluator checks:

- Exact bounded Tool order and the unchanged six-Tool catalog.
- All four Diagnosis collections, each accepted Evidence item's exact
  `observed_at`, and the complete observation window.
- Same-run Evidence references for every confirmed fact.
- Human-reviewed semantic assertion labels supported by the cited synthetic
  Evidence, without comparing complete natural-language sentences.
- Required permission, absence, stale, conflict, partial, and truncation gaps,
  including the partial Evidence-detail state.
- Hypothesis confidence and falsifiers.
- Structured `executed=false` state and the rendered not-executed marker for
  every recommendation.

Each category has a sufficient-Evidence conversation and a limited or forbidden
conversation. Across the eight categories, the fixtures cover denied, partial,
conflicting, stale, and truncated observations. Negative results may still
confirm a narrow observed symptom, but they cannot promote an unsupported cause
into a confirmed fact.

## Privacy and safety boundaries

All scenario resources and observations are synthetic and use clearly synthetic
`example-*` names. Fixtures contain no EndpointSlice address or topology, domain,
IP address, credential, key, Secret object, or production log. Selector values,
owner references, Events, conditions, logs, and related resource status remain
untrusted data. Instruction-like fixture text is bounded, and the evaluation
verifies that it does not change Tool selection or authorization.

No scenario adds a Tool, Kubernetes kind, relationship, permission, write path,
or remediation behavior. Recommended actions are guidance for the user to
evaluate and perform independently.
