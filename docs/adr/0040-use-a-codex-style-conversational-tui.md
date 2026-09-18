# ADR-0040: Use One Conversational Supervision Screen

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
