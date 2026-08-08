# ADR-0014: Isolate Runs with ClusterScope Generation

- Status: Accepted
- Date: 2026-08-05

## Context

Context and Namespace changes race with model streams, Kubernetes requests,
Tool projection, persistence, and TUI delivery. Cancellation alone cannot prove
that an SDK or network operation stopped before returning. A late result from a
previous cluster scope could otherwise be displayed, persisted, sent back to
the model, or treated as current Evidence.

A mutable scope reference on an active run would make the same question span
more than one cluster boundary and destroy Evidence provenance.

## Decision

Every AgentRun is bound to one immutable ClusterScope containing a verified
Context name, verified Namespace, process-local generation, and activation time.
Application copies that value into an immutable RunInput before the run starts.
The model and Tool arguments cannot set or change it.

Application owns one monotonically increasing live generation for the process.
A committed Context switch follows this order:

1. Validate the command's expected generation and the local Context candidate.
2. Increment generation and mark scope as activating.
3. Cancel the active run.
4. Clear the selected ResourceRef and all old-generation caches.
5. Dispose the old client bundle and construct and verify a target bundle.
6. Publish the new active scope, or remain unavailable at the new generation on
   failure. The old bundle is not restored implicitly.

A Namespace candidate may be verified before its commit point while new run
starts are serialized. A failed candidate does not commit a switch. A
successful switch performs the same generation increment, cancellation, and
cache and ResourceRef invalidation before publishing the new scope.

Every run-scoped external Tool call has three mandatory acceptance gates:

1. A pre-call comparison of the complete bound scope and current generation.
   Failure returns `stale_scope` and the external call count remains zero.
2. A post-result comparison after every success, partial, and error return.
   Failure discards the candidate result before it reaches any sink.
3. An Application event comparison of run ID, generation, monotonic sequence,
   and terminal state before persistence, TUI state, or model context changes.

The run context is cancelled as well, but cancellation is a responsiveness
mechanism rather than the sole stale-work guard. Persisted generations are
diagnostic history only and cannot be restored as live authority after process
startup.

## Consequences

Positive consequences:

- A run and all accepted Evidence have one unambiguous Context and Namespace.
- Late network and stream results fail closed even when cancellation is delayed.
- Picker results, completion results, and TUI events can use the same stale-work
  rule.
- Race tests can assert both zero-call and zero-accepted-result properties.

Costs and constraints:

- Every asynchronous request and event carries run identity and generation.
- Scope switches must be serialized and can leave the application deliberately
  unavailable after a failed Context activation.
- Adapters and fakes must expose deterministic barriers so tests can place a
  generation change before or during I/O.
- Application must distinguish a candidate validation failure from a committed
  switch failure.

## Alternatives considered

- Cancellation without generation was rejected because an external operation
  can return after cancellation.
- Checking only when the result reaches the TUI was rejected because stale data
  could already have entered persistence or model context.
- Checking only before the external call was rejected because the scope can
  change while the call is in flight.
- Mutating the active run's scope was rejected because it mixes Evidence
  provenance and invalidates the one-question, one-scope contract.
- Reusing the previous client after failed Context activation was rejected
  because it can conceal which cluster is active.

## Security and privacy impact

Generation prevents cross-scope data confusion and stale-result disclosure. It
does not grant Kubernetes access and does not replace local credential handling,
RBAC, fixed Kind and relationship allowlists, or output safety.

The scope value contains display names and generation only. It excludes server
addresses, credential material, kubeconfig contents, and live client handles.
Those remain inside the Kubernetes adapter.

`v0.2` approval state must bind generation and become invalid on a committed
scope switch before any external operation can be authorized.

## Validation

The required call order and all three gates are documented in
[Architecture](../architecture.md). Deterministic concurrency tests must use
channels or barriers rather than timing sleeps to prove:

- A stale generation before invocation produces zero Kubernetes calls.
- A generation change during I/O produces zero accepted Evidence, persistence
  writes, successful TUI results, and model Tool results.
- Late and duplicate events cannot change a terminal run.
- A failed Context activation does not silently reuse the old client.
- Process resume does not restore a persisted live generation.

## Revisit triggers

- The product accepts concurrent scopes or concurrent AgentRuns.
- A process boundary is introduced and process-local generation is no longer
  sufficient.
- A reproducible race requires a stronger lease or capability design while
  preserving the same fail-closed contract.

## References

- [Architecture](../architecture.md)
- [Product Contract](../product.md)
- [Glossary](../glossary.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
