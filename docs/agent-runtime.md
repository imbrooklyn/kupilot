# Agent Runtime

Kupilot runs one supervised AgentRun at a time. Stable Eino ADK
`ChatModelAgent`, `Runner`, message state, Tool-message pairing, and
summarization middleware are confined to `internal/agent/einoadapter`; the rest
of the system sees project-owned Session context, run inputs, events, bound
capabilities, Evidence, ActionEnvelopes, outcomes, and safe errors.

This document distinguishes the accepted `v0.5` target from current
reachability. The checked-in runtime now uses the stable Eino ADK path, named
model profiles, role-bound consent, safe Session context and summarization,
separate Agent, Agent-summary, and Reviewer budgets, broad policy-bound
Kubernetes resource reads, and deterministic observability adapters. It also
contains the deterministic permission/action foundation and Reviewer routing used by the existing
supervised Deployment restart. Public permission controls and new execution or
remediation paths remain targets. Review-class logs and optional sources fail
closed before I/O in the default `ask` composition until that permission
delivery path can supply their ActionEnvelopes.

The currently implemented protocol versions are:

- System prompt: `kupilot-agent-policy-v9`
- Capability catalog: `kupilot-operational-tools-v4`

## Frozen run input

Application currently creates an immutable RunInput containing:

- run, Session, and user-message identifiers;
- normalized user question;
- verified Context, working Namespace, namespace-access policy, scope
  generation, and activation time;
- optional selected ResourceRef;
- exact prompt and capability-catalog versions, the exact resource-policy
  catalog, and its policy generation;
- eligible ordered same-Session context, safe summary, and content-free
  coverage metadata; and
- finite Agent and Agent-summary call, time, byte, stream, Tool, resource page,
  item, response-byte, aggregate-byte, and cost limits.

The composition root separately binds the exact Agent profile and optional
Reviewer profile. Application checks the current Agent role, canonical origin,
category consent, and safe-context eligibility before creating a run. Reviewer
transport and its independent consent and budget are connected only through
Application's deterministic `review` route. A Reviewer recommendation is never
permission or execution authority by itself.

The current ActionEnvelope freezes policy generation, permission profile,
capability/risk policy, exact action limits, and any matched process-local
Session rule. Those values remain deliberately separate from Agent RunInput and
are not reconstructed from model-visible context.

The model cannot supply or modify these values. A Context, working-Namespace,
or namespace-policy change invalidates scope generation. A permission,
capability, Session-rule, relevant data-source, or origin-policy change
invalidates policy generation. Both changes first clear dependent review,
approval, and action state, then cancel old work and reject late results.

## Turn lifecycle

1. Application durably creates the run before model or Kubernetes I/O.
2. Runtime atomically reserves one Agent step and one model call.
3. Application selects eligible safe committed Session context. The adapter
   gives the trusted policy, ordered context, current question exactly once,
   current scope metadata, versioned capability catalog, and remaining code-
   owned ceilings to Eino ADK `Runner`.
4. `ChatModelAgent` and `Runner` own the in-run conversation and ReAct
   iteration. The Eino boundary drains one bounded model stream and asks Eino
   to assemble exactly one assistant message. After each Eino-decoded content
   chunk passes
   stream validation, a passive projector may decode the first top-level
   `answer_markdown` string and publish only its normalized, sensitive-filtered,
   scope-current provisional text. The projector does not interpret response
   modality or Tool calls. Commentary accompanying a Tool selection is
   discarded and cannot authorize a Tool.
5. Complete indexed Tool calls are strictly decoded and bound as one atomic
   batch. An admitted batch is canonicalized, budget-reserved, scope-injected,
   and dispatched through the fixed table. If every call is known and
   structurally safe but strict semantic binding denies any call, the whole
   batch instead receives fixed local policy feedback and performs no Tool
   handler or Kubernetes I/O.
6. The handler performs bounded typed I/O, projects and sanitizes locally, and
   creates Evidence only after the post-I/O scope gate.
7. Tool results return through a project-owned envelope and the ADK loop
   continues until a final structured answer or a terminal policy outcome.
8. The final answer is validated, persisted according to privacy mode, and
   atomically replaces any provisional transcript text. Structured same-Kind
   inventories with shared attributes default to one compact Markdown table per
   Kind without requiring the user to request formatting. When needed, Eino
   summarization middleware compacts eligible history through a separate
   Agent-profile summary budget and project-owned coverage finalizer.

Runtime performs no automatic model, Reviewer, Kubernetes, data-source, remote-
exec, or local-process retry. Local policy feedback
is not an I/O retry because the rejected batch never reached a handler. A model
may make a new corrected selection as another bounded Agent decision; model,
step, and consecutive no-progress budgets still apply and prevent an
indefinite correction loop.

## Capability binding

The current `kupilot-operational-tools-v4` catalog contains eleven fixed
diagnostic Tools. Resource get/list now select only a local `resource_type` ID
from the frozen built-in and exact configured CRD catalog. They support exact
get, bounded list/count/table projections, normalized describe detail, typed
field predicates, runtime-owned server selectors, and runtime-owned
continuation. Events add typed filters and pagination; current/previous Pod logs
add explicit all-container and literal-search modes; Pod/Node metrics use the
typed Metrics API; and optional Prometheus/Loki Tools select only enabled
code-owned query IDs. Related resources and cluster overview retain their
narrower typed behavior. Container files, Pod diagnostics, additional typed
remediation, restricted local argv, and the separate shell risk class remain
later slices.
Every current model schema is strict: all object properties are
declared, every property is required, optional values use explicit `null`, and
additional properties are rejected.

Binding performs these checks before handler I/O:

- known name and exact catalog version;
- one complete JSON object with no duplicate, unknown, wrong-type, or overlong
  field;
- code-defined built-in or exact policy-admitted API, operation, and risk;
- explicit namespace semantics under the frozen policy;
- no Context, endpoint, credential, raw selector, arbitrary GVR, executable,
  image, network destination, command string, YAML, deadline, or hard-limit
  authority;
- canonical argument serialization and digest; and
- atomic run and per-capability budget reservation.

Malformed structured output, Tool-like prose, and unknown names cause zero
handler calls.

A complete selection with a known Tool name, bounded non-duplicated JSON
object, and no runtime-authority field may still fail semantic binding, for
example because a cluster-scoped Kind carries a Namespace, a requested
Namespace is outside the frozen policy, or a requested value exceeds a fixed
capability limit. The entire batch then produces one code-authored policy
feedback Tool message per selection. Rejected arguments and live scope values
are not copied into that feedback, and no ToolInvocation, Tool budget
reservation, Kubernetes request, persistence record, or Evidence results. A
subsequent corrected batch starts strict binding from the beginning. Unknown
Tools, malformed JSON or strict object shapes, injected authority fields, and
sensitive model text remain terminal policy failures.

## Evidence and answer validation

Only accepted deterministic Tool results create Evidence. Every Evidence item
binds the run, invocation, scope generation, exact API group/version/resource,
Kind and scope, exact ResourceRef, category, applicable resource or
observability policy version and generation, source path, observation time,
safe fact, and partial/truncation/redaction state. External-source Evidence
also binds the canonical source-origin hash, normalized series identity, and
query window. Continuation tokens, generated PromQL/LogQL, and raw Kubernetes
or data-source objects never enter Evidence.

The final wire object contains these members in order so the answer can be
projected without treating the rest of the envelope as visible text:

- `answer_markdown`;
- `evidence_citations`; and
- `proposed_actions`.

`answer_markdown` is bounded to 128 KiB before the complete Diagnosis ceiling
is applied. It is normalized, terminal-safe, sensitive-processed, and rendered
without mandatory headings. Citation IDs must exist in the same run; invalid
or duplicate references are removed and produce visible validation warnings.

Provisional text is delivery-only. It is independently bounded, checked for
the exact model credential and sensitive patterns across chunk boundaries,
normalized across split terminal controls, and rechecked against the immutable
scope before each event. Application emits an immediate first safe fragment and
then uses bounded byte- and time-based coalescing. A Tool request
clears pre-Tool provisional prose. Cancellation, timeout, stale scope, malformed
or length-limited output, and final validation failure replace it with the safe
terminal result. It is never Evidence, Tool authority, a committed Message,
SQLite data, audit content, log content, export content, or terminal scrollback.

The runtime cannot prove that prose semantically follows Evidence. Evidence
metadata improves traceability but does not turn model interpretation into a
verified fact.

## Proposed actions

The deterministic permission and controlled-action foundation described here
is implemented. Current production composition uses it only for the existing
supervised Deployment restart; the expanded remediation catalog is not exposed.

The final response may contain only versioned typed proposals from the P0
catalog: restart, scale, rollback, one controller-owned ordinary Pod delete,
cordon, uncordon, drain, and separately admitted sensitive/remote/local
operations. A proposal contains no UID, resource version, digest, Reviewer
decision, approval, or executor authority.

Application performs fresh target or executable-policy preparation and creates
one immutable `ActionEnvelope`. Deterministic risk and the permission profile
route it to automatic safe handling, a human, the optional Reviewer, a matching
human Session rule, or denial. `ask` is the default. Reviewer routing is limited
to `review`; `critical` remains human under `ask` and `auto-review`.

After a valid decision, Application rechecks the envelope, both generations,
time, policy, target, and one-time state; atomically consumes authority and
persists pre-operation audit; performs one final generation check; and reaches
an executor at most once. Any pre-operation storage failure produces zero
executor calls. Accepted, failed, and ambiguous outcomes are distinct and no
external execution is retried automatically. Verification is separately
bounded and cannot rewrite the attempt outcome. See
[Permissions and Controlled Actions](user-guide/approval.md).

## Implemented budget profiles

The following table records current code-owned safety ceilings. They are not
claims about an endpoint's context window, token accounting, or monetary cost;
endpoint evidence may require a tighter configuration.

| Boundary | Compact | Balanced (default) | Extended | Hard ceiling |
| --- | ---: | ---: | ---: | ---: |
| AgentRun wall clock | 2 min | 10 min | 30 min | 30 min |
| Agent steps | 12 | 32 | 64 | 128 |
| Tool calls | 16 | 48 | 128 | 256 |
| Model calls | 6 | 16 | 32 | 64 |
| Agent model request timeout | 60 sec | 120 sec | 300 sec | 300 sec |
| Agent model request bytes | 256 KiB | 256 KiB | 256 KiB | 256 KiB |
| Agent model stream bytes | 8 MiB | 8 MiB | 8 MiB | 8 MiB |
| Agent model cost units | 6 | 16 | 32 | 32 |
| Agent-summary calls | 1 | 2 | 4 | 4 |
| Agent-summary request timeout | 30 sec | 45 sec | 60 sec | 60 sec |
| Agent-summary request bytes | 256 KiB | 256 KiB | 256 KiB | 256 KiB |
| Agent-summary output bytes | 16 KiB | 16 KiB | 16 KiB | 16 KiB |
| Agent-summary cost units | 1 | 2 | 4 | 4 |
| Reviewer calls | 2 | 8 | 16 | 16 |
| Reviewer request timeout | 15 sec | 30 sec | 60 sec | 60 sec |
| Reviewer request bytes | 48 KiB | 48 KiB | 48 KiB | 48 KiB |
| Reviewer output bytes | 8 KiB | 8 KiB | 8 KiB | 8 KiB |
| Reviewer cost units | 2 | 8 | 16 | 16 |
| Kubernetes request | 15 sec | 30 sec | 60 sec | 60 sec |
| Resource pages per query | 2 | 4 | 8 | 8 |
| Resource items per page | 25 | 50 | 100 | 100 |
| Resource bytes per response | 128 KiB | 256 KiB | 1 MiB | 1 MiB |
| Resource items scanned per query | 50 | 200 | 500 | 500 |
| Resource items returned per query | 25 | 50 | 50 | 50 |
| Resource bytes per query | 256 KiB | 1 MiB | 4 MiB | 4 MiB |
| Cumulative Tool results | 1 MiB | 4 MiB | 12 MiB | 16 MiB |
| Pod-log calls | 4 | 12 | 32 | 32 |
| Pod-log containers per call | 4 | 8 | 16 | 16 |
| Pod-log lines per call | 100 | 400 | 1,000 | 1,000 |
| Pod-log bytes per call | 64 KiB | 256 KiB | 1 MiB | 4 MiB |
| Pod-log time window | 1 hour | 6 hours | 24 hours | 24 hours |
| Event pages per call | 2 | 4 | 8 | 8 |
| Event items per page | 25 | 50 | 100 | 100 |
| Event bytes per page | 64 KiB | 128 KiB | 256 KiB | 4 MiB |
| Event aggregate bytes | 128 KiB | 512 KiB | 2 MiB | 4 MiB |
| Metrics calls | 4 | 12 | 32 | 32 |
| Metric containers per Pod | 20 | 35 | 50 | 50 |
| Metric response bytes | 128 KiB | 256 KiB | 1 MiB | 4 MiB |
| Optional data-source calls | 4 | 16 | 64 | 64 |
| Loki pages per call | 2 | 4 | 8 | 8 |
| Prometheus series per call | 10 | 25 | 100 | 100 |
| Prometheus samples per call | 100 | 400 | 1,000 | 1,000 |
| Loki lines per call | 100 | 400 | 1,000 | 1,000 |
| Data-source response bytes | 128 KiB | 512 KiB | 4 MiB | 4 MiB |
| Data-source query window | 1 hour | 6 hours | 24 hours | 24 hours |
| Data-source step ceiling | 1 min | 5 min | 15 min | 15 min |
| Consecutive no-progress steps | 2 | 4 | 6 | 10 |

The model and Tool request deadlines are additionally capped by the owning
run's remaining time. One ToolResult remains at most 64 KiB. Resource queries
intersect the selected profile with the exact resource-policy entry and enforce
each response before decoding plus cumulative page, scanned-item, returned-item,
and byte ceilings. Resource and Event items, logs, Evidence, and relationship
graphs retain their independent ceilings.

Reservations happen before I/O. Completion accounts actual Tool-result bytes,
accepted Evidence progress, and retryability. Once stopped, a budget cannot be
reopened.

The current Agent, Reviewer, and Agent-summary reservations are independent.
Each model call consumes one code-defined cost unit; that unit is a finite call
budget, not a price estimate. Exact context-window and token values still
require evidence from the selected endpoint. Missing token evidence never
permits an unlimited request; byte, call, time, and cost-unit ceilings fail
closed. Remote-exec, local-process, stream, and idle budgets remain part of the
accepted later capability work.

## Session context and summarization

Every AgentRun after the first question in a Session receives exactly one
ordered, bounded representation of all retained eligible prior user and final
assistant Messages. Standard mode sources it in process and, after explicit
resume, across processes. Minimal mode sources it only from the current process
and persists no model memory. Resume itself performs zero model, Kubernetes,
Tool, Reviewer, approval, process, or executor I/O. The next submitted question
sends the eligible representation only after current consent, scope, policy,
coverage, and budget checks. A failed gate causes zero model calls and never a
silent current-question-only fallback.

The current question appears exactly once. Partial streams, Tool messages,
raw model traffic, command output, approval dialogs, and framework objects are
not replayed. Historic scope, ResourceRef, Evidence, permission rules, Reviewer
decisions, ActionEnvelopes, approvals, execution, clients, and generations are
never restored as authority.

Eino summarization middleware produces a bounded safe summary plus a complete
eligible recent tail. Project-owned coverage records the first/last covered
Message IDs, ordered count, digest, versions, origin hash, profile, time, and
degraded/truncation state. The summary call reuses `agent` with a separate
non-streaming no-Tool one-attempt budget. If required compaction fails, runtime
sends no oversized or silently truncated context and preserves the last
committed state. The existing SQLite messages are the stable durable source
until a non-prerelease Eino runner-managed Session passes ADR-0047's gate.

The current aggregate selection is capped at 4,096 eligible Messages and 4 MiB
and is read in ascending keyset pages of 100. The Eino middleware is configured
with `ContextMessages=160`, a 128 KiB UTF-8-content resource trigger, and an
exact 16-Message recent tail. The resource counter is deliberately not called
a tokenizer or token estimate. A safe summary is capped at 16 KiB.

## Cancellation and stale work

Every model, Reviewer, Kubernetes, data-source, remote-exec, and local-process
operation accepts the owning Context. Runtime checks complete scope and every
applicable scope/policy generation before external I/O, after every return, and
again when Application accepts an event. Execution adds one final check after
durable consumption and before the attempt.

Cancellation, timeout, stale scope or policy, terminal state, or budget
exhaustion stops new work. Late stream fragments, Tool results, Evidence,
Diagnosis objects, Reviewer decisions, Session rules, approvals,
ActionEnvelopes, process output, and verification events are discarded. Each
goroutine and child process has one owner, cancellation path, and bounded join
path.

## Event ordering and TUI

Application accepts monotonic run events with exact run ID, generation,
sequence, and terminal-state checks. The TUI receives project-owned UI events;
Bubble Tea does no business I/O in `Update` or `View`.

The internal run stream is capped at 32,768 ordered events. This is large enough
for the hard Tool, Evidence, and model budgets but remains independently finite;
the TUI receives a smaller projection because model-start and individual
Evidence-acceptance events are not rendered as transcript entries.

The current transcript shows bounded provisional answer Markdown, compact
diagnostic steps, safe warnings, the existing restart approval interaction, and
the validated final Markdown answer. Application already owns permission
routing, Reviewer decisions, and content-free typed action status; complete
permission and operation-specific delivery remains later work.
The internal stream and the smaller UI projection have independent run-wide
event ceilings; excess provisional refreshes may be omitted because the
validated terminal answer replaces the draft. `/status` is a local Application
query exposing the current catalog, scope, named model roles/origin hashes,
consent, content-free memory and summary coverage, budgets and usage, run state,
the resource page/item/per-response/aggregate ceilings, the
byte/call/time/cost-unit evidence basis with no endpoint-token claim,
privacy, and storage health without model, Kubernetes, Reviewer, process, or
executor activity. The complete `/permissions` interaction and detailed action
outcome display remain later work.

## Failure classes

Vendor, transport, Kubernetes, parsing, and persistence failures are translated
at their adapter boundary into stable project-owned classes. Safe UI and model
messages contain no raw error, body, header, credential, kubeconfig path, raw
object, or Tool result.

A failed durable run start prevents model and Tool I/O. A later read-side
persistence failure may finish the in-memory answer with visible degraded state
and no false resume claim. Any approval or pre-write audit failure produces zero
executor calls.

## References

- [Architecture](architecture.md)
- [Security Threat Model](security.md)
- [Diagnostic Capabilities](diagnostic-capabilities.md)
- [ADR-0037](adr/0037-adopt-an-operational-capability-catalog.md)
- [ADR-0038](adr/0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0039](adr/0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0043](adr/0043-use-one-eino-runtime-boundary.md)
- [ADR-0044](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045](adr/0045-admit-controlled-execution-and-remediation.md)
- [ADR-0046](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
