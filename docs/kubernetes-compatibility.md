# Kubernetes Compatibility

KuPilot pins `k8s.io/client-go v0.35.7`. The matching `k8s.io/api` and
`k8s.io/apimachinery` modules are also resolved at `v0.35.7`; mixed Kubernetes
module minors are not supported.

The upstream [`v0.35.7` module declaration](https://github.com/kubernetes/client-go/blob/v0.35.7/go.mod)
requires Go 1.25.0, matching KuPilot's minimum Go version. Client-go publishes
`v0.x.y` from the corresponding Kubernetes `v1.x.y` source, so this dependency
matches Kubernetes 1.35.7 exactly. The upstream
[versioning and compatibility contract](https://github.com/kubernetes/client-go/blob/v0.35.7/README.md)
distinguishes an exact minor match from cross-minor use of shared APIs.

## Cluster version matrix

KuPilot supports the following Kubernetes API server minors:

| Kubernetes API server | KuPilot contract with client-go v0.35.7 |
| --- | --- |
| 1.34.x | Supported for code-allowlisted stable APIs shared with 1.35. |
| 1.35.x | Supported; this is the exact client-go minor match. |
| 1.36.x | Supported for code-allowlisted stable APIs shared with 1.35. |

Older and newer server minors are outside the supported matrix. Patch versions
within a supported minor do not change KuPilot's resource, relationship,
projection, or request allowlists.

This is a deliberately narrow KuPilot support contract, not a claim that every
client-go API works across the three minors. The Kubernetes
[version skew policy](https://kubernetes.io/releases/version-skew-policy/)
defines a one-minor rule specifically for `kubectl`; KuPilot does not reinterpret
that statement as a general guarantee for arbitrary Go clients. KuPilot admits
only stable typed APIs and verifies their exact request paths across this matrix.
Discovery never expands the supported surface.

## Kubeconfig loading

KuPilot uses client-go's standard loading precedence:

- When `KUBECONFIG` is non-empty, its platform-separated file list is loaded in
  order, with duplicate paths removed. The first file to define a map entry wins.
- Otherwise, the standard per-user kubeconfig file is selected.
- Missing entries in a multi-file list are ignored when another usable source
  exists. An entirely missing, empty, or invalid result fails with a safe summary.
- Relative certificate, key, token, and related paths are resolved by client-go
  against the source file that declared them.

KuPilot disables client-go's legacy migration rule because loading must never
copy or modify a user-owned kubeconfig. On Unix platforms, group- or
world-accessible selected sources produce a fixed warning without disclosing a
path or changing permissions.

Only sorted Context names, the effective default Namespace, current selection,
and whether a Context declares exec credentials leave the Kubernetes adapter.
Raw kubeconfig values, source paths, clusters, users, server addresses,
credentials, `rest.Config`, transports, and client-go clients remain private.

## Client bundle contract

Each selected Context creates a fresh, independently owned bundle with these
fixed settings:

- User-Agent `kupilot/0.1`.
- QPS `5` and Burst `10`.
- A 10-second ceiling covering one HTTP request and any exec credential refresh
  needed by that request.
- Normal HTTPS certificate and hostname verification with TLS 1.2 or newer.
- Rejection of `insecure-skip-tls-verify`, plain HTTP server URLs, URL user
  information, queries, fragments, custom transports, legacy auth providers,
  and every HTTP redirect.

The transport has no request or response dump wrapper. Authentication headers
are attached only to the selected API server request and cannot cross a
redirect. Closing the bundle prevents new requests, cancels and waits for
in-flight requests and exec credential children, clears bundle-local credential
state, and closes idle connections.

The bundle contains only a private typed clientset. It exposes no REST client,
dynamic client, discovery client, generic resource interface, request builder,
Watch, informer, or write-capable consumer method.

## Scope activation and bounded reads

Application owns the live scope generation and receives only an opaque client
lifecycle handle plus project-owned DTOs. A Context activation first validates
the exact local candidate, commits generation invalidation, cancels the active
run, clears selected-resource and same-generation list caches, calls the fixed
scope-invalidation hook, and closes the old bundle before creating the target
bundle. It then verifies the target Context's effective Namespace with one exact
core `v1` Namespace `GET`. A failed Context construction or Namespace
verification leaves the new generation unavailable and never restores the old
bundle implicitly.

A Namespace candidate is checked with one exact core `v1` Namespace `GET`
before its commit point. A failed or forbidden check leaves the current scope
unchanged. A successful change advances generation, performs the same local
invalidation, and reuses the current Context bundle. Namespace picker reads use
one core `v1` Namespace `LIST`, request at most 50 items, and return sorted names
only. Empty Namespace input is never interpreted as all Namespaces.

The directly readable target surface is fixed:

| Target Kind | Stable typed API | Current-Namespace requests |
| --- | --- | --- |
| Pod | core `v1` | Exact `GET` and bounded `LIST` of `pods` |
| Service | core `v1` | Exact `GET` and bounded `LIST` of `services` |
| Deployment | `apps/v1` | Exact `GET` and bounded `LIST` of `deployments` |
| ReplicaSet | `apps/v1` | Exact `GET` and bounded `LIST` of `replicasets` |
| Job | `batch/v1` | Exact `GET` and bounded `LIST` of `jobs` |

Each list accepts a code-validated limit from 1 through 50, sends that limit to
the API server, applies the same ceiling again after return, and accepts no raw
label selector, field selector, continuation token, or all-Namespace option.
Same-generation Namespace and resource-list results may be held in ephemeral
Picker caches; every generation change clears them. Exact resource reads are
not object-body cache entries.

The public projection contains only the fixed ResourceRef, optional creation
time, bounded phase or reason, applicable non-negative readiness and workload
counts, Service type, and at most one allowlisted controller owner reference.
It excludes labels, annotations, selectors, messages, container environment,
volume details, addresses, managed fields, and raw Kubernetes objects.

EndpointSlice remains package-internal to the fixed Service relationship. Its
only admitted read is a namespaced `discovery.k8s.io/v1` list with the
code-constructed `kubernetes.io/service-name` selector, at most 50 slices, and a
local ceiling of 1,000 endpoint entries. Only ready and not-ready counts survive;
addresses never enter the result. A Pod's existing `apps/v1` StatefulSet
controller reference may be projected as `reference_only`; no StatefulSet
`GET` or `LIST` exists.

Secret, ConfigMap, EndpointSlice as a direct target, StatefulSet as a direct
target, every unknown Kind, cross-Context, cross-Namespace, empty-Namespace,
invalid-limit, and foreign-client requests are rejected before a resource
client action whenever locally decidable. Scope-managed reads compare the
complete bound Context, Namespace, and generation before invocation and after
every return; a mismatch produces `stale_scope` and discards the candidate
result.

## Exec credential contract

Exec credentials are accepted only when the selected kubeconfig Context
declares them and `kubernetes.exec_credentials` is `allow`. With `deny`, a
Context that requires exec authentication fails before client construction or
process launch. Static credentials take precedence without launching a
redundant exec program.

KuPilot supports `client.authentication.k8s.io/v1` and `v1beta1` in
non-interactive mode. `Never` and `IfAvailable` run without standard input;
`Always` is rejected before launch. The executable, arguments, protocol version,
declared environment, and optional cluster information originate only in the
selected kubeconfig. The executable is launched directly without a shell.

KuPilot uses client-go's kubeconfig resolution, ExecCredential codecs, and TLS
transport types, while keeping process invocation bundle-local so that the
owning Context can cancel it. The process environment removes
`KUPILOT_MODEL_API_KEY` from both the inherited environment and kubeconfig
declarations. KuPilot supplies only the protocol-owned `KUBERNETES_EXEC_INFO`
value in addition to the filtered environment.

Credential standard output is limited to 64 KiB and is consumed only by the
client-go protocol decoder. Standard error is retained only in an 8 KiB
discarding buffer and never enters an error, log, UI, model request, or durable
field. Missing executables, nonzero exits, malformed credentials, cancellation,
and timeout are translated to fixed safe error classes without command,
argument, environment, path, or output text.

An allowed exec credential program runs with the local user's authority and may
perform its own network or filesystem operations. KuPilot does not sandbox or
audit those operations; strict deny is the fail-closed option for users who do
not trust that kubeconfig execution surface.
