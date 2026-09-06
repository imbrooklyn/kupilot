# Kupilot Product Contract

- Status: Accepted `v0.5` target
- Date: 2026-09-06

The checked-in implementation now includes the named-model, Eino ADK runtime,
role-scoped consent, safe Session context and summarization, and deterministic
permission/action foundation of ADR-0044 through ADR-0048. Active-run steering,
bounded process-local follow-up input, and edit-last remain Application-owned
while Eino retains the one in-run Agent loop. The supervised
Deployment restart, typed scale/rollback/controller-owned-Pod delete/cordon/
uncordon/drain actions, and default-off exact local argv and separate shell
paths are composed through the shared Application action lifecycle. The broad
read/observability and default-off remote-diagnostic slices are also present.
This is deterministic implementation evidence, not a live integration or
`v0.5` release-artifact claim.

## Product definition

Kupilot is a local, single-process, single-user Kubernetes operations Agent
with a conversational terminal interface. A user states an operational intent,
Kupilot chooses code-owned typed capabilities, shows the work and permission
state inline, and returns bounded free-form Markdown backed by deterministic
Evidence.

Kupilot is designed for everyday investigation and explicitly controlled
recovery. It is not a resource browser with an assistant attached. The primary
interaction remains one low-chrome conversation, not a resource tree,
dashboard, command palette, YAML editor, action menu, or shell console.

## Product principles

1. **Agent-first interaction.** The user states intent and supervises visible
   capability, review, action, and verification steps.
2. **Typed authority, natural answers.** Capabilities and actions use strict
   versioned schemas. The final answer is the Markdown shape that best answers
   the question.
3. **Exact operational context.** Every AgentRun has one verified Context, one
   working Namespace, one namespace-access policy, one scope generation, and
   one policy generation.
4. **Runtime-owned Evidence and permission.** Only deterministic local handling
   creates Evidence, classifies risk, or creates action authority. User text,
   model prose, Reviewer output, and Kubernetes data remain untrusted.
5. **Explicit breadth.** Built-in resources, exact policy-admitted CRDs,
   optional data sources, sensitive reads, execution, and remediation are
   admitted individually with projection, consent, RBAC, budget, and tests.
6. **Permission is layered.** Technical capability, Kubernetes RBAC, data
   consent, risk class, permission profile, human/Reviewer routing, audit, and
   target revalidation remain separate gates.
7. **Side effects are transactions.** Every sensitive or effectful operation
   uses one immutable digest-bound `ActionEnvelope`, durable pre-operation
   audit, at most one attempt, and separate outcome and verification states.
8. **Framework-first Agent runtime.** Kupilot directly uses stable Eino ADK
   Agent, Runner, message-state, and summarization facilities inside one
   adapter boundary. It does not rebuild those facilities or add facades for
   hypothetical framework replacement.
9. **Finite capability-aware budgets.** Agent, Reviewer, summary, read, log,
   metrics, remote-exec, local-process, item, byte, time, and cost limits remain
   finite and visible. Exact model values require dependency and endpoint
   evidence.
10. **Local ownership and deletion.** Kupilot has no hosted control plane or
    product telemetry. Eligible local history follows the retention, export,
    and deletion contracts.
11. **Explicit input commitment.** Acceptance of active-run input is not model
    commitment. Application commits a steer only at the next Eino model
    boundary, queues successor work only in the current process, and never
    automatically resends input after an unsafe or unknown outcome.

## Intended user journey

1. The user starts a new Session with bare `kupilot`, or explicitly resumes an
   eligible standard Session. Resume itself performs no model, Kubernetes,
   Tool, Reviewer, approval, process, or executor I/O.
2. The single-screen TUI completes any required named model-profile setup. The
   required `agent` and optional `approval_reviewer` profiles each bind one
   explicit canonical origin and credential source.
3. The user verifies a kubeconfig Context and working Namespace, reviews the
   namespace policy, and selects a permission profile. `ask` is the default;
   `full-access` is never selected implicitly.
4. Before content leaves the workstation, the user reviews the exact model
   role, canonical origin, and enabled data categories. Consent is separate for
   each role and origin.
5. The user asks an operational question. Application freezes scope, policy,
   consent, catalog, and capability-aware budgets for one AgentRun.
6. Inline steps show bounded reads, optional data-source access, and any
   proposed sensitive or effectful action. `/status` and `/permissions` are
   local views and cause no operational I/O.
7. Kupilot validates the final free-form answer, its current-run Evidence
   references, and any typed proposed action. Partial or invalid streams never
   become committed assistant history.
8. A `review` request is routed according to the permission profile. Under
   `auto-review`, the optional Reviewer may `approve`, `deny`, or
   `escalate_to_user`. A `critical` request remains human-reviewed under `ask`
   and `auto-review`.
9. An admitted action executes at most once after all current checks and
   durable pre-operation audit. Acceptance, failure, ambiguous outcome,
   progress, and verified completion remain different states.
10. Every question after the first in a Session receives one ordered, bounded
    representation of all retained eligible prior turns. Standard mode supports
    this in process and after explicit cross-process resume; minimal mode only
    in process. Resume sends nothing, and a failed consent, scope, policy,
    coverage, or budget gate causes zero model calls rather than a silent
    current-question-only fallback.
11. During a regular active run, ordinary `Enter` input may steer the next
    model invocation and `Tab` may queue a FIFO successor. `Alt+Up` restores
    the newest editable queued, rejected, or recovered item only into an empty
    composer.
    A clean durably completed turn starts at most one queued successor;
    failure, cancellation, timeout, stale state, degraded persistence, and an
    unknown outcome never auto-send it.

## P0 operational capability contract

The `v0.5` P0 catalog covers these code-owned categories:

- typed built-in and exact policy-admitted CRD `get`, `list`, conversational
  `describe`, and bounded query/count/table projections;
- related Events; current, previous, and explicit all-container logs; and
  bounded local log search;
- Pod and Node metrics plus explicitly configured optional Prometheus and Loki
  data sources;
- a safe Secret-metadata projection and explicitly policy-admitted exact
  ConfigMap keys or non-credential container environment values, with values
  treated as sensitive reads rather than generic object dumps;
- bounded container-file reads, predefined no-shell Pod diagnostics, separately
  gated Pod Exec, and bounded diagnostic Pods;
- typed restart, scale, rollback, single controller-owned ordinary Pod delete,
  cordon, uncordon, and drain operations; and
- default-off restricted direct argv integrations for exact `kubectl`, `helm`,
  `argocd`, or another policy-selected executable, with shell as a separate
  default-off `critical` class.

Each capability names an operational need, exact source and operation, model
fields, privacy category, projection, limits, errors, partial behavior,
Evidence or verification mapping, RBAC, and deterministic tests. Optional does
not mean dynamically discoverable. A model cannot supply a Context, credential,
origin, arbitrary API, raw selector, executable, image, destination, YAML,
stdin, deadline, or hard limit. Restricted argv remains policy-owned; only the
separately enabled `critical` shell schema may carry one exact bounded command
string, which is displayed and digest-bound like every other effectful
parameter.

Generic patch/apply/edit/delete, arbitrary YAML, wildcard commands, Watch,
informers, background scans, scheduled work, autonomous remediation,
cross-Context calls, cluster-admin, wildcard RBAC, port forwarding, plugins,
MCP, RAG, retrievers, and Multi-Agent orchestration remain outside P0.

## Permission contract

Risk classes are `safe`, `review`, `critical`, and `deny`. Classification is a
deterministic function of the versioned operation and normalized parameters,
including data, sink, network, and side-effect characteristics.

| Profile | `safe` | `review` | `critical` | `deny` |
| --- | --- | --- | --- | --- |
| `read-only` | Automatic | Human only for admitted sensitive reads | Denied | Denied |
| `ask` (default) | Automatic | Human | Human | Denied |
| `auto-review` | Automatic | Reviewer, with human escalation | Human | Denied |
| `full-access` | Automatic | Automatic | Automatic | Denied |
| `custom` | Explicit route | Explicit route, including Reviewer | Exact automatic, human, or deny rule; human by default | Denied |

Profiles route already admitted operations. They do not grant RBAC, enable a
default-off capability, broaden scope or consent, expose credentials, change
risk, bypass durable audit or revalidation, or override `deny`. `full-access`
and a custom critical-auto rule require explicit high-risk selection and local
policy permission.

A Reviewer is an optional bounded decision input, not permission authority. It
reviews only `review` envelopes through a strict non-streaming no-Tool response
and cannot create Session rules, approve `critical`, or override deterministic
policy. Failure is fail-closed; there is no implicit model fallback.

A human Session rule can cover only a narrow `review` operation in the current
process and Session. It is revocable, expiring, generation-bound, never resumed,
and never model- or Reviewer-created. Approve-once remains default reject,
single-use, digest-bound, and valid for exactly 60 seconds.

## Action and execution contract

Every sensitive or effectful operation is represented by a versioned immutable
`ActionEnvelope` binding operation and policy versions, risk and permission
profile, Session/run/request identity, exact scope and both generations, target
identity and fingerprints, typed parameters or fixed executable plus argv,
stdin/TTY/shell flags, data/sink/network effects, finite limits, expiry, and a
verification plan.

Canonical fixed-order length-prefixed encoding and SHA-256 bind approval,
review, audit, and execution. A map, raw JSON/YAML, human description, model
text, or unnormalized command is never authority.

Application alone performs schema and policy checks, routing, fresh target or
executable validation, decision matching, final revalidation, atomic
single-use consumption plus durable pre-operation audit, one final generation
check, and at most one external attempt. Storage failure before the attempt
means zero executor calls. Cancellation, timeout, conflict, restart, or an
ambiguous result never triggers automatic execution retry. Verification is a
separate bounded read phase and cannot rewrite the attempt outcome.

## Model and Session context contract

Kupilot keeps one protocol kind, `openai_compatible`, while allowing several
explicit named profiles and origins. The required `agent` and optional
`approval_reviewer` roles select exactly one profile each. There is no provider
auto-detection, router, fallback, load balancing, or cross-origin retry.
Summarization reuses `agent` with an independent reserved budget; there is no
prebuilt `context_compactor` role.

Inside `internal/agent/einoadapter`, stable Eino ADK `ChatModelAgent`, `Runner`,
message state, and summarization middleware own the framework conversation
loop. The current stable path uses eligible messages from the existing SQLite
repository through a thin selection and translation bridge. Runner-managed
durable Session support may replace that bridge only after a non-prerelease tag
passes the adoption gate in ADR-0047.

Standard mode may persist a bounded safe summary, coverage metadata, and the
eligible recent committed tail. Minimal mode persists no model memory. History
never restores current scope, ResourceRef, Evidence, Tool state, permission
rule, Reviewer decision, approval, ActionEnvelope, execution, or generation.
Summary or compaction failure cannot cause an oversized or silently truncated
model request.

One completed run may contain one initial committed user Message, zero or more
committed steer user Messages, and one final assistant Message. Pending,
committing, queued, rejected, and recovered drafts are current-process state
rather than durable model history. An unknown lifecycle can follow only a
Message commit, but an incomplete or failed run group is ineligible for model
replay. Application owns input identity, limits, generations, persistence
barrier, FIFO/LIFO transitions, and one-at-a-time clean-success drain. Eino
v0.9.19 supplies the existing
`BeforeModelRewriteState` and `WrapModel` boundaries without becoming the queue
owner or adding `TurnLoop`.

## Evidence, data, and network contract

Only deterministic runtime handling creates Evidence. Model output can cite an
accepted current-run Evidence identifier and propose an action, but it cannot
create an observation, permission, or execution result. Historic Evidence is
display-only.

The visible result remains bounded free-form Markdown without mandatory report
headings. Same-Kind inventories with shared attributes default to a compact
table, while prose and lists remain available when they fit the question
better. Denial, truncation, missing data, stale state, and unsupported sources
remain explicit rather than being hidden behind a confident answer.

Every model-bound field follows this local order: source allowlist,
project-owned projection, text normalization and unsafe-control removal,
sensitive-value block or typed redaction, hard item and byte limits, neutral
serialization, and a final role/origin/category consent check.

Credentials, kubeconfig, Secret values, ServiceAccount tokens, raw Kubernetes
objects, raw model traffic, raw Tool results, raw Events/logs/process output,
and framework objects never become generic model, history, log, audit, SQLite,
or child-environment data. Specific sensitive sources may be admitted only by
an explicit policy and category; credentials remain a hard denial.

## Product identity and non-goals

Kupilot remains intentionally not:

- k9s, Kubernetes Dashboard, or a primary inventory application;
- a generic kubectl wrapper, shell console, terminal multiplexer, IDE, YAML
  editor, or arbitrary Kubernetes client;
- a controller, operator, daemon, scheduled scanner, or autonomous remediation
  service;
- a hosted service, cluster-resident component, multi-user control plane, or
  telemetry collector; or
- a plugin host, MCP client, RAG system, arbitrary network agent, framework
  abstraction kit, or Multi-Agent orchestrator.

These boundaries reject generic surfaces, not the reviewed typed capabilities
needed for daily operations.

## References

- [Version Scope](scope.md)
- [Architecture](architecture.md)
- [Security Threat Model](security.md)
- [Privacy Overview](privacy-overview.md)
- [Data Retention Contract](data-retention.md)
- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](adr/0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045: Admit Controlled Execution and Remediation](adr/0045-admit-controlled-execution-and-remediation.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](adr/0046-use-named-model-roles-and-optional-auto-review.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](adr/0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0048: Own Run Steering and Queued Follow-Up Input](adr/0048-own-run-steering-and-queued-follow-up-input.md)
