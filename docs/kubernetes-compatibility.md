# Kubernetes Compatibility

- Status: Accepted `v0.5` target with current dependency evidence
- Date: 2026-09-05

The checked-in runtime implements the `v0.5` read and observability slices:
reviewed built-ins, safe Secret metadata, exact policy-admitted CRD reads,
typed queries, runtime-owned pagination, normalized Events, bounded Pod logs,
and typed Pod/Node Metrics API snapshots. Optional Prometheus and Loki clients
are separate non-Kubernetes adapters. It also implements the default-off exact
Pod Exec, container-file, and diagnostic-Pod handlers. Their human and Reviewer
routes use the shared inline supervision flow and release no remote attempt
until exact authority is durably consumed. Typed scale, rollback, controller-
owned Pod delete, cordon, uncordon, and drain are implemented behind the shared
action dispatcher. The exact local runner is a separate non-Kubernetes adapter
and never serves as a fallback for these typed operations. Deterministic tests
are not live-cluster, RBAC-installation, or local-tool evidence.

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

## Accepted `v0.5` Kubernetes surface

The accepted catalog adds reviewed stable built-ins and exact policy-admitted
CRDs, conversational `describe`, bounded query/count/table, Events, current/
previous/all-container logs and local search, Pod/Node metrics, container-file
read, Pod diagnostics, diagnostic Pods, and typed restart/scale/rollback/Pod
delete/cordon/uncordon/drain.

The target also admits an exact safe Secret-metadata projection and, only under
a separately consented `review` policy, one exact ConfigMap key or
non-credential container environment value. Secret values, generic or bulk
values, full objects, and credential-shaped data remain denied.

The implemented broad-read slice maps every model-selected resource type to an
exact code-owned API identity and projection. Built-ins use their exact typed
client-go mapping; only exact configured CRDs use the confined dynamic client.
An exact CRD
policy names group, version, resource, Kind, scope, verbs, projected fields,
limits, and Evidence mapping; discovery may validate availability but never
grants model authority. Any dynamic client required inside `internal/kube` is
confined behind the exact consumer-owned CRD port and never crosses as GVR,
object, REST, discovery, or generic operation authority.

Optional Prometheus and Loki sources are not Kubernetes API calls and use their
own explicitly configured boundaries. Restricted local `kubectl`, `helm`, or
`argocd` argv is not a Kubernetes adapter fallback and cannot replace typed P0
operations.

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

- the fixed User-Agent `kupilot/0.5`;
- QPS 5 and Burst 10;
- a Kubernetes request deadline selected from the immutable run profile, at
  most 60 seconds and no later than the owning run deadline;
- normal certificate and hostname verification with TLS 1.2 or newer; and
- no HTTP redirect.

The adapter rejects insecure TLS, plain HTTP API servers, URL user information,
queries, fragments, custom transports, and legacy auth providers. It exposes
no dynamic client, discovery client, REST client, arbitrary GVR, raw request
builder, Watch, informer, generic resource operation, or generic write port.

Closing a bundle rejects new requests, cancels and joins in-flight requests,
remote-command streams, and exec credential children, clears bundle-local
credential state, and closes idle connections.

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

## Implemented `v0.5` broad resource reads

<!-- markdownlint-disable MD013 -->

| API | Kind | Exact typed operations | Projection notes |
| --- | --- | --- | --- |
| core `v1` | Namespace | GET, bounded LIST | Identity, creation time, phase, and one bounded reason; no Namespace contents. |
| core `v1` | Node | GET, bounded LIST | Identity, creation time, Ready/NotReady state, and one bounded health reason; no addresses, provider ID, images, system info, taint values, or capacity maps. |
| core `v1` | Pod | GET, bounded LIST | Phase, readiness, restart/termination and bounded diagnostic status; no environment or volume-source data. |
| core `v1` | Service | GET, bounded LIST | Type, safe ports/counts, selector-key count; no cluster/external addresses. |
| core `v1` | PersistentVolumeClaim | GET, bounded LIST | Identity, creation time, and phase only; no access modes, storage class, volume source, or credential material. |
| core `v1` | PersistentVolume | GET, bounded LIST | Identity, creation time, phase, and bounded reason only; no capacity, access modes, claim details, or volume source. |
| core `v1` | ConfigMap | metadata GET, bounded metadata LIST | PartialObjectMetadata identity and creation time only; `data` and `binaryData` are never requested through this broad-read path. |
| core `v1` | Secret | metadata GET, bounded metadata LIST | PartialObjectMetadata identity and creation time only; `data`, `stringData`, and type-specific values never enter the project projection. |
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

Every list sends a server-side item limit and owns continuation tokens inside
`internal/kube`. The model may select only a local field ID, code-defined
operator, and bounded scalar value. Kupilot translates eligible exact metadata
name/Namespace and label predicates to server selectors and applies all
predicates again to the allowlisted projection. It accepts no raw selector,
continuation token, JSONPath, template, `jq`, URL, Watch flag, or hard ceiling.
All-Namespace behavior is explicit under the frozen `all` policy and is never
inferred from an empty Namespace.

Resource policies and the run profile are intersected before I/O. The compact,
balanced, and extended profiles respectively allow at most 2/4/8 pages,
25/50/100 items per server page, 128 KiB/256 KiB/1 MiB per response,
50/200/500 scanned items, 25/50/50 returned items, and 256 KiB/1 MiB/4 MiB
cumulative response bytes. A policy may only tighten those values. A complete
page may be retained when a later page exceeds a page or aggregate ceiling;
the result then reports explicit partial/truncated state and never exposes the
continuation token.

`get_cluster_overview` retains its separate fixed Namespace/Node behavior and
combined limit of 2 through 50.

For a configured CRD, one discovery GET validates the policy's exact group,
version, resource, Kind, scope, and requested verb. Discovery cannot add a
catalog entry or change fields, predicates, Evidence mapping, or limits.
Unknown resource types, verbs, subresources, Namespace expansion, sensitive
fields, and model-supplied continuation state are rejected before resource
I/O; an unavailable configured API stops after its exact discovery request.

Secret values, arbitrary custom resources without an exact policy entry,
admission objects, generic discovery authority, and unknown API versions
remain denied. Safe Secret metadata is implemented. Exact ConfigMap values,
non-credential environment values, and RBAC-status projections still require
their separate sensitive-read policy, permission, category consent, and sink
work and are not reachable in this slice.

## Events, logs, metrics, and relationships

- Events use an exact code-built `involvedObject` selector plus admitted
  reason/type/time filters. Runtime-owned continuation, deduplication, series
  counts, per-page and aggregate ceilings, and explicit partial state apply. A
  cluster-scoped target requires the frozen `all` namespace-access policy and a
  cluster-wide Event list, still bound to that exact target.
- Current and previous Pod logs use exact namespaced `pods/log` GETs. One or a
  bounded explicit set of regular, init, and ephemeral containers may be read;
  local search is literal and cannot become a selector, regex, command, or
  shell. There is no follow mode. Tail, time, container, line, and byte limits
  are intersected with the frozen run profile, and container-output consent is
  mandatory.
- Pod and Node metrics use exact core-resource identity GETs followed by exact
  `metrics.k8s.io/v1beta1` GETs. Kubernetes quantities are parsed inside the
  adapter and cross the boundary only as CPU millicores and memory bytes.
  Unavailable, unsupported, stale, and partial observations remain distinct.
- Optional Prometheus and Loki sources are not Kubernetes API fallbacks and do
  not broaden this surface. Their fixed queries and origins are documented in
  [Configuration](configuration.md).
- Relationship traversal is code-defined and remains within the root
  Namespace, at most two hops, 25 nodes, and 40 edges.
- EndpointSlice contributes only address-free ready/not-ready counts through
  the fixed Service relationship.

## Implemented supervised Kubernetes actions

The restart adapter supports one exact `apps/v1` Deployment. Proposal
preparation and post-approval revalidation use exact GETs. Execution uses one
merge PATCH with a fresh resource-version precondition and changes only the
Kupilot-owned Pod-template restart annotation. Separate typed adapters also
implement bounded scale, rollback, one controller-owned ordinary Pod delete,
cordon, uncordon, and drain. Each binds its exact semantic plan and receives no
generic patch/apply/delete authority.

The model cannot supply the patch, annotation, timestamp, UID, resource
version, template fingerprint, generation, or concurrency precondition. No
automatic write retry is allowed. Rollout verification performs bounded exact
Deployment GETs and never changes the prior write outcome.

## Implemented `v0.5` remote diagnostics

Remote command transport is confined to `internal/kube` and uses the pinned
client-go v0.35.7 `remotecommand.NewSPDYExecutorRejectRedirects` API. The exact
POST targets `api/v1/namespaces/<namespace>/pods/<pod>/exec`, sends a typed
`PodExecOptions`, and sets `stdin=false`, `tty=false`, `stdout=true`, and
`stderr=true`. The runtime includes the exact container and argv parameters and
one bounded timeout query. It owns cancellation, stream completion, ordered
project-owned stdout/stderr chunks, byte and line ceilings, and bundle close.
It deliberately performs no WebSocket fallback or second transport attempt:
after an upgrade failure, a retry could execute the command twice.

- Container-file read binds one exact Pod/container/path and denies unsafe
  credential and ServiceAccount mounts and paths, devices, and unsafe pseudo-
  filesystems before returning a bounded projection. One no-shell exact USTAR
  argv with a one-block record size reports each normalized path component and
  keeps deterministic framing within the transport-byte ceiling. The local
  parser requires ordered directory headers followed by one bounded regular-
  file header and rejects links, duplicates, extra entries, and stderr. This
  cooperative check has a documented filesystem race residual and is not an
  atomic `openat2` guarantee.
- Predefined read-only Pod diagnostics and separately gated other Pod Exec bind
  exact Pod UID/resource version/container identity and policy-owned executable
  and argv. Application revalidates those facts after the decision and before
  durable consumption; the adapter repeats the check before the sole remote-
  command attempt. Shell, stdin, and TTY remain false; output, time, data,
  sink, network effects, and the exact live Pod/container destination digest
  are explicit. Metacharacters remain literal argv bytes. A configured general
  executable can still implement in-container side effects or network access;
  the exact argv and conservative remote-Pod network effect are bound for
  review, but no in-container network sandbox is claimed.
- Diagnostic Pods use a policy-selected pinned image, non-root security
  context, no privilege, read-only root filesystem, no host mounts or host
  network, finite resources and lifetime, disabled ServiceAccount-token
  automount, disabled Service links, `preemptionPolicy=Never`, only a zero
  API-default priority with no PriorityClass, exact same-Namespace Service and
  port, and separately audited create, wait, log, delete, and ambiguous-cleanup
  states.
  Service UID, resource version, non-empty selector, and port are revalidated
  before create; the returned and subsequently observed Pod must preserve the
  exact restricted spec. ExternalName, selectorless, obvious metadata/link-
  local/address-confusion, and policy-external targets are rejected. A
  definite create rejection performs no delete. Ambiguous create or delete can
  clean only the exact generated, labelled, invocation-bound Pod and never
  retries create. Bundle close cancels the owner while retaining transport for
  bounded cleanup, then joins the owner before transport shutdown. The
  envelope also binds the exact Service DNS/port destination digest. Policy
  requires an operator assertion that a matching NetworkPolicy and compatible
  CNI are deployed; Kupilot does not inspect or prove that enforcement, and an
  image allowlist is not a network sandbox.

Each remote operation first becomes a versioned `ActionEnvelope` binding exact
scope and policy generations, target identity/fingerprint, executable and argv
or typed path, false stdin/TTY/shell flags, data/sink/network effects, limits,
expiry, and verification plan. Application first proves that this normalized
plan exactly matches its process-frozen catalog, then performs hard policy,
permission, fresh revalidation, one-time durable approval consumption and pre-
operation audit, final generation validation, at most one external attempt,
and bounded outcome audit. Raw command, archive, file, and Pod-log output is not
persisted; their Evidence facts contain only safe target metadata, counts, and
a content fingerprint. Scope, policy generation, and category consent are
checked again after safe projection and after the durable outcome audit before
the prepared result is returned to the Agent.

## Implemented typed `v0.5` remediation

- Scale uses the exact Deployment or StatefulSet `scale` subresource and sends
  the prepared resource version with one replica target. Rollback selects one
  bounded owned ReplicaSet revision, ignores and removes the controller-owned
  `pod-template-hash`, then updates only the exact Deployment template from the
  fresh source. A paused Deployment is denied.
- Pod delete accepts one ordinary controller-owned Pod and sends one 30-second
  delete with UID and resource-version preconditions. Force, grace-zero, bulk,
  unmanaged, static, mirror, deleting, or ambiguous-owner targets are denied.
- Cordon and uncordon send one merge patch containing only the prepared
  resource version and `spec.unschedulable`. Drain first binds one schedulable
  Node, its complete bounded all-Namespace Pod set, and every matching PDB. It
  denies DaemonSet, static, mirror, unmanaged, deleting, ambiguous-PDB,
  insufficient-disruption, partial, or empty plans. `emptyDir`, `hostPath`,
  generic ephemeral, inline CSI, and deprecated GitRepo volumes are treated as
  local data and denied. After cordoning once, it repeats the same bounded
  all-Namespace Pod query and requires the complete approved UID/resource-
  version/fingerprint set before issuing one preconditioned eviction per bound
  Pod in deterministic order.

Every operation uses a versioned `ActionEnvelope`, fresh UID/resource version
or operation-specific fingerprint, scope and policy generations, exact
SelfSubjectAccessReview attributes, durable pre-operation audit, at most one
attempt per bound side effect, fail-closed ambiguous outcome, and a separate
bounded verification read. A changed target or drain set invalidates approval;
partial drain acceptance is explicit and is never retried automatically.
Generic patch/apply/edit/delete/YAML is not an adapter API.

## Exec credential authentication

Exec credential authentication remains a distinct adapter-owned external
process. Its program and arguments come only from the selected kubeconfig,
execute directly without a shell, receive a filtered environment without model
or Kubernetes credentials unrelated to authentication, have bounded output,
and terminate with the owning request or bundle. `deny` rejects an exec-bearing
Context before launch. The separate `v0.5` restricted local argv and shell
capabilities never reuse credential-plugin output or authority. Kupilot does
not validate or control an external program's own network behavior.

## Verification boundary

The read-only deterministic tests assert exact methods, groups, versions,
resource paths, Namespaces, query strings, selectors, content types, page
limits, internal continuation, projections, cancellation, timeout,
scope/policy generation, RBAC denial, partial state, and zero-call local
denials. Request-recording Kubernetes and loopback Prometheus/Loki HTTP
fixtures supplement client-go object fakes because the fake alone is not a
security oracle. The remote-diagnostic matrix additionally exercises a real
loopback SPDY upgrade, exact exec query/options, redirect denial, ordered and
bounded stream collection, cancellation/join, stale and permission zero-call
paths, file-archive validation, exact diagnostic-Pod spec and request sequence,
and ambiguous cleanup. It is deterministic API evidence, not live-cluster,
RBAC-installation, CNI, image, or endpoint compatibility evidence. The typed-
remediation matrix records exact preparation and verification reads,
SelfSubjectAccessReview attributes, `scale` PUT, Deployment PUT, Pod DELETE,
Node merge PATCH, and eviction POST bodies and paths. It also covers stale
targets, cancellation, transport ambiguity, immutable drain sets, unsafe Pod/
PDB cases, and zero-call denial without contacting a cluster.

## References

- [Scope](scope.md)
- [Security Threat Model](security.md)
- [Least-Privilege RBAC](rbac/README.md)
- [ADR-0007](adr/0007-use-client-go-behind-narrow-kubernetes-ports.md)
- [ADR-0037](adr/0037-adopt-an-operational-capability-catalog.md)
- [ADR-0044](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045](adr/0045-admit-controlled-execution-and-remediation.md)
