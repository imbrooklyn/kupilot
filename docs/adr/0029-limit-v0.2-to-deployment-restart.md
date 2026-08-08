# ADR-0029: Limit `v0.2` to Deployment Restart

- Status: Accepted
- Date: 2026-08-08

## Context

`v0.2` admits one supervised Kubernetes change: restarting one Deployment. A
generic patch, apply, delete, scale, rollback,
or command surface would multiply authorization and safety semantics and could
turn model output into arbitrary mutation.

"Restart" also needs a precise semantic contract. Deleting Pods or accepting a
model-generated patch would have broader targets and parameters than the user
approved.

## Decision

`v0.2` adds exactly one write operation, `restart_deployment`, for one exact
namespaced Deployment admitted by the current ClusterScope. There is no write
operation in `v0.1`.

The semantic operation is to change exactly one KuPilot-owned Pod-template
annotation, `kupilot.io/restartedAt`, to a locally generated UTC value so the
Deployment controller observes a new Pod template. KuPilot does not delete Pods,
change replicas, image, strategy, selector, labels, environment, or any other
Deployment field.

The model-facing proposal input contains only the immutable ClusterScope, an
exact Deployment reference, and a bounded reason. It cannot contain a patch,
YAML, annotation key, annotation value, timestamp, resource version, or write
method.

The proposal and digest bind:

- `restart_deployment` and its operation schema version.
- Complete current Context, Namespace, and generation.
- Target `apps/v1` Deployment Kind, name, and UID.
- A deterministic fingerprint of the projected current Pod template and the
  Deployment generation.
- Canonical parameters, including a bounded reason summary, and the prior value
  or absence of the KuPilot annotation.
- Policy version and 60-second expiry.

ADR-0012 approval is mandatory. Immediately before the write KuPilot re-reads
the Deployment and revalidates active scope, name, UID, template fingerprint,
generation, canonical parameters, and policy. A status-only resource-version
change does not invalidate approval. KuPilot uses the fresh resource version as
the concurrency precondition, commits consumed approval and pre-write audit,
performs a final scope check, and issues at most one mutation request. A conflict
or ambiguous result is not retried automatically; a new proposal and approval
are required.

Post-operation verification uses bounded read-only observations of
`observedGeneration` and updated and available replica counts against the target.
KuPilot reports request acceptance, observed rollout progress, timeout, failure,
unavailable Evidence, and verified completion as separate states. It never
equates an accepted API request with a completed rollout.

The concrete client-go mutation method and wire representation must preserve
this semantic contract. Rollout timeout and poll interval are fixed, bounded,
and code-defined rather than model- or user-selected.

## Consequences

Positive consequences:

- The only write has a finite target, field diff, approval, audit, and test
  contract.
- Model output cannot supply an arbitrary patch or restart parameter.
- Target races and duplicate requests fail closed.
- Users can distinguish request acceptance from actual rollout observation.

Costs and constraints:

- KuPilot cannot restart StatefulSets, DaemonSets, individual Pods, or multiple
  Deployments.
- A changed Deployment invalidates approval rather than merging or retrying.
- The KuPilot-owned annotation becomes visible cluster metadata.
- Verification may time out or remain inconclusive even after the API accepted
  the request.

## Alternatives considered

- Deleting Pods was rejected because it targets child resources directly,
  behaves differently across controllers, and creates broader failure modes.
- Accepting an arbitrary patch or YAML was rejected because it turns approval
  into authorization for model-generated mutation content.
- Scaling down and up was rejected because it changes availability and desired
  replica state and requires multiple writes.
- Reusing another tool's restart annotation without ownership was rejected
  because KuPilot needs a stable, independently testable operation field.
- Automatically retrying conflicts or timeouts was rejected because the first
  request may have succeeded and the target may have changed.

## Security and privacy impact

The executor receives a project-owned immutable request only after digest, TTL,
nonce, generation, identity, fingerprint, durable audit, and one-time-state
checks. It exposes no generic patch or write method to Agent, TUI, Tool, or other
Application code.

Audit stores safe target and outcome metadata, not raw Deployment or wire bodies.
Minimal-persistence cannot disable the default 180-day write-audit contract.

## Validation

Before a write executor is reachable, fake-executor, client-go, and
request-recording HTTP fixture tests must prove:

1. The request changes only `kupilot.io/restartedAt` and carries an effective
   optimistic concurrency precondition.
2. Missing annotation maps, existing annotation values, conflicts, permission
   denial, timeout, cancellation, and ambiguous transport outcomes are handled
   without a broader diff or automatic retry.
3. A request-recording fake observes at most one write and no write for every
   digest, TTL, nonce, scope, identity, fingerprint, audit, or policy mismatch.
4. A status-only resource-version change retains approval, while a UID, template
   fingerprint, generation, canonical-parameter, policy, or active-scope change
   produces zero writes.
5. The chosen API and client-go version satisfy ADR-0007's compatibility gate.

Fixtures must prove that the fixed rollout timeout and poll interval distinguish
request accepted, progress observed, timeout, failure, unknown, and verified
completion by using
`observedGeneration` and updated and available replica targets.

The selected mutation API and rollout parameters remain documented with their
compatibility evidence.

## Revisit triggers

- A second write operation or another workload Kind is proposed.
- Kubernetes removes or materially changes the admitted Deployment semantics.
- The selected API cannot provide a one-field diff with an effective concurrency
  precondition.
- Product evaluation shows restart is not a safe or useful first supervised
  operation.

## References

- [Scope](../scope.md)
- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0011: Keep `v0.1` Strictly Read-Only](0011-keep-v0.1-strictly-read-only.md)
- [ADR-0012: Require Digest-Bound Approval for Writes](0012-require-digest-bound-write-approval.md)
