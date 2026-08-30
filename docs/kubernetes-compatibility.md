# Kubernetes Compatibility

Kupilot pins `k8s.io/client-go v0.35.7` together with matching `k8s.io/api` and
`k8s.io/apimachinery` modules. The upstream module requires Go 1.25.0. Kupilot
does not support mixed Kubernetes module minors.

The upstream [client-go compatibility contract](https://github.com/kubernetes/client-go/blob/v0.35.7/README.md)
and [module declaration](https://github.com/kubernetes/client-go/blob/v0.35.7/go.mod)
are dependency inputs; Kupilot's product surface remains narrower than
client-go.

## Cluster version matrix

| Kubernetes API server | Kupilot contract |
| --- | --- |
| 1.34.x | Supported for the documented stable APIs shared with 1.35. |
| 1.35.x | Supported; exact client-go minor match. |
| 1.36.x | Supported for the documented stable APIs shared with 1.35. |

Older and newer minors are outside the supported matrix. Discovery never
expands the surface, and this matrix is not a claim that every client-go API is
supported.

## Kubeconfig loading

Kupilot uses client-go standard loading precedence:

- A non-empty `KUBECONFIG` supplies a platform-separated file list, with
  duplicate paths removed and normal client-go merge precedence.
- Otherwise the standard per-user kubeconfig is selected.
- Missing entries in a multi-file list are tolerated only when another usable
  source exists.
- Relative credential and certificate paths are resolved against their source
  file.

Legacy migration is disabled; Kupilot never copies or modifies kubeconfig.
Only sorted Context names, the effective default Namespace, current selection,
and the presence of exec credentials leave the adapter. Raw kubeconfig,
locations, server addresses, users, clusters, tokens, certificates, private
keys, exec output, `rest.Config`, transports, and client-go values remain
inside `internal/kube`.

## Client bundle contract

Each selected Context owns a fresh bundle with:

- User-Agent `kupilot/0.4`;
- QPS 5 and Burst 10;
- a Kubernetes request deadline selected from the immutable run profile, at
  most 60 seconds and no later than the owning run deadline;
- normal certificate and hostname verification with TLS 1.2 or newer; and
- no HTTP redirect.

The adapter rejects insecure TLS, plain HTTP API servers, URL user information,
queries, fragments, custom transports, and legacy auth providers. It exposes
no dynamic client, discovery client, REST client, arbitrary GVR, raw request
builder, Watch, informer, generic resource operation, or generic write port.

Closing a bundle rejects new requests, cancels and joins in-flight requests and
exec credential children, clears bundle-local credential state, and closes idle
connections.

## Scope activation

Application owns Context, working Namespace, namespace-access policy, and
generation. Activating a Context verifies its working Namespace with one exact
core `v1` Namespace GET. A Namespace selection is likewise verified before
commit. A committed scope change invalidates generation first, cancels the old
run, clears resource and approval state, and disposes or rebinds the client.

The Namespace picker uses one bounded core `v1` Namespace LIST for input help.
Picker results are not Evidence and do not prove that a later resource exists.
An empty Namespace is never interpreted as all Namespaces.

For run reads:

- `current` permits namespaced calls only in the working Namespace.
- `all` permits a validated explicit Namespace and an explicit all-Namespace
  list in the same Context.
- Kubernetes RBAC remains mandatory for every request.
- Cluster-scoped references contain no Namespace.

## Typed direct reads

<!-- markdownlint-disable MD013 -->

| API | Kind | Exact typed operations | Projection notes |
| --- | --- | --- | --- |
| core `v1` | Namespace | GET, bounded LIST | Identity, creation time, phase, and one bounded reason; no Namespace contents. |
| core `v1` | Node | GET, bounded LIST | Identity, creation time, Ready/NotReady state, and one bounded health reason; no addresses, provider ID, images, system info, taint values, or capacity maps. |
| core `v1` | Pod | GET, bounded LIST | Phase, readiness, restart/termination and bounded diagnostic status; no environment or volume-source data. |
| core `v1` | Service | GET, bounded LIST | Type, safe ports/counts, selector-key count; no cluster/external addresses. |
| core `v1` | PersistentVolumeClaim | GET, bounded LIST | Identity, creation time, and phase only; no access modes, storage class, volume source, or credential material. |
| core `v1` | PersistentVolume | GET, bounded LIST | Identity, creation time, phase, and bounded reason only; no capacity, access modes, claim details, or volume source. |
| core `v1` | ConfigMap | GET, bounded LIST | Identity and creation time only; `data` and `binaryData` are never projected. |
| `apps/v1` | Deployment | GET, bounded LIST | Replica/condition summary and fixed relationships. |
| `apps/v1` | ReplicaSet | GET, bounded LIST | Replica/condition and owner summary. |
| `apps/v1` | StatefulSet | GET, bounded LIST | Desired, current, ready, and available replica counts; no conditions or embedded Pod-template fields. |
| `apps/v1` | DaemonSet | GET, bounded LIST | Desired, ready, and available counts; no conditions or embedded Pod-template fields. |
| `batch/v1` | Job | GET, bounded LIST | Active/succeeded/failed counts, conditions, bounded policy counts. |
| `batch/v1` | CronJob | GET, bounded LIST | Active-job count and Active, Idle, or Suspended phase; no schedule, last-run time, or embedded Job template. |
| `networking.k8s.io/v1` | Ingress | GET, bounded LIST | Identity and creation time only; no class, rules, backends, addresses, annotations, or Secret material. |
| `autoscaling/v2` | HorizontalPodAutoscaler | GET, bounded LIST | Desired/current replica counts and one bounded failing-condition reason; no target identity or raw metrics. |
| `policy/v1` | PodDisruptionBudget | GET, bounded LIST | Desired/current health, disruptions-allowed count, and one bounded failing-condition reason; no raw selector. |

<!-- markdownlint-enable MD013 -->

Every direct list limit is 1 through 50, is sent to the API server, and is
enforced again after return. `get_cluster_overview` accepts a combined total of
2 through 50 and splits it between its fixed Namespace and Node lists. Lists
accept no model-controlled label selector, field
selector, continuation token, or Watch flag. All-Namespace behavior is a
separate explicit boolean in the internal port, never inferred from an empty
Namespace.

Secret, arbitrary custom resources, admission objects, RBAC objects, generic
discovery, and unknown API versions are denied before a Kubernetes call when
locally decidable.

## Events, logs, and relationships

- Events use an exact code-built `involvedObject` selector and return at most 50
  normalized entries. A cluster-scoped target may require a cluster-wide Event
  list, still bound to the exact target.
- Current and previous Pod logs use exact namespaced `pods/log` GETs. There is
  no follow mode. Each call is capped at 200 lines, 15 minutes, and 64 KiB and
  additionally requires the enabled container-output consent category.
- Relationship traversal is code-defined and remains within the root
  Namespace, at most two hops, 25 nodes, and 40 edges.
- EndpointSlice contributes only address-free ready/not-ready counts through
  the fixed Service relationship.

## Supervised Deployment restart

The sole mutation adapter supports one exact `apps/v1` Deployment. Proposal
preparation and post-approval revalidation use exact GETs. Execution uses one
merge PATCH with a fresh resource-version precondition and changes only the
Kupilot-owned Pod-template restart annotation.

The model cannot supply the patch, annotation, timestamp, UID, resource
version, template fingerprint, generation, or concurrency precondition. No
automatic write retry is allowed. Rollout verification performs bounded exact
Deployment GETs and never changes the prior write outcome.

## Exec credential authentication

Exec credential authentication is the only admitted external process. The
program and arguments come only from the selected kubeconfig, execute directly
without a shell, receive a filtered environment without the model key, have
bounded output, and terminate with the owning request or bundle. `deny` rejects
an exec-bearing Context before launch. Kupilot does not validate or control the
external program's own network behavior.

## Verification boundary

Deterministic tests assert exact verbs, groups, versions, resource paths,
Namespaces, subresources, selectors, limits, projections, cancellation, and
zero-action denials. Request-recording HTTP fixtures supplement client-go
object fakes because the fake alone is not a security oracle.

## References

- [Scope](scope.md)
- [Security Threat Model](security.md)
- [Least-Privilege RBAC](rbac/README.md)
- [ADR-0007](adr/0007-use-client-go-behind-narrow-kubernetes-ports.md)
- [ADR-0037](adr/0037-adopt-an-operational-capability-catalog.md)
