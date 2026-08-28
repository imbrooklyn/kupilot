# ADR-0023: Use a Single-Screen Agent-Supervision TUI

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot is Agent-first, but a chat-only terminal can hide which cluster is
active, what the Agent is reading, whether output is partial, and why a result is
missing. A resource-first TUI would create the opposite problem: browsing and
direct manipulation would become a parallel Kubernetes interface outside the
Agent and Tool contracts.

The TUI must help the user establish context and supervise bounded work without
owning use cases or authority.

## Decision

KuPilot will provide one Bubble Tea v2 TUI organized around Agent supervision.
It uses a low-chrome single screen with this fixed vertical order:

1. A continuous scrollable transcript. Historic user input reuses the composer
   surface as a non-editable block; Agent prose is unframed and neither side has
   a repeated role label.
2. Exactly one bottom multiline composer, three through eight rows, with `Enter`
   to submit and `Ctrl+J` for a newline. It may retain a draft during a run but
   does not steer or queue work for that run.
3. An optional untitled suggestion or Picker list directly below the composer,
   with at most eight displayed completion candidates. Every Picker reuses the
   same composer text and no second editor or search box exists.
4. A scope footer of at most two rows, prioritizing Context, Namespace,
   read-only or approval state, ResourceRef, run, model, and privacy state in
   that order as width permits.

Its supervised content includes:

- Always-visible active Context, Namespace, scope state, and privacy mode.
- Session and conversation view with one natural-language question input.
- Optional bounded Resource Picker for Pod, Deployment, ReplicaSet, Job, or
  Service in the current Namespace.
- Ordered ToolInvocation timeline showing fixed Tool name, bounded purpose,
  pending/running/succeeded/partial/denied/failed state, safe summary, and
  truncation or permission gaps.
- Streaming assistant progress that is visibly provisional until a validated
  final Diagnosis replaces it.
- Cancellation, recoverable safe errors, persistence-degraded state, and clear
  new-versus-resumed Session state.
- Informed-consent display before the first eligible model transfer.
- In `v0.2` only, a dedicated default-reject Approval Dialog driven entirely by
  typed Application approval state.

The compile-time Slash registry is limited to `/help`, `/model`, `/context`,
`/namespace` (`/ns`), `/resource` (`/res`), `/status`, `/new`, `/resume`,
`/rename`, `/privacy`, `/cancel`, and `/quit` (`/exit`). A Slash command is a
typed local or Application command, never a model message or dynamic extension.
Unknown commands and `!` syntax perform no external action. A leading `//`
escapes a literal slash for ordinary chat.

When the model endpoint, model identifier, or credential is absent, the same
single-screen TUI opens a fixed model-setup flow before a question can start a
run. `/model` opens that flow again. It reuses the one composer for endpoint,
model identifier, and credential input; the credential step is masked and its
value is excluded from transcript, draft history, completion requests,
Application events, errors, and rendering. The user explicitly chooses either
local plaintext persistence under the KuPilot Home or process-only use after
seeing that local storage is not encrypted. Model reconfiguration cancels and
joins an active run before Application replaces the single runtime and
re-evaluates origin-bound consent.

The Resource Picker is a bounded input aid, not an inventory or navigation tree.
It has no full YAML, raw log view, Watch, live dashboard, action menu, write
shortcut, shell, kubectl, or arbitrary filter. Selecting a ResourceRef does not
create Evidence or prove existence.

TUI state is a projection of Application events. Commands and completions carry
expected scope generation or request identity where stale work is possible.
`Update` and `View` perform no business I/O. Long work remains owned and
cancelled by Application. Text deltas may be coalesced while structural Tool and
terminal events cannot be dropped.

Only local code chooses style and layout. Semantic dark, light, automatic,
16-color, and `NO_COLOR` behavior keeps meaning independent of color. All
external text is normalized, bounded, and terminal-safe before it enters render
state. Critical scope, consent, approval, execution, and verification meaning is
never conveyed by color alone.

## Consequences

Positive consequences:

- Users can supervise data collection and see uncertainty and scope continuously.
- The product remains centered on one diagnostic question rather than resource
  management.
- Stale UI messages and provisional streams have explicit identities and states.
- Consent, degraded persistence, and future approval are visible rather than
  implicit side effects.

Costs and constraints:

- The TUI needs careful state-machine, narrow-terminal, keyboard, paste,
  accessibility, and shutdown tests.
- It does not provide the broad navigation expected from a Kubernetes dashboard.
- Mapping neutral events into delivery messages creates deliberate boilerplate.
- Terminal rendering cannot be treated as harmless presentation code.

## Alternatives considered

- A resource table with an assistant pane was rejected because it makes browsing
  and direct resource state a second product center.
- Chat-only output was rejected because it hides Tool supervision, scope, gaps,
  and provisional state.
- Letting the TUI call repositories or Kubernetes directly was rejected because
  it creates a competing use-case and authorization path.
- A web UI was rejected because it introduces listeners, browser security,
  process boundaries, and a different distribution model.

## Security and privacy impact

The TUI cannot authorize Tools, create Evidence, select a model endpoint from
external text, or execute a write. The future Approval Dialog returns typed user
intent but the Application validates digest, nonce, TTL, scope, target, durable
audit, and one-time state.

Context and resource display names can be sensitive. The TUI shows only bounded
safe fields and never raw kubeconfig, credentials, raw errors, raw objects,
unprocessed container output, or SQLite rows. Terminal control sequences from
every external source are removed or visibly replaced.

## Validation

Reducer and state tests plus golden rendering fixtures must cover:

- New Session, explicit resume, scope activation/switch/failure, question,
  streaming, each Tool state, cancellation, timeout, stale scope, final
  Diagnosis, and persistence degradation.
- Request identity, generation, sequence, duplicate, late, and terminal-event
  rejection.
- Narrow and resized terminals, empty and large content, paste, keyboard focus,
  supported signals, three-through-eight-row composer behavior, one-editor
  invariants, suggestion placement, and terminal restoration.
- Invalid UTF-8, escape, C0/C1, operating-system command, device-control,
  bidirectional, combining, wide, and oversized text fixtures.
- No direct imports or calls from TUI to model, Eino, Tools, Kubernetes,
  persistence, or a future executor.
- Fixed Slash registry, aliases, `//` escaping, unknown command, disabled state,
  and proof that commands do not enter the model or bypass Application.
- `v0.2` approval spoof, expiry, rejection, replay, degraded audit, request
  acceptance, unknown outcome, and verification states before that version is
  released.

Bubble Tea v2 integration must satisfy ADR-0005's compatibility and lifecycle
requirements.

## Revisit triggers

- User evaluation shows that supervision information cannot be understood in
  the terminal while preserving Agent-first interaction.
- A proposed context aid passes the feature admission gate without becoming a
  general resource interface.
- The product leaves the terminal or adds a second delivery surface through a
  separate architecture decision.

## References

- [Product Contract](../product.md)
- [Scope](../scope.md)
- [Security Threat Model](../security.md)
- [ADR-0003: Keep the Product Agent-First](0003-agent-first-interaction.md)
- [ADR-0005: Use Bubble Tea v2 for the TUI Runtime](0005-use-bubble-tea-v2.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
