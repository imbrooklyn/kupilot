# ADR-0040: Use a Codex-Style Conversational TUI

- Status: Accepted
- Date: 2026-08-30
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
  it safely.
- User messages retain the `›` marker and use the same quiet surface and
  vertical spacing as the composer. Assistant Markdown is unframed and rendered
  into width-aware prose, lists, inert code, and readable tables. Tables fall
  back to key/value records when a grid would starve its columns. Repeated role
  labels and fixed report headings are not added.
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
  permanently expanded card. On terminal runs, activity precedes a full-width
  separator, the final answer, and a muted `Worked for <duration>` line.
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

## Security and privacy impact

Moving details to `/status` changes presentation only. The status result remains
a bounded Application DTO and contains no credential, kubeconfig, raw endpoint
authorization, raw Tool data, or live client. Scope, approval, and execution
meaning never depends on color alone.

## Validation

Golden and reducer tests must cover dark, light, ANSI-16, and `NO_COLOR` modes;
narrow and resized terminals; one-to-eight-row composer growth; tool and action
states and final ordering; multiline key aliases; disabled mouse reporting,
native-selection compatibility, and explicit input-history shortcuts; wide and
narrow Markdown tables; inert links; status width; terminal controls; and
proof that `/status`, `Update`, and `View` cause no business I/O.

## References

- [Codex CLI](https://learn.chatgpt.com/docs/codex/cli)
- [Codex TUI style guide](https://github.com/openai/codex/blob/main/codex-rs/tui/styles.md)
- [Codex TUI semantic styles](https://github.com/openai/codex/blob/main/codex-rs/tui/src/style.rs)
- [Codex chat composer](https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/chat_composer.rs)
- [Product Contract](../product.md)
