# Agent Runtime

Kupilot runs one supervised ReAct-style AgentRun at a time. Eino is confined to
`internal/agent/einoadapter`; the rest of the system sees project-owned
messages, events, Tool calls, Evidence, and safe errors.

The current protocol versions are:

- System prompt: `kupilot-agent-policy-v4`
- Capability catalog: `kupilot-operational-tools-v1`

## Frozen run input

Application creates an immutable RunInput containing:

- run, Session, and user-message identifiers;
- normalized user question;
- verified Context, working Namespace, namespace-access policy, generation, and
  activation time;
- optional selected ResourceRef;
- exact prompt and capability-catalog versions;
- model-transfer consent already checked by Application; and
- one compact, balanced, or extended budget snapshot.

The model cannot supply or modify these values. A Context, working-Namespace,
or policy change invalidates the generation, cancels the old run, clears
resource/action state, and rejects late results.

## Turn lifecycle

1. Application durably creates the run before model or Kubernetes I/O.
2. Runtime atomically reserves one Agent step and one model call.
3. The model receives the trusted policy, safe conversation projection,
   current scope metadata, complete seven-capability catalog, and remaining
   code-owned ceilings.
4. The Eino boundary drains one bounded model stream and asks Eino to assemble
   exactly one assistant message. Candidate answer content remains hidden until
   the complete message and its metadata validate. Commentary accompanying a
   Tool selection is discarded and cannot authorize a Tool.
5. Complete indexed Tool calls are strictly decoded, canonicalized,
   budget-reserved, scope-injected, and dispatched through the fixed table.
6. The handler performs bounded typed I/O, projects and sanitizes locally, and
   creates Evidence only after the post-I/O scope gate.
7. Tool results return through a project-owned envelope and the loop continues
   until a final structured answer or a terminal policy outcome.
8. The final answer is validated, persisted according to privacy mode, and
   published to the transcript.

Runtime performs no automatic model or Kubernetes retry. A retry is admitted
only as another visible Agent decision, must remain within all budgets, and
cannot repeat an identical non-retryable call indefinitely.

## Capability binding

The catalog contains exactly the seven capabilities documented in
[Scope](scope.md). Every model schema is strict: all object properties are
declared, every property is required, optional values use explicit `null`, and
additional properties are rejected.

Binding performs these checks before handler I/O:

- known name and exact catalog version;
- one complete JSON object with no duplicate, unknown, wrong-type, or overlong
  field;
- code-defined Kind and API version;
- explicit namespace semantics under the frozen policy;
- no Context, endpoint, credential, raw selector, GVR, deadline, or hard-limit
  authority;
- canonical argument serialization and digest; and
- atomic run and per-capability budget reservation.

Malformed structured output, Tool-like prose, and unknown names cause zero
handler calls.

## Evidence and answer validation

Only accepted deterministic Tool results create Evidence. Every Evidence item
binds the run, invocation, scope generation, exact ResourceRef, category,
source path, observation time, safe fact, and partial/truncation/redaction
state.

The final wire object contains:

- `answer_markdown`;
- `evidence_citations`; and
- `proposed_actions`.

`answer_markdown` is bounded to 128 KiB before the complete Diagnosis ceiling
is applied. It is normalized, terminal-safe, sensitive-processed, and rendered
without mandatory headings. Citation IDs must exist in the same run; invalid
or duplicate references are removed and produce visible validation warnings.

The runtime cannot prove that prose semantically follows Evidence. Evidence
metadata improves traceability but does not turn model interpretation into a
verified fact.

## Proposed actions

The final response may contain at most one typed `restart_deployment` proposal.
It must target the exact `apps/v1` Deployment name in the working Namespace and
must not contain UID or resource version.

The proposal is not authority. Application asks a read-only trusted preparer to
perform one fresh exact Deployment GET and derive UID, Pod-template
fingerprint, and Deployment generation. Only that local intent may become a
pending approval request. Preparation failure leaves the otherwise valid answer
intact and performs no write.

Approval, execution, and rollout verification follow
[Deployment Restart Approval](user-guide/approval.md).

## Budget profiles

| Boundary | Compact | Balanced (default) | Extended | Hard ceiling |
| --- | ---: | ---: | ---: | ---: |
| AgentRun wall clock | 2 min | 10 min | 30 min | 30 min |
| Agent steps | 12 | 32 | 64 | 128 |
| Tool calls | 16 | 48 | 128 | 256 |
| Model calls | 6 | 16 | 32 | 64 |
| Model request | 60 sec | 120 sec | 300 sec | 300 sec |
| Kubernetes request | 15 sec | 30 sec | 60 sec | 60 sec |
| Cumulative Tool results | 1 MiB | 4 MiB | 12 MiB | 16 MiB |
| Pod-log calls | 4 | 12 | 32 | 32 |
| Consecutive no-progress steps | 2 | 4 | 6 | 10 |

The model and Tool request deadlines are additionally capped by the owning
run's remaining time. One ToolResult remains at most 64 KiB. Resource and Event
items, logs, Evidence, and relationship graphs retain their independent
ceilings.

Reservations happen before I/O. Completion accounts actual Tool-result bytes,
accepted Evidence progress, and retryability. Once stopped, a budget cannot be
reopened.

## Cancellation and stale work

Every model and Kubernetes operation accepts the owning Context. Runtime checks
the complete scope and generation before external I/O, after every return, and
again when Application accepts an event.

Cancellation, timeout, stale scope, terminal state, or budget exhaustion stops
new work. Late stream fragments, Tool results, Evidence, Diagnosis objects, and
approval proposals are discarded. Each goroutine has one owner, cancellation
path, and bounded join path.

## Event ordering and TUI

Application accepts monotonic run events with exact run ID, generation,
sequence, and terminal-state checks. The TUI receives project-owned UI events;
Bubble Tea does no business I/O in `Update` or `View`.

The internal run stream is capped at 32,768 ordered events. This is large enough
for the hard Tool, Evidence, and model budgets but remains independently finite;
the TUI receives a smaller projection because model-start and individual
Evidence-acceptance events are not rendered as transcript entries.

The transcript shows one code-authored validation-progress message, compact
Tool steps, safe warnings, the validated final Markdown answer, and typed
approval state. `/status` is a local Application query exposing the catalog,
namespace policy, budget profile and usage, run state, privacy mode, and storage
health without model or Kubernetes activity.

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
