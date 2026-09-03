# ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles

- Status: Accepted
- Date: 2026-09-03
- Amends: ADR-0037, ADR-0039, and ADR-0040

## Context

The `v0.4` catalog proved strict schemas, scope isolation, bounded projection,
deterministic Evidence, and supervised writes. Its exact seven read Tools,
sixteen direct built-in Kinds, small global output limits, and restart-only
interaction do not cover the daily investigation and recovery work expected of
a Kubernetes operations Agent.

Removing the catalog boundary would be worse: model-selected APIs, commands,
or unbounded output would turn untrusted text into authority. Kupilot therefore
needs a larger but still versioned P0 catalog and an explicit permission model
that separates technical capability, risk classification, and the party that
reviews a request.

This ADR defines the accepted `v0.5` target. It does not claim that the current
binary, configuration schema, RBAC fixtures, or tests implement it.

## Decision

### P0 capability matrix

Kupilot remains Agent-first and code-owned. Every entry has a versioned strict
schema, a deterministic local policy decision, a project-owned bounded result,
and an exact RBAC and test contract. The P0 categories are:

| Category | Admitted target | Default risk or policy |
| --- | --- | --- |
| Built-in resources | Typed `get`, `list`, conversational `describe`, and bounded query/count/table projections for reviewed stable built-in APIs. | `safe` unless a selected field, sink, or scope makes the request sensitive. |
| Custom resources | Typed `get`, `list`, conversational `describe`, and bounded query/count/table projections for exact CRDs explicitly admitted by policy, with fixed group, version, resource, Kind, scope, verbs, fields, limits, and Evidence mapping. | No discovery-driven model authority; absent policy is `deny`. |
| Events and logs | Related Events; current, previous, and explicit all-container non-following logs; bounded local log search. | Container output remains consented and sensitive; follow and unbounded output are denied. |
| Metrics and observability | Pod and Node metrics plus explicitly configured optional Prometheus and Loki data sources. | Each destination, credential, query shape, data category, and budget is separately allowlisted. No implicit source selection. |
| Container files | Bounded read of an exact admitted path in one container. | `review`; path, symlink, pseudo-filesystem, device, credential, size, and sink rules are fail-closed. |
| Pod diagnostics | A predefined read-only argv in one exact container, or a bounded diagnostic Pod using a pinned policy-selected image and target. | Predefined Pod diagnostics are `review`; other Pod Exec and diagnostic Pods are `critical` and disabled until explicitly enabled. |
| Typed remediation | Restart, scale, rollback, delete one controller-owned ordinary Pod, cordon, uncordon, and drain, each with its own exact target and verification plan. | Operation and normalized parameters determine `review` or `critical`; generic patch/apply/edit/delete is denied. |
| Restricted local tools | Policy-selected direct argv for bounded `kubectl`, `helm`, `argocd`, or another exact local executable when no typed operation fits. | Default off; direct argv with `shell=false`; read-only forms may be `review`, state-changing forms are `critical`. |
| Shell | A separately named risk class, never an implicit fallback from argv execution. | `critical`, default off, and available without a per-action prompt only under explicit `full-access` or an exact `custom` critical-auto rule. |

The catalog does not admit model-selected GVRs, executables, images, network
destinations, flags, selectors, YAML, stdin, or hard limits. It does not admit
Watch, informers, background scans, scheduled runs, autonomous remediation,
cluster-admin, wildcard RBAC, cross-Context work, generic patch/apply/edit,
port forwarding, plugin execution, or an MCP authority boundary.

`describe` is assembled from typed reads and fixed relationships; it is not a
`kubectl describe` subprocess. Query/count/table forms use code-defined fields
and operators, not arbitrary selectors, JSONPath, templates, `jq`, or scripts.
Typed operations remain preferred over any long-tail local runner.

Lists and queries use safe server-side filtering when the exact API supports
it, then runtime-owned continuation and pagination. Per-page and aggregate
item, byte, page, and time ceilings apply, and every incomplete result carries
explicit partial or truncation metadata. A model cannot provide a continuation
token or expand a page or aggregate ceiling.

Sensitive Kubernetes sources are not one undifferentiated prohibition. A
Secret may contribute only an exact safe metadata projection; Secret values,
ServiceAccount token material, and credential-shaped values remain hard
denials. An exact ConfigMap key or non-credential container environment value
may be admitted only by a versioned policy, is at least `review`, and requires
its own data-category consent and sink policy. Generic dumps, full-object YAML,
bulk values, and unlisted fields are denied.

All-container log requests first materialize one bounded container set and
apply both per-container and aggregate ceilings; init and ephemeral containers
remain excluded unless the exact schema admits them. Pod/Node metrics expose an
explicit unavailable or stale state and never install Metrics Server. Optional
Prometheus and Loki operations use policy-owned query templates rather than
arbitrary PromQL or LogQL.

### Risk classes and permission profiles

The deterministic policy engine classifies the normalized operation and its
data, sink, network, and side-effect characteristics as:

- `safe`: bounded, projected, non-sensitive, side-effect-free work;
- `review`: sensitive reads or egress, fixed no-shell diagnostics, fixed
  read-only local argv, or narrow recoverable typed side effects;
- `critical`: remote arbitrary argv, diagnostic Pods, rollback, drain, shell,
  high-impact scale, or another explicitly admitted high-impact operation; or
- `deny`: anything outside the catalog, scope, RBAC, policy, data, command, or
  hard-limit boundary.

Risk is code-defined. Model prose, Reviewer rationale, Kubernetes RBAC, and a
permission profile cannot lower it or turn `deny` into an admitted action.

| Profile | `safe` | `review` | `critical` | `deny` |
| --- | --- | --- | --- | --- |
| `read-only` | Automatic | Human only for admitted sensitive reads | Denied | Denied |
| `ask` | Automatic | Human | Human | Denied |
| `auto-review` | Automatic | Optional Reviewer may `approve`, `deny`, or `escalate_to_user` | Human | Denied |
| `full-access` | Automatic | Automatic | Automatic | Denied |
| `custom` | Explicit automatic, human, or deny rule | Explicit automatic, human, Reviewer, or deny rule | Exact operation may be automatic, human, or deny; human is the default | Denied |

`ask` is the default. `full-access` must be selected explicitly and permitted by
local or organizational policy. It skips prompts only for already admitted and
enabled actions; it does not grant RBAC, expand scope, enable a default-off
capability, bypass consent, expose credentials, suppress audit or revalidation,
or weaken a hard denial. A `custom` critical-auto rule is equivalent high-risk
authorization and must be equally explicit.

`read-only` cannot be escalated through an approval into a mutation, Pod Exec,
diagnostic Pod, or local process. A technical sandbox, Kubernetes RBAC, the
permission profile, and the selected reviewer are distinct layers.

### Session rules and generations

A human may create a narrow, revocable Session rule only for `review` work. It
binds a versioned operation, exact scope, target pattern, normalized parameter
or argv template, data/sink/network effects, ceilings, and expiry. It exists
only in the current process and Session, is never resumed, and cannot approve
`critical` or override `deny`. Neither a model nor a Reviewer can create it.

Kupilot adds `policy_generation` beside scope generation. A change to the
profile, catalog policy, Session rules, relevant data-source policy, or origin
policy invalidates pending proposals, reviews, approvals, and rules before old
work is cancelled. External work checks both applicable generations before
I/O, after return, at Application event acceptance, and immediately before an
execution attempt.

### Finite capability-aware budgets and TUI

Budgets remain immutable, local, visible, and finite, but `v0.5` no longer
freezes one small global token or output ceiling for unrelated capabilities.
Profiles reserve independent model-role, summary, Tool, query, log, metrics,
remote-exec, local-process, item, byte, line, sample, wall-time, and estimated
cost budgets. Each capability also has its own hard ceiling and cancellation
path. Exact token, context-window, request, stream, latency, and cost values may
be set only from pinned dependency and selected-endpoint evidence.

The TUI remains one low-chrome conversational Agent screen. Permission profile,
risk, reviewer identity, pending review, exact approval, execution, ambiguous
outcome, and verification state are P0 interactions. `/permissions` and
`/status` are local, bounded views; neither performs operational I/O. No
resource browser, action dashboard, shell console, or second editor is added.

## Consequences

Kupilot can cover common investigation and supervised recovery without making
arbitrary Kubernetes or process access the normal Agent contract. Operators can
choose a review posture independently from RBAC and capability enablement.

The catalog, configuration, RBAC, privacy, persistence, TUI, evaluation, and
test matrices become larger. Optional high-risk features must remain visibly
off until their exact policy and platform evidence are available. A nominal
permission from the API server or operating system is never enough to admit a
model-facing capability.

## Security and privacy impact

Broader reads, logs, metrics, files, remote execution, local processes, and
optional observability endpoints create new data and side-effect boundaries.
Each source must use allowlisting before projection, normalization, sensitive
value handling, hard bounds, neutral serialization, and final consent and
origin checks. Credentials, Secret values, ServiceAccount tokens, kubeconfig,
raw objects, raw process environments, and unbounded output remain prohibited
from model, terminal, history, logs, audit, SQLite, and child environments.

Codex permission mechanisms inform the separation between technical access and
review routing; they do not make an operating-system sandbox equivalent to
Kubernetes RBAC or remote Pod Exec. HolmesGPT informs the capability inventory
and path/image/target/argv controls; Kupilot does not adopt its branding, MCP
topology, discovery behavior, or automatic remediation policy.

## Validation

Before any P0 capability is reachable, deterministic tests must cover success,
denial, cancellation, timeout, one-over limits, stale scope and policy,
sensitive data, and exact zero-call paths. Kubernetes fixtures must record the
exact verb, group, version, resource, Namespace, subresource, selector, body,
precondition, projection, and RBAC expectation. Process fixtures must record
the exact executable, argv, environment, stdin, TTY, shell flag, timeout,
output bounds, cancellation, and child join.

The profile-by-risk-by-reviewer matrix, Session-rule matching, generation
invalidation, full-access disclosure, `/permissions`, and `/status` require
deterministic state-machine tests. Tagged live integration and model evaluation
may add compatibility and quality evidence but never replace deterministic CI.

## Revisit triggers

- A capability needs a new source, verb, Kind, subresource, destination, sink,
  data category, execution primitive, or risk class.
- A permission profile would grant capability, RBAC, scope, or consent rather
  than only route an already admitted decision.
- A platform sandbox can be proved strong enough to change a local runner's
  risk classification.
- Endpoint and dependency evidence supports different finite budget values.
- Product evaluation proposes a dashboard, controller, background automation,
  plugin, MCP, or multi-Agent topology.

## References

- [ADR-0037: Adopt an Operational Capability Catalog](0037-adopt-an-operational-capability-catalog.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0040: Use a Codex-Style Conversational TUI](0040-use-a-codex-style-conversational-tui.md)
- [Product Contract](../product.md)
- [Scope](../scope.md)
- [Security Threat Model](../security.md)
- [OpenAI permission modes](https://learn.chatgpt.com/docs/permission-modes)
- [OpenAI auto-review](https://learn.chatgpt.com/docs/sandboxing/auto-review)
- [HolmesGPT Kubernetes toolset](https://holmesgpt.dev/latest/data-sources/builtin-toolsets/kubernetes/)
- [HolmesGPT remediation toolset](https://holmesgpt.dev/latest/data-sources/builtin-toolsets/kubernetes-remediation-mcp/)
