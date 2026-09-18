# ADR-0045: Require Digest-Bound Controlled Execution

- Status: Accepted

## Context
Sensitive reads and side effects require exact, short-lived authority even when
a model proposes an operation or an external request has an ambiguous outcome.

## Decision
Application canonicalizes an immutable version-1 ActionEnvelope binding operation,
policy/profile/risk/generation, Session/run/request, full scope, exact target
identity/fingerprint/revision/set, typed parameters or fixed executable+argv,
stdin/TTY/shell, data/sinks/network destinations, finite limits, times and a
verification plan. Use fixed-order length-prefixed encoding and SHA-256.
Model prose, JSON ordering, maps and human summaries never carry authority.

Approve-once defaults to rejection, expires exactly 60 seconds after proposal
creation and is single-use. Bind decisions and audits to digest, actor, time and
safe Reviewer/rule metadata. Application alone performs strict decoding, hard
policy, routing, fresh RBAC/target validation, decision checks, nonce/digest/time/
scope/policy recheck, atomic durable consumption plus pre-operation audit, final
generation check, at most one attempt, outcome and separate verification/audit.

Terminal process-local approval records are removed after returning their final
value. Missing records cannot be claimed or consumed. Application persists and
projects terminal results; durable identity and atomic consumption remain the
replay barrier. Do not retain an unbounded second archive of terminal authority.

Typed mutations remain exact: Deployment restart changes only the Kupilot
annotation; scale binds one Deployment/StatefulSet; rollback binds a fresh prior
ReplicaSet; Pod delete requires one ordinary controller-owned Pod; cordon and
uncordon change one Node field; drain binds one complete bounded Node/Pod/PDB
plan. Drain has no force, delete-emptydir or ignore-daemonset escape hatch.
Every pre-bound mutation has at most one attempt and a durable outcome.

Remote file reads bind Pod UID/container/path, recheck symlinks and deny
credentials, devices and unsafe pseudo-filesystems. Pod Exec and diagnostic Pods
are separately gated/default-off critical capabilities with exact command,
security, destination, output and lifetime constraints. Diagnostic Pod create,
observe, delete and ambiguous cleanup are distinct audited states.

Local execution uses a policy-selected executable and exact argv, fixed working
directory, minimal credential-free environment, no inherited stdin, bounded output
and owned process-group cancellation/join. Shell is a separate default-off critical
operation; restricted argv never admits shell smuggling or fallback.

## Consequences and validation
Pre-operation storage failure means zero executor calls. Cancellation, conflict,
timeout, restart or ambiguous outcome never retries automatically. Acceptance,
unknown outcome and verified completion remain distinct. Tests alter bound
fields independently, inject failures at every barrier, and prove zero attempts
before durable consumption and at most one afterward. Exact operation semantics,
risk and RBAC are in [Operational Capabilities](../diagnostic-capabilities.md),
[Security](../security.md) and [RBAC](../rbac/README.md).
