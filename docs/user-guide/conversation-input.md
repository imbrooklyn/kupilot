# Steering and Queued Follow-Ups

Kupilot keeps one active Agent run. While a regular run is working, ordinary
chat input can affect the next model step or wait as a separate successor run.
Neither path starts a concurrent Agent.

## Keys

| State | Key | Result |
| --- | --- | --- |
| Idle | `Enter` | Start one ordinary Agent run. |
| Idle | `Tab` | Use visible completion when available; otherwise do nothing. |
| Regular run active | `Enter` | Hold ordinary chat input as a pending steer for the next model invocation. |
| Regular run active | `Tab` | Add ordinary chat input to the FIFO follow-up queue. |
| Editable queued, rejected, or recovered input exists | `Alt+Up` | With an empty composer and no active local interaction, restore the newest editable item. |

`Shift+Left` is ordinary selection/cursor input and is never an edit-last
fallback. A picker, completion, modal, Reviewer, approval, input method, paste,
selection, or composer edit keeps its existing precedence. `Ctrl+C` and `Esc`
retain their documented cancellation behavior.

A single-leading-slash entry is handled by the fixed local Slash registry and
is not queued or steered. Start ordinary chat with `//` when the text itself
must begin with a slash. `!` input is rejected; it never opens a shell.

## What the states mean

- `Pending steer` means Application owns the draft for the exact active run.
  It is not yet durable history and has not reached the model.
- `Committing steer` means the next Eino model boundary claimed it and the
  Application persistence barrier is running.
- `Committed steer` means the safe user Message was durably accepted before
  model I/O. It does not promise that the model request succeeded.
- `Rejected steer` hit a known validation or claim-notification barrier before
  its Message commit and can be recovered for editing.
- `Unknown input outcome` means model I/O may have begun but its result is not
  known. Kupilot does not resend it automatically.
- `Recovered input` could not pass the remaining request budget, or survived
  cancellation, timeout, stale state, degraded persistence, another unsafe
  terminal result, or invalidation. Edit and submit it explicitly after
  reviewing current scope and policy.
- `Queued follow-up` is a separate process-local successor question.

Pending, committing, queued, rejected, and recovered drafts do not enter
committed transcript scrollback, SQLite, Session resume, summary coverage, or
export. Only the bounded working preview may show their normalized text. An
unknown display can appear only after the input was committed, so its user row
already exists; the incomplete or failed run group is excluded from later model
context. A standard-mode persistence failure recovers the uncommitted input.
If the Message append succeeds but the commit notification cannot be delivered,
the state remains known committed and model I/O is blocked.
`/status` shows counts and bytes without content.

## Automatic follow-up

After a regular turn and its final answer are cleanly and durably completed,
Kupilot starts at most one queued follow-up. The queue remains FIFO across
successive clean turns. Kupilot never auto-sends after failure, cancellation,
timeout, stale scope or policy, degraded persistence, or an unknown outcome.

If a pending steer arrives after the final model boundary, a clean completion
turns it into an ordinary queued successor. An unsafe completion recovers it
for editing. Input is never silently dropped, retargeted, or retried.

The current limits are eight process-local conversation-input lifecycle items,
65,536 UTF-8 bytes per item, 262,144 aggregate bytes, and eight committed
steers per active run. The queue ends with the current process or Session and
is not resumable.

Use `/queue cancel <item-id>` to remove one exact `queued`, `rejected`, or
`recovered` item. The item ID is shown only in the bounded working projection.
Use `/queue clear` to open a local confirmation and remove all currently
editable items. A stale revision or a race with edit, commitment, or drain
changes nothing. Pending, committing, committed, draining, and unknown items
cannot be removed. The command must match the current scope and policy
generations; once it does, a recovered draft may be discarded even though its
preserved provenance names an earlier invalidated generation.

`/copy` copies only the latest committed successful assistant final answer
when terminal-native clipboard support is known. `/find` or `Ctrl+F` reuses the
composer to search the current committed transcript; fixed next/previous keys
navigate and Escape restores the prior draft. Neither interaction includes
queue, composer, streaming, failed, or recovered content.

`/plan` arms the next ordinary input as a one-shot plan-only run; `/plan off`
cancels before start. The plan may use safe reads, has at most twelve steps,
cannot propose or execute actions, and never continues automatically.
`/compact` requests a one-attempt compaction only while the interaction is
idle.

## Scope and resume

Every item is bound to the exact Session, run when applicable, verified scope,
scope generation, policy generation, model role and origin, consent, and
budget state accepted when it was created. A relevant change invalidates old
input before cancellation and never retargets it automatically.

Resume restores eligible committed conversation context, not historic
Kubernetes authority or queued drafts. The resume command itself performs no
model, Kubernetes, Tool, Reviewer, approval, process, or executor I/O. The
configured or resumed Context and Namespace is independently activated unless
an identical current scope was already verified in this process; unavailable
or conflicting candidates return to the scope picker.

## Terminal retention

The working preview is visible terminal content. A terminal emulator,
multiplexer, recorder, or operating system may retain it even though Kupilot
does not store the draft in SQLite or export it. Use the terminal's own
clearing and retention controls for sensitive input.
