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

`Ctrl+A`/`Ctrl+E` or `Home`/`End` move to the current line's start/end without
leaving the composer. Command+Left/Right work when the terminal forwards those
keys directly or maps them to these line-navigation keys. Terminal-owned
shortcuts depend on your terminal configuration. `Alt+E` opens supporting
observations; `Esc` returns to the composer with the draft preserved.

| Cursor operation | Keys |
| --- | --- |
| Previous/next character | Left/Right or `Ctrl+B/F` |
| Previous/next word | Option/Alt+Left/Right, `Alt+B/F`, or Ctrl+Left/Right |
| Previous/next visual line | Up/Down or `Ctrl+P/N` (Up/Down recall history only at the documented input boundaries) |
| Select characters/words | Shift+Left/Right or Option/Alt/Ctrl+Shift+Left/Right |
| Delete previous/next word | Option/Alt/Ctrl+Backspace/Delete; `Ctrl+W`/`Alt+D` |

Typing or pasting replaces the selected text within the existing draft limit.
Selection remains local to the composer; `/copy` still copies only an eligible
committed assistant answer. Word and logical-line boundaries follow Bubbles;
repeated `Ctrl+A/E` stays at the current line boundary.

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

`/copy` copies only the latest committed successful assistant final answer.
On local macOS it uses the system clipboard, including Apple Terminal. Linux
and SSH use OSC 52; tmux and screen use passthrough framing. Terminal clipboard
permissions still apply. A terminal request is reported as unconfirmed, not as
a successful copy; terminal selection or `/export` remains available if it is
blocked. No terminal-brand allowlist is required.

Wheel/trackpad gestures scroll conversation or dialog content, even above the
composer; they never edit input or recall submitted history. Drag visible
transcript text to highlight it, then Ctrl+C or right-click copies that display
selection through the same bounded clipboard route. Escape clears it; scrolling,
resizing or a changed transcript display invalidates it. This explicit selection
may include provisional text and local notices; `/copy` still selects only a
committed successful final. Command+C on Mac is a terminal shortcut and does not
copy the application's selection. A stationary click does not select or copy.

`/find` or `Alt+S` reuses the
composer to search the current committed transcript; fixed next/previous keys
navigate and Escape restores the prior draft. Both `/copy` and `/find` exclude
queue, composer, streaming, failed, or recovered content.

`Ctrl+R` searches only committed ordinary submitted input, while another
`Ctrl+R` or the standard previous/next keys traverses matches. `Enter` accepts;
`Esc` or `Ctrl+C` restores the exact prior draft. This delivery history is
separate from model context. `Alt+Z` and `Alt+Y` provide bounded grapheme-safe
undo/redo for ordinary composer editing; paste and committed IME input are one
edit. Search, modal, picker, approval, secret, queue-edit, successful submit,
Session switch, deletion, and shutdown isolate or clear the in-memory stacks.

Committed transcript landmarks use fixed shortcuts: `Alt+U`/`Alt+Shift+U` for
user input, `Alt+A`/`Alt+Shift+A` for assistant finals,
`Alt+I`/`Alt+Shift+I` for failure or unknown, and
`Alt+P`/`Alt+Shift+P` for approval. `Alt+E` opens the claim index; Left/Right
select a claim and Up/Down select only Evidence cited by it. These jumps
do not create a transcript page. Navigation clears a display text selection.

`/plan` arms the next ordinary input as a one-shot plan-only run; `/plan off`
cancels before start. The plan may use safe reads, has at most twelve steps,
cannot propose or execute actions, and never continues automatically.
`/compact` requests a one-attempt compaction only while the interaction is
idle.

Every fixed Slash entry reports `available`, `busy`, `not applicable`,
`disabled`, or `unsupported terminal` with a content-free reason. `/doctor`
shows redacted local build/configuration/storage/Session/terminal/feature/
compatibility health and performs no operational I/O. Terminal titles contain
only fixed states; notifications are disabled. `NO_COLOR`, ANSI-16, reduced
motion, narrow resize, IME/Unicode, and restored scrollback keep safety meaning
in text or stable symbols rather than color or animation alone.

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
