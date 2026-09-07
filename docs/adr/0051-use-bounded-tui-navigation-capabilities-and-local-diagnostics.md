# ADR-0051: Use Bounded TUI Navigation, Capabilities, and Local Diagnostics

- Status: Accepted
- Date: 2026-09-07
- Amends: ADR-0025, ADR-0031, ADR-0038, ADR-0039, ADR-0040, ADR-0041, ADR-0043, ADR-0047, ADR-0048, ADR-0049

## Context

The conversational TUI already has one composer, committed transcript search,
Evidence detail, status, privacy controls, terminal-native copy, and fixed
status titles. Users still need content-free model-egress visibility, clear
command availability, local health diagnostics, submitted-input recall,
semantic transcript navigation, bounded editing recovery, and an accessibility
contract that does not rely on terminal color or motion.

Terminal capabilities differ across direct terminals, multiplexers, SSH,
`NO_COLOR`, and alternate-screen configurations. Guessing support can falsely
claim that private content was copied or that a notification was delivered.
Capability detection is a delivery concern and cannot become Application
authority.

The interaction reference is Codex CLI `rust-v0.153.4`, peeled commit
`3d2ee51ca2d5db578f328aa75e20aa22c0197c9a`. Kupilot adopts only bounded local
interaction ideas from its deletion confirmation, reverse history search,
copy, terminal handling, command-state, diagnostics, and reduced-motion tests.
It does not adopt shell, Git/worktree, remote task, MCP/plugin, subagent, image,
archive, or side-conversation surfaces.

## Decision

Kupilot extends the existing single-screen TUI. It adds no second editor,
dashboard, raw Evidence browser, transcript page, or delivery authority.

### Typed terminal reasons and safe next actions

Application projects one terminal reason from accepted lifecycle state:
`completed`, `cancelled`, `timed_out`, `failed`, `unknown`, `recovered`,
`persistence_degraded`, `stale_generation`, `insufficient_evidence`,
`source_unavailable`, `policy_denied`, `budget_exhausted`,
`conflicting_evidence`, `partial_result`, or `needs_user_input`.

Each reason has a fixed, typed, bounded list of safe next actions such as edit
recovered input, submit a new explicit request, review Evidence, restore
storage, change scope through its existing control, or wait for a local state
transition. TUI renders this projection and never infers retry, drain,
restore, resume, or execution authority. Optional clipboard, notification, or
title failure is not an Agent failure.

### Evidence navigation and provenance strip

A committed final answer exposes a bounded claim-to-Evidence index. Left and
Right select a claim; Up and Down select only its exact cited Evidence. The
existing Evidence detail returns the bounded claim numbers and fixed types that
reference the selected Evidence, and Enter returns to the exact claim selection.
Navigation uses IDs and accepted coverage metadata, never raw payload search.

Each committed assistant final has a low-chrome textual provenance strip with
Evidence count, observed-at range, frozen scope/policy generations, coverage
state (`complete`, `partial`, `truncated`, or `unavailable`), inference and
uncertainty presence, conflicting/superseded presence, and checked/not-checked
source counts. It contains no raw Evidence payload. Bounds and stable symbols
remain visible in `NO_COLOR`, ANSI-16, narrow, and screen-reader order.

### Content-free egress preflight

Immediately before each actual Agent model invocation, Application accepts one
run-bound preflight and publishes a content-free projection before allowing the
adapter call. It includes role, canonical origin hash, consent state, context
mode, exact message count and bytes, summary/recent-tail state, eligible data
categories, reserved budget facts, scope/policy generations, sink availability,
and ordinary/plan-only mode. It contains no question, prompt, history text,
resource value, credential, URL secret, or endpoint error. Rendering is an
acknowledgement only and grants no authority or approval.

### Fixed command availability

The compile-time Slash registry remains the only command registry. Every item
has one Application/TUI-projected state: `available`, `busy`,
`not_applicable`, `disabled`, or `unsupported_terminal`, plus a fixed bounded
content-free reason. State affects selection and dispatch but never registers a
new command. `/help` and completion list `/delete`, `/sessions`, and `/doctor`.

### Terminal capability profile

Delivery creates one run-local, non-persisted projection for native clipboard,
OSC 52, tmux/screen/SSH restriction, terminal title, fixed notification,
ANSI/`NO_COLOR`, alternate screen, and restored-scrollback strategy. It uses
only already available writer properties, fixed configuration, and a
conservative allowlist of environment tokens. It does not read the clipboard,
send terminal capability queries, inspect arbitrary environment values, or
launch `pbcopy`, `xclip`, PowerShell, or another process.

Copy and title report success only when the exact selected terminal-native
mechanism is admitted. Notification payload is limited to
`Working`, `Approval needed`, `Complete`, or `Failed`; title uses ADR-0049's
fixed allowlist. Duplicate, late, stale, terminal, or foreign-run events emit
nothing. Title updates are optional and configurable; this implementation keeps
terminal notifications disabled. The owning teardown restores the title and
committed scrollback on normal exit, cancel, error, or panic unwinding.

### Submitted-input reverse search

`Ctrl+R` opens or moves to an older match and `Alt+R` moves to a newer match.
The sole composer becomes the bounded query editor; `Enter` accepts and `Esc`
or `Ctrl+C` cancels and restores the exact pre-search draft. This is separate
from `Ctrl+F` transcript search, Up/Down submitted history, Alt+Up queue edit,
and model context. It searches only committed ordinary submitted input for the
visible Session. Pending, queued, rejected, recovered, Slash, secret, and
composer-only content are excluded. Resumed submitted history is populated
from committed user Messages but remains a delivery aid, not resumable model
authority. Query, snapshot, results, and traversal have fixed byte/item limits
and are never persisted.

### Semantic scrollback navigation

Fixed shortcuts jump over the committed transcript: `Alt+U`/`Alt+Shift+U` for
previous/next user Message, `Alt+A`/`Alt+Shift+A` for assistant final,
`Alt+F`/`Alt+Shift+F` for failure or unknown, `Alt+P`/`Alt+Shift+P` for approval,
and `Ctrl+E` for cited Evidence. Stable text identifies the selected landmark.
The interaction does not capture mouse selection, create a transcript page, or
include provisional rows.

### Composer undo and redo

`Alt+Z` and `Alt+Y` operate on bounded in-memory full-draft snapshots. The
stack holds at most 100 operations and 1 MiB total across undo and redo; an edit
that would exceed the stack budget evicts the oldest complete snapshot.
Snapshots occur only after accepted grapheme-safe composer edits; IME
composition is committed as one edit, and paste or multiline insertion is one
edit. Undo/redo is disabled in secret mode, pickers, modals, approvals,
reverse/transcript search, and queue restoration. A new edit clears redo.
Session switch, successful submit, current deletion, and shutdown clear both
stacks. No snapshot is persisted.

### Local redacted doctor

`/doctor` and `kupilot doctor` use a versioned typed health schema. They inspect
only application/build version; configuration schema and safe origin hash;
SQLite open/schema/migration/storage health; bounded Session counts and Last
active integrity; terminal capabilities; fixed feature availability; pinned
Eino/provider compatibility configuration; and pending recovery/degraded
states. They perform zero model, Kubernetes, Tool, Reviewer, approval, child
process, and executor I/O by default.

Doctor output never contains credentials, endpoint query/userinfo, arbitrary
environment values, absolute sensitive paths, Session titles, Messages,
Evidence payloads, SQL, driver errors, or raw external failures. CLI doctor
short-circuits before TUI and operational adapters. JSON output has an exact
schema version and concrete fields.

### Accessibility

Reduced motion disables working-animation frame changes while retaining a
stable `Working` label. `NO_COLOR` and ANSI-16 retain text/symbol meaning;
focus and selected rows use prefixes and labels, not color alone. Dynamic and
narrow resize preserve the single logical order: committed transcript,
current interaction, composer, then status hint. External Unicode is validated
and unsafe device/bidirectional controls are removed or visibly represented.
IME owns the real cursor. Title and notification can both be disabled.
Alternate-screen teardown restores only committed scrollback and excludes
search UI, picker rows, previews, composer, footer, notification, and duplicate
transcript rows.

## Retention and privacy impact

Terminal profile, searches, undo/redo, semantic selection, command state,
doctor projection, egress preflight, and notification dedupe are current-run or
current-interaction only. They do not enter SQLite, logs, exports, model input,
or audit payloads. Terminal scrollback and clipboard remain external retention
surfaces accurately disclosed by the privacy contract.

## Rejected alternatives

- A second editor, transcript browser, dashboard, raw Evidence pane, or generic
  command palette.
- Clipboard reads, terminal capability queries, arbitrary environment capture,
  or external notification/clipboard processes.
- Persisted undo, search, terminal profile, notification, or navigation state.
- Color-only, animation-only, or pointer-only safety meaning.

## Validation

Deterministic tests cover every terminal reason and next action; bidirectional
claim/Evidence navigation; provenance bounds and negative coverage; preflight
ordering and redaction; every command availability state; capability matrices;
reverse-search traversal, accept/cancel/draft restore and exclusions; semantic
jumps; undo/redo with Unicode, IME, paste, multiline, history and queue edit;
doctor success/failure/JSON/redaction/zero external calls; notification and
title dedupe/teardown; `NO_COLOR`, ANSI-16, reduced motion, narrow/resize,
alternate screen, logical order, stale/late/foreign events, shutdown transcript,
and sensitive canaries.

## Consequences

The single screen becomes more inspectable and recoverable without adding
authority or persisted UI state. Conservative terminal detection may report an
optional feature as unsupported even when a terminal could technically accept
it; that is preferable to a false success claim.
