# Permissions and Controlled Actions

This page defines the Accepted `v0.5` interaction target. The checked-in
`v0.4` binary still exposes only one supervised `restart_deployment` action;
the broader permission profiles, Reviewer, remote/local execution, and typed
remediation are not yet available.

## Choose a permission profile

`ask` is the default. `/permissions` is the planned local control for reviewing
and changing the profile and current-Session rules; `/status` reports the same
bounded state without external I/O.

| Profile | Behavior |
| --- | --- |
| `read-only` | Runs safe reads automatically, asks a human for an admitted sensitive read, and denies writes, Pod Exec, diagnostic Pods, local processes, and shell. |
| `ask` | Runs safe work automatically and asks the human for every `review` or `critical` request. |
| `auto-review` | Sends `review` requests to the optional Reviewer, which may `approve`, `deny`, or `escalate_to_user`; `critical` remains human-reviewed. |
| `full-access` | Runs admitted and enabled `review` and `critical` work without a per-action prompt after explicit high-risk selection. It never overrides `deny`. |
| `custom` | Uses exact code/config routes. `review` may be automatic, human, Reviewer, or denied. `critical` may be automatic, human, or denied and defaults to human. |

A profile does not grant Kubernetes RBAC, enable a default-off capability,
broaden Context/Namespace scope, add a model or data destination, change
consent, expose a credential, lower deterministic risk, bypass audit or target
revalidation, or turn a denied operation into an admitted one.

The optional Reviewer is a decision input, not permission authority. It receives
a minimal normalized action and policy projection, uses a strict non-streaming
no-Tool response, and can act only on `review`. Missing configuration, missing
consent, timeout, cancellation, malformed output, stale policy, or exhausted
budget authorizes nothing and never falls back to the Agent or another model.

A human may create a narrow Session rule only for `review`. The rule binds an
exact operation, scope, target/parameter or argv template, data/sink/network
effects, ceilings, and expiry. It is revocable, valid only in the current
process and Session, never created by a model or Reviewer, and never restored by
resume.

The inline auto-review states are `Reviewing`, `Approved`, `Denied`,
`Escalated`, and `Timed out`; they are never styled as human decisions. A
failure or timeout performs no action. The human review surface offers only
approve once, an eligible narrow Session rule, deny, and cancel, and displays
the exact envelope fields before any choice.

## Review an ActionEnvelope

Every sensitive or effectful request is normalized into one immutable
versioned `ActionEnvelope`. The review shows its operation and schema version,
risk, permission profile, exact Context/Namespace and generations, target
identity, typed parameters or fixed executable plus argv, stdin/TTY/shell
flags, data categories, sinks, network destinations, time/output limits,
expiry, verification plan, and digest.

The digest uses a versioned fixed-order length-prefixed canonical encoding and
SHA-256. A human summary, model phrase, Reviewer rationale, JSON key order,
raw YAML, map, or unnormalized command is never authority.

Approve-once defaults to Reject, expires exactly 60 seconds after proposal
creation, and is single-use. Approval of one envelope does not authorize a
different target, parameter, destination, command, cleanup, retry, or follow-up
operation. Changing scope or permission policy invalidates pending state before
old work is cancelled.

## Understand execution states

Application revalidates the exact target or executable policy, checks the
decision and digest, atomically consumes single-use authority and persists
pre-operation audit, performs one final scope/policy-generation check, and then
makes at most one external attempt.

| State | Meaning |
| --- | --- |
| Denied or rejected | No external attempt occurred. A hard denial cannot be overridden by another profile. |
| Accepted | The external system accepted the single request; remediation success is not yet known. |
| Failed | The external system definitively rejected or failed the attempt. Kupilot does not retry it automatically. |
| Ambiguous or unknown | The request may have reached the external system, but timeout, cancellation, transport loss, or cleanup uncertainty prevents a definitive result. It is never retried automatically. |
| Progress | A bounded verifier observed an intermediate state. It does not rewrite the attempt result. |
| Verified | The operation-specific verification plan observed its success condition. |
| Verification failed | A fixed failure condition was observed after an attempt. |
| Verification timed out or unavailable | The attempt result remains separate; use an independently authorized read before proposing another action. |
| Audit failed before execution | No executor call occurred. |
| Result audit failed | The external attempt is not repeated; the durable pre-operation fact remains and the UI reports degraded audit. |

If an outcome is unknown, do not assume that repeating it is harmless. Inspect
current state through a separately authorized bounded read and create a fresh
ActionEnvelope only when another operation is still necessary.

## P0 action risk

| Operation | Base interaction |
| --- | --- |
| Restart one exact Deployment | `review`; only Kupilot's restart annotation changes. |
| Scale one exact Deployment or StatefulSet | Positive delta of one is `review`; scale-to-zero or another delta is `critical`. |
| Roll back one exact Deployment | `critical`; binds a freshly validated prior ReplicaSet revision. |
| Delete one ordinary controller-owned Pod | `review`; force, grace-zero, bulk, unmanaged, static, and mirror cases are denied. |
| Cordon or uncordon one exact Node | `review`; only `spec.unschedulable` changes. |
| Drain one exact Node | `critical`; binds a complete bounded eligible Pod set and admits no force, delete-emptydir, or ignore-daemonset shortcut. |
| Container-file read or predefined read-only Pod diagnostic | `review`, with exact path/argv and output policy. |
| Other Pod Exec or diagnostic Pod | `critical`, default off. |
| Restricted local argv | Default off; risk follows the exact admitted behavior. |
| Shell | Separate `critical` class, default off; binds a policy-selected shell executable and one exact bounded command string. |

Generic patch/apply/edit/delete, arbitrary YAML, model-selected executable,
image, destination, command string or flags, and wildcard operations remain
denied.

## RBAC and local policy

RBAC is an independent Kubernetes authorization layer. The `v0.5` target splits
read, metrics, Pod log, Pod Exec, diagnostic Pod, scale, eviction, Node patch,
and other optional permissions. Do not grant `cluster-admin` or wildcard
resources/verbs. RBAC cannot enforce Kupilot's field projection, exact command,
digest, one-attempt, or verification rules.

The current YAML fixtures remain the `v0.4` read and exact Deployment-restart
examples. They deliberately do not pre-grant future capabilities. See
[Least-Privilege RBAC](../rbac/README.md).

## Current `v0.4` restart interaction

The current binary prepares one exact `apps/v1` Deployment by reading its UID,
Pod-template fingerprint, generation, and fresh resource version. The dialog
defaults to Reject and binds the fixed restart operation for 60 seconds.
Execution makes at most one annotation-only merge PATCH. PATCH accepted,
failed, or unknown and rollout progress, success, failure, timeout, or
unavailable remain separate. This is retained as the operation-specific
foundation that the future common ActionEnvelope must generalize.
