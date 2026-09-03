# ADR-0045: Admit Controlled Execution and Remediation

- Status: Accepted
- Date: 2026-09-03
- Supersedes: ADR-0029's restart-only product boundary
- Amends: ADR-0012, ADR-0025, ADR-0037, and ADR-0040

## Context

ADR-0012 established a strong digest-bound approval transaction, and ADR-0029
selected one Deployment restart as the first concrete write. Daily operations
also need narrowly typed scale, rollback, Pod replacement, Node scheduling,
drain, remote diagnostics, and a last-resort restricted local argv path.

Those operations have different targets, data flows, failure ambiguity, and
verification semantics. Copying the restart-specific approval fields into each
path would fragment authority; exposing a generic patch, YAML, shell, or
executor would give untrusted output too much power. `v0.5` therefore retains
the transaction invariants and replaces the restart-only intent with one
versioned project-owned envelope and operation-specific schemas.

This ADR is an accepted implementation target, not evidence that the current
binary or RBAC manifests expose these operations.

## Decision

### ActionEnvelope

Every sensitive read, network access, remote or local execution, or cluster
mutation is canonicalized as an immutable `ActionEnvelope` before review. Its
versioned fields include:

- envelope and operation schema versions, operation, policy version,
  permission profile, deterministic risk class, and policy generation;
- Session, run, and request identity;
- verified Context identity, working Namespace, namespace policy, and scope
  generation;
- exact target API version, Kind, Namespace, name, UID, resource version, and
  operation-specific fingerprint, generation, revision, or target set;
- either typed parameters or one policy-selected executable plus argv, never a
  generic payload;
- explicit stdin, TTY, and shell booleans;
- data categories, allowed sinks, and exact network destinations;
- timeout and line, item, byte, and output ceilings;
- request and expiry times; and
- a code-defined verification-plan identifier.

The canonical encoding has a version, fixed field order, length-prefixed bytes,
and SHA-256 digest. JSON key order, maps, human summaries, model prose, raw
YAML, and unnormalized command strings are not authority inputs. A decision or
audit record separately binds the envelope digest, actor type, decision, time,
applicable Reviewer profile and canonical-origin hash, and a bounded safe
rationale. Reviewer response bytes are never authority.

Human approve-once remains default reject, single-use, and valid for exactly 60
seconds from proposal creation. A matching human Session rule may route only a
`review` envelope under ADR-0044. `critical` work requires an exact human
decision under `ask` and `auto-review`; only explicitly selected `full-access`
or an exact `custom` critical-auto rule may omit that per-action prompt.

### Decision and execution order

Application is the only owner allowed to reach an executor. It follows this
order without bypass:

1. strict catalog and operation-schema decoding and canonicalization;
2. local capability policy and hard-deny evaluation;
3. permission-profile routing;
4. fresh RBAC/target revalidation or local executable-policy validation;
5. exact human approval, permitted Reviewer decision, or matching Session rule;
6. nonce, digest, time, profile, policy, scope, generation, and target recheck;
7. atomic single-use consumption and durable pre-operation audit;
8. final scope and policy-generation check;
9. at most one external execution attempt;
10. accepted, failed, or ambiguous/unknown outcome classification; and
11. separate bounded verification and post-operation audit.

A pre-operation persistence or audit failure yields zero executor calls. Once
an attempt might have reached an external system, cancellation, timeout,
transport loss, conflict, cleanup uncertainty, or process interruption never
causes an automatic execution retry. A new attempt requires a fresh envelope.
Request acceptance, progress, verified completion, failure, timeout,
verification unavailable, and ambiguous outcome remain distinct states.

### Typed remediation catalog

The P0 operation schemas are:

| Operation | Exact boundary | Risk rule and verification |
| --- | --- | --- |
| Restart | One exact `apps/v1` Deployment; only the Kupilot-owned Pod-template annotation changes. | `review`; verify target generation and rollout separately. |
| Scale | One exact Deployment or StatefulSet with an exact replica target. | A positive delta of one is `review`; scale-to-zero and other deltas are `critical`; verify observed desired state. |
| Rollback | One exact Deployment to one freshly validated prior ReplicaSet revision. | `critical`; bind revision and fingerprints; verify rollout separately. |
| Delete Pod | One exact ordinary, controller-owned Pod after fresh owner and safety validation. | `review`; force, grace-zero, bulk, unmanaged, static, mirror, or ambiguous ownership is denied. |
| Cordon | One exact Node; set only `spec.unschedulable=true`. | `review`; bind UID/resource version and verify the exact field. |
| Uncordon | One exact Node; set only `spec.unschedulable=false`. | `review`; bind UID/resource version and verify the exact field. |
| Drain | One exact Node and a bounded fully materialized eligible Pod target set. | `critical`; bind Node and Pod identities, PDB/eviction plan, and exclusions; no force, delete-emptydir, or ignore-daemonset escape hatch. |

Restart may reuse the existing pure domain state transitions, default rejection,
single-use 60-second authority, nonce-hash storage, length-prefixed digest
pattern, Application-only coordinator, scope-generation checks, target
fingerprint revalidation, atomic consume plus pre-audit, one-attempt handling,
and separate verification.

The restart-only operation enum, policy/schema constants, Deployment-specific
intent fields and digest order, preparer/revalidator/executor interfaces,
SQLite constraints, TUI copy, PATCH-only outcomes, and rollout-only DTOs must
be replaced or split. Released migrations remain unchanged; a future
forward-only checksummed migration must use explicit typed columns rather than
a generic JSON payload.

A composite drain envelope authorizes only one execution of its fixed ordered
plan. Each pre-bound mutation in that plan may be attempted at most once and
has its own durable attempt outcome; a changed target set or retry requires a
fresh envelope. Verification reads remain separate from those mutations.

### Remote diagnostics, local argv, and shell

Container file reads bind an exact Pod UID, container, and normalized path,
recheck in-container symlink resolution, and deny credential paths,
ServiceAccount material, devices, and unsafe pseudo-filesystems. A predefined
read-only Pod diagnostic argv is a separate client-go `pods/exec` operation
with `stdin=false`, `tty=false`, and `shell=false`. Other Pod Exec is
`critical`, default off, binds the exact Pod UID, container, executable, and
argv, and keeps stdin, TTY, and shell disabled unless a separately admitted
operation says otherwise. Environment, network, time, output, cancellation,
and sink policy remain explicit and bounded.

A diagnostic Pod is `critical` and default off. Policy selects a pinned image,
non-root and non-privileged security context, read-only root filesystem, no
host mounts or host network, finite resources and lifetime, disabled
ServiceAccount-token automount, exact Namespace, bounded command and in-cluster
destination, and cleanup plan. Create, observation, deletion, and an ambiguous
cleanup outcome are audited separately. An image allowlist is not a network
sandbox; NetworkPolicy and actual CNI enforcement are independent evidence.

A restricted local runner directly launches one policy-selected executable and
argv with `shell=false`, a fixed validated working directory, an allowlisted
minimal environment that omits every model and Kubernetes credential, no
inherited stdin, bounded output, an owned process group, cancellation, timeout,
and a bounded join. `kubectl` policy denies kubeconfig, Context, credential,
token, and impersonation overrides. Helm admits no arbitrary values file,
stdin, or plugin; Argo CD binds an explicit configured server origin,
credential reference, application, revision, and consent. These are named
integrations only when their exact verbs, flags, files, destinations, output
projections, risk, and verification are policy-defined, and never a fallback
for typed P0 operations.

Shell is a distinct `critical` operation. The restricted argv runner rejects
attempts to smuggle a shell through `-c`, a command string, wrapper, script
file, environment value, stdin, Helm values, Argo plugin, or kubectl argument.
An admitted shell envelope instead names the policy-selected shell executable
and binds the exact bounded command string as an explicit typed parameter. It
is default off and does not become safe merely because the host process has an
operating-system sandbox.

### RBAC

Documentation and fixtures must split optional permissions by capability. CRD
reads name exact resources and verbs. Logs use `pods/log get`; Pod Exec uses
`pods/exec create`; diagnostic Pods use bounded Pod create/get/delete;
scale uses the exact `scale` subresource where selected; eviction and Node
patch permissions are separated for drain and scheduling. No baseline or
shared fixture grants wildcard verbs/resources, Secret-value access,
cluster-admin, or a catch-all write. An optional Secret-metadata rule must be a
dedicated exact capability fixture and cannot make Secret values eligible.
Current `v0.4` manifests must not be silently broadened before the matching
runtime and tests exist.

## Consequences

One authority model can cover both cluster and local side effects while still
preserving operation-specific safety and verification. Users see why a request
is `review`, `critical`, or denied and can distinguish external acceptance from
verified completion.

Every new operation requires concrete schemas, adapters, audit mappings,
fixtures, and recovery tests. Some useful but ambiguous shortcuts remain
unavailable. A database outage denies execution, and an unknown outcome may
require independent observation before another proposal.

## Security and privacy impact

The envelope records only bounded safe metadata. It never stores credentials,
raw objects, raw process output, raw logs, arbitrary command strings, model
responses, or generic executor payloads. Operational output may be sent only to
an envelope-declared sink after the applicable consent, normalization,
redaction/blocking, and hard bounds.

Neither Agent, model, Reviewer, Tool, CLI, nor TUI receives an executor. The
deterministic Application policy is the authority. RBAC is necessary but cannot
replace field-level semantics, target freshness, audit, one-attempt behavior,
or verification.

## Validation

For each operation, deterministic tests must alter every digest field, target
identity, policy value, generation, time boundary, parameter, and precondition
independently. They must prove zero calls on denial and at most one external
attempt after durable consumption. Request-recording fixtures must assert exact
wire verbs, subresources, bodies, resource versions, argv, environments,
destinations, process termination, and output ceilings.

Tests must inject cancellation, timeout, conflict, process interruption,
pre-audit failure, result-audit failure, and ambiguous transport outcomes at
every boundary and prove there is no automatic retry. Verification tests must
keep acceptance, progress, completion, failure, timeout, unavailable, cleanup,
and unknown states separate. Forward-only migration tests must upgrade an old
fixture and preserve released checksums.

## Revisit triggers

- A new typed write, remote execution form, executable, destination, or shell
  capability is proposed.
- A target cannot provide a fresh identity or concurrency precondition strong
  enough for its semantic operation.
- An operation requires more than one external attempt and cannot preserve
  explicit per-attempt authority and ambiguous-outcome safety.
- A platform cannot provide bounded child-process cancellation and joining.
- A durable rule, remote approval, multi-user actor, controller, or autonomous
  remediation path is proposed.

## References

- [ADR-0012: Require Digest-Bound Approval for Writes](0012-require-digest-bound-write-approval.md)
- [ADR-0025: Enforce Data Retention and User Deletion](0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0029: Limit `v0.2` to Deployment Restart](0029-limit-v0.2-to-deployment-restart.md)
- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [Security Threat Model](../security.md)
- [Data Retention Contract](../data-retention.md)
- [Least-Privilege RBAC](../rbac/README.md)
