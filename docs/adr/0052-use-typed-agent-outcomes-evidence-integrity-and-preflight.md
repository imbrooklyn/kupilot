# ADR-0052: Use Typed Agent Outcomes, Evidence Integrity, and Preflight

- Status: Accepted
- Date: 2026-09-07
- Amends: ADR-0025, ADR-0031, ADR-0038, ADR-0039, ADR-0040, ADR-0041, ADR-0043, ADR-0047, ADR-0048, ADR-0049

## Context

ADR-0049 binds declared claims to same-run Evidence, but a complete supervised
answer also needs a strict clarification outcome, structured stop reason,
freshness and negative-coverage vocabulary, conflict handling, bounded safe-read
deduplication, per-category budget explanation, model preflight, and one recovery
matrix. These are integrity controls, not a second critic or proof that model
reasoning is semantically correct.

The pinned boundary remains Eino `v0.9.19` and its one `ChatModelAgent`, Runner,
ReAct state, Tool pairing, and summarization middleware. The pinned OpenAI
extension is `v0.1.13` with ACL `v0.1.17` and `go-openai` `v0.1.2`. Their exact
stream API still exposes no same-response reattachment and replay contract.

## Decision

Kupilot extends the existing strict final protocol and Application lifecycle.
It adds no Agent, model role, conversation loop, answer critic, generic memory,
checkpoint, event store, or provider fallback.

### Clarification outcome

A strict final response is a tagged union of `answer` and `needs_user_input`.
A clarification contains one through three ordered questions. Each question has
bounded text and either two or three fixed bounded choices or one bounded
free-form answer slot, never both. Unknown fields, prose-only substitutes,
duplicate IDs, empty choices, malformed bounds, Tool calls, or action-bearing
content fail closed.

Clarification creates no ToolInvocation, Evidence, ActionEnvelope, approval,
Reviewer call, Session rule, or execution. It is a terminal result with no queue
auto-drain. A response becomes a new explicit AgentRun input. Old clarification
state carries no authority after Session, scope, policy, origin, profile,
consent, or generation change. The same Eino Agent/Runner handles both runs.

### Stop reasons and Application validation

The model may declare a stop reason from the fixed vocabulary, but Application
derives the authoritative terminal reason from accepted lifecycle, error,
policy, budget, persistence, and coverage state. It rejects or overrides any
attempt to represent denied, partial, conflicting, unknown, degraded, or
insufficient-Evidence state as completed. Typed terminal reason and safe next
actions follow ADR-0051. The locally built completeness manifest also records
one fixed `stop_reason_basis`: `coverage`, `clarification`, or
`runtime_budget`. The model does not supply this field. In particular, an
unsupported observation is insufficient Evidence; it becomes budget exhaustion
only when the deterministic runtime records the budget basis.

### Evidence freshness, conflict, and negative coverage

Every eligible Evidence projection includes its original `observed_at`, exact
canonical source and subject identity, and a source revision/fingerprint only
when deterministic acquisition already supplied a safe exact value. A
superseded relation references an earlier same-subject Evidence ID. Freshness
is `fresh`, `stale`, or `freshness_unknown`; only a code-owned source policy
with an exact ceiling may select fresh or stale. Absence of such a ceiling is
always unknown.

Conflict is established only for exact typed source, subject, field, and
incompatible revision/value-digest facts. Runtime never parses free-form prose
to infer conflict. Conflicting or superseded Evidence cannot silently support a
complete current-state claim.

Source coverage distinguishes `checked_absent`, `not_checked`, `unavailable`,
`denied`, `partial`, `truncated`, `timed_out`, `stale`, and `conflicting`.
Absence from a bounded list is not global nonexistence unless deterministic
runtime records the exact query as complete.

### Equivalent safe-read deduplication

Within one active Session and Run, deterministic runtime may reuse a successful,
complete, policy-admitted `safe` read only when Session/Run, scope generation,
policy generation, catalog operation/version, canonical target, typed canonical
parameters, and response bytes are identical and the code-owned operation has
an exact freshness window. The initial observation time is retained. Reuse
emits bounded metadata linking the new Tool lifecycle to the original accepted
Evidence; it does not copy raw payload or fabricate a new observation time.

Review, critical, deny, mutation, Exec, diagnostic Pod, local process, failed,
partial, truncated, unknown, cross-run, cross-generation, stale, conflicting,
or mutation-invalidated results are never reused. The model cannot choose the
key, window, TTL, or bypass. Any intervening accepted mutation or exact conflict
invalidates affected subjects before another lookup.

### Fine-grained budget explanation

`/status`, egress preflight, and terminal stop projection expose content-free
measured/reserved/limit state separately for model input bytes, model output
bytes, summary reserve, model attempts, Tool calls, Kubernetes/data-source
calls, Evidence items/bytes, pages/lines/samples, wall/idle time, queue items
and bytes, and protocol continuation attempts/time. A field states whether a
value is measured, configured, reserved, unavailable, or estimated. Estimated
values are never labelled measured and no prompt or sensitive content is
shown.

Every terminal outcome carries the fixed seventeen-category snapshot. When an
invalidation path has no exact run-local accounting, every category is marked
`unavailable` with no invented usage or ceiling. A compact terminal line keeps
the reason and next action visible; `/status` remains the detailed projection.

### Application-owned invocation preflight

Immediately before every adapter model endpoint entry, the existing Eino
middleware calls one run-bound Application preflight bridge. Application
verifies the exact active Session/Run, initial input and committed steer
sequence, scope/policy generations, permission profile, role/origin, consent
categories, context coverage, summary/recent tail, Tool catalog, storage health,
budget reservation facts, sink availability, frozen ordinary/plan-only mode,
and recovery state. It publishes ADR-0051's content-free event and returns one
admit/deny decision. Failure causes zero model calls and no current-question-only
fallback. The bridge neither moves Eino types out of `einoadapter` nor creates a
second loop.

### Answer completeness manifest

### Question-start authority and typed recovery

`scope_generation` and `policy_generation` are monotonically changing
process-local versions of independently verified authority. They are not
application, database, migration, schema, or Session versions. Persisted
generations describe historic provenance only. A compatible binary restart or
forward migration therefore keeps eligible Session history readable, while
restoring none of the historic Context, Namespace, generation, ResourceRef,
Evidence, approval, action, client, or retry authority.

Every TUI question-start command binds the exact current Session ID, expected
scope generation, expected policy generation, and exact selected ResourceRef or
the explicit absence of one. Application compares those values with its own
current state before durable start and again at the existing run preflight. It
never retargets, retries, queues, or sends a mismatched question automatically.

A refused start returns one project-owned typed reason, one fixed recovery
action, and a bounded safe current-state projection. The closed reasons are
`session_unavailable`, `scope_not_verified`, `scope_generation_stale`,
`selected_resource_stale`, `policy_generation_stale`,
`policy_snapshot_invalid`, `run_active`, `run_starting`,
`application_operation_active`, `persistence_degraded`,
`precommit_persistence_failed`, `model_configuration_missing`,
`consent_required`, `input_rejected`, and `unknown_safe_failure`. The
projection contains only current Session/run identity, scope lifecycle and
generation, verified Context/Namespace when available, policy generation and
health, selected-resource state, persistence state, and accepted UI sequence.
It contains no question, history, resource content, credential, raw error, or
storage path.

Delivery correlates the result to the exact request and restores the unsent
draft once. It appends no optimistic user transcript row or submitted-input
history entry. A currently verified scope returned at a newer generation may
replace a stale delivery projection, but the question still requires a new
explicit submit. An unavailable scope or stale ResourceRef enters the existing
bounded picker while retaining the draft. If Application reports an active run
while delivery appeared idle, delivery resynchronizes to that exact run and
keeps the draft for an explicit `Enter` steer or `Tab` queue action. No failure
outcome itself sends, steers, queues, retargets, or retries input.

Resume continues to load only eligible history and an unverified historic scope
candidate with zero model or operational I/O. An exact candidate matching a
scope independently verified in the new process reuses that current authority;
an unavailable or conflicting candidate enters the normal scope picker. Program
version and migration changes do not participate in that comparison.

### Answer completeness manifest

Strict response schema 2 contains bounded ordered claims, claim type and hash,
Evidence IDs, observation/inference/recommendation/uncertainty distinction,
limitations, checked sources, unchecked sources, partial/truncated/unavailable
coverage, conflicts, freshness, declared stop reason, and response/coverage
schema versions. Application binds the accepted manifest to the exact Run,
scope and policy generation and validates Evidence eligibility, ordering,
hashes, source coverage, and authoritative stop reason before success commit.
Legacy schema 1 retained answers remain readable as historic non-authority.

The manifest proves only structure, provenance ownership, declared coverage,
bounds, and reference completeness. It cannot prove semantic alignment between
prose and claims, correctness of reasoning or recommendations, or exhaustive
enumeration of real-world facts.

### Deterministic conformance and injection evidence

A project-owned loopback endpoint suite validates strict structured final
output, Tool call IDs, stream ordering, usage reporting, cancellation,
malformed/duplicate/out-of-order events, timeout, unknown outcome, the exact
continuation capability decision, and no cross-origin retry. It is not run at
normal startup and never contacts a live endpoint.

A synthetic prompt-injection corpus places untrusted directives in user
history, resumed summary, Kubernetes projection, logs/Events/metrics, Tool
result, Evidence text, model error, Reviewer rationale, Session title, and
clipboard/search input. Deterministic tests prove those values cannot change
scope, policy/generations, consent, origin, budget, risk, Tool catalog,
approval/execution, Evidence authority, or system/developer instructions. Fake
fixtures are not live model-quality evidence.

### Unified degraded and recovery matrix

Application uses a fixed matrix for model transport, summary, SQLite,
Tool/Kubernetes/data source, approval/Reviewer, notification/title, clipboard,
transcript/history search, endpoint continuation, and terminal shutdown. Each
row specifies terminal reason, persistence effect, queue auto-drain permission,
input state, model retry permission, Tool/action retry permission, safe next
action, and maximum external-call consequence.

Optional delivery failures remain local notices and do not become Agent
failure. Persistence, authority, ambiguity, and unknown-side-effect failures
cannot be downgraded to success. No row authorizes automatic model, Tool,
ActionEnvelope, or executor retry. Only a new explicit input can create a later
run; recovered editable input is not committed authority.

### Protocol continuation decision

The admission gate from ADR-0049 remains unsatisfied. Eino's pinned extension
creates one complete Chat Completions stream request; `Recv()` exposes decoded
chunks and `[DONE]` but no stable replay event identity/offset,
`Last-Event-ID`, or same-response reattach operation. Response/chunk or HTTP
request IDs are diagnostic, not continuation authority. Therefore the exact
outcome remains **Protocol continuation unavailable**.

No continuation interface, config, checkpoint, polling, replay request,
automatic retry, raw event persistence, or dependency upgrade is added.
Disconnect continues to become unknown/recovered, and restart restores no
active stream authority.

## Retention and privacy impact

Clarification content and valid completeness metadata follow the existing safe
Message/Diagnosis retention rules in standard mode and remain process-only in
minimal mode. Freshness, conflict, supersession, source coverage, and reuse
metadata contain identifiers and digests, never raw payload. Preflight, budget
projection, dedup cache, endpoint fixture state, and recovery projections are
run-local and not persisted as authority. Export advances only when its strict
allowlist can represent the new safe manifest; delete continues to remove the
whole graph.

## Rejected alternatives

- Prose-only clarification, model-selected authority, or automatic execution
  after clarification or plan output.
- A runtime answer critic or reuse of `approval_reviewer` for answer quality.
- Prose-derived conflicts, invented freshness TTLs, or cross-run Evidence cache.
- Generic response cache, raw payload copies, model-controlled cache keys, or
  reuse of sensitive/mutating outcomes.
- Retry/failover, polling, provider routing, or a second Agent loop.

## Validation

Deterministic tests cover clarification success, malformed/one-over/stale
output and zero side effects; stop-reason override; all positive/negative,
fresh/unknown/stale, conflict and supersession states; cross-run/generation and
invalid Evidence; every safe-read reuse admission and denial; exact budget
categories; every preflight denial with zero model calls; manifest versions,
hash/order/limits and legacy reading; injection corpus authority invariants;
loopback endpoint event/usage/cancel/timeout/unknown behavior; unavailable
continuation and one request; recovery-matrix rows; exact-once initial/steer
messages; Tool pairing and summary coverage.

Question-start tests additionally cover exact, one-behind, and one-ahead
generations; activation and Context/Namespace/policy races; stale ResourceRef;
delivery-idle/Application-active resynchronization; starting and other bounded
operations; degraded and precommit persistence failures; missing model,
consent, and locally rejected input; late, duplicate, and out-of-order results;
single draft restoration; compatible migration/restart resume; corrupt storage;
and zero model calls with no automatic retry, queue, retarget, or send on every
denial.

Live endpoint, model-quality, Reviewer, Kubernetes, release, and deployment
evidence is explicitly not run by this decision.

## Consequences

Answers and terminal stops become easier to audit, and repeated exact safe
reads can avoid redundant I/O under a narrow policy window. The stricter
protocol may reject more model output. Freshness is deliberately unknown when
source policy has no proved ceiling, and interrupted streams still require a
new explicit user decision rather than an unverifiable continuation.
