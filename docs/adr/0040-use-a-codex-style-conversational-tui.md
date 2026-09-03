# ADR-0040: Use a Codex-Style Conversational TUI

- Status: Accepted
- Date: 2026-08-30
- Amended: 2026-09-02
- Supersedes: ADR-0023
- Amended by: ADR-0044 and ADR-0045

ADR-0044 and ADR-0045 make permission selection, Reviewer delegation, exact
approval, execution outcome, and verification P0 interactions for `v0.5`.
`/permissions` and the expanded `/status` remain local bounded controls on the
same low-chrome Agent-first screen. They do not add a resource browser, action
dashboard, shell console, or second editor.

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
  it safely. Only the first visual row renders `›`; explicit and soft-wrapped
  continuation rows reserve the same prompt width so their text stays aligned.
  The textarea uses the terminal's real cursor at the insertion point, while
  placeholder text is rendered separately. Operating-system input methods
  therefore receive a stable candidate-window position, and committed Unicode
  text is inserted exactly without placeholder overpainting or delivery-added
  spaces. When the composer is repurposed for a multi-step command or bounded
  Picker, a short code-authored field label is rendered directly above it and
  remains visible after typing; placeholders remain hints rather than labels.
- User messages retain the `›` marker and use the same quiet surface and
  vertical spacing as the composer. Assistant Markdown is unframed and rendered
  into width-aware prose, lists, inert code, and readable tables. Wide tables
  use bold headers, a heavy header rule, light body rules, cell padding, and no
  outer or vertical border. Tables fall back to key/value records when a grid
  would starve its columns. Repeated role labels and fixed report headings are
  not added.
- Ordinary conversation starts by clearing the visible primary-terminal frame
  and filling its height, without erasing prior terminal scrollback. The TUI
  leaves readable vertical gaps and one terminal-safe right wrapping column,
  keeps output above a bottom-anchored composer, and retains suggestions and
  the footer below it. User history and the composer use the same left origin,
  with no additional root-level horizontal inset.
  A delivery-only runtime wrapper prepares each newly immutable terminal-safe
  history block, removes it from the live Bubble Tea projection, and shrinks
  that projection before insertion. It waits for the compact frame to settle,
  then inserts the block in batches no taller than the terminal's protected
  history area. Every block ends with exactly one inert separator row; this
  separates submitted history from the live Working row and a final `Worked
  for` row from the composer without committing transient layout spacers. A
  block is acknowledged only after all rows have been handed to the renderer.
  If a terminal is too short to reserve an insertion row, the block remains in
  the safe live and pending projection rather than risking duplicated history.
  Eligible blocks include completed user, notice, Tool, Evidence, answer, and
  elapsed-time entries. The live frame retains
  provisional Agent output, Working state, the composer and its draft or
  placeholder, suggestions, dialogs, and the footer; none of those provisional
  or chrome surfaces enters terminal scrollback.
  The bounded source transcript remains in memory for explicit keyboard review.
  On shutdown, the composition root clears only the remaining live frame and
  writes a bounded pending-history fallback only if a completed block was not
  already acknowledged and insertion had not begun. Once any batch may have
  reached scrollback, shutdown does not replay the whole ambiguous block and
  risk a permanent duplicate; the bounded source remains subject to the
  configured Session-persistence policy.
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
- The focused composer uses one real terminal cursor at the insertion point so
  operating-system input methods receive a stable location. Like Codex, its
  shape, color, and blink policy remain the user's terminal defaults. Before a
  composer-bearing primary-screen frame reaches Bubble Tea, Kupilot bounds
  every section to the accepted width and clips any excess rows itself around
  that insertion point; renderer-side top clipping therefore cannot leave the
  cursor at an obsolete row. Modal interactions hide the background composer
  cursor.
- Tool and action activity appears inline as compact `•` and `└` timeline rows.
  Purpose, result, partial state, and approval state remain available without a
  permanently expanded card. While the run is active, one separate live row
  renders `• Working (<duration> • esc to interrupt)`. Its bullet and `Working`
  label use a restrained local shimmer, and `Esc` sends the same typed cancel
  intent as `/cancel` when no dialog, Picker, or suggestion owns the key. On
  final-answer turns, safe provisional Markdown grows in the active Agent entry
  while Working remains visible. Delivery follows the newest output when the
  user is at the live bottom and preserves an explicit Page Up review position.
  A requested Tool clears any pre-Tool provisional prose, and a terminal event
  replaces the remaining draft instead of appending a duplicate answer. On
  terminal runs, activity precedes a full-width separator and the final answer;
  the turn ends with a muted full-width
  `─ Worked for <duration> ─────────` separator.
  Entering or leaving Working state may resize the transcript, but a live
  transcript remains attached to its bottom so the complete submitted-user
  surface and newest Agent output stay visible. Explicit Page Up review remains
  anchored and is not forced back to the bottom by reflow, animation ticks, or
  later stream events.
- The persistent footer prioritizes the verified Kubernetes Context, working
  Namespace, and access/action state. Labels and separators are secondary,
  Context and Namespace values use the accent, and verified supervision uses
  the success semantic; the same meaning remains explicit in no-color mode.
  Model configuration, Session identity, budget counters, retention mode, and
  detailed run state move to `/status`.
- `/status` is a local query. It performs no model or Kubernetes call and shows
  the current Session, model, scope generation, namespace-access policy,
  mutation availability, privacy mode, active run, and budget usage in aligned
  Session, Scope, Run, and Budget groups.
- Suggestions and bounded Pickers remain directly below the one composer. The
  product has no primary resource table, navigation tree, dashboard, YAML
  editor, raw log pane, shell, or kubectl mode.
- Kupilot never enables terminal mouse reporting and does not install a mouse
  callback. Drag selection, copy, wheel movement, trackpad momentum, and their
  event density remain terminal-owned. Mouse input therefore never enters the
  reducer and cannot recall or mutate submitted-input history. `Page Up` and
  `Page Down` provide an explicit retained-transcript review fallback. In the
  ordinary composer, `Up` enters submitted-input history
  from an empty draft. After recall, `Up` and `Down` continue history navigation
  only while the recalled text is unchanged and the cursor is at the beginning
  or end of the complete input; otherwise they retain normal multiline cursor
  behavior. Bounded Pickers own their arrow keys, and repurposed fields such as
  model setup and export never read ordinary input history. Only `Up` and
  `Down` navigate ordinary submitted-input history. After
  Application accepts an explicit Session resume, the composer replaces its
  current submitted-input history with the resumed Session's bounded,
  terminal-safe user messages in committed order. Assistant messages, system
  notices, and Evidence references never become editable input history. A
  staged, rejected, failed, or cancelled
  resume leaves the current input history unchanged.
  `Ctrl+C` is routed to the active local interaction before the ordinary
  composer or process. Model setup, bounded Pickers, observation detail,
  privacy, export, deletion, resume-scope conflict, and approval interactions
  therefore cancel or reject safely without first clearing their field or
  exiting. With no child interaction, `Ctrl+C` clears a non-empty ordinary
  composer first; only a later `Ctrl+C` with no draft follows the quit or
  active-run cancellation path.
- `Page Up` scrolls the retained in-memory transcript at the current width, and
  `Page Down` returns to the live projection at the bottom. This review state
  and the post-exit terminal transcript do not restore an AgentRun,
  Evidence authority, approval, scope, or Session persistence.

Layout and palette behavior are derived from public Codex CLI documentation and
the open-source Codex TUI implementation, but Kupilot retains its Kubernetes
scope, consent, Evidence, and approval semantics.

## Consequences

The initially full-height primary-screen frame reads as an Agent conversation,
uses little terminal chrome, and keeps high-value Kubernetes context persistent
without crowding every frame with diagnostic metadata. Committing immutable
history above a smaller live frame makes completed text immediately available
to terminal-native selection and scrollback.

Golden fixtures must change, and terminal-background detection remains
imperfect. Unknown modes therefore prefer terminal defaults and semantic text
over assumptions about exact colors. Terminal-native wheel and trackpad
behavior varies by emulator, but Kupilot no longer quantizes dense gestures,
captures drag selection, or converts wheel input into draft-history actions.
`Page Up` and `Page Down` remain deterministic retained-review fallbacks.

Completed conversation remains available in normal terminal scrollback during
and after the process because immutable blocks are inserted once above the live
frame. In-process keyboard review reflows from bounded source state after a
resize. Terminal scrollback is outside Kupilot deletion and retention control,
so switching or deleting Sessions does not erase rows already displayed by the
terminal.

## Security and privacy impact

Moving details to `/status` changes presentation only. The status result remains
a bounded Application DTO and contains no credential, kubeconfig, raw endpoint
authorization, raw Tool data, or live client. Scope, approval, and execution
meaning never depends on color alone.

Runtime history insertion and shutdown cleanup create no new model, Kubernetes,
Tool, executor, SQLite, log, or audit call. Only normalized bounded completed
render state is eligible; composer drafts, partial model output, credentials, raw
payloads, unsafe controls, clipboard sequences, and model-selected styles
remain excluded. Terminal-emulator scrollback may outlive Kupilot and is
disclosed as an external local retention surface.

The real cursor and Working animation are delivery-only. Working ticks carry
the active run ID, scope generation, accepted sequence, and terminal snapshot;
stale, duplicate-time, mismatched, and post-terminal ticks are discarded. They
cannot advance runtime time, counters, progress, Evidence, approval, or any
external action.

## Validation

Golden and reducer tests must cover dark, light, ANSI-16, and `NO_COLOR` modes;
narrow and resized terminals; one-to-eight-row composer growth; tool and action
states and final ordering; multiline key aliases; real-cursor position with an
empty placeholder and committed Unicode input; terminal-default cursor shape,
color, and blink policy; cursor anchoring through transient undersized frames
and growing streamed output; exact absence of delivery-added
spaces; absence of alternate-screen and mouse-reporting enable sequences,
inert injected mouse input, terminal-owned wheel isolation from composer
history, boundary-aware arrow-only history, accepted resume reconstruction
from user messages only, failed and
cancelled resume history preservation, live-bottom retention across Working
reflow, explicit-review retention across later updates, and isolated
repurposed fields; borderless wide and record-fallback narrow Markdown
tables; mixed East Asian and Latin soft wrapping; compact duration formatting;
stale and post-terminal Working-frame rejection; persistent labels for
repurposed composer fields; local-first `Esc` and `Ctrl+C` cancellation,
including request correlation and stale results;
incremental Markdown, pre-Tool draft clearing, terminal replacement, explicit
review retention during later deltas, inert links, status width, split terminal
controls, and proof that `/status`,
`Update`, and `View` cause no business I/O. Runtime
tests must also prove one-time immutable history insertion, retained keyboard
review, live-frame cleanup, one trailing separator row per committed block,
pending-history fallback, and the absence of a composer, placeholder, footer,
Working state, duplicate entry, streaming draft, unsafe terminal control, or
mouse-reporting mode in committed history.

## References

- [Codex CLI](https://learn.chatgpt.com/docs/codex/cli)
- [Codex TUI style guide](https://github.com/openai/codex/blob/main/codex-rs/tui/styles.md)
- [Codex TUI semantic styles](https://github.com/openai/codex/blob/main/codex-rs/tui/src/style.rs)
- [Codex chat composer](https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/chat_composer.rs)
- [Codex chat composer history](https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/chat_composer_history.rs)
- [Codex bottom-pane input routing](https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/mod.rs)
- [Codex status indicator](https://github.com/openai/codex/blob/main/codex-rs/tui/src/status_indicator_widget.rs)
- [Codex Markdown renderer](https://github.com/openai/codex/blob/main/codex-rs/tui/src/markdown_render.rs)
- [Codex final-message separators](https://github.com/openai/codex/blob/main/codex-rs/tui/src/history_cell/separators.rs)
- [Codex inline terminal runtime](https://github.com/openai/codex/blob/main/codex-rs/tui/src/tui.rs)
- [Codex inline history insertion](https://github.com/openai/codex/blob/main/codex-rs/tui/src/insert_history.rs)
- [Codex terminal event stream](https://github.com/openai/codex/blob/main/codex-rs/tui/src/tui/event_stream.rs)
- [Codex custom terminal](https://github.com/openai/codex/blob/main/codex-rs/tui/src/custom_terminal.rs)
- [Product Contract](../product.md)
- [ADR-0044: Prioritize Daily Operations and Adopt Permission Profiles](0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045: Admit Controlled Execution and Remediation](0045-admit-controlled-execution-and-remediation.md)
