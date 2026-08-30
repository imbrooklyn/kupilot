# Operational and Diagnostic Capabilities

Kupilot answers Kubernetes operational questions through a versioned catalog
of typed, bounded capabilities. Support means the Agent can gather a safe
Evidence path and explain what it observed. It does not guarantee that every
incident has an observable cause or that model interpretation is correct.

The [Product Contract](product.md) and [Scope](scope.md) are authoritative.

## Common contract

The visible result is free-form Markdown. It can be a one-line answer, table,
comparison, checklist, or longer investigation. The local final-response
protocol separately carries Evidence citations and typed proposed actions.

Only deterministic Tool handlers create Evidence. User text, Kubernetes text,
model prose, and a proposed action cannot create an observation, change scope,
broaden a budget, grant approval, or claim execution. Permission denial,
absence, stale data, conflict, sensitive-output blocking, partial data, and
truncation remain visible gaps.

## Read catalog

| Capability | Operational use | Fixed boundary |
| --- | --- | --- |
| `get_resource` | Inspect one exact resource. | One allowlisted Kind and exact name; bounded summary or diagnostic projection. |
| `list_resources` | Find or compare resources by safe name and health projection. | One Kind; bounded name query and health filter; no raw selector. |
| `get_cluster_overview` | Summarize Namespace and Node health. | Two fixed typed lists share one combined 2-through-50 limit; no discovery, addresses, provider IDs, images, system data, or capacity maps. |
| `get_events` | Correlate recent Kubernetes Events with one target. | Exact involved-object selector, bounded time and count, normalized projection. |
| `get_pod_logs` | Inspect a current Pod container tail. | One exact Pod/container, no follow, consent required, strict line/window/byte limits. |
| `get_previous_pod_logs` | Inspect a prior container instance. | Same constraints as current logs, with `previous=true`. |
| `get_related_resources` | Follow controller, workload, Service, and readiness relationships. | Code-defined same-Namespace edges, at most two hops, 25 nodes, and 40 edges. |

Direct resources are Namespace, Node, Pod, Service,
PersistentVolumeClaim, PersistentVolume, ConfigMap metadata, Deployment,
ReplicaSet, StatefulSet, DaemonSet, Job, CronJob, Ingress,
HorizontalPodAutoscaler, and PodDisruptionBudget.

Secret is never a source. ConfigMap `data` and `binaryData`, Pod environment
values, credential references, raw objects, arbitrary annotations, custom
resources, and discovery-expanded APIs remain unavailable.

## Namespace behavior

Every run has one verified Context, one visible working Namespace, and one
immutable namespace policy:

- `current` pins every namespaced read to the working Namespace.
- `all` also permits a validated explicit Namespace or explicit `*`
  all-Namespace list in the same Context.

`all` is an Application policy, not an RBAC bypass. Cluster-scoped Namespace,
Node, and PersistentVolume references contain no fake Namespace. Cross-Context
and cross-cluster calls are always denied.

## Regression scenarios

Kupilot retains deterministic sufficient- and limited-Evidence fixtures for
these incident families:

| Scenario | Typical Evidence path | Caution |
| --- | --- | --- |
| CrashLoopBackOff | Pod state, Events, previous/current logs, owner. | A log line alone does not prove the root cause. |
| OOMKilled | Previous termination reason, exit code, restart count, previous logs. | Memory wording in logs does not confirm an OOM kill. |
| ImagePullBackOff | Waiting reason and pull Events. | Secret data is never read; registry credential failure must remain a hypothesis unless projected Events support it. |
| Pod Pending | Phase, scheduling condition, Events, owner, relevant Node status. | Missing Events do not prove capacity shortage. |
| Readiness failure | Ready/container state, Unhealthy Events, bounded logs. | Service unavailability alone does not prove probe failure. |
| Deployment unavailable | Replica counts/conditions, ReplicaSet and Pod graph, Events. | Controller state does not by itself identify the underlying Pod cause. |
| Job failed | Job counts/condition, related Pods, Events, bounded logs when enabled. | A failed count does not prove an application error. |
| Service without ready endpoints | Service projection, matching Pods, address-free EndpointSlice readiness counts. | Partial relationship data cannot prove a zero-backend conclusion. |

These fixtures are regression baselines, not a product whitelist. The broader
typed resource catalog supports ordinary inventory summaries, cross-Namespace
comparison, workload rollout analysis, Node pressure checks, CronJob/Job
inspection, storage phase checks, Ingress identity lookup, autoscaling replica
status, and disruption-budget analysis within the same safety boundaries.

## Supervised action

The only composed mutation is `restart_deployment` for one exact `apps/v1`
Deployment in the working Namespace. A typed suggestion triggers one fresh
local read that derives UID, template fingerprint, and Deployment generation.
It still performs no write.

Execution requires a default-reject 60-second local approval, digest and nonce
validation, fresh target revalidation, durable consumed-approval and pre-write
audit, a final scope check, and at most one fixed merge PATCH. Request
acceptance, rollout progress, timeout, failure, unknown outcome, and verified
completion remain distinct states. Kupilot never retries a write automatically.

## Fixed per-capability ceilings

- One ToolResult: 64 KiB.
- Resource summaries or Events: at most 50 per result.
- Evidence: at most 100 items per result.
- Pod logs: at most 200 lines, 15 minutes, and 64 KiB per call.
- Relationships: two hops, 25 nodes, and 40 edges.

Run-wide limits come from the selected compact, balanced, or extended profile;
see [Agent Runtime](agent-runtime.md). A run can end earlier due to cancellation,
stale scope, consent, RBAC, no progress, or a smaller remaining deadline.

## Deterministic evaluation

Scripted model turns and synthetic Kubernetes observations pass through the
production Agent adapter, Tool binding, projection, Evidence registry, and
final-answer validator. They never require a real model or cluster.

The evaluator checks exact Tool names and order, accepted Evidence provenance,
free-form answer assertions, citation validity, explicit gaps, typed action
state, bounded output, and zero-call denials. Fixtures contain no real address,
credential, Secret, kubeconfig, or production output.

## Remaining limitations

- No Watch, informer, background scan, continuous monitoring, or scheduled run.
- No arbitrary selector, custom-resource discovery, raw YAML, shell, kubectl,
  Pod Exec, port forwarding, Helm, or generic patch/apply/delete.
- Container output is opt-in model content and remains sensitive even after
  bounded processing.
- Evidence is a timestamped snapshot and may become stale after observation.
- Model protocol compatibility and model answer quality are separate concerns.

See [Kubernetes Compatibility](kubernetes-compatibility.md),
[Model Compatibility](model-compatibility.md), and
[Least-Privilege RBAC](rbac/README.md).
