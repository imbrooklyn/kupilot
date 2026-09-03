# Least-Privilege Kubernetes RBAC

This page defines the Accepted `v0.5` RBAC target and identifies the narrower
fixtures currently present. The checked-in YAML still grants only the `v0.4`
typed reads and one separately gated exact Deployment restart. It deliberately
does not pre-grant `v0.5` metrics, CRD, Pod Exec, diagnostic Pod, scale,
rollback, Pod delete, Node scheduling, or drain permissions.

Do not grant `cluster-admin`, wildcard verbs or resources, Secret-value access,
generic write permissions, or broad discovery for convenience.

RBAC belongs to the kubeconfig identity. Kupilot does not create a
ServiceAccount, RoleBinding, or ClusterRoleBinding because subjects and
administration workflows are cluster-specific. The files here define reusable
rules only; an administrator must review placeholders and create the binding.

## `v0.5` capability-split target

Each optional capability must have a separate reviewed fixture or documented
rule set and must be bound only where enabled:

| Capability | Exact Kubernetes RBAC shape |
| --- | --- |
| Built-in reads | Only the named API groups/resources and required `get`/`list`; no `watch`. |
| Exact policy-admitted CRD | Named API group and plural resource with only the admitted `get`/`list`; local `describe` and query use those bounded projections, not extra generic verbs. No wildcard API group/resource. |
| Events and logs | `events list` and `pods/log get` only where the corresponding data category is enabled. |
| Pod/Node metrics | Exact metrics API resources and read verbs required by the typed projection. |
| Sensitive Kubernetes reads | A dedicated exact rule only when Secret metadata, one ConfigMap key, or one non-credential environment value is enabled. Kubernetes RBAC cannot distinguish metadata or one key from values, so source projection, `review`, consent, and sink policy remain mandatory and Secret values remain denied. |
| Container file or Pod Exec | `pods/exec create` only in exact admitted Namespaces; Application still binds Pod/container/path or argv. |
| Diagnostic Pod | Namespaced Pod `create`, `get`, and `delete` only for the dedicated policy; image, security context, target, lifetime, and cleanup remain local controls. |
| Scale | Exact controller read plus the selected `scale` subresource operation; resource names should be constrained where Kubernetes supports it. |
| Restart/rollback | Exact Deployment and required ReplicaSet reads plus only the operation-specific Deployment mutation verb. |
| Delete Pod | Exact namespaced Pod read/delete permission only for installations that enable the controller-owned-Pod operation. |
| Cordon/uncordon | Exact Node read/patch permission for admitted Node names where practical. |
| Drain | Exact Node read/patch, Pod reads, PDB reads, and eviction subresource permission required by the fixed plan; no wildcard or force shortcut. |

RBAC cannot express field projection, CRD-field allowlists, exact exec argv,
path and symlink rules, diagnostic image/destination policy, semantic mutation
diffs, permission profiles, ActionEnvelope digest, durable audit, one-attempt
behavior, or verification. Those remain independent mandatory Kupilot checks.

Prometheus and Loki are non-Kubernetes data sources and use separate endpoint
and credential policy. Restricted local `kubectl`, `helm`, or `argocd` argv
uses the local user's kubeconfig identity and cannot be made safe by broadening
RBAC. Shell remains a separate default-off critical capability.

## Choose a namespace policy first

The runtime setting and RBAC should agree:

- `kubernetes.namespace_access: current`: bind namespaced reads with
  [role.yaml](role.yaml) in the working Namespace, or bind
  [cluster-role.yaml](cluster-role.yaml) through a RoleBinding in each exact
  admitted Namespace.
- `kubernetes.namespace_access: all`: bind
  [cluster-role.yaml](cluster-role.yaml) through an identity-specific
  ClusterRoleBinding. This intentionally grants the catalog's namespaced reads
  in all Namespaces; Kubernetes has no RoleBinding form for an arbitrary
  runtime-selected Namespace set.

The default application policy is `all`, but RBAC denial still wins. Set the
application policy to `current` when cluster-wide namespaced visibility is not
intended. Do not rely on RBAC alone to explain which policy is active; `/status`
shows the immutable run policy.

## Current `v0.4` namespaced read surface

<!-- markdownlint-disable MD013 -->

| API group | Resources | Verbs | Purpose |
| --- | --- | --- | --- |
| Core | `pods`, `services`, `persistentvolumeclaims`, `configmaps` | `get`, `list` | Direct typed operational projections. ConfigMap values are removed locally. |
| Core | `events` | `list` | Exact code-built involved-object queries. |
| Core | `pods/log` | `get` | Bounded current or previous Pod output after privacy consent. |
| `apps` | `deployments`, `replicasets`, `statefulsets`, `daemonsets` | `get`, `list` | Workload status and fixed relationships. |
| `batch` | `jobs`, `cronjobs` | `get`, `list` | Batch status and fixed relationships. |
| `networking.k8s.io` | `ingresses` | `get`, `list` | Identity and creation-time projection only. |
| `autoscaling` | `horizontalpodautoscalers` | `get`, `list` | Bounded current/desired replica and failing-reason projection. |
| `policy` | `poddisruptionbudgets` | `get`, `list` | Bounded disruption and health-count projection. |
| `discovery.k8s.io` | `endpointslices` | `list` | Address-free ready/not-ready counts for the fixed Service relationship. |

<!-- markdownlint-enable MD013 -->

Kubernetes RBAC cannot restrict a ConfigMap GET to metadata fields or an
EndpointSlice LIST to address-free fields. Kupilot's source allowlist and
projection are independent mandatory controls.

Neither current namespaced fixture grants create, update, patch, delete,
deletecollection, watch, exec, attach, port-forward, ephemeral containers,
TokenRequest, SubjectAccessReview, or Secret access.

## Current `v0.4` cluster-scoped read surface

[cluster-observer-cluster-role.yaml](cluster-observer-cluster-role.yaml) grants
`get` and `list` for Namespace, Node, and PersistentVolume. Bind it only when
those catalog capabilities are intended. It is required for the complete
cluster overview and cluster-scoped direct reads.

The code projection excludes Node addresses, provider IDs, images, system info,
taint values, capacity maps, and PersistentVolume source details. RBAC cannot
express those field exclusions.

If cluster observation is not needed, choose one Namespace helper instead:

- [namespace-verifier-cluster-role.yaml](namespace-verifier-cluster-role.yaml)
  grants exact-name Namespace `get` for configured scope verification.
- [namespace-picker-cluster-role.yaml](namespace-picker-cluster-role.yaml)
  grants Namespace `get` and `list` for `/namespace` completion.

Namespace `list` exposes names available to that identity. The picker remains
an input aid and does not create Evidence.

Cluster-scoped resource Events are implemented as an all-Namespace Event list
with an exact involved-object selector. That observation therefore also needs
cluster-wide `events` list permission, normally supplied when the namespaced
ClusterRole is bound with a ClusterRoleBinding. Without it, Kupilot reports an
explicit permission gap.

## Current `v0.4` exact Deployment restart

[restart-role.yaml](restart-role.yaml) is the only write-bearing fixture. It
grants `get` and `patch` on one placeholder `apps/v1` Deployment in one
placeholder Namespace. Replace both placeholders, use a RoleBinding in that
Namespace, and create another reviewed resource-name rule for each additional
Deployment.

`get` supports proposal preparation, revalidation, and rollout observation.
`patch` supports the one fixed restart request. The Role grants no Deployment
list, watch, create, update, or delete and no Pod write.

RBAC cannot constrain a permitted PATCH to one field. Kupilot independently
enforces the fixed annotation-only patch, local 60-second digest-bound approval,
fresh UID/template/generation checks, resource-version precondition, durable
pre-write audit, and one non-retried attempt. Never replace `resourceNames`
with a wildcard or bind this Role cluster-wide.

## Partial permission behavior

Kupilot never broadens a request after denial:

- Missing Namespace list disables completion, while exact verification can
  still work with Namespace get.
- Missing cluster-observer permissions makes Node, Namespace, PersistentVolume,
  and cluster overview observations unavailable.
- Missing Event, log, EndpointSlice, or a direct Kind permission leaves that
  Evidence branch explicit and incomplete.
- Missing restart get or patch permission prevents preparation, execution, or
  verification at its exact stage; no broader credential or request is tried.
- A future `v0.5` capability with no matching exact optional permission remains
  unavailable. Kupilot does not retry with another identity, permission
  profile, local command, or broader API.

Raw API denial text is translated to a stable safe gap and is not copied into
model content, ordinary logs, SQLite, audit, or the TUI.

## Defense in depth

Even when the selected identity has broader RBAC, Kupilot still enforces the
code-owned Kind and API allowlist, immutable Context and namespace policy,
strict model schemas, exact typed requests, generation gates, result
projection, sensitive-data handling, finite budgets, and local write approval.

Adding a verb, resource, subresource, cluster scope, Pod Exec, diagnostic Pod,
or write is a product, security, privacy, configuration, fixture, and test
change. Permission-profile selection never changes RBAC. Review
[Security](../security.md), [Privacy](../privacy-overview.md), and
[Operational Capabilities](../diagnostic-capabilities.md) before changing these
rules.
