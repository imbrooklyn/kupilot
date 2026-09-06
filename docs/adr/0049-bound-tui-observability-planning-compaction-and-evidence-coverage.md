# ADR-0049: Bound TUI Observability, Planning, Compaction, and Evidence Coverage

- Status: Accepted
- Date: 2026-09-07
- Amends: ADR-0038, ADR-0039, ADR-0040, ADR-0041, ADR-0043, ADR-0047, ADR-0048

## Context

Kupilot already has one Application-owned Session and AgentRun lifecycle, one
Eino `ChatModelAgent` and `Runner` boundary, a current-process steering queue,
safe SQLite Messages, and automatic Eino summarization. Several routine
supervision tasks nevertheless require more precision: users need to remove
an exact uncommitted follow-up, copy or search only committed output, see
content-free context pressure, request compaction explicitly, ask for a plan
that cannot become action authority, and distinguish structurally supported
claims from inference or limitation.

Terminal affordances and transport recovery add separate safety concerns.
Clipboard and title escape sequences are data sinks. A disconnected stream
cannot be resumed safely unless the exact stable protocol supplies both a
same-response continuation operation and replay identity or sequence. A
second request, polling loop, client checkpoint, or SDK response identifier
alone cannot prove continuation and would violate the no-blind-retry rule.

The exact references inspected for this decision were:

- Codex CLI `rust-v0.153.4`, commit
  `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a`, for general interaction
  semantics around copy, search, compaction, planning, queues, terminal
  notifications, teardown, and disconnect handling; and
- Eino `v0.9.19`, Eino OpenAI extension `v0.1.13`, its pinned OpenAI ACL
  `v0.1.17`, and `go-openai` `v0.1.2`, for exact middleware ordering,
  summarization, stream decoding, and cancellation behavior.

Codex product text, shell integration, cloud tasks, Git/worktree behavior,
MCP, plugins, subagents, side conversations, and its retry policy are not
Kupilot contracts.

## Decision

Kupilot adds bounded queue mutation, committed-content copy and search,
content-free context pressure, explicit compaction, fixed terminal status
titles, a one-shot plan-only AgentRun mode, and a typed claim/Evidence coverage
manifest. Application remains the sole authority for Session, run, queue,
generations, consent, budgets, compaction, plan mode, event acceptance, and
recovery. Eino remains the sole Agent/ReAct/conversation loop.

### Exact queue cancellation and clear

`queued`, `rejected`, and `recovered` are the only editable queue states.
`pending`, `committing`, `committed`, `unknown`, and the internal drain claim
cannot be cancelled or cleared. An exact cancel binds Session ID, item ID,
current scope generation, current policy generation, and the caller-observed
queue revision. A clear binds the same current Session and generations plus
the expected revision and removes exactly the editable set observed at its
atomic commit point. An editable recovered draft retains its prior run binding
for provenance, but that obsolete binding neither grants deletion authority
nor prevents a command bound to the current generations from discarding it.

The Application queue lock owns revision comparison, exact removal, clear,
edit-last, claim-for-commit, and claim-for-drain. Therefore cancel, clear,
edit-last, commitment, and automatic drain have one winner. A stale revision,
foreign Session or item, command-to-current-generation mismatch, or ineligible
state performs no mutation. Clear requires an explicit local confirmation when
at least one editable item exists. Delivery never removes optimistically.

Queue mutations perform no model, Kubernetes, Tool, Reviewer, executor, or
repository I/O. The queue remains current-process-only. `/status` exposes only
counts, aggregate bytes, lifecycle counts, limits, and revision.

### Committed-answer clipboard and transcript search

`/copy` is an explicit user action that selects only the latest successfully
committed assistant final answer in the currently visible Session transcript.
It excludes streaming, failed, cancelled, pending, queued, recovered,
composer, search, and working-area content. The sink is Bubble Tea's bounded
terminal-native OSC 52 clipboard command. Kupilot does not read the clipboard
or start a platform clipboard process. Support must be established from the
current terminal capability policy; otherwise the command reports
unavailable. ANSI, device, terminal, and bidirectional controls are removed.
The 65,536-byte limit is exact and one-over is rejected without truncation.

`/find` and `Ctrl+F` reuse the sole composer as a local query editor. They
search only bounded committed safe transcript entries currently held by the
TUI: at most 4 MiB, 4,096 entries, 512 query bytes, and 100 matches. The query
and match projection are current-interaction-only and never enter SQLite,
history, logs, errors, export, model input, or terminal shutdown transcript.
Match position is visible in text as well as styling, so safety and navigation
do not depend on color. Search supports valid Unicode and multiline entries;
narrow and `NO_COLOR` rendering keep the same bounds.

### Content-free context pressure

Context pressure is a deterministic projection of exact eligible safe Message
count and UTF-8 bytes, summary coverage, recent-tail count, and the published
message, history-byte, and summary-trigger limits. It is never called a token,
cost, or endpoint-context estimate. The fixed states are `normal`, `elevated`,
`critical`, and `degraded`. `degraded` has priority when storage or committed
coverage is unhealthy. Otherwise `critical` means either summary message/byte
trigger or 80 percent of an independent message/history-byte hard ceiling is
reached. `elevated` means 50 percent of any corresponding trigger or ceiling
is reached; lower usage is `normal`.

The footer may show only the compact state. `/status` may show its exact
content-free count, byte, coverage, tail, trigger, and ceiling basis.

### Explicit manual compaction

`/compact` creates one Application-owned compaction intent. It is admitted
only for the current Session when no AgentRun, approval, Reviewer review, or
execution is active and storage is healthy. Application freezes the exact
scope and policy generation, current `agent` profile and canonical origin
hash, current category consent, safe retained conversation selection, and the
independent Agent-summary budget limits before adapter I/O. The adapter owns
the atomic one-call reservation immediately before the summary request.

The adapter invokes the same Eino summarization handler used by automatic
compaction. The call is non-streaming, Tool-free, and one-attempt. It uses the
existing `agent` profile, summary instruction, byte limits, safe-message
source, complete-run coverage grammar, and 16 KiB result limit. It does not
create another Agent, Runner, summarizer, memory manager, checkpoint, copied
tail, or conversation store. Minimal mode compacts only eligible
current-process context and persists nothing. Standard mode atomically commits
only a fully validated newer summary and exact coverage; both same-process and
explicitly resumed Sessions use the same retained safe Message source.

Cancellation, timeout, stale scope or policy generation, changed profile or
origin, missing consent, exhausted summary budget, invalid summary, corrupt
coverage, or persistence failure preserves the prior committed summary and
Messages. It causes no subsequent ordinary Agent model call, Tool, Kubernetes,
Reviewer, approval, executor, queue submission, or automatic successor drain;
a failed summary attempt remains the sole possible model call. A no-op because
too little complete history is eligible is reported distinctly from success.

### One-shot plan-only mode

`/plan` arms the next ordinary input in the current process; `/plan off`
cancels the arm before run start. Both commands are fixed local Application
state changes and perform zero model, Kubernetes, Tool, Reviewer, approval,
or executor I/O. A successful run start consumes the arm and freezes
`plan_only` in its immutable `RunInput`; failures before start do not silently
convert another input to ordinary mode.

A plan-only run uses the same `ChatModelAgent`, `Runner`, ReAct state,
summarization handler, history selection, steering bridge, model profile,
consent, scope and policy generations, and budgets. Its Tool catalog is a
fixed subset of policy-admitted safe read Tools. Sensitive reads, mutations,
action proposals, `ActionEnvelope` construction, approval, Reviewer routing,
Pod Exec, diagnostic Pods, local processes, and shell are denied before their
model or executor boundary with zero such calls.

The terminal plan protocol is strict and typed: schema version 1, one title,
one through twelve ordered steps, and zero through eight limitations. Each
field and the complete result have fixed UTF-8 byte limits. Unknown,
duplicate, malformed, over-limit, action-bearing, stale, or unbound output
fails closed and is not committed as a successful answer. A validated plan is
rendered as ordinary safe assistant content; it mints no Evidence, decision,
approval, execution, Session rule, or future authority. Kupilot never executes
it automatically. A new explicit ordinary input is required. Existing active
steer semantics apply at the next model boundary. Only a clean successful plan
commit permits the existing one-item FIFO drain rule.

### Claim and Evidence coverage

Every new successful model final response carries a project-owned, typed,
bounded claim manifest. Each entry has a strict sequence, claim kind,
normalized claim text, SHA-256 text hash, ordered Evidence IDs, coverage state,
and runtime-bound Run ID, scope snapshot, and policy generation. Claim kinds
are `current_observation`, `inference`, `recommendation`, `uncertainty`, and
`unsupported_observation`. Coverage states are `verified`, `supported`,
`limited`, and `unsupported`.

A `current_observation` must be `verified` and cite at least one accepted
Evidence item from the same run, frozen scope, and policy generation. Evidence
IDs must be known, eligible, unique within each claim, and in acceptance
order. The same accepted Evidence may support more than one claim. Inference,
recommendation, uncertainty, and an explicitly unsupported observation remain
visibly distinct and cannot be promoted to verified observation by prose.
The validator rejects missing, unknown, duplicate, cross-run, cross-scope,
stale-generation, out-of-order, over-limit, or hash-mismatched coverage and
prevents the final answer from being committed as success.

Validated coverage is durable safe Diagnosis metadata in standard mode and is
deleted/exported with that Diagnosis. Legacy retained answers without this
field remain readable as historic, non-authoritative content; replay encodes
no historic Evidence authority. Minimal mode keeps coverage only in the
current process.

Deterministic coverage proves structure, provenance ownership, temporal and
generation binding, limits, and citation completeness for the declared
manifest. It does not prove that model reasoning, prose-to-manifest semantic
alignment, a recommendation, or an inference is correct. No approval Reviewer
or runtime answer critic is used.

A deterministic synthetic-fixture quality harness computes valid Evidence
reference rate, unsupported current-state claim rate, stale/cross-run
rejection, uncertainty or limitation handling, and response/citation bound
compliance. It performs no network or model calls. Its scripted scores are
only validator/evaluator evidence, never live model-quality evidence.

### Content-free terminal status titles

Terminal status titles are optional and enabled by one boolean configuration
field. The complete title allowlist is `Kupilot`, `Kupilot — Working`,
`Kupilot — Approval needed`, `Kupilot — Complete`, and `Kupilot — Failed`.
No dynamic suffix or interpolation is permitted.
Accepted current-run lifecycle state alone selects a title. Late, duplicate,
stale-generation, wrong-run, or terminal events cannot emit another change.

Kupilot uses terminal-native title control only and never starts a notification
process or external service. Delivery also requires a directly attached,
conservatively recognized title-capable terminal; multiplexers and unknown
terminals receive no title sequence. It saves the terminal title slot before
the TUI starts and restores it in the owning teardown path after normal exit,
cancellation, returned error, or panic unwinding. Title bytes do not enter the
transcript. User input, answer text, Session or scope names, resource names,
Evidence, commands, raw errors, and credentials are prohibited from the sink.

### Protocol-safe stream continuation

Production continuation is admitted only if an exact stable server protocol
and the pinned adapter together expose all of the following: a same-response
reattachment operation; a stable response identity; stable replay event
identity or monotonic offset; no second user/model request; binding to the
exact Session, Run, request, content hash, origin, scope and policy
generations, consent, and budget; deterministic deduplication of deltas, Tool
call/results, committed steer, and terminal events; and bounded cancellation,
idle, wall-time, byte, and attempt behavior.

That gate is not satisfied. The pinned extension sends a complete
Chat Completions request for each stream. Its SSE decoder recognizes `data:`
records and `[DONE]`, but exposes no SSE replay ID, sequence, offset,
`Last-Event-ID`, or same-response reattach method. A chunk response ID and the
optional HTTP `X-Request-ID` are diagnostic metadata, not replay identity.
Eino checkpoints resume Agent execution, not the same provider stream.
Codex Responses `previous_response_id` links a later request and likewise does
not prove same-response continuation.

The exact outcome is therefore **Protocol continuation unavailable**. Kupilot
adds no continuation interface, configuration, checkpoint, polling, event log,
vendor persistence, retry, or dependency update. Disconnect after endpoint
entry remains unknown/recovered under ADR-0048; automatic model, Tool, or
action retry remains prohibited.

### Retention, resume, export, and terminal surfaces

Queue drafts, composer/search query and matches, clipboard state, title state,
notification state, plan arm, compaction intent, and any provider response
handle are never persisted. A validated plan is an ordinary committed
assistant Message. Claim coverage is safe Diagnosis metadata, contains no
Evidence payload, and follows existing standard retention, Session
deletion, and evidence-expiry explanation. Resume restores neither plan mode,
queue authority, compaction authority, current Evidence, nor an active stream.
Resume itself continues to perform zero external operational I/O.

This decision amends ADR-0041 by advancing new exports to
`kupilot.export-summary.v3`. The v3 allowlist adds bounded claim kind, coverage
state, processed claim text and recomputed hash, same-run identifier, scope and
policy generations, and Evidence IDs. It adds no raw Evidence, model payload,
or authority. A plan remains exported through its ordinary committed assistant
answer and validated Diagnosis answer; the one-shot plan arm is never
exported. Existing v1 and v2 files remain standalone and are not imported or
rewritten.

Terminal scrollback and the user's clipboard remain external retention
surfaces. Search overlays, queue previews, composer text, footer, titles, and
notifications are excluded from the final restored transcript.

## Rejected alternatives

- A second plan Agent, ReAct loop, goal system, summarizer, context manager,
  checkpoint, or history store.
- Persisted drafts, search queries, clipboard data, terminal status, plan arm,
  compaction intent, or active response handles.
- Clipboard reads or `pbcopy`, `xclip`, PowerShell, or another process.
- Runtime answer criticism or reuse of `approval_reviewer` for answer quality.
- Treating tokens estimated from bytes, characters, or a model name as exact
  endpoint context evidence.
- Reissuing a model request, using `previous_response_id`, polling, or resuming
  an Eino checkpoint as stream continuation.

## Security and privacy impact

All new data sinks use explicit allowlists and fixed byte ceilings. Copy and
search exclude uncommitted content by construction. Title output is fixed and
content-free. Plan-only restrictions reduce authority before Tool and action
binding; they are independent of model prose. Manual compaction preserves the
last committed state on every failure. Claim coverage rejects invalid
provenance rather than silently deleting citations.

The clipboard, terminal title, terminal scrollback, and provider remain
external retention surfaces. No encryption, forensic deletion, semantic
correctness, network isolation, or live model quality claim follows from this
decision.

## Validation

Deterministic validation must cover:

- exact cancel and confirmed clear for zero, one, and many editable items;
  stale revision, foreign identity, generation mismatch, commit/drain/edit
  races, and zero external calls;
- copy source selection, unavailable transport, controls and bidi removal,
  Unicode/multiline, exact byte limits and one-over, and no clipboard read or
  process launch;
- repeated transcript search, next/previous/escape, source exclusions, query,
  scan and match limits plus one-over, narrow/resize, `NO_COLOR`, IME,
  selection, mouse, alternate screen, and unchanged shutdown transcript;
- all four context-pressure states and their exact byte/message/coverage basis;
- standard/minimal, same-process/resumed compaction success, no-op, busy,
  failure, timeout, cancellation, stale generations, consent, origin, budget,
  persistence rollback, Tool pairing, and no automatic run or queue drain;
- terminal title allowlist, disablement, exact-once state transition,
  stale/late/foreign event rejection, content canaries, and teardown restore;
- plan arm/off/one-shot freezing, safe reads, strict step and byte bounds,
  action-bearing/malformed denial, steer ordering, successful drain, unsafe
  no-drain, persistence/resume/export, and zero action/review/executor calls;
- valid and invalid claim manifests including every identity, hash, order,
  duplicate and one-over condition, plus deterministic synthetic quality
  metrics and proof of zero network/model calls;
- exact pinned-source compatibility proof for unavailable continuation,
  disconnect-to-unknown/recovered, zero automatic retry, restart behavior, and
  absence of dead continuation interfaces; and
- all repository tests, race tests, vet, lint, security, import, migration,
  build, and applicable cross-build gates.

Live model, Reviewer, Kubernetes, process, release, or deployment evidence is
outside this decision and cannot be inferred from deterministic fixtures.

## Consequences

Users gain precise current-process queue control and local transcript
affordances without expanding model or execution authority. Context pressure
is useful but deliberately does not pretend to be token accounting. Manual
compaction consumes an independent summary budget and may safely decline when
too little complete history exists. Plans are durable conversation content but
never resumable action authority. Strict coverage rejects more malformed model
answers instead of repairing their citations.

Same-response stream reconnection remains unavailable until an exact stable
protocol and pinned adapter satisfy every admission condition. An interrupted
response can therefore require a new explicit user request, which is safer
than an unprovable replay.

## Revisit triggers

- Eino changes `ChatModelAgent`, Runner, handler ordering, summarization,
  message-state, Tool pairing, or cancellation semantics.
- A stable pinned provider protocol exposes verifiable same-response reattach
  and replay identity or sequence.
- Exact endpoint tokenizer/context evidence becomes available.
- Clipboard or title capability detection cannot be made conservative for a
  supported terminal family.
- A new plan output, claim kind, model role, data category, or action authority
  is proposed.

## References

- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0040: Use a Codex-Style Conversational TUI](0040-use-a-codex-style-conversational-tui.md)
- [ADR-0041: Export Free-Form Session Summaries](0041-export-free-form-session-summaries.md)
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0048: Own Run Steering and Queued Follow-Up Input](0048-own-run-steering-and-queued-follow-up-input.md)
