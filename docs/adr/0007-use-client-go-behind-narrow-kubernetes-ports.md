# ADR-0007: Use client-go Behind Narrow Kubernetes Ports

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot must resolve local kubeconfig Contexts, use standard Kubernetes
authentication, issue cancellable namespaced reads, and interpret stable
Kubernetes API types. Reimplementing Kubernetes transport, authentication,
serialization, and API negotiation would create unnecessary security and
compatibility risk.

At the same time, a generic client exposed to the Agent, Tools, TUI, or
Application would turn broad client capability into accidental product
authority. A concrete client-go version and the exact APIs required by KuPilot
have not been validated in this repository.

## Decision

KuPilot will use the maintained Kubernetes Go client, client-go, inside
`internal/kube`. Client-go types, clients, configuration, transport, discovery,
watch, and fake types do not cross the adapter boundary.

The adapter will:

- Load and resolve the explicitly selected local kubeconfig Context without
  copying kubeconfig contents into Application state or SQLite.
- Construct one client bundle for the active Context and dispose it on a
  committed Context switch.
- Implement consumer-owned, task-specific ports for scope activation, the
  Resource Picker, and the six read-only Tools.
- Prefer typed stable API clients and explicit projected fields. Discovery does
  not expand the Kind or relationship allowlists.
- Apply the run Context, a shorter request deadline, the active Namespace, and
  fixed server-side bounds where the API supports them.
- Translate responses immediately into project-owned DTOs and translate raw
  errors into stable safe classes.

The adapter does not expose a dynamic client, REST client, generic resource
interface, arbitrary GroupVersionResource, raw request builder, informer,
Watch, or write-capable interface to its consumers. The existence of a method in
client-go does not admit it into KuPilot.

Kubeconfig exec authentication is governed separately by ADR-0020. The concrete
client-go version must be compatible with the selected Go lower bound and the
supported Kubernetes skew determined by the S04 and S09 gates.

## Consequences

Positive consequences:

- KuPilot uses the standard Kubernetes authentication and transport ecosystem.
- Typed API values can be projected at one reviewed boundary.
- Context cancellation and request deadlines integrate with the run lifecycle.
- The product allowlist remains smaller than the dependency's capability.

Costs and constraints:

- client-go has a large dependency graph and supports many capabilities KuPilot
  must not expose.
- Version selection must account for Go requirements and Kubernetes API skew.
- Client fakes may not reproduce transport, RBAC, defaulting, or serialization;
  request-recording and HTTP fixtures remain necessary.
- Explicit mapping is required for every projected API field and safe error.

## Alternatives considered

- Shelling out to kubectl was rejected because it would add executable lookup,
  command construction, parsing, environment, and output-injection risks and
  would resemble a general command runner.
- Implementing Kubernetes HTTP and authentication directly was rejected because
  it would duplicate a complex maintained client ecosystem.
- Exposing a dynamic or generic Kubernetes client was rejected because model or
  feature input could expand the resource surface.
- Using only client-go's object fake as the security oracle was rejected because
  it cannot prove exact wire requests or server authorization behavior.

## Security and privacy impact

Credentials stay inside client construction and transport. They do not enter
safe errors, events, DTOs, the model, TUI, logs, or SQLite. RBAC is required but
does not replace KuPilot's fixed scope, Kind, relation, projection, and budget
controls.

Every forbidden locally identifiable request must fail before client-go is
called. A broad kubeconfig identity therefore cannot make Secret, cross-
Namespace, generic discovery, Watch, or write behavior reachable from a Tool.

## Validation gate

S04 must use official module metadata to lock one client-go minor, record its Go
requirement, and declare the corresponding Kubernetes minor plus the adjacent
minor on each side as the supported test matrix. No later than S09 and before
constructing the production client bundle, a repository spike must verify:

1. A maintained client-go release compatible with the selected Go lower bound
   and a documented Kubernetes version-skew target.
2. Kubeconfig loading, Context selection, namespace verification, authentication
   provider, transport, cancellation, timeout, and user-agent behavior.
3. Typed APIs for every allowlisted read and the fixed Service-to-EndpointSlice
   relationship, without importing beta APIs unless separately justified.
4. Exact request recording for verbs, resource paths, Namespace, subresources,
   query bounds, and cancellation.
5. Safe mapping for permission, authentication, not-found, timeout, throttling,
   unavailable, unsupported, and malformed-response errors.
6. Default QPS 5, Burst 10, a 10-second request ceiling, and configuration that
   can only tighten those values.
7. Disposal behavior for transports and exec credential children on scope
   switch and shutdown.

S10 must complete request-recording contract tests for the bounded Resource
Service before broader Tool work begins. The gates must record the selected
version, supported Kubernetes range, and exact API paths. This ADR does not claim
those choices have already passed.

## Revisit triggers

- No maintained client-go line satisfies the Go and supported-platform matrix.
- A required stable Kubernetes API is unavailable across the supported skew.
- The adapter cannot prevent generic capability or sensitive types from leaking
  through its ports.

## References

- [Architecture](../architecture.md)
- [Scope](../scope.md)
- [Security Threat Model](../security.md)
- [ADR-0020: Contain Kubeconfig Exec Credentials](0020-contain-kubeconfig-exec-credentials.md)
- [ADR-0024: Freeze Kubernetes Kind and Relationship Allowlists](0024-freeze-kubernetes-kind-and-relationship-allowlists.md)
