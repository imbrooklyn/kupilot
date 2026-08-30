# ADR-0037: Adopt an Operational Capability Catalog

- Status: Accepted
- Date: 2026-08-30
- Supersedes: ADR-0009, ADR-0011, ADR-0024, and ADR-0029

## Context

The original read-only MVP proved the scope, projection, Evidence, consent, and
approval boundaries, but its exact six-Tool, five-Kind, one-Namespace contract
prevents common Kubernetes operational investigations. In particular, an Agent
cannot answer ordinary questions about Nodes, Namespaces, common controllers,
storage, networking, or a workload in another Namespace even when the selected
Kubernetes identity already has the required RBAC permission.

Removing every runtime boundary would create a shell or generic Kubernetes
client controlled by untrusted model output. Keeping the original product
boundary, however, makes the Agent unsuitable for its intended daily use.

## Decision

Kupilot will be a local conversational Kubernetes operations Agent. It remains
single-process, single-user, and centered on one natural-language conversation;
it does not become a resource dashboard, k9s clone, kubectl terminal, or shell.

The model-visible capability catalog is versioned and code-owned rather than
permanently fixed to six entries. Every capability still requires a strict
schema, canonical arguments, a consumer-owned port, local authorization,
bounded projection, sensitive-data handling, deterministic Evidence or action
metadata, and request-recording tests. There is no prompt-parsed command,
dynamic plugin, arbitrary GVR, raw REST request, YAML apply, or shell fallback.

The first operational read catalog expands direct typed access to these stable
built-in resources:

- Core `v1`: Namespace, Node, Pod, Service, PersistentVolumeClaim,
  PersistentVolume, and ConfigMap metadata only.
- `apps/v1`: Deployment, ReplicaSet, StatefulSet, and DaemonSet.
- `batch/v1`: Job and CronJob.
- `networking.k8s.io/v1`: Ingress.
- `autoscaling/v2`: HorizontalPodAutoscaler.
- `policy/v1`: PodDisruptionBudget.

Secret objects and data remain prohibited. ConfigMap values, container
environment values, projected credentials, admission objects, RBAC objects,
custom resources, and arbitrary discovered APIs are not model-readable. Adding
a source still requires explicit projection and sink review; the catalog may
evolve without another "exactly N Tools" product freeze, but not dynamically at
runtime.

`ClusterScope` continues to bind one verified Context, one working Namespace,
and one generation to a run. The working Namespace is the default target and is
always visible in the TUI. A frozen namespace-access policy additionally
authorizes either:

- `current`: only the working Namespace; or
- `all`: an explicit different Namespace or a bounded all-Namespace list in the
  same Context.

The default operational profile is `all`; operators can tighten it to
`current`. The model cannot change the policy. Every explicit Namespace is
validated locally, included in canonical arguments and Evidence, and remains
subject to Kubernetes RBAC. An all-Namespace request is represented explicitly,
never by an empty value that can be confused with the working Namespace.
Cluster-scoped Node, Namespace, and PersistentVolume references carry no fake
Namespace. Cross-Context and cross-cluster calls remain prohibited.

Reads remain side-effect free. Mutations are separate typed operations. Every
mutation requires a code-defined semantic diff, exact target, digest-bound
local approval, revalidation, durable pre-operation audit, a single execution
attempt, and separate verification. The existing Deployment restart is the
first admitted mutation, but it is no longer a permanent claim that all later
versions may contain exactly one write. A new mutation requires its own schema,
risk text, precondition, audit, denial tests, and explicit catalog entry. There
is no autonomous remediation or reusable approval.

## Consequences

Kupilot can investigate the cluster topology and common workload, storage, and
networking failures that dominate daily Kubernetes operations. RBAC remains the
operator's final Kubernetes authorization boundary, while Kupilot still
minimizes and validates what reaches the model and terminal.

The adapter and test surface grows because every built-in Kind needs a stable
typed client mapping and a reviewed projection. The product must show the
active namespace-access policy and action availability through `/status` so a
broader policy is not invisible.

## Security and privacy impact

Broader read authority can expose more names, status, topology, and operational
text. Consent categories, projection, normalization, sensitive-value blocking,
byte and item limits, and retention apply before every sink. Kubernetes RBAC
denial is never bypassed or retried with another identity.

Cluster-scoped and cross-Namespace Evidence records the exact observed resource
scope. A working Namespace is presentation and defaulting context, not a false
claim that every observation came from it. Scope generation still invalidates
all late work for the selected Context and access policy.

## Validation

Deterministic tests must cover every admitted Kind and Namespace mode with exact
verb, group, version, resource, Namespace, limit, and projection assertions.
Secret and ConfigMap-data requests, unknown APIs, cross-Context calls, policy
expansion, malformed Namespaces, and all-Namespace calls under `current` must
produce zero Kubernetes actions.

Every write catalog entry must additionally prove zero executor calls on
missing approval, mismatch, expiry, replay, stale scope, target change,
persistence failure, or ambiguous prior outcome.

## References

- [Product Contract](../product.md)
- [Scope](../scope.md)
- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0012: Require Digest-Bound Approval for Writes](0012-require-digest-bound-write-approval.md)
- [ADR-0014: Isolate Runs with ClusterScope Generation](0014-cluster-scope-generation-isolation.md)
