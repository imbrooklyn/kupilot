# Least-Privilege Kubernetes RBAC

KuPilot `v0.1` needs only read access. Do not grant it `cluster-admin`, wildcard
verbs, wildcard resources, write verbs, Secret access, or an all-Namespace
ClusterRoleBinding for convenience.

RBAC is attached to the Kubernetes identity selected by kubeconfig. KuPilot does
not create a ServiceAccount, RoleBinding, or ClusterRoleBinding because the
correct subject and administration workflow are cluster-specific. The YAML in
this directory defines only the reusable permission rules. A cluster
administrator must review the placeholders and bind the chosen rules to the
actual user, group, or ServiceAccount.

## Exact request surface

<!-- markdownlint-disable MD013 -->

| API group | Resource | Verbs | Scope | Why KuPilot uses it |
| --- | --- | --- | --- | --- |
| Core | `namespaces` | `get` | Cluster-scoped exact name | Verify the Namespace before activating a ClusterScope. |
| Core | `namespaces` | `list` | Cluster-scoped, optional | Populate `/namespace`; the client requests at most 50 names. |
| Core | `pods` | `get`, `list` | One Namespace | Direct reads, candidate listing, Event/log target verification, and fixed relationships. |
| Core | `services` | `get`, `list` | One Namespace | Direct reads, candidate listing, and fixed Pod-to-Service relationships. |
| Core | `events` | `list` | One Namespace | Read bounded Events using a code-constructed involved-object field selector. |
| Core | `pods/log` | `get` | One Namespace | Read bounded current or previous Pod container output when the privacy category is enabled. |
| `apps` | `deployments` | `get`, `list` | One Namespace | Direct reads, candidate listing, and fixed workload relationships. |
| `apps` | `replicasets` | `get`, `list` | One Namespace | Direct reads, candidate listing, and fixed owner relationships. |
| `batch` | `jobs` | `get`, `list` | One Namespace | Direct reads, candidate listing, and fixed owner relationships. |
| `discovery.k8s.io` | `endpointslices` | `list` | One Namespace | Produce address-free ready and not-ready counts for one Service relationship. |

<!-- markdownlint-enable MD013 -->

All Kubernetes HTTP operations are reads. Kubernetes client-go represents
`list` and `pods/log` retrieval as HTTP `GET`; the RBAC verbs above are the
authorization verbs evaluated by the API server.

KuPilot has no request path for `create`, `update`, `patch`, `delete`,
`deletecollection`, `watch`, `impersonate`, `bind`, `escalate`, or `approve`.
It has no Secret, ConfigMap, Node, StatefulSet, custom-resource, discovery,
SubjectAccessReview, TokenRequest, exec, attach, port-forward, or ephemeral-
container permission.

## Choose the namespaced rule form

[role.yaml](role.yaml) is the narrowest option for one Namespace. Replace
`example-namespace`, create or review the identity-specific RoleBinding in that
same Namespace, and repeat for each Namespace the user is allowed to diagnose.

[cluster-role.yaml](cluster-role.yaml) contains the same namespaced rules as a
reusable ClusterRole. Bind it with a **RoleBinding in each exact Namespace**.
Do not use a ClusterRoleBinding for this ClusterRole: that would grant the
identity these reads in every Namespace and would exceed KuPilot's one-Namespace
run boundary.

A Role and the reusable ClusterRole are alternatives for namespaced resources;
do not bind both unless another reviewed consumer needs both objects.

## Choose Namespace verification or picker access

Namespace is a cluster-scoped resource, so a namespaced Role cannot authorize
its verification.

[namespace-verifier-cluster-role.yaml](namespace-verifier-cluster-role.yaml)
allows `get` for the single placeholder `example-namespace`. Replace that value
and bind the ClusterRole with an identity-specific ClusterRoleBinding. This is
the least-privilege choice when Context and Namespace are supplied by
configuration or CLI and the user does not need to browse Namespace names.

[namespace-picker-cluster-role.yaml](namespace-picker-cluster-role.yaml) allows
both `get` and `list` for Namespace metadata. Use it only when `/namespace`
completion is required. Kubernetes RBAC cannot limit an ordinary Namespace
`list` to one `resourceNames` entry, so this option exposes Namespace names that
the identity can list. It does not grant access to namespaced workload objects
without a separate RoleBinding.

The two Namespace ClusterRoles are alternatives. Do not bind both.

## Partial permissions and Diagnosis gaps

KuPilot never compensates for an RBAC denial by broadening a request:

- Without Namespace `list`, `/namespace` completion is unavailable, but an
  explicitly named Namespace can still be verified when exact `get` is allowed.
- Without Event `list`, Event-based conclusions remain missing information.
- Without `pods/log` `get`, or while container output is disabled in privacy
  settings, log-based conclusions remain missing information.
- Without EndpointSlice `list`, a Service Diagnosis cannot confirm address-free
  ready and not-ready endpoint counts.
- Without the relevant direct resource `get` or `list`, that resource or
  relationship branch is unavailable.

Permission errors are translated to safe gaps. Raw Kubernetes denial messages
are not copied into model content, local logs, SQLite, or the TUI.

## Defense in depth

RBAC does not replace KuPilot's runtime controls. Even if the selected identity
has broader permissions, KuPilot still binds every request to one verified
Namespace, rejects unlisted Kinds and subresources, uses fixed selectors and
limits, projects allowlisted fields, strips EndpointSlice addresses, checks
scope generation, and has no reachable write method.

Review [Security](../security.md), [Privacy](../privacy-overview.md), and
[Diagnostic Capabilities](../diagnostic-capabilities.md) before changing these
rules. Adding any verb, resource, subresource, or cluster scope is a security
and product-contract change, not a documentation-only convenience.
