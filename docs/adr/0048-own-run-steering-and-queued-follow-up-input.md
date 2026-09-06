# ADR-0048: Own Run Steering and Queued Follow-Up Input

- Status: Accepted
- Date: 2026-09-06
- Amends: ADR-0040, ADR-0043, ADR-0047

## Context

Kupilot permits one active `AgentRun` in one verified Kubernetes scope. Before
this decision, ordinary composer input could start only an idle run. A user who
learned something while a regular run was streaming or executing a Tool had to
wait, cancel, or keep a private draft. That made conversational correction and
the natural sequencing of follow-up work unnecessarily fragile.

OpenAI Codex distinguishes acceptance of steering input from commitment at a
later model boundary. It also preserves follow-up input across connection and
submission failures instead of treating an acknowledgement as proof that a
model consumed the input. Those interaction semantics are useful, but Codex's
protocol, outer turn loop, shell and slash queueing, multi-Agent mailbox,
runtime keymap, and recovery policy are not Kupilot contracts.

Eino v0.9.19 retains the stable `ChatModelAgent`, `Runner`, message-state,
Tool-pairing, handler, and summarization APIs already used by Kupilot. Its
`BeforeModelRewriteState` handler runs before each actual model invocation and
persists returned state. Its `WrapModel` handler wraps the later model I/O.
Eino's `TurnLoop`, however, is an additional buffered outer turn loop with its
own checkpoint and preemption semantics. Preemption can discard consumed
items; it is not a product-level user-input commit protocol.

Kupilot therefore needs a narrow run-local bridge at the existing Eino
boundary while keeping queue, generation, persistence, and commit authority in
Application.

## Decision

Kupilot adopts active-run steering, process-local queued follow-up input, and
edit-last for ordinary chat input. It also upgrades
`github.com/cloudwego/eino` from v0.9.13 to exactly v0.9.19. The Eino OpenAI
extension and unrelated dependencies do not change without separate evidence.

This decision preserves one active `AgentRun`. Queued follow-ups are successor
runs, never concurrent runs. Application is the sole owner of input identity,
queue ordering, lifecycle transitions, generation validity, commit authority,
durable Message intent, and automatic-drain eligibility.

Eino remains the sole owner of the in-run `ChatModelAgent`, `Runner`, ReAct
iteration, Tool call/result pairing, message state, and summarization
middleware. Eino and provider types remain confined to
`internal/agent/einoadapter`.

### Fixed input gestures

The fixed low-chrome composer behavior is:

- while idle, `Enter` submits one ordinary question and `Tab` remains a
  completion key or no-op;
- during a regular active run, with no picker, completion, modal, approval,
  Reviewer, or other local interaction, `Enter` submits ordinary chat text as
  one pending steer;
- in that same active state, `Tab` adds ordinary chat text to the FIFO
  follow-up queue;
- `Alt+Up`, only with an empty composer and no owning local interaction,
  atomically removes the newest editable queued, rejected, or recovered item
  and restores it to the composer;
- `Shift+Left` is not an edit-last fallback because it conflicts with text
  selection, input methods, and cursor behavior;
- a single-leading-slash entry uses the fixed local Slash registry and never
  enters steering or the queue; `//` is ordinary chat; and `!` input remains
  rejected with no shell fallback.

Existing cancellation, input-method, paste, multiline, selection, history,
mouse, cursor, and exit precedence remains authoritative ahead of these new
gestures.

### Input identity and bounds

Every admitted in-memory item contains a project-owned item/request ID, Session
ID, the exact active Run ID for a steer, the complete scope snapshot and scope
generation, policy generation, normalized terminal-safe text, SHA-256 content
hash, creation time, queue revision, and explicit lifecycle state.

The queue is bounded to eight items, 65,536 UTF-8 bytes per item, and 262,144
aggregate UTF-8 bytes after normalization and sensitive-output handling. It
lives only for the current process and current accepted Session. A pending
steer is additionally bounded by the active run's finite lifetime. No model,
Tool, user prose, or UI event can raise a ceiling.

The working-area preview is bounded before wrapping to four items and two
lines per item. `/status` exposes counts, aggregate bytes, lifecycle counts,
run identity, and generations without content. The footer remains compact and
does not become a queue dashboard.

### Steering lifecycle and commit barrier

Steering uses the following explicit states:

1. `pending`: Application accepted and owns the input for one exact active run.
   No durable conversation Message exists and the model has not consumed it.
2. `committing`: the next Eino model-invocation boundary atomically claimed the
   still-current item. Editing and duplicate claims are prohibited.
3. `committed`: Application appended the safe user Message for the active run
   (durably in standard mode and to the process-local context in minimal mode)
   before model I/O begins. The corresponding UI event is a notification, not
   commit authority: if delivery rejects it, the known committed state remains
   and the model call is blocked. Commitment is single-use and does not mean
   the model call succeeded.
4. `rejected`: a known validation or precommit claim-notification barrier
   refused the item before a Message commit. The model call count remains zero
   for that invocation boundary, and the ordinary input is editable. A request-
   budget, persistence, consent, generation, cancellation, timeout, or other
   authority failure after claim is instead `recovered`.
5. `unknown`: model I/O may have begun but its outcome is not known. The
   committed Message is not automatically retried, requeued, or made editable.
6. `recovered`: an uncommitted item survived an unsafe terminal condition or
   invalidation. It is editable but never automatically sent.

Acceptance is not commitment. A TUI acknowledgement may display `pending` but
must not optimistically append a transcript row. Only the Application commit
event appends the user Message once.

At each Eino model boundary, handlers run in this order:

1. Eino summarization evaluates and, when required, commits its safe summary;
2. the run-local steering handler atomically claims at most one current pending
   steer in `BeforeModelRewriteState` and appends one Eino user Message;
3. Eino persists the returned message state;
4. the same handler's `WrapModel` runs the Application commit barrier before
   delegating to the actual model endpoint;
5. a commit, scope, policy, consent, budget, persistence, or event-sink failure
   returns an error without delegating, so the actual model call count is zero;
   an uncommitted claimed item is recovered, while failure to deliver the
   notification after a successful append leaves it known `committed`.

The barrier is single-use and rejects wrapper re-entry without another Message
write. A concurrent invalidation after a standard-mode Message append remains
a known commit and blocks model I/O; `unknown` is reserved for failure after
the wrapped endpoint was actually entered. After model I/O starts, a transport
failure is an unknown committed outcome and cannot authorize an automatic
retry. Kupilot configures no Eino model retry or failover path.

Steering never mutates a request already sent to an HTTP endpoint. Input is
visible only to the next model invocation assembled after commitment. Tool
call/result pairs already in Eino state remain ordered and intact.

### Queue, edit, and automatic drain

Ordinary follow-ups execute FIFO. Edit-last removes eligible input LIFO. The
Application lock that owns the queue revision also owns claim-for-drain and
edit, so exactly one side wins an edit-versus-drain race.

After a regular run reaches a clean successful terminal state and its final
assistant Message, Diagnosis, audit, summary updates, and terminal state are
durably committed, Application may atomically claim and automatically start
exactly one FIFO follow-up. Further items wait for that successor run to finish
under the same rule.

No automatic send occurs after failure, cancellation, timeout, stale scope or
policy, degraded persistence, rejected terminal persistence, ambiguous or
unknown model outcome, shutdown, or invalidation. A final answer with no later
model boundary converts unclaimed pending steer input into ordinary successor
queue input after a clean completion. The same input becomes `recovered` after
an unsafe terminal condition. Neither path loses or duplicates input.

### Invalidation

A scope, namespace-access policy, capability policy, permission profile, model
role or profile, canonical origin, consent, or relevant budget change first
advances its owning generation or validity state, invalidates dependent
pending/committing/queued authority, and then cancels old work. Items are never
retargeted to a new scope, policy, profile, origin, or consent grant.

Pending work invalidated before commitment becomes recovered. A commitment
that crossed the model-I/O boundary may become unknown but cannot be replayed.
Late, duplicate, stale-generation, wrong-run, and out-of-order adapter or TUI
events are rejected without changing transcript or queue ownership.

### Durable Session context

The process-local queue is not resumable authority and is not model history.
Pending, committing, rejected, recovered, and queued drafts are absent from
SQLite, Session export, retention summaries, and the next model request. The
`unknown` lifecycle itself is likewise process-local, but it can exist only
after its safe user Message was committed; that ordinary Message follows the
existing retention and export rules while its incomplete or failed run group
remains ineligible for model replay. Composer and working-area preview content
remains an external terminal retention surface while displayed.

One completed run may now contribute this ordered durable conversation shape:

1. one initial committed user Message;
2. zero or more committed steer user Messages in commit order; and
3. one final committed assistant Message.

These remain rows in the existing `messages` table associated with the same
run. A forward-only checksummed migration adds only the explicit ordering
constraint needed for this shape. Kupilot does not add a run-input log, raw
Eino transcript, framework event store, or second conversation store.

Eligible Session selection accepts only complete successful run groups with
the exact shape above. Summary coverage may end only at a complete-run
boundary. Standard, minimal, same-process, explicit resume, recent-tail, and
compaction paths preserve commit order and place every current run input in
the final Eino state exactly once. Historic scope, Evidence, approval,
Reviewer, action, Tool, and execution state remains non-authoritative.

Resume itself continues to perform zero model, Kubernetes, Tool, Reviewer,
approval, process, or executor I/O. A resumed historic scope is only a
candidate. A matching scope already independently verified in the current
process may supply current authority; otherwise the candidate enters the scope
picker and only an explicit picker selection may begin independent activation.
Unavailable or conflicting candidates never silently fall back.

New Sessions receive the configured default Context and Namespace as a
candidate and use the normal current-scope activation path before any run.

### Retention, deletion, and export

Only committed Messages follow the existing standard/minimal persistence,
retention, deletion, and export contracts. Minimal mode retains the same
process-local safe conversation projection but persists no model memory.
Deleting or ending a Session drops its in-memory queue. Exports contain no
queue content or queue authority. SQLite deletion is logical, not forensic,
and terminal scrollback, backups, provider retention, and user-copied text
remain external surfaces.

## Rejected alternatives

- Eino `TurnLoop` is rejected because it would become a second outer
  conversation and queue owner with checkpoint and preemption semantics that
  do not prove product commitment.
- A second Agent/ReAct loop, generic checkpoint, framework-neutral Agent or
  memory facade, custom summarizer, and second durable conversation store are
  rejected by ADR-0043 and ADR-0047.
- Modifying an already-sent HTTP request is rejected; only the next Eino model
  boundary can consume a steer.
- Optimistic transcript insertion is rejected because it creates ghost or
  duplicate Messages on rejection and failure.
- Automatic retry or queue drain after an unsafe terminal outcome is rejected
  because it can duplicate model transfer or side effects.
- Persisting queued drafts is rejected because it would create resumable
  authority and a second durable input log.

## Security and privacy impact

Queue text is untrusted external input. Admission uses the existing Unicode,
terminal-control, bidirectional-control, sensitive-output, and byte-bound
pipeline before state or preview. Content may appear only in the composer,
bounded working preview, a committed Message, or the exact model request after
all consent and generation checks. `/status`, logs, safe errors, audit,
credentials, child environments, SQLite queue state, and exports receive no
draft content.

The commit barrier is a new model-transfer authorization point. It must recheck
the exact run, Session, scope and policy generations, profile, role, canonical
origin, consent categories, persistence health, and remaining immutable model
budget before any I/O. Denial tests assert zero model, Kubernetes, Tool,
Reviewer, repository where applicable, process, and executor calls.

## Validation

Deterministic tests must cover:

- Eino v0.9.19 request, stream, Tool, structured-final, handler ordering, and
  summarization compatibility;
- Enter steering before a model call and while model streaming, Tool execution,
  and final streaming are active;
- pending to committing to committed, rejection, no-next-boundary fallback,
  unknown, recovered, late, duplicate, wrong-run, and out-of-order events;
- FIFO queueing, one-at-a-time clean drain, LIFO edit, non-empty composer,
  active interaction, exact limits plus one-over, and edit-versus-drain races;
- cancellation, timeout, transport failure, persistence failure, sink
  rejection, generation invalidation, and zero model calls on every precommit
  failure;
- no automatic send after failure, cancellation, timeout, stale state,
  degraded persistence, or unknown outcome;
- exact Eino messages with every active input once, intact Tool pairs, summary
  ordering, recent-tail and coverage integrity, and summary failure gates;
- standard and minimal memory, same-process and accepted resume, restart, long
  history, compaction, retention, export, and deletion;
- new-Session default scope, historic resume candidate, independently verified
  current-scope reuse, picker fallback, and resume-command zero external I/O;
- TUI reducer, rendering, terminal, IME, multiline, history, selection, mouse,
  narrow/resize, no-color, alternate-screen, and restored-scrollback behavior;
  and
- sensitive canaries, import guards, migration failure rollback, no Eino type
  outside the adapter, and no production use of Eino `TurnLoop` or a generic
  checkpoint.

Live model, Reviewer, process, or Kubernetes integration is optional evidence
for one exact target and is not required or authorized by this decision.

## Consequences

Users can correct an active regular run and stage bounded successor questions
without creating concurrent authority. The distinction between draft
ownership, durable commitment, and uncertain model delivery is visible and
testable. Session replay gains a grouped run grammar while retaining one safe
Message source and no historic Kubernetes authority.

Application and the Eino adapter gain a small amount of run-local coordination
and a forward migration. The safer recovery policy can require explicit user
editing and resubmission after failures, but it avoids silent duplicate model
requests and operations.

## Revisit triggers

- Eino changes handler ordering, state persistence, retry behavior,
  `ChatModelAgent`, `Runner`, Tool pairing, or summarization semantics.
- Product requirements need durable offline drafts, cross-device queues,
  concurrent AgentRuns, or steering of non-regular interactions.
- A stable Eino runner-managed Session passes every ADR-0047 replacement gate.
- A new input source, model role, origin, consent category, or automatic-retry
  policy is proposed.

## References

- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
- [ADR-0039: Use Configurable Runtime Budget Profiles](0039-use-configurable-runtime-budget-profiles.md)
- [ADR-0040: Use a Codex-Style Conversational TUI](0040-use-a-codex-style-conversational-tui.md)
- [ADR-0043: Use One Eino Runtime Boundary](0043-use-one-eino-runtime-boundary.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [OpenAI Responses steering reference](https://developers.openai.com/api/reference/cli/resources/beta/subresources/responses)
- [Eino v0.9.19](https://github.com/cloudwego/eino/releases/tag/v0.9.19)
