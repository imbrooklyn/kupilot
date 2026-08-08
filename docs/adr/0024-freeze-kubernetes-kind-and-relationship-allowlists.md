# ADR-0024: Freeze Kubernetes Kind and Relationship Allowlists

- Status: Accepted
- Date: 2026-08-08

## Context

Kubernetes discovery and owner references can expose a very broad and
extensible resource graph. Model-selected Kind, arbitrary GroupVersionResource,
raw selector, recursive owner traversal, or all-Namespace access would make
scope depend on untrusted input and could reach Secret or custom-resource data.

The eight MVP diagnostic categories need only a small set of workload, Pod,
Service, Event, and bounded relationship observations.

## Decision

`v0.1` freezes the following directly addressable, current-Namespace target
Kinds:

- Pod
- Deployment
- ReplicaSet
- Job
- Service

The intended stable API groups are core `v1` for Pod and Service, `apps/v1` for
Deployment and ReplicaSet, and `batch/v1` for Job. The selected client-go APIs
must support these resources throughout the Kubernetes version matrix in
ADR-0007.

Fixed observation sources and relationships are:

- One direct projection of an allowlisted target.
- Recent bounded Events related to one verified ResourceRef.
- Current or previous bounded container output for one explicitly resolved
  container in one Pod.
- Deployment to owned ReplicaSets and, through them, owned Pods.
- ReplicaSet to its projected Deployment owner when present and to owned Pods.
- Job to owned Pods.
- Pod to projected owner references needed by the preceding relationships.
- Service to locally derived matching Pods and to EndpointSlice readiness counts
  without endpoint addresses.

Relationship traversal is directed, at most two hops, and additionally capped at
25 nodes and 40 edges. UID owner references and runtime-derived selectors are
used only inside the adapter and Tool policy. The model cannot provide a raw
selector or relationship edge. Cycles, missing UIDs, changing ownership, and
ambiguous matches produce partial Evidence or an explicit gap.

EndpointSlice is an indirect source only for safe ready/not-ready counts in a
Service relationship. It is not selectable, listed independently, or returned
with addresses. StatefulSet may appear only as a bounded existing owner
reference already present on a Pod; KuPilot does not fetch or list it.

Secret, ConfigMap data, custom resources, arbitrary API discovery, Namespace as
a Tool target, Node, PersistentVolume, PersistentVolumeClaim, Ingress, RBAC
objects, and every unlisted Kind or edge are denied. There is no fallback from a
denied Kind to a generic client.

## Consequences

Positive consequences:

- Cluster access is finite, reviewable, and tied to named diagnostic needs.
- Broad RBAC or discovery cannot expand the product surface.
- Projection and sensitive-field review can be specific to each source.
- Request-recording tests can compare exact resources and relationships.

Costs and constraints:

- Diagnostics involving unlisted controllers, storage, networking, Nodes, or
  custom resources will report missing information.
- Service matching and ownership changes can produce incomplete snapshots.
- Adding a Kind requires product, RBAC, privacy, projection, budget, fixture, and
  threat-model work.

## Alternatives considered

- Kubernetes discovery plus a denylist was rejected because new or custom APIs
  become reachable by default and sensitive fields are unbounded.
- A generic `get` or `list` Tool was rejected because model arguments would
  choose resource authority.
- Recursive owner traversal was rejected because malformed or broad graphs can
  expand data access and cost.
- Fetching StatefulSet from any owner reference was rejected because it is not
  required as a direct MVP Evidence source.
- Returning EndpointSlice addresses was rejected because counts satisfy the
  admitted Service diagnosis with less data exposure.

## Security and privacy impact

Source allowlisting precedes projection and redaction. Redaction cannot make an
unlisted source permissible. Secret and ConfigMap data calls fail locally with
zero Kubernetes requests when the forbidden source is known.

Names, selectors, Events, status, and container output may still be sensitive.
Every admitted field remains subject to consent, normalization, sensitive-value
blocking, output limits, Evidence rules, and retention eligibility.

## Validation

Request-recording fake Kubernetes API and projection fixtures for the Resource
Service, Picker, and all six Tools must prove:

- Exact verbs, group, version, resource, subresource, Namespace, query bounds,
  and maximum calls for every Tool and Picker operation.
- Zero calls for every unlisted Kind, arbitrary selector, all-Namespace request,
  generic discovery attempt, Secret, and ConfigMap-data request.
- Golden projected fields for each allowed source with distinct prohibited
  canaries in omitted fields.
- Correct UID-bound edges, direction, two-hop/node/edge limits, cycles, stale
  resources, missing owners, and selector changes.
- EndpointSlice output contains counts and no addresses.
- A scope switch during any relationship call produces zero accepted sinks.

## Revisit triggers

- A named diagnostic category repeatedly needs a new source and the proposal
  passes the feature admission gate with a bounded fixture.
- A Kubernetes stable API used by the allowlist is removed or materially changes
  semantics in the supported skew.
- The product admits cluster-scoped or custom-resource diagnosis through a new
  architecture and security decision.

## References

- [Scope](../scope.md)
- [Product Contract](../product.md)
- [Security Threat Model](../security.md)
- [ADR-0007: Use client-go Behind Narrow Kubernetes Ports](0007-use-client-go-behind-narrow-kubernetes-ports.md)
- [ADR-0009: Use Fixed Structured Tools](0009-use-fixed-structured-tools.md)
- [ADR-0016: Freeze Runtime Budgets](0016-freeze-runtime-budgets.md)
