# Operational and Diagnostic Capabilities

- Status: Accepted `v0.5` target; read, observability, remote-diagnostic,
  local-execution, and typed-remediation slices implemented
- Date: 2026-09-05

The checked-in implementation now includes the broad built-in/CRD resource
read and query slice and the deterministic Events, logs, Metrics API,
Prometheus, and Loki adapters. It also includes the default-off exact Pod Exec,
container-file, and diagnostic-Pod capabilities described below, plus exact
default-off local process policies and the seven typed remediation operations.
These paths have deterministic composition and adapter evidence; they are not
claims of live tool, cluster, RBAC, CNI, or operating-system sandbox validation.

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

The first four rows are implemented through the fixed resource get/list Tool
names: one local `resource_type` selects a frozen exact policy, `detail`
selects summary or normalized describe output, and `format` selects bounded
list, count, or table data. Filters use only policy field IDs, closed operators,
and scalar values. Safe server selectors are built only for exact metadata
field or label mappings; the same predicates are evaluated again over the
allowlisted projection. CRD discovery validates one exact configured API and
cannot grant another one.

Events now support exact target, Namespace, time, reason, and type filters with
runtime-owned pagination, deduplication, series/count normalization, and
explicit partial state. Current and previous Pod logs support one or all
explicit containers, including separately selected init and ephemeral
containers, with bounded literal search. Pod and Node metrics use exact typed
Metrics API reads and normalized integer CPU/memory quantities. Prometheus and
Loki use only configured canonical origins and code-owned query IDs.

The default `ask` composition routes container output and optional external
data-source access to permission review. The S04 remote-diagnostic gate creates
and atomically consumes the same durable `ActionEnvelope` lifecycle for an
automatic full-access/custom route or an eligible current-process Session
rule. Human and Reviewer routes fail closed before remote execution until their
delivery integration is completed; configuring an entry alone never executes
it. Deterministic adapter and Tool tests exercise the authorized path without
claiming live endpoint or cluster integration. The separate shared dispatcher
now owns local-process and typed-remediation decisions; it does not silently
extend authority to the remote-diagnostic gate.

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

The implemented broad-read Secret and ConfigMap Tool paths request
`PartialObjectMetadata` only. ConfigMap data, environment values, credential
references, and every configured field classified `sensitive` remain denied
because the separate sensitive-read permission, category-consent, and sink
path is not part of this slice.

## P0 remote and local diagnostics

| Capability | Base risk | Required boundary |
| --- | --- | --- |
| Container file read | `review` | Exact Pod UID/container/normalized path; one-exec archive-reported component-type checks; deny credentials, ServiceAccount paths, devices, and unsafe pseudo-filesystems; bounded projected output and an explicit filesystem-race residual. |
| Predefined Pod diagnostic | `review` | One exact read-only policy-owned argv through client-go `pods/exec`; `stdin=false`, `tty=false`, `shell=false`; exact Pod UID/container; finite time/output and owned cancellation. |
| Other Pod Exec | `critical`, default off | Exact Pod UID/container/executable/argv and data/network/sink effects; stdin, TTY, and shell default off; explicit enablement and decision; no shell smuggling or inherited credential. |
| Diagnostic Pod | `critical`, default off | Policy-selected pinned image, Namespace, non-root/non-privileged context, read-only root filesystem, no host mounts/network, finite resources/time/output, disabled token automount, exact in-cluster target, and separately audited create/observe/delete/ambiguous-cleanup states. |
| Restricted local argv | Risk from exact behavior; default off | Policy-selected executable and argv, direct launch with `shell=false`, fixed validated working directory, allowlisted minimal environment, no inherited stdin, owned process group, bounded output, cancellation, and join. |
| Shell | `critical`, default off | Separate typed operation binding a policy-selected shell and exact bounded command string; never an argv fallback; only explicit full-access or exact custom critical-auto may omit a per-action prompt. |

The current `pod_exec` implementation resolves one exact Pod UID/resource
version and container. After the permission decision, Application invokes a
fresh target revalidation before durable consumption/pre-audit; the Kubernetes
adapter repeats the exact GET before the sole exec attempt. Before creating
approval authority, Application also proves the normalized plan is an exact
member of the process-frozen remote-diagnostics catalog. A configured
executable and argv must match byte for byte, must contain no credential-shaped
value, and cannot name a direct shell or dispatch one through a recognized
executable multiplexer. It issues one redirect-denying client-go SPDY
`pods/exec` request with stdout and stderr enabled and stdin/TTY/shell disabled.
The adapter owns cancellation, deadline, stream close and join, ordered bounded
project-owned chunks, and bundle shutdown. It performs no WebSocket fallback or
second transport attempt because a failed upgrade can leave command execution
ambiguous. Metacharacters are literal argv bytes, not shell syntax. A general
policy-owned command can still have in-container side effects or network
behavior: its exact argv and the conservative remote-Pod network effect are
approval-visible, but Kupilot does not infer or sandbox the executable's
implementation and the policy never admits credential input or output.

The current container-file reader uses one exact no-shell USTAR invocation with
a one-block record size. The action binds the full archive transport ceiling
and a smaller content ceiling after deterministic framing is reserved. It
passes every normalized parent followed by the final file under a configured
application-data root. The local parser requires those archive headers in that
exact order, requires every parent to be a directory and the final entry to be
a bounded regular file, and rejects links, devices, duplicates, extra entries,
stderr, Secret/ConfigMap/projected/credential mounts, ServiceAccount-token
paths, and unsafe pseudo-filesystems. A sensitive mount at `/`, sensitive
volume device, duplicate volume identity, or unknown mount/device reference
causes the Pod projection to fail closed. This is a cooperative in-container
archive check, not an atomic kernel `openat2` guarantee; a container able to
race its filesystem remains a documented residual risk.

Each current `run_diagnostic_pod` policy entry uses the fixed TCP-connect
contract; a unique local ID selects one exact target. Policy fixes its
Namespace, same-Namespace Service and port, digest-pinned image, and
`/bin/nc -z -v -w 5` prefix. Runtime fixes the generated Pod name,
disabled token automount, default ServiceAccount identity, non-root IDs,
runtime-default seccomp, dropped capabilities, no privilege escalation,
read-only root filesystem, no volumes or host namespaces, disabled Service
links, non-preempting scheduling, zero admitted API-default priority, finite
resources, restart policy, active deadline, and output limits.
Service UID/resource version, non-empty selector, and port are revalidated
before create, and the returned/admitted Pod spec is checked before waiting.
ExternalName, selectorless, obvious metadata/link-local/address-confusion, and
policy-external targets are rejected. Create, wait, log, delete, and cleanup
are distinct, atomically audited states; definite create rejection does not
delete a pre-existing Pod, while ambiguous create/delete paths clean only the
exact invocation-bound target and never retry creation. Bundle shutdown first
cancels the owner, retains its transport for bounded cleanup, joins it, and
only then closes the transport. An enforced matching NetworkPolicy and
compatible CNI remain an operator-provided deployment prerequisite that
Kupilot does not verify.

Restricted `kubectl`, `helm`, and `argocd` integrations are long-tail escape
valves only when exact verbs, flags, files, destinations, output projections,
and verification are policy-owned. Kubectl denies Context, kubeconfig,
credential, token, and impersonation overrides; Helm admits no arbitrary values
file, stdin, or plugin; Argo CD binds an explicit server origin, credential
reference, application, revision, and consent. They never replace a typed
restart, scale, rollback, Pod delete, cordon, uncordon, or drain. An
operating-system sandbox is not equivalent to Kubernetes RBAC, remote Pod
isolation, or NetworkPolicy.

The implemented local runner accepts only a configured policy ID from the
model. Runtime resolves a fixed absolute executable, structurally classified
argv or separately configured shell string, normalized working directory,
minimal `LANG`/`LC_ALL`/`NO_COLOR` environment, filesystem identities, data and
network effects, and finite timeout/line/byte ceilings into the envelope.
Direct argv keeps stdin and TTY closed and treats metacharacters literally;
known interpreters, command multiplexers, wrapper executables, credential or
identity override flags, values-file/plugin surfaces, symlink paths, and script
executables are rejected. The adapter owns one process group, cancellation,
bounded drain/join, and combined ordered output. It provides no filesystem or
network sandbox, so `ask` never automatically approves an unsandboxed arbitrary
command and shell remains a separate default-off `critical` operation.

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
decision, digest, scope/policy generation, target revalidation before durable
consumption/pre-operation audit, final generation and adapter target checks,
and one-attempt execution. Acceptance, failure, ambiguous
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

The diagnosis fixture matrix covers bounded CPU and memory snapshots, service
and network source checks, rollout progress, CrashLoop and OOM signals, Jobs,
and Node pressure. Sufficient and intentionally limited Evidence variants keep
confirmed facts distinct from hypotheses when a source is absent or partial.

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
