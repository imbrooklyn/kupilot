# ADR-0005: Confine Kubernetes Access and Exec Credentials

- Status: Accepted

## Context
Kubernetes objects and authentication mechanisms expose more data and authority
than any admitted Tool needs.

## Decision
Use client-go only inside internal/kube, behind exact consumer-owned ports.
Keep rest.Config, credentials, clients, discovery, GVRs and raw objects confined.
Use typed reviewed resource projections and fixed relationships. Exact CRD policy
names group, version, resource, Kind, scope, verbs, fields, limits and Evidence
mapping; discovery validates an entry but cannot create authority.

Deny generic request builders, arbitrary selectors, JSONPath/templates,
model-selected APIs, Watch and informers. Enforce frozen scope and safe
server-side filtering, runtime-owned continuation, independent page/item/byte/time
ceilings and explicit partial results.

Kubeconfig exec credentials are a distinct opt-in local-process boundary.
Strict deny is available. Allow invokes only the configured program and argv,
without a shell, with a minimal environment, bounded output and timeout, and
Context-owned cancellation and child reaping. Output goes only to the credential
decoder. Never forward credentials or raw kubeconfig to the model, application
events, logs, history, SQLite, exports or other children.

## Consequences and validation
Exact projections cost adapter code but prevent raw APIs from becoming model
authority. RBAC complements rather than replaces these controls. Request
fixtures verify exact verb/GVR/Namespace/subresource/body/filter/precondition,
pagination and zero-call denials. Process fixtures verify environment, output
bounds, cancellation and join. See [Kubernetes Compatibility](../kubernetes-compatibility.md),
[Security](../security.md) and [RBAC](../rbac/README.md).
