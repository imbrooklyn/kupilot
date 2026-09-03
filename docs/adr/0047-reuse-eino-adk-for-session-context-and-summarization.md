# ADR-0047: Reuse Eino ADK for Session Context and Summarization

- Status: Accepted
- Date: 2026-09-03
- Amends: ADR-0025, ADR-0031, ADR-0034, ADR-0041, and ADR-0043

## Context

Kupilot persists safe Session messages but `v0.4` does not pass resumed
conversation history to the model. Large conversations also need a bounded
compaction strategy. Building another conversation loop, memory manager,
summary engine, or framework-neutral facade would duplicate Eino ADK behavior
and create a second source of message ordering, Tool pairing, cancellation, and
token-budget semantics.

The dependency evidence reviewed for this decision is intentionally precise.
The repository pins `github.com/cloudwego/eino v0.9.13` and the matching OpenAI
extension used by Kupilot. Tagged `v0.9.13` source and tests contain stable ADK
`ChatModelAgent`, `Runner`, and summarization middleware. They do not provide a
stable runner-managed durable Session contract suitable for Kupilot. On
2026-09-03, the latest visible stable Eino release is `v0.9.19`; the visible
`v0.10.0-alpha.30` line is prerelease. A discussion or prerelease API is not a
stable dependency contract.

This ADR therefore selects the thinnest stable path for `v0.5`. It does not
authorize a dependency update, schema migration, or implementation in this
documentation change.

## Decision

### Session memory modes

`standard` and `minimal` are Session-level model-memory modes:

| Situation | `standard` | `minimal` |
| --- | --- | --- |
| Next question in the same process | Receives one ordered, bounded representation of all retained eligible prior user and final assistant context from the same Session. | Receives the same ordered, bounded representation from context held only in this process. |
| Process restart | Only explicit `kupilot resume`, `resume <session-id>`, or `resume --last` may select safe persisted context. | No cross-process model memory and no resume. |
| Resume command | Reads only eligible local history and unverified candidates; performs zero model, Kubernetes, Tool, approval, Reviewer, process, or executor I/O. | Returns the existing non-resumable result with zero external I/O. |
| First model transfer | Occurs only when the user asks the next question and current origin/role/category consent, scope, policy generations, and budget all pass. | Same checks for same-process context; no durable load. |

For every AgentRun after the first submitted question in a Session, when any
prior eligible Message exists, model input must contain exactly one ordered,
bounded representation of all retained eligible prior turns. That representation
is either the direct safe Messages while they fit or one validated safe summary
plus the complete eligible recent tail. Silently omitting eligible history or
falling back to a current-question-only model call is forbidden. A selection,
coverage, consent, scope-generation, policy-generation, or budget failure causes
zero model calls.

Only committed, locally processed user messages and final validated assistant
messages are eligible. The current question appears exactly once in the final
Eino input. Partial streams, Tool calls or results, raw model traffic, provider
errors, approval dialogs, command output, and framework objects are not replayed
as conversation history.

Historic Context, Namespace, ResourceRef, Evidence, Tool state, permission
profile, Session rule, Reviewer decision, approval, ActionEnvelope, execution,
client, generation, or cancellation state never regains authority through
history. Saved scope and resource values remain unverified candidates. A new
current-run fact requires new deterministic Evidence.

### Framework-first runtime boundary

Inside the sole `internal/agent/einoadapter` boundary, Kupilot directly
composes stable Eino ADK `ChatModelAgent` and `Runner`. Eino owns the in-run
message state, Tool-message pairing, ReAct iteration, and event sequence.
Application selects the exact Session's eligible ordered messages, applies
current consent and authority checks, and supplies project-owned DTOs to the
adapter. The adapter translates them once and calls `Runner.Run`.

Kupilot directly installs Eino summarization middleware. Project-owned code is
limited to safe input selection, exact budget evidence, a finalizer that
projects a bounded safe summary and recent tail, coverage metadata, and the
Application/persistence boundary. Eino and provider values do not leave the
adapter.

Kupilot must not implement:

- another conversation or ReAct loop;
- a `MemoryManager`, general `ContextManager`, custom tokenizer, summarizer, or
  summary engine;
- a generic checkpoint or event store, a second durable conversation log, or
  raw Eino transcript persistence;
- a framework-neutral Agent, memory, middleware, or provider facade created for
  hypothetical replacement;
- RAG, retrievers, automatic long-term memory, cross-Session user profiling,
  hidden retry/failover, or a predeclared `context_compactor` role.

Checkpointing, if separately adopted later, can represent one interrupted
execution only; it cannot substitute for completed-turn Session persistence or
restore operational authority.

### Stable bridge and adoption gate

Until an Eino stable tag provides a runner-managed Session contract that passes
the gate below, the existing SQLite Message repository is the only durable
conversation source. The bridge performs only eligible-row selection, ordering,
coverage lookup, and project/Eino DTO translation. It is not a memory API.

A runner-managed Session may replace this bridge only when all of these are
proved for one non-prerelease tag:

1. the exact stable tag contains the Session/Store/event contract and tests;
2. it is compatible with the selected Go, Eino OpenAI extension,
   `ChatModelAgent`, `Runner`, and middleware APIs;
3. storage accepts project-owned safe projections and does not require raw
   model, Tool, framework, or event payloads;
4. resume still performs zero external operational or model I/O;
5. current-question-once, ordering, Tool pairing, cancellation, and late-event
   behavior are deterministic;
6. retention, deletion, export, corrupt/unknown versions, and migration failure
   satisfy the public contracts;
7. historic scope, Evidence, action, and permission state cannot regain
   authority;
8. current consent, role, origin, budget, and generations gate every replay;
   and
9. targeted, repository, race, import, security, migration, and platform gates
   pass.

When this gate passes, the runner-managed implementation replaces the thin
bridge; it is not layered beneath a second custom memory manager.

### Summary, recent tail, coverage, retention, and failure

A standard Session may persist only:

- bounded normalized and sensitive-processed summary text;
- summary schema and policy versions;
- covered first and last committed message IDs, ordered count, coverage digest,
  and generation time;
- source Session ID and the generating `agent` profile name and canonical
  origin hash, without credentials or model traffic;
- truncation, partial, and degraded markers.

The recent tail is the ordered eligible Message rows after coverage; it is not
copied into a generic payload. Summary input is the prior safe summary plus
uncovered eligible messages. Middleware triggers only from endpoint- and
dependency-supported budget evidence. The finalizer emits one safe summary
message plus a complete recent tail and verifies that no Tool pairing is
broken. Coverage prevents the same committed messages from being summarized or
charged twice.

Summarization reuses the `agent` profile through a reserved, non-streaming,
no-Tool, one-attempt sub-budget. There is no fallback or automatic retry. If
compaction is optional and context fits, the turn may continue without it. If
compaction is required and fails, times out, is cancelled, becomes stale,
oversized, sensitive, or corrupt, Kupilot sends no oversized or silently
truncated model request, preserves the last committed state, and reports a
blocked or degraded turn.

Standard summary and coverage rows follow Session retention and are deleted
with the Session. Minimal mode persists none of them. Export includes only the
versioned safe summary/coverage explanation and public-contract-eligible
messages or final answers, never raw prompts, model traffic, Tool/Exec/log
content, or framework objects. A pre-run durable-start or required-summary
persistence failure produces zero model and Tool calls. A later read failure
may show degraded state but cannot claim that an unsaved turn is resumable.

`/status` reports the memory mode, eligible-message count, covered count and
last ID, recent-tail count, compaction state and failure, exact-versus-estimated
budget basis, Agent profile, canonical-origin hash, consent, and storage health.
It does not display summary/history content, credentials, or raw tokens.

Exact context windows, token counters, summary triggers, recent-tail size,
summary output, stream behavior, and latency/cost limits remain evidence-gated.
The middleware's example defaults and generic characters-per-token estimates
are not Kupilot contracts.

## Consequences

Kupilot gains useful Session continuity and bounded long-context behavior while
using one framework message state and one durable safe history source. Explicit
resume remains a privacy boundary, and old operational facts never become new
authority.

The adapter must adopt stable ADK APIs and the persistence schema must add
explicit coverage fields through a future forward-only migration. Dependency
or middleware limitations remain visible instead of being hidden behind a
custom memory layer.

## Security and privacy impact

Conversation replay is a new model-transfer path. Every selected field passes
the normal allowlist, projection, normalization, sensitive handling, byte
limits, and final role/origin/category consent check. Summary text is untrusted
derived content, not Evidence or policy. Profile and origin hashes support
review but reveal no credential.

Deletion is logical rather than forensic. Terminal scrollback, exported files,
backups, and provider-side retention remain outside SQLite deletion. No raw
framework state or external output becomes durable merely to simplify resume.

## Validation

Deterministic tests must prove that the second question receives the first
eligible committed turn in same-process standard and minimal modes and after
cross-process standard resume. They must also cover all explicit resume forms,
current-question-once, exact message order, safe eligibility, zero-I/O resume,
current consent, stale generations, zero model calls for every failed context
gate, history that contains prompt injection, and proof that historic authority
is not restored.

Summary tests must cover trigger boundaries, one-over budgets, recent-tail and
coverage integrity, changed/deleted/out-of-order rows, corrupt metadata,
partial Tool history, cancellation, timeout, sensitive output, persistence
failure, retention, deletion, export, `/status`, and no fallback or duplicate
charge. Tagged live integration may validate one exact endpoint's counting and
middleware behavior; it does not replace deterministic CI.

## Revisit triggers

- A non-prerelease Eino release offers runner-managed Session support and all
  adoption-gate evidence is available.
- Eino removes or materially changes `ChatModelAgent`, `Runner`, message-state,
  event, or summarization middleware semantics.
- A new durable conversation field, memory mode, export field, model role,
  checkpoint, RAG, retriever, or cross-Session memory is proposed.
- Product requirements need automatic resume, raw transcript storage, or
  restoration of operational authority.
- Endpoint evidence changes exact context or compaction limits.

## References

- [ADR-0025: Enforce Data Retention and User Deletion](0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
- [ADR-0034: Export Only Versioned Redacted Session Summaries](0034-export-only-versioned-redacted-session-summaries.md)
- [ADR-0041: Export Free-Form Session Summaries](0041-export-free-form-session-summaries.md)
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0046: Use Named Model Roles and Optional Auto-Review](0046-use-named-model-roles-and-optional-auto-review.md)
- [Data Retention Contract](../data-retention.md)
- [Eino memory and Session example](https://www.cloudwego.io/docs/eino/quick_start/chapter_03_memory_and_session/)
- [Eino summarization middleware](https://github.com/cloudwego/eino-ext/blob/main/skills/eino-agent/reference/middleware.md#summarization-middleware)
- [Eino runner-managed Session discussion](https://github.com/cloudwego/eino/discussions/1159)
- [Eino releases](https://github.com/cloudwego/eino/releases)
