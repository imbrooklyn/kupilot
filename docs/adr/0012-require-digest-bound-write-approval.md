# ADR-0012: Require Digest-Bound Approval for Writes

- Status: Accepted
- Date: 2026-08-08

## Context

The admitted `v0.2` Deployment restart crosses a fundamentally different trust
boundary from read-only diagnosis. The Agent may recommend an operation, but
model output cannot express user authorization. A broad confirmation can become
stale, be replayed, apply to a changed target, or hide a database failure before
the external side effect.

Approval must therefore be a runtime state machine with durable evidence and
deterministic failure behavior, not a prompt instruction or a modal boolean.

## Decision

Every KuPilot write, beginning with the sole operation admitted by ADR-0029,
requires a dedicated Application-owned approval coordinator. The coordinator is
absent from the `v0.1` composition.

An ApprovalRequest binds a versioned canonical encoding of:

- Request, Session, and AgentRun identity.
- Fixed operation and policy version.
- Complete immutable ClusterScope and process-local generation.
- Exact target Kind, Namespace, name, UID, Pod-template fingerprint, Deployment
  generation, and operation-specific identity fields.
- Fixed canonical parameters and a human-readable risk summary.
- Creation time and expiry exactly 60 seconds later.

KuPilot computes a versioned operation digest over that encoding. The digest is
an integrity identifier, not a secret. Any field change creates a different
proposal; a proposal is never edited or extended in place. S30 must select and
record the exact digest algorithm and canonical byte representation before the
state machine is implemented; this ADR does not claim they have been verified.

The resource version observed while preparing the proposal may be recorded as
safe observation metadata, but it is not part of the operation digest. Only the
resource version from the mandatory fresh read is used as the write concurrency
precondition.

The Approval Dialog defaults to rejection and displays operation, exact target,
scope, risk, expiry, and digest. A decision returns the request identity, digest
actually displayed, approve or reject choice, local user actor, decision time,
and an Application-issued one-time UI nonce. The TUI cannot mint, validate, or
consume approval state.

Before execution, the coordinator must:

1. Recheck one-time state, request identity, nonce, displayed digest, policy,
   current time, and complete current scope and generation.
2. Re-read the exact target and compare UID, template fingerprint, Deployment
   generation, and canonical parameters. A status-only resource-version change
   does not invalidate approval by itself.
3. Reconstruct the bound intent without the resource version and compare its
   digest.
4. Atomically persist the approval decision, consumed state, and pre-operation
   audit record.
5. Recheck scope and consumed state and invoke the fixed executor at most once
   using the fresh resource version as an optimistic concurrency precondition.

Reject, expiry, cancellation, scope change, process restart, target change,
fingerprint change, digest or nonce mismatch, duplicate decision, storage
failure, or uncertain prior execution makes the request terminal and
non-executable. There is no automatic write retry after process interruption or
an ambiguous transport outcome.

External request acceptance and bounded post-operation verification are stored
and displayed as distinct outcomes. Verification failure cannot rewrite the
historical fact that a request may have been accepted.

## Consequences

Positive consequences:

- User authorization is tied to one visible immutable operation.
- Replay, stale scope, changed target, and category-approval paths fail closed.
- Durable pre-operation state establishes whether KuPilot was authorized before
  an external request.
- Crash recovery cannot silently repeat a write.

Costs and constraints:

- Approval requires a clock, canonical encoder, digest, nonce, durable state
  machine, target re-read, concurrency control, and explicit recovery states.
- A database outage prevents writes even if the user is willing to proceed.
- Sixty seconds may require the user to request a new proposal after reading or
  interruption.
- Request success and rollout verification need separate UI and audit models.

## Alternatives considered

- Prompting the model to ask for confirmation was rejected because model text is
  not an authorization channel.
- A generic yes/no modal was rejected because it does not bind scope, target,
  parameters, time, or policy.
- Persisting audit after the external write was rejected because a crash could
  leave an unaudited side effect and enable accidental retry.
- Retrying on timeout or restart was rejected because an ambiguous request may
  already have changed the cluster.
- Long-lived or reusable approval was rejected because it becomes delegated
  authority rather than one supervised action.

## Security and privacy impact

Approval is enforced in Application and the executor boundary, never in prompt
text or TUI rendering. The model may propose only the admitted operation and
cannot approve, alter the digest, choose the nonce, extend the TTL, or represent
execution as verified.

Approval and write audit contains only allowlisted safe metadata and follows the
180-day default retention contract even under minimal-persistence, unless the
user deletes the owning Session or clears local history earlier. Only a hash of
the UI nonce is durable. Audit contains no raw object, arbitrary patch,
credential, request body, or response body.

## Validation

Before a write executor is reachable, fake-clock, fake-store, and request-
recording tests must:

- Change every bound field independently and prove zero external calls.
- Test immediately before, at, and after the 60-second expiry boundary.
- Replay decisions and nonces, deliver duplicate UI events, and restart from
  every durable state.
- Switch scope before approval, during target re-read, after audit commit, and
  immediately before executor invocation.
- Change target UID and operation fingerprint at deterministic barriers.
- Fail every proposal, decision, consumed-state, pre-operation, request-outcome,
  and verification transaction.
- Prove the executor call count is zero before durable consumed/audit state and
  at most one afterward.
- Keep request acceptance, timeout, unknown outcome, rollout observation, and
  verified completion distinct.

S30 must implement and prove the domain state machine and digest. S31 must prove
persistence, Application, TUI, and fake-executor boundaries. The concrete
Kubernetes mutation API is not claimed verified by this ADR and must pass S32
under ADR-0029 before `v0.2` code is wired; S33 must complete verification and
end-to-end security tests.

## Revisit triggers

- A second write operation is proposed.
- A process boundary, remote user, or multi-user approval flow is introduced.
- The selected digest algorithm or canonical encoding no longer satisfies the
  integrity contract.
- Implementation evidence shows that the target needs a stronger server-side
  precondition while retaining the fail-closed state machine.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0011: Keep `v0.1` Strictly Read-Only](0011-keep-v0.1-strictly-read-only.md)
- [ADR-0029: Limit `v0.2` to Deployment Restart](0029-limit-v0.2-to-deployment-restart.md)
