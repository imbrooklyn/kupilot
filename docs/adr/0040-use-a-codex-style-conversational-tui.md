# ADR-0040: Use a Codex-Style Conversational TUI

- Status: Accepted
- Date: 2026-08-30
- Amended: 2026-08-31
- Supersedes: ADR-0023

## Context

The original TUI meets its supervision contract but looks like a form: user
messages and the composer use prominent rounded frames, the footer carries too
many permanent fields, and dark-theme secondary text can have poor contrast.
The fixed Diagnosis renderer further makes the conversation feel like a report
generator rather than an interactive Agent.

Codex CLI demonstrates a lower-chrome terminal conversation: a `›` composer,
terminal-adaptive primary text, restrained semantic color, compact inline
activity, and detailed session information behind `/status`.

## Decision

Kupilot keeps one conversational screen and one multiline composer, but adopts
these presentation rules:

- The composer has a bold `›` prompt and no full border. It grows from one to
  eight content rows, submits with `Enter`, and inserts a newline with
  `Shift+Enter`, `Alt+Enter`, or a distinguishable `Ctrl+J`. It uses subtle
  vertical spacing or background only when the terminal color mode can render
  it safely. The textarea uses the terminal's real cursor at the insertion
  point, while placeholder text is rendered separately. Operating-system input
  methods therefore receive a stable candidate-window position, and committed
  Unicode text is inserted exactly without placeholder overpainting or
  delivery-added spaces.
- User messages retain the `›` marker and use the same quiet surface and
  vertical spacing as the composer. Assistant Markdown is unframed and rendered
  into width-aware prose, lists, inert code, and readable tables. Wide tables
  use bold headers, a heavy header rule, light body rules, cell padding, and no
  outer or vertical border. Tables fall back to key/value records when a grid
  would starve its columns. Repeated role labels and fixed report headings are
  not added.
- Ordinary conversation uses the primary terminal buffer, not a full-session
  alternate screen. The TUI keeps streaming output, the composer, suggestions,
  and the footer in a compact managed frame. When a leading transcript block is
  immutable, it advances a monotonic commit boundary and schedules that safe
  rendered block once into terminal-owned scrollback. A user question waits
  for a subsequent accepted transcript item, normally the run start; an Agent
  block waits for terminal text, Tool steps, Evidence references, and elapsed
  time. Resize and duplicate events do not recommit it.
- Primary text uses the terminal's default foreground. Secondary information
  uses the terminal dim attribute instead of a low-contrast hard-coded gray.
  Cyan is the default accent, green means success, red means failure, and
  warning color is paired with text. Unknown-background and 16-color modes do
  not depend on custom RGB values. Automatic mode keeps foreground and surface
  on terminal defaults until a response arrives. Automatic and fixed RGB modes
  request the terminal background through Bubble Tea and derive the user
  surface from the actual background: 12% white over a dark background or 4%
  black over a light background. Applying that surface does not reset
  transcript, composer, Picker, or dialog state.
- Tool and action activity appears inline as compact `•` and `└` timeline rows.
  Purpose, result, partial state, and approval state remain available without a
  permanently expanded card. While the run is active, one separate live row
  renders `• Working (<duration> • esc to interrupt)`. Its bullet and `Working`
  label use a restrained local shimmer, and `Esc` sends the same typed cancel
  intent as `/cancel` when no dialog, Picker, or suggestion owns the key. On
  terminal runs, activity precedes a full-width separator and the final answer;
  the turn ends with a muted full-width
  `─ Worked for <duration> ─────────` separator.
- The persistent footer prioritizes the verified Kubernetes Context, working
  Namespace, and access/action state. Model configuration, Session identity,
  budget counters, retention mode, and detailed run state move to `/status`.
- `/status` is a local query. It performs no model or Kubernetes call and shows
  the current Session, model, scope generation, namespace-access policy,
  mutation availability, privacy mode, active run, and budget usage.
- Suggestions and bounded Pickers remain directly below the one composer. The
  product has no primary resource table, navigation tree, dashboard, YAML
  editor, raw log pane, shell, or kubectl mode.
- Kupilot does not enable terminal mouse reporting in the conversation view,
  so visible text remains available to native drag-selection and copy. Mouse
  wheel and trackpad gestures remain terminal-owned and are never interpreted
  as composer-history actions. `Page Up` and `Page Down` scroll the retained
  transcript; `Ctrl+P` and `Ctrl+N` explicitly recall submitted input. Plain
  arrow keys remain available to the multiline editor and bounded Pickers.
- `Page Up` temporarily re-renders the retained in-memory transcript at the
  current width, and `Page Down` returns to the live projection at the bottom.
  This review state and terminal scrollback do not restore an AgentRun,
  Evidence authority, approval, scope, or Session persistence.

Layout and palette behavior are derived from public Codex CLI documentation and
the open-source Codex TUI implementation, but Kupilot retains its Kubernetes
scope, consent, Evidence, and approval semantics.

## Consequences

The screen reads as an Agent conversation, uses less terminal chrome, and keeps
high-value Kubernetes context persistent without crowding every frame with
diagnostic metadata.

Golden fixtures must change, and terminal-background detection remains
imperfect. Unknown modes therefore prefer terminal defaults and semantic text
over assumptions about exact colors. Disabling application mouse reporting
trades direct wheel control of the retained viewport for terminal-native text
selection; deterministic `Page Up` and `Page Down` navigation remains
available for transcript content outside the visible screen.

Completed conversation remains available in normal terminal scrollback after
graceful exit. Already committed rows retain the width and palette used when
they were emitted; explicit in-process transcript review reflows from bounded
source state after a resize. Terminal scrollback is outside Kupilot deletion
and retention control, so switching or deleting Sessions does not erase rows
already displayed by the terminal.

## Security and privacy impact

Moving details to `/status` changes presentation only. The status result remains
a bounded Application DTO and contains no credential, kubeconfig, raw endpoint
authorization, raw Tool data, or live client. Scope, approval, and execution
meaning never depends on color alone.

Primary-screen commitment creates no new model, Kubernetes, Tool, executor,
SQLite, log, or audit call. Only normalized bounded render state is eligible;
partial model drafts, credentials, raw payloads, unsafe controls, clipboard
sequences, and model-selected styles remain excluded. Terminal-emulator
scrollback may outlive Kupilot and is disclosed as an external local retention
surface.

The real cursor and Working animation are delivery-only. Working ticks carry
the active run ID, scope generation, accepted sequence, and terminal snapshot;
stale, duplicate-time, mismatched, and post-terminal ticks are discarded. They
cannot advance runtime time, counters, progress, Evidence, approval, or any
external action.

## Validation

Golden and reducer tests must cover dark, light, ANSI-16, and `NO_COLOR` modes;
narrow and resized terminals; one-to-eight-row composer growth; tool and action
states and final ordering; multiline key aliases; real-cursor position with an
empty placeholder and committed Unicode input; exact absence of delivery-added
spaces; disabled mouse reporting, native-selection compatibility, and explicit
input-history shortcuts; borderless wide and record-fallback narrow Markdown
tables; compact duration formatting; stale and post-terminal Working-frame
rejection; `Esc` cancellation; inert links; status width; terminal controls;
and proof that `/status`, `Update`, and `View` cause no business I/O. Runtime
tests must also prove primary-screen operation, immutable-prefix ordering, no
commit of streaming drafts, no duplicate commit after resize or duplicate
events, retained keyboard review, and absence of alternate-screen control
sequences in ordinary conversation.

## References

- [Codex CLI](https://learn.chatgpt.com/docs/codex/cli)
- [Codex TUI style guide](https://github.com/openai/codex/blob/main/codex-rs/tui/styles.md)
- [Codex TUI semantic styles](https://github.com/openai/codex/blob/main/codex-rs/tui/src/style.rs)
- [Codex chat composer](https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/chat_composer.rs)
- [Codex status indicator](https://github.com/openai/codex/blob/main/codex-rs/tui/src/status_indicator_widget.rs)
- [Codex Markdown renderer](https://github.com/openai/codex/blob/main/codex-rs/tui/src/markdown_render.rs)
- [Codex final-message separators](https://github.com/openai/codex/blob/main/codex-rs/tui/src/history_cell/separators.rs)
- [Codex inline terminal runtime](https://github.com/openai/codex/blob/main/codex-rs/tui/src/tui.rs)
- [Product Contract](../product.md)
