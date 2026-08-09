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
| 1.34.x | Supported for the stable, code-allowlisted API paths shared with 1.35. |
| 1.35.x | Supported; this is the exact client-go minor match. |
| 1.36.x | Supported for the stable, code-allowlisted API paths shared with 1.35. |

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
