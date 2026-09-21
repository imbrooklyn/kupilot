# ADR-0010: Use One Conversational Supervision Screen

- Status: Accepted

## Context
Users need to supervise one active Agent while retaining normal terminal reading,
selection and text-entry behavior.

## Decision
Use Bubble Tea v2 for one low-chrome conversational screen. CLI/TUI depend only
on Application commands, DTOs and events; Update and View perform no business
I/O. Async delivery validates request/run IDs, generations, sequence and terminal
state. No dashboard, primary resource tree, raw log pane or second editor exists.

Use one borderless composer growing from one to eight content rows. Native
multiline cursor navigation takes precedence over history or recovered-input
shortcuts. Slash commands are compile-time fixed; unknown commands and ! syntax
perform no external action. Suggestions and pickers are bounded aids, never
Evidence or proof that a resource exists.

Show consent, permission, exact approvals, execution ambiguity, verification,
terminal reasons and provenance inline. Reviewer states remain visibly distinct
from human decisions. status, permissions and doctor are local bounded queries.
Plan mode uses the same Agent and safe-read subset. Search, semantic selection,
undo/redo, clipboard gestures, status titles and notifications are ephemeral,
bounded delivery features and cannot change run authority.

Copy uses a bounded native clipboard write when a local platform helper is
available, then an OSC 52 request on an interactive terminal if needed. SSH
uses the attached terminal; tmux requests use its passthrough framing. Do not
gate copying on terminal-brand allowlists or describe an unacknowledged terminal
request as a confirmed copy. Helpers receive only the explicitly selected safe
text on stdin, a minimal environment, no shell, and a finite deadline.

Use one alternate-screen renderer and the existing transcript viewport for
conversation, dialogs and review. Capture mouse wheel and drag events: wheel
input scrolls the conversation or open dialog, even over the composer, and never
enters input history or edits the draft. Dragging in the visible transcript
selects display text; Ctrl+C or right-click copies that selection through the
same bounded clipboard route. Escape clears selection. Scrolling, resizing or
replacement of the selected display clears it; selection never includes the
composer or authorizes an action. Ordinary keyboard editing and history retain
their existing bindings. Page Up/Page Down also navigate the viewport
(Fn+Up/Fn+Down on a Mac keyboard). On clean exit,
restore the original terminal and print only the completed safe transcript once.
Never transfer live rows to unmanaged
scrollback while rendering: inline frame resizing and timer-based insertion do
not provide an atomic handoff. All layouts remain bounded by the terminal.

Normalize external Unicode and strip or visibly replace unsafe terminal,
device and bidirectional controls. Do not depend on color alone. Optional
presentation failures preserve the business result and cause no external retry.

## Consequences and validation
One editor and one supervision surface avoid duplicated UI state and resource
management workflows. Native platform behavior still requires tests. Exercise
cursor/Unicode/paste, cancellation, stale events, queue races, local zero-I/O
queries, safe rendering, terminal cleanup and dark/light/ANSI-16/NO_COLOR fixtures.
See [Conversation Input](../user-guide/conversation-input.md) and
[Interaction Conformance](../interaction-conformance.md).
