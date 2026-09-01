# Kupilot Scope

This document defines the executable `v0.4` product boundary. It replaces the
MVP feature freeze while preserving local authority, projection, approval, and
data-safety controls.

## In scope for `v0.4`

- A local, single-process, single-user TUI with one active AgentRun. Ordinary
  conversation uses one full-height alternate-screen runtime with the composer
  anchored at the bottom. After graceful terminal restoration, one bounded
  completed transcript enters terminal-owned scrollback; runtime drafts and
  chrome do not.
- One configured `openai_compatible` model origin and the accepted streaming
  structured-capability protocol.
- One verified Kubernetes Context, one visible working Namespace, and one
  immutable `current` or `all` namespace-access policy per run.
- Startup Context resolution uses the effective configured Context, the last
  successfully verified local Context, then kubeconfig `current-context`.
  Without an explicit Namespace, the startup working Namespace is `default`.
- Natural-language questions, safe validation progress, compact inline
  capability steps, cancellation, and a validated free-form Markdown answer.
- A versioned code-owned read catalog over the built-in resource allowlist in
  the Product Contract.
- Cross-Namespace exact reads and bounded all-Namespace lists in the same
  Context when the frozen namespace-access policy is `all`.
- Cluster-scoped Namespace, Node, and PersistentVolume reads with exact
  scope-aware Evidence.
- One supervised `restart_deployment` action behind digest-bound approval,
  target revalidation, durable audit, one execution attempt, and bounded
  rollout verification.
- Compact, balanced, and extended run-budget profiles with balanced as the
  default and `/status` visibility.
- Explicit Session resume, local SQLite persistence, informed model-transfer
  consent, privacy modes, retention controls, redacted export, and deletion.
- macOS and Linux support on `amd64` and `arm64`; Windows remains experimental.

## Read capability catalog

The complete initial `v0.4` read catalog is:

### `get_resource`

Reads one exact allowlisted resource and returns a bounded summary or diagnostic
projection. Namespaced resources default to the working Namespace and may use
an explicit Namespace under `all` policy. Cluster-scoped resources reject a
Namespace argument.

### `list_resources`

Lists one allowlisted Kind with an optional bounded name query and health
filter. It accepts the working Namespace, an explicit Namespace, or the explicit
all-Namespace marker when policy and resource scope allow it. It never accepts a
raw selector or pagination token.

### `get_cluster_overview`

Returns a bounded, typed Namespace and Node health summary. It is not API
discovery and does not return Node addresses, provider IDs, allocatable values,
taint values, annotations, or Namespace contents. Its limit is one combined
total from 2 through 50, shared across the two fixed lists so neither Kind can
consume the entire request.

### `get_events`

Returns recent bounded normalized Events for one exact allowlisted resource.
The target Namespace follows the same explicit policy as `get_resource`.

### `get_pod_logs`

Returns one non-following current container-output tail for one exact Pod and
container. Container output remains disabled until the user enables its consent
category. The Namespace is explicit and policy-bound.

### `get_previous_pod_logs`

Uses the same bounds and policy as `get_pod_logs`, but requests the previous
container instance.

### `get_related_resources`

Follows only code-defined relationships from one exact root inside the root's
Namespace. Relationship depth, nodes, and edges are runtime ceilings. It never
performs recursive discovery or crosses Namespace boundaries implicitly.

Every capability has strict input decoding, canonical arguments, runtime-
injected Context and ceilings, pre- and post-I/O generation checks, project-
owned projection, sensitive-value handling, and deterministic Evidence.

## Resource source allowlist

<!-- markdownlint-disable MD013 -->

| API | Direct Kinds | Data boundary |
| --- | --- | --- |
| core `v1` | Namespace, Node, Pod, Service, PersistentVolumeClaim, PersistentVolume, ConfigMap | ConfigMap metadata only; no `data` or `binaryData`. Node addresses, images, provider IDs, system info, and volume source details are excluded. |
| `apps/v1` | Deployment, ReplicaSet, StatefulSet, DaemonSet | Bounded metadata, replica status, conditions, and fixed relationships. No Pod template environment, volume source, or arbitrary annotations. |
| `batch/v1` | Job, CronJob | Job status and conditions; CronJob activity and suspend state. No schedule or embedded Pod/Job template data. |
| `networking.k8s.io/v1` | Ingress | Identity and creation time only; no class, rules, backends, addresses, arbitrary annotations, or Secret references. |
| `autoscaling/v2` | HorizontalPodAutoscaler | Desired/current replica counts and one bounded failing-condition reason; no target identity or raw metrics. |
| `policy/v1` | PodDisruptionBudget | Bounded desired/current health and disruption counts; no raw selector. |

<!-- markdownlint-enable MD013 -->

EndpointSlice remains an indirect Service-read source and contributes readiness
counts only. Secret is denied as both a direct and related source. Unknown and
custom resources are denied before a Kubernetes call whenever locally
decidable.

## Namespace policy

The working Namespace remains mandatory because it anchors default intent,
resource selection, action targets, and the persistent footer.

- `current` permits only that Namespace for namespaced resources and denies
  all-Namespace lists locally.
- `all` permits a validated explicit Namespace and the explicit all-Namespace
  list marker. It does not bypass API-server RBAC.
- Cluster-scoped resources use no Namespace and never inherit a fake one.
- No policy permits another Context or cluster during the same run.

A committed Context, working-Namespace, or namespace-policy change increments
the generation, cancels the run, clears resource and approval state, and rejects
late results at all three scope gates.

## Action catalog

`restart_deployment` is the only currently composed write operation. It is
proposed through typed Agent output and requires the approval contract in
ADR-0012. The approved diff changes only `kupilot.io/restartedAt` on one exact
Deployment Pod template.

No current action performs scale, delete, apply, generic patch, rollback, exec,
port forwarding, Helm, batch mutation, or automatic remediation. These are not
permanent product prohibitions, but each requires a separate typed design and
Accepted decision before it can enter the catalog.

## Execution budgets

The compact, balanced, and extended profiles and hard ceilings are normative in
ADR-0039. Configuration selects a profile before a run. Model text, Tool output,
and a partial result cannot expand it. `/status` shows usage and remaining run
time; reaching a limit produces a safe partial answer or terminal explanation.

## Explicit non-goals

- A primary resource table, tree, dashboard, full YAML view/editor, or raw log
  browser.
- Shell, kubectl, Pod Exec, command generation/execution, or arbitrary HTTP.
- Watch, informer, continuous live monitoring, background scan, scheduled run,
  or controller behavior.
- Generic Kubernetes discovery exposed to the model, custom resources, dynamic
  plugins, MCP, RAG, retrievers, or Multi-Agent orchestration.
- Cross-cluster diagnosis or concurrent active AgentRuns.
- Autonomous approval or remediation.

## Capability admission gate

A new source or action must identify the operational need and document:

1. the exact typed API, verb, scope, and RBAC impact;
2. strict model schema and runtime-injected authority;
3. allowed and prohibited source fields;
4. projection, normalization, sensitive-data, consent, and retention behavior;
5. time, item, byte, call, retry, and traversal budgets;
6. partial, cancellation, timeout, conflict, and stale-scope behavior;
7. deterministic Evidence or action-state mapping;
8. request-recording success and zero-call denial tests; and
9. why the capability helps the conversational Agent rather than creating a
   parallel cluster interface.

An action must additionally define the semantic diff, digest fields, approval
copy, TTL, revalidation, concurrency precondition, audit transaction, ambiguous
outcome behavior, and verification states.

## References

- [Product Contract](product.md)
- [Architecture](architecture.md)
- [Security Threat Model](security.md)
- [Privacy Overview](privacy-overview.md)
- [ADR-0037: Adopt an Operational Capability Catalog](adr/0037-adopt-an-operational-capability-catalog.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](adr/0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0042: Remember the Last Verified Kubernetes Context](adr/0042-remember-the-last-verified-kubernetes-context.md)
