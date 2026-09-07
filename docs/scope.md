# Kupilot Scope

- Status: Accepted `v0.5` target
- Date: 2026-09-07

The current code includes the named-model and safe Session-context runtime,
broad read/observability, remote-diagnostic adapters, deterministic permission
foundation, typed remediation, and default-off exact local-process slices. The
items below define the complete admitted `v0.5` scope. Deterministic reachability
for one slice is not live-integration or release evidence, and no broader
capability may be inferred from it.

## In scope for `v0.5`

- One local process, one local user, one low-chrome Agent-first TUI, one active
  AgentRun, and one verified Kubernetes Context at a time.
- Application-owned active-run `Enter` steering at the next Eino model
  invocation, a bounded current-process FIFO follow-up queue on active-run
  `Tab`, and empty-composer `Alt+Up` LIFO edit-last.
- One visible working Namespace, one immutable `current` or `all` namespace
  policy, one scope generation, and one policy generation per run.
- A versioned strict capability catalog for reviewed stable built-in resources
  and exact policy-admitted CRDs. No model-selected discovery or GVR.
- Typed `get`, `list`, conversational `describe`, and bounded
  query/count/table projections.
- Related Events; current, previous, and explicit all-container non-following
  logs; bounded local log search; and Pod and Node metrics.
- Explicitly configured optional Prometheus and Loki sources, each with its own
  endpoint, credential, query, data-category, consent, projection, and budget
  policy.
- Bounded container-file reads, predefined no-shell Pod diagnostics,
  separately enabled Pod Exec, and bounded diagnostic Pods.
- Typed restart, scale, rollback, one ordinary controller-owned Pod delete,
  cordon, uncordon, and drain operations.
- Default-off restricted direct argv integrations for exact `kubectl`, `helm`,
  `argocd`, or another policy-selected local executable, plus a separate
  default-off shell risk class.
- Permission profiles `read-only`, `ask`, `auto-review`, `full-access`, and
  `custom`; `ask` is the default.
- A required `agent` model role, an optional `approval_reviewer` role, several
  explicit named OpenAI-compatible profiles and origins, and no fallback or
  routing.
- Standard and minimal Session model-memory modes, explicit resume, direct Eino
  ADK message-state and summarization reuse, bounded safe summary coverage and
  recent tail, and capability-aware finite budgets.
- Informed model-transfer consent, safe local SQLite persistence, retention,
  deletion, redacted export, durable action audit, and local `/status` and
  `/permissions` views.
- Bounded `/sessions` discovery, authoritative Last active, exact current or
  historical deletion, and digest-bound inactive-Session batch deletion through
  the fixed TUI and pre-TUI CLI entry points.
- Content-free model-egress preflight, typed terminal reasons and safe next
  actions, provenance and bidirectional claim/Evidence navigation, fixed Slash
  availability, conservative terminal capabilities, committed-input reverse
  search, semantic scrollback jumps, bounded undo/redo, local redacted doctor,
  reduced motion, and non-color accessibility.
- Strict answer/clarification outcomes, declared source coverage, exact
  freshness/conflict/supersession state, narrow same-run safe-read reuse,
  category-level budget explanation, and a unified no-blind-retry recovery
  matrix.
- macOS and Linux support on `amd64` and `arm64`; Windows remains experimental.

## Capability contract

Every model-visible or locally selected capability is compile-time code-owned,
versioned, strictly decoded, and bound to a task-specific consumer port. It
must define:

1. the operational need and exact operation schema;
2. exact API group, version, resource, Kind, verb, Namespace behavior, and RBAC,
   or exact executable and argv policy;
3. runtime-injected scope, policy, identity, deadlines, and hard ceilings;
4. eligible and prohibited source fields, data categories, sinks, and network
   destinations;
5. projection, normalization, sensitive-value handling, consent, and retention;
6. finite call, time, item, page, sample, line, byte, stream, traversal, output,
   and cost limits;
7. partial, cancellation, timeout, conflict, stale-generation, and ambiguous-
   outcome behavior;
8. deterministic Evidence or verification mapping; and
9. request-recording success and zero-call denial tests.

`describe` is a conversational result over typed projections and fixed
relationships. Query/count/table uses code-defined fields and operators.
Neither is a subprocess or an arbitrary selector/template language.

Lists and queries use safe server-side filtering where the exact API supports
it. Runtime owns continuation tokens and pagination and enforces per-page and
aggregate page, item, byte, and time ceilings. Partial or truncated results are
explicit; a model cannot inject a continuation token or increase a ceiling.

Built-in membership and CRD policy are versioned independently from the model.
An exact CRD policy names group, version, resource, Kind, namespaced or cluster
scope, allowed verbs, allowed projected fields, limits, and Evidence mapping.
No unknown or discovered CRD is available merely because RBAC permits it.

## Scope and policy generations

The working Namespace remains the default target and visible UI context:

- `current` permits namespaced operations only in the working Namespace.
- `all` permits a validated explicit Namespace and an explicit bounded all-
  Namespace read in the same Context when the operation and RBAC allow it.
- Cluster-scoped resources carry no fake Namespace.
- No policy permits another Context in the same AgentRun.

A Context, Namespace, or namespace-policy change advances scope generation
before cancelling old work. A permission profile, capability policy, Session
rule, relevant data-source policy, or origin-policy change advances policy
generation before invalidating old work. Pending reviews, approvals, Session
rules, ActionEnvelopes, and late results from either old generation are unusable.
Pending steer and queued follow-up input is likewise bound to the exact scope,
policy generation, model role and origin, consent, and budget state. A change
invalidates it before cancellation; it is recovered for explicit editing and
is never automatically retargeted.

Both generations are process-local verified-authority versions. They are not
binary, schema, migration, database, or Session versions, and a persisted value
is historic provenance rather than resumable authority. A TUI question binds
the exact current Session, both expected generations, and selected-resource
state. A mismatch returns a typed safe reason and current-state projection with
zero model calls; it never automatically retargets, queues, retries, or sends
the question. A newer independently verified current scope may be shown for an
explicit resubmit. An unverified scope or stale selected ResourceRef uses the
existing bounded picker.

## Permission and risk scope

Risk classes are `safe`, `review`, `critical`, and `deny`; only deterministic
code can classify them.

- `read-only` permits safe reads automatically and human-routed admitted
  sensitive reads, while denying writes, Pod Exec, diagnostic Pods, and local
  processes.
- `ask` is the default and routes `review` and `critical` to the human.
- `auto-review` routes only `review` to the optional Reviewer and keeps
  `critical` human-routed.
- `full-access` automatically routes admitted `review` and `critical` only
  after explicit high-risk selection and policy permission.
- `custom` defines exact routes without creating a capability or lowering its
  risk. `critical` defaults to human and can never be Reviewer-routed.

`deny` cannot be approved. A profile never grants RBAC, changes scope, enables a
default-off operation, bypasses consent/audit/revalidation, or exposes a
credential. Human Session rules are current-process, current-Session, expiring,
revocable, and limited to exact `review` work; they are never resumed.

## Read and observability scope

The P0 read surface includes reviewed built-in workload, batch, networking,
storage, autoscaling, disruption, RBAC-status, and cluster-health projections;
the exact implemented list must be published and tested before reachability.
The catalog also includes the explicit CRD policy described above.

Events and all log modes are bounded and non-following. Log search is local over
the bounded retrieved projection, not a second unbounded query. Pod and Node
metrics have fixed sample and field projections. Prometheus and Loki are
optional explicit data sources, not implicit fallbacks; a missing source leaves
an honest gap.

A Secret may contribute only a safe metadata projection; Secret values,
ServiceAccount tokens, kubeconfig, credentials, raw objects, generic YAML,
managed fields, unrestricted annotations, device data, and unbounded output
remain hard exclusions. An exact ConfigMap key or non-credential container
environment value may be admitted only through a versioned policy, is at least
`review`, and requires data-category consent and sink policy. Generic dumps,
bulk values, and unlisted fields remain denied; redaction does not make them
eligible.

## Execution and remediation scope

The typed operations and their base risk are:

| Operation | Target boundary | Base risk |
| --- | --- | --- |
| Restart | One exact Deployment; only Kupilot's restart annotation | `review` |
| Scale | One exact Deployment or StatefulSet | Positive delta of one is `review`; scale-to-zero or another delta is `critical` |
| Rollback | One exact Deployment and freshly validated prior ReplicaSet revision | `critical` |
| Delete Pod | One exact controller-owned ordinary Pod | `review`; force, grace-zero, bulk, unmanaged, static, and mirror Pods are denied |
| Cordon/uncordon | One exact Node and only `spec.unschedulable` | `review` |
| Drain | One exact Node and a bounded complete eligible Pod target set | `critical`; no force, delete-emptydir, or ignore-daemonset escape hatch |

Container file read is `review`. Predefined read-only Pod diagnostic argv is
`review`; other Pod Exec and diagnostic Pods are `critical` and default off.
Restricted local argv is default off and risk-classified from exact behavior.
It uses a policy-selected executable and argv, fixed validated working
directory, allowlisted minimal environment, and finite process/output lifetime.
Shell is always separate, `critical`, and default off; its own schema binds one
exact bounded command string. The current model proposal is narrower and
selects only its configured policy ID. The local adapter provides no OS
filesystem or network sandbox; exact approval must display that fact. OS
sandboxing would not substitute for Kubernetes scope, RBAC, remote target,
network, data, and audit controls.

Every such operation uses one immutable digest-bound `ActionEnvelope`, current
target and generation revalidation, durable pre-operation audit, at most one
external attempt, fail-closed unknown outcome, and a separate verification
plan. Generic patch/apply/edit/delete/YAML and command fallback remain denied.

## Model and Session scope

All profiles use the one `openai_compatible` protocol kind. Each role binds one
explicit profile and origin. There is no automatic discovery, fallback, router,
load balancing, cross-origin retry, or model-selected endpoint. Credentials are
opaque and consent is role-, origin-, policy-, and category-bound.

Every AgentRun after the first question in a Session must receive one ordered,
bounded representation of all retained eligible prior user and final assistant
messages. Standard mode supplies it in process and after explicit cross-process
resume; minimal mode supplies it only from same-process context and persists no
model memory. Resume itself only loads local eligible history and unverified
candidates; the next explicit question is the earliest possible model transfer.
A failed consent, generation, coverage, or budget gate causes zero model calls,
not a current-question-only fallback.

History never restores operational authority. Eino ADK `ChatModelAgent`,
`Runner`, message state, and summarization middleware are reused directly
inside the sole adapter. The stable implementation path is the existing safe
SQLite messages through a thin ordered bridge until a stable Eino runner-
managed Session passes ADR-0047's adoption gate.

A compatible restart or forward migration preserves eligible Session listing
and explicit resume. Historic Context and Namespace remain candidates only. An
exact match with a scope independently verified in the current process reuses
that current authority; unavailable or conflicting candidates enter the scope
picker. Resume itself remains zero model and operational I/O, and only the next
explicit question can transfer eligible history.

The process-local input queue is not Session memory or resumable authority.
Only a committed steer enters the existing Message history. One completed run
may therefore contain one initial user Message, zero or more ordered committed
steer Messages, and one final assistant Message. Pending, committing, queued,
rejected, and recovered drafts are excluded from replay and export. Unknown
lifecycle metadata is process-local; its committed Message may be retained or
exported, but the incomplete or failed run group is excluded from replay.

Queue admission is fixed at eight items, 65,536 UTF-8 bytes per item, and
262,144 aggregate UTF-8 bytes after safe normalization. Only a clean,
successfully persisted turn may start one FIFO successor. Failure,
cancellation, timeout, stale state, degraded persistence, and unknown outcomes
never auto-send. Eino `TurnLoop`, a second Agent loop, and a second durable
input store remain out of scope.

## Finite budgets

Budget profiles remain immutable for a run and include independent Agent,
Reviewer, summary, capability, Kubernetes, data-source, remote-exec, local-
process, byte, item, line, sample, stream, wall-time, and cost reservations.
Model or Tool output cannot expand them. Exact token windows, request/output
tokens, stream behavior, and summary thresholds are not universal constants;
they require tagged dependency source/tests and selected-endpoint evidence.

## Explicit non-goals

- A primary resource table/tree, dashboard, full YAML view/editor, raw log
  browser, action menu, shell console, or second editor.
- Model-selected arbitrary Kubernetes API, selector, destination, image,
  executable, flag, stdin, runner command, patch, YAML, or unlimited output;
  the exact bounded command field of an explicitly enabled shell operation is
  the only separate exception and remains `critical`.
- Watch, informer, continuous monitoring, background scan, scheduled run,
  controller behavior, or autonomous remediation.
- Cross-cluster diagnosis or simultaneous active AgentRuns.
- Dynamic plugins, MCP, RAG, retrievers, generic framework facades, or
  Multi-Agent orchestration.
- Provider auto-detection, fallback, routing, load balancing, or a Reviewer as
  permission authority.

## Evidence levels

The current scope also includes deterministic claim/Evidence manifest
validation and a synthetic offline quality harness. The harness measures
reference validity, unsupported current-state claims, stale/cross-run
rejection, limitation handling, and response bounds. It exercises local code
only and is not evidence of live model quality.

Plan-only mode, manual compaction, queue mutation, committed transcript search,
clipboard output, and fixed terminal status titles stay within the existing
single-process and single-AgentRun boundary. Images, Web search, external
retrieval, durable drafts/goals, side conversations, plugins, MCP, subagents,
parallel Tool fan-out, runtime answer critics, and generic checkpoints remain
out of scope.

Deterministic CI with scripted models, request-recording Kubernetes fixtures,
local HTTP servers, direct process fixtures, and real temporary SQLite files is
the required correctness and security proof. It uses no real cluster, model,
credential, or public network.

Opt-in tagged live integration may establish compatibility for one exact
Kubernetes/model/data-source/tool version. Model evaluation separately measures
Agent quality and Reviewer decisions, escalation, cost, and latency. Neither
live integration nor evaluation replaces deterministic CI or generalizes to an
untested endpoint or version.

Session metadata listing and `/doctor` are local read-only use cases. Exact
deletion is transactional; inactive batch automation requires a frozen absolute
cutoff and matching schema-1 selection digest. Search state, undo snapshots,
terminal capability state, deletion plans, preflight events, and safe-read
reuse are not new durable stores or resumable authority.

## References

- [Product Contract](product.md)
- [Architecture](architecture.md)
- [Security Threat Model](security.md)
- [Operational and Diagnostic Capabilities](diagnostic-capabilities.md)
- [ADR-0044](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045](adr/0045-admit-controlled-execution-and-remediation.md)
- [ADR-0046](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0048](adr/0048-own-run-steering-and-queued-follow-up-input.md)
- [ADR-0049](adr/0049-bound-tui-observability-planning-compaction-and-evidence-coverage.md)
- [ADR-0050](adr/0050-use-authoritative-session-activity-and-transactional-deletion.md)
- [ADR-0051](adr/0051-use-bounded-tui-navigation-capabilities-and-local-diagnostics.md)
- [ADR-0052](adr/0052-use-typed-agent-outcomes-evidence-integrity-and-preflight.md)
