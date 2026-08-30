# Deployment Restart Approval

The current `v0.4` composition supports one supervised write:
`restart_deployment` for one exact `apps/v1` Deployment in the working
Namespace. It changes only the
`kupilot.io/restartedAt` Pod-template annotation to a locally generated UTC
value. It does not delete Pods, change replicas, edit images, scale, roll back,
run a command, or accept a model-generated patch.

## Review the proposal

The Agent can emit one typed restart suggestion, but it cannot approve or
execute it. Before opening an approval, Kupilot performs one fresh exact
Deployment GET and locally derives the UID, Pod-template fingerprint, and
generation. Model output cannot supply those fields, a resource version, nonce,
digest, annotation, timestamp, or patch. If preparation fails, the answer
remains available and no approval or write is created.

The Approval Dialog displays the exact operation,
Context, Namespace, scope generation, Deployment identity, current generation,
Pod-template fingerprint, proposed fixed change, reason, risk, operation digest,
and expiry. The dialog defaults to **Reject**.

Use Tab or the arrow keys to move between Reject and Approve. Enter confirms the
selected choice. Esc rejects. Approval expires exactly 60 seconds after proposal
creation. Expired, rejected, cancelled, invalidated, and already-used approvals
cannot be reopened or reused.

Approving authorizes at most one fixed PATCH attempt. It does not authorize a
successful rollout, a retry, another Deployment, or any follow-up operation.
Immediately before the attempt, Kupilot verifies the durable approval, re-reads
the Deployment, compares its UID, generation and Pod-template fingerprint, uses
the fresh resource version as a concurrency precondition, and commits the
consumed approval plus pre-operation audit. Any mismatch or pre-operation audit
failure produces zero writes.

## Understand execution and rollout states

The dialog remains visible while the approved operation is attempted and
verified. These states have different meanings:

| State | Meaning |
| --- | --- |
| PATCH accepted | Kubernetes accepted the single fixed request. Rollout success is not yet known. |
| PATCH failed | Kubernetes definitively rejected or conflicted with the request. Kupilot does not retry it. |
| PATCH outcome unknown | Transport cancellation, timeout, or another ambiguous response prevents Kupilot from knowing whether the request took effect. It is never retried automatically. |
| Rollout progress | Kupilot observed a changed bounded projection of target generation, updated replicas, and available replicas. |
| Rollout succeeded | The target generation was observed and updated and available replicas both reached the post-PATCH target. |
| Rollout failed | The target was replaced or changed again, or the Deployment reported `ProgressDeadlineExceeded` or `ReplicaFailure`. |
| Rollout timed out | The 90-second observation window ended without verified success or a fixed failure. The PATCH remains accepted; timeout is not reported as PATCH failure or rollout success. |
| Rollout unavailable | Cancellation, Context or Namespace change, permission loss, or a read failure stopped verification after an accepted PATCH. |
| Result audit failed | Kupilot could not store required post-attempt audit metadata through its bounded idempotent procedure. Further verification stops and the PATCH is not repeated. |

The default observer polls the exact Deployment no faster than every two seconds
for at most 90 seconds and 45 observations. A deployment composition may only
shorten the window or poll more slowly. The observer uses no Watch, informer, or
background controller and stops when its owning Context is cancelled.
If no Deployment read completes before the window ends, timeout is reported
without inventing replica counts.

If verification times out or becomes unavailable, inspect the Deployment using
an independently authorized read path before deciding whether to create a new
proposal. Never assume that repeating the approval is harmless: the original
PATCH may already have been accepted.

## Audit and local data

Kupilot records fixed structured metadata for the proposal, local decision,
consumed intent, PATCH accepted/failed/unknown result, changed rollout progress,
and terminal verification result. Write-audit metadata is retained for 180 days
under both standard and minimal persistence. It excludes raw Deployment objects,
Pod-template data, resource versions, patch bodies, Kubernetes condition
messages, response bodies, and vendor errors.

The restart annotation is visible Kubernetes metadata. Kupilot does not claim
that local SQLite audit data is encrypted, tamper-resistant, or forensically
deleted.

## RBAC

Use the read rules required for the selected operational catalog and add only the exact namespaced
Deployment rule documented in [Least-Privilege RBAC](../rbac/README.md). The
provided restart Role restricts `get` and `patch` to one placeholder Deployment
name. Do not grant wildcard writes, Deployment `update` or `delete`, Pod writes,
Watch, or a ClusterRoleBinding.
