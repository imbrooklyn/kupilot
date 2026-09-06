# Permissions and Controlled Actions

This page defines the Accepted `v0.5` interaction and its current deterministic
implementation. The checked-in composition dispatches supervised restart,
scale, rollback, controller-owned Pod delete, cordon, uncordon, drain, exact
local direct argv, the separate shell operation, and the default-off remote-
diagnostic handlers. Human and Reviewer routes for remote diagnostics, review-
class Pod logs, and optional Prometheus/Loki reads block the Tool call until the
exact envelope is durably consumed or safely closed. No universal live
compatibility or release readiness is implied.

## Choose a permission profile

`ask` is the default. `/permissions` opens the fixed local profile picker using
the same composer and shows each profile's technical boundary, Reviewer route,
and residual risk. `/status` reports the same bounded state without external
I/O.

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
resume. For a restricted local command, the template is the complete exact
argv vector; a partial prefix cannot authorize a longer local command.

The inline auto-review states are `Reviewing`, `Approved`, `Denied`,
`Escalated to user`, and `Timed out`; they are never styled as human decisions. A
failure or timeout performs no action. The human review surface offers only
approve once, an eligible narrow Session rule, deny, and cancel. Deny is the
safe default. Creating a Session rule invalidates the displayed source action;
that action does not execute and a fresh request is required.

## Review an ActionEnvelope

Every sensitive or effectful request is normalized into one immutable
versioned `ActionEnvelope`. The review shows its operation and schema version,
risk, permission profile, exact Context/Namespace and generations, target
identity, typed parameters or fixed executable plus argv, stdin/TTY/shell
flags, data categories, sinks, network destinations, time/output limits,
expiry, verification plan, and digest.

For local execution the review also states that no operating-system sandbox is
provided. Direct argv shows the exact executable, argv, executable/cwd identity,
minimal environment, false stdin/TTY/shell flags, data/network effects, opaque
credential-reference identity, and ceilings. Shell is a distinct `critical`
envelope; it is never inferred from direct argv.

The digest uses a versioned fixed-order length-prefixed canonical encoding and
SHA-256. A human summary, model phrase, Reviewer rationale, JSON key order,
raw YAML, map, or unnormalized command is never authority.

Approve-once defaults to Deny, expires exactly 60 seconds after proposal
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
| Output blocked | A local process started, but sensitive-output handling rejected its output before display. The original bytes are not forwarded to the model, Evidence, history, audit, logs, SQLite, or export, and the action is reported as failed. |
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

Current YAML fixtures remain capability-split and opt-in: remote diagnostics,
scale, rollback, owned-Pod delete, Node scheduling, and drain each have a narrow
example in addition to reads and restart. They grant no local process authority
and deliberately avoid wildcard verbs/resources. See
[Least-Privilege RBAC](../rbac/README.md).

## Current shared action interaction

Application prepares each Kubernetes target or local filesystem identity before
review, defaults the dialog to Deny, and binds the envelope for exactly 60
seconds. After a valid human, Reviewer, automatic, or Session-rule decision it
revalidates the complete plan, durably consumes authority and pre-audits, then
calls only the matching narrow executor or source port at most once. Restart
retains its annotation-only PATCH. Other Kubernetes actions use their exact
scale/update/delete/Node-patch/eviction contracts; Pod logs and optional sources
use bounded read contracts; local direct argv and shell use distinct process
contracts. Attempt, partial acceptance, ambiguous outcome, and bounded
verification remain separate; retry always requires a fresh envelope.
