# ADR-0062: Preserve Composer Cursor Navigation

- Status: Accepted
- Date: 2026-09-17
- Amends: ADR-0005 and ADR-0051

## Context

The pinned Bubbles textarea uses `Ctrl+E` for line-end movement. Kupilot's
Evidence shortcut intercepted that event before the editor and blurred the
composer whenever a committed answer had supporting observations. Subsequent
typing was ignored by the non-editable observation index. A terminal that maps
macOS Command+Right to `Ctrl+E` therefore triggers an unrelated focus change.

The same conflict affected `Alt+F`, intercepted for failure navigation, while
`Alt+B` reached word-left movement. `Ctrl+F` was also intercepted for transcript
search instead of character-right movement. Terminals that encode Option+Right
as Meta-F therefore exposed asymmetric word navigation.

Codex CLI `rust-v0.154.0`, peeled commit
`6b9826e3aa83b1a5947db50f4332cb9c65f1b340`, is the stable source reference
verified on 2026-09-17. Its editor owns `Ctrl+B/F`, `Alt+B/F`, Alt/Ctrl-modified
arrows, and `Ctrl+A/E`; popup and history handling have their own contexts.

Bubbles v2.1.1 also loops indefinitely on word-left at an empty or all-whitespace
input prefix. Stable v2.2.1 fixes that boundary and natively supports Ctrl-modified
word navigation and deletion. It retains Go 1.25.0, Bubble Tea v2.0.8, Lip Gloss
v2.0.5, and the MIT license.
Its module graph also requires `github.com/mattn/go-runewidth v0.0.27`; that
exact indirect update is included in the dependency and platform gates.

## Decision

Use `Alt+E` to open or close supporting-observation inspection. Preserve
`Ctrl+A`/`Ctrl+E` and `Home`/`End` as native composer line-start/line-end keys.
Add `super+left` and `super+right` aliases to the same existing textarea
bindings for terminals that deliver Command-modified arrows directly.

Reserve `Ctrl+B/F` for character movement and `Alt+B/F`, Alt+Left/Right, and
Ctrl+Left/Right for word movement. Transcript search uses `Alt+S` or `/find`;
previous/next failure navigation uses `Alt+I`/`Alt+Shift+I`. These fixed shortcuts
leave the editor's directional aliases symmetric.

Pin Bubbles v2.2.1 and reuse its corrected movement, selection, and deletion
operations. Composer byte accounting subtracts selected text when typing,
pasting, or inserting a newline replaces that selection. Draft selection stays
in the one in-memory editor; native selection copying is disabled because
Kupilot admits clipboard output only through its existing committed-answer
copy policy. The existing clipboard-read binding also remains disabled.

The source comparison establishes shortcut ownership, not identical editors.
Kupilot retains Bubbles' word and logical-line boundaries. Codex's repeated
Ctrl+A/E movement to adjacent lines, configurable keymap, and Vim editor are
not adopted by this decision.

Key aliases only select existing Bubbles cursor operations. Do not introduce a
terminal-specific parser, configurable keymap, another editor, or focus repair
on arbitrary input. A terminal-owned shortcut cannot be handled unless the
terminal forwards a supported key event.

Observation inspection remains explicit and display-only. Modal, approval,
pending submission, and terminal blur continue to own or block input before
composer editing. Escape, cancellation, and observation-detail acceptance keep
their existing semantics. The change creates no Application command, Evidence,
permission, model request, storage write, or execution authority.

## Validation

Deterministic reducer tests cover character, word, and line movement and subsequent Unicode input
with and without committed Evidence, an empty or multiline draft, and a regular
active run. Inspection must still open explicitly and restore the exact draft
and cursor on exit. Tests also preserve actual terminal blur and approval
ownership. Existing stale-result, cancellation, and safe-projection tests remain
required with the new inspection key. Raw terminal-byte fixtures exercise native
Bubble Tea decoding for Meta-letter, modified-arrow, and Control encodings.
Empty/whitespace input, selection replacement, exact/one-over byte limits,
multiline input, undo, and zero clipboard commands gate the dependency update.

These tests establish handling of decoded key events. They do not establish
which event an untested terminal or user key configuration sends.

## Source evidence

- [Codex CLI v0.154.0](https://github.com/openai/codex/releases/tag/rust-v0.154.0)
- [Codex editor bindings](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/keymap.rs#L1535)
- [Codex editor dispatch](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/bottom_pane/textarea.rs#L600)
- [Codex composer dispatch](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/bottom_pane/chat_composer.rs#L3501)
- [Bubbles v2.2.1 editor and boundary fix](https://github.com/charmbracelet/bubbles/blob/v2.2.1/textarea/textarea.go#L997)
- [Bubbles v2.2.1 boundary tests](https://github.com/charmbracelet/bubbles/blob/v2.2.1/textarea/textarea_test.go#L1922)
