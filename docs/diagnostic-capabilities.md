# Operational and Diagnostic Capabilities

- Status: Accepted `v0.5` target
- Date: 2026-09-03

The checked-in implementation remains the narrower `v0.4` catalog. This page
defines the P0 capability target and must not be read as a claim that the new
Tools, RBAC, configuration, migrations, or tests already exist.

Kupilot answers operational questions through a versioned, compile-time
catalog of typed, bounded capabilities. Support means the Agent can gather a
safe Evidence path or present a controlled action. It does not guarantee that
every incident has an observable cause or that model interpretation is correct.

## Common contract

Every entry has a strict schema, canonical arguments, immutable scope and
policy generations, deterministic risk, runtime-injected ceilings, a task-
specific consumer-owned port, project-owned projection, sensitive-data and
consent treatment, stable partial/error behavior, deterministic Evidence or
verification mapping, exact RBAC impact, and request-recording tests.

Only deterministic local handling creates Evidence, permission, approval,
attempt, or verification state. User text, Kubernetes text, model prose,
Reviewer rationale, and an action proposal create no authority. Unknown,
malformed, stale, sensitive, denied, and over-budget inputs fail closed.

No capability accepts a Context, kubeconfig, credential, model origin,
arbitrary GVR, raw selector, executable, image, destination, command string,
YAML, stdin, deadline, hard limit, or generic payload from the model.

## P0 read and observability matrix

| Category | Operational use | Required boundary |
| --- | --- | --- |
| Built-in `get`/`list` | Inspect and compare reviewed stable Kubernetes resources. | Exact typed API and project projection; explicit Namespace semantics; no raw object or discovery expansion. |
| CRD `get`/`list`/`describe`/query | Inspect, explain, or compare an organization-approved custom resource. | Exact policy names group, version, resource, Kind, scope, verbs, fields, predicates, limits, and Evidence; absent entry is `deny`. `Describe` and query are local operations over the admitted projection. |
| Conversational `describe` | Explain one resource using typed status, Events, and fixed relationships. | Assembled locally from admitted capabilities; never a kubectl subprocess or YAML dump. |
| Query/count/table | Answer bounded inventory and comparison questions. | Code-defined fields, filters, aggregations, ordering, and limits; no arbitrary selector, JSONPath, template, or `jq`. |
| Events | Correlate recent Events with an exact target or bounded scope. | Fixed selectors, time/count/byte bounds, normalization, and partial state. |
| Current/previous/all-container logs | Inspect explicit bounded non-following container output. | Exact Pod/container set, line/window/byte limits, consent, normalization, sensitive blocking, and no raw persistence. Init or ephemeral containers are excluded unless the exact schema admits them. |
| Log search | Find a bounded pattern in already bounded admitted output. | Local code-defined search; cannot expand source window or become a remote shell/regex denial-of-service path. |
| Pod/Node metrics | Inspect bounded current resource usage and pressure signals. | Typed metrics API, fixed fields/samples, explicit unavailable/stale state, no unbounded time series, and no automatic Metrics Server installation. |
| Prometheus | Query an explicitly configured optional metrics source. | Exact origin, credential, query templates, labels/fields, time range, samples, consent, and budget; no implicit fallback. |
| Loki | Query an explicitly configured optional log source. | Exact origin, credential, query templates, labels/fields, range/line/byte bounds, consent, and budget; no implicit fallback. |

Source allowlisting occurs before projection, normalization, sensitive-value
handling, limits, neutral serialization, and final role/origin/category consent.
Lists and queries use safe server-side filtering where available and
runtime-owned pagination with per-page and aggregate ceilings. Partial and
truncated results are explicit. A Secret may contribute only a safe metadata
projection; Secret values, ServiceAccount tokens, kubeconfig, raw objects,
unrestricted annotations, and unbounded output remain hard exclusions. An
exact ConfigMap key or non-credential container environment value is at least
`review` and requires a versioned policy, category consent, and sink policy.
Generic or bulk values remain denied; redaction alone never authorizes an
unlisted source.

## P0 remote and local diagnostics

| Capability | Base risk | Required boundary |
| --- | --- | --- |
| Container file read | `review` | Exact Pod UID/container/normalized path; rechecked in-container symlink resolution; deny credentials, ServiceAccount paths, devices, and unsafe pseudo-filesystems; bounded projected output. |
| Predefined Pod diagnostic | `review` | One exact read-only policy-owned argv through client-go `pods/exec`; `stdin=false`, `tty=false`, `shell=false`; exact Pod UID/container; finite time/output and owned cancellation. |
| Other Pod Exec | `critical`, default off | Exact Pod UID/container/executable/argv and data/network/sink effects; stdin, TTY, and shell default off; explicit enablement and decision; no shell smuggling or inherited credential. |
| Diagnostic Pod | `critical`, default off | Policy-selected pinned image, Namespace, non-root/non-privileged context, read-only root filesystem, no host mounts/network, finite resources/time/output, disabled token automount, exact in-cluster target, and separately audited create/observe/delete/ambiguous-cleanup states. |
| Restricted local argv | Risk from exact behavior; default off | Policy-selected executable and argv, direct launch with `shell=false`, fixed validated working directory, allowlisted minimal environment, no inherited stdin, owned process group, bounded output, cancellation, and join. |
| Shell | `critical`, default off | Separate typed operation binding a policy-selected shell and exact bounded command string; never an argv fallback; only explicit full-access or exact custom critical-auto may omit a per-action prompt. |

Restricted `kubectl`, `helm`, and `argocd` integrations are long-tail escape
valves only when exact verbs, flags, files, destinations, output projections,
and verification are policy-owned. Kubectl denies Context, kubeconfig,
credential, token, and impersonation overrides; Helm admits no arbitrary values
file, stdin, or plugin; Argo CD binds an explicit server origin, credential
reference, application, revision, and consent. They never replace a typed
restart, scale, rollback, Pod delete, cordon, uncordon, or drain. An
operating-system sandbox is not equivalent to Kubernetes RBAC, remote Pod
isolation, or NetworkPolicy.

## P0 typed remediation

| Operation | Exact semantic boundary | Risk and verification |
| --- | --- | --- |
| Restart | One exact `apps/v1` Deployment; change only the Kupilot-owned Pod-template annotation. | `review`; bind UID, template fingerprint, generation, resource version; verify rollout separately. |
| Scale | One exact Deployment or StatefulSet and an exact replica target. | Positive delta of one is `review`; scale-to-zero or another delta is `critical`; verify desired and observed state. |
| Rollback | One exact Deployment and one freshly validated prior ReplicaSet revision. | `critical`; bind revision/fingerprints; verify rollout separately. |
| Delete Pod | One exact ordinary controller-owned Pod. | `review`; force, grace-zero, bulk, unmanaged, static, mirror, or ambiguous ownership is denied; verify replacement/state separately. |
| Cordon | One exact Node; set only `spec.unschedulable=true`. | `review`; bind UID/resource version and verify the exact field. |
| Uncordon | One exact Node; set only `spec.unschedulable=false`. | `review`; bind UID/resource version and verify the exact field. |
| Drain | One exact Node plus a bounded fully materialized eligible Pod set. | `critical`; bind Node/Pod identities, PDB/eviction plan, and exclusions; no force, delete-emptydir, or ignore-daemonset escape hatch. Each pre-bound side effect is attempted and audited at most once. |

Every sensitive or effectful operation first becomes an immutable versioned
`ActionEnvelope`. Application alone performs policy, permission, fresh target,
decision, digest, scope/policy generation, durable pre-operation audit, final
revalidation, and one-attempt execution. Acceptance, failure, ambiguous
outcome, progress, cleanup, timeout, and verified completion are distinct.
There is no automatic execution retry.

Generic patch/apply/edit/delete, arbitrary YAML, taint/label/annotate/run,
wildcard commands, model-selected image/network/executable/flag, cluster-admin,
Watch/informers, background/scheduled work, and autonomous remediation remain
denied.

## Permission routing

`ask` is the default. `read-only` cannot be escalated into mutation, Pod Exec,
diagnostic Pod, or local process. `auto-review` delegates only `review` to an
optional strict Reviewer and keeps `critical` human-routed. `full-access` and
exact custom critical-auto rules require explicit high-risk selection and never
enable a default-off capability or bypass scope, RBAC, consent, durable audit,
revalidation, finite budgets, or hard denial.

Human Session rules are narrow, expiring, revocable, limited to `review`, and
valid only in the current process and Session. They are never created by a
model or Reviewer and never restored by resume.

## Finite budgets

Each capability has independent call, item, page, sample, line, byte, result,
stream, deadline, and repetition ceilings. Run profiles also reserve finite
Agent, Reviewer, summary, Kubernetes, optional data-source, remote-exec, local-
process, wall-time, and cost budgets before I/O. Exact model token and stream
values require pinned dependency and endpoint evidence; no unresolved path is
unlimited.

## Evidence levels

Deterministic CI uses scripted models, request-recording Kubernetes and HTTP
fixtures, direct child-process fixtures, fake clocks/barriers, and real
temporary SQLite files. It proves exact success and zero-call denial behavior
without a real cluster, model, credential, or public network.

Opt-in tagged live integration may prove compatibility for one exact cluster,
endpoint, data source, or executable version. Model evaluation separately
measures diagnostic quality and Reviewer decisions, false approval/denial,
escalation, latency, and cost. Neither replaces deterministic CI or generalizes
to an untested version.

## References

- [Product Contract](product.md)
- [Scope](scope.md)
- [Kubernetes Compatibility](kubernetes-compatibility.md)
- [Least-Privilege RBAC](rbac/README.md)
- [ADR-0044](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045](adr/0045-admit-controlled-execution-and-remediation.md)
