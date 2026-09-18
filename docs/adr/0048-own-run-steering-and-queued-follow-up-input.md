# ADR-0048: Own Steering and Queued Follow-Up Input

- Status: Accepted

## Context
Input submitted during an active run can race model entry, cancellation and
completion. Delivery must not decide which text becomes durable conversation.

## Decision
Application owns one bounded process-local queue, revisions, FIFO drain and LIFO
recovery/edit under the existing queue mutex. Each item binds Session/run/request,
generations and safe text identity. Pending drafts are not persisted.

Steering is claimed at a native Eino boundary and becomes history only after the
Application persistence barrier commits it. Verify run identity, generations,
consent, coverage, budget and storage before endpoint entry. A completed run group
has one initial user Message, committed steers in order and one final assistant
Message. Unknown commit outcome never authorizes automatic replay.

Cancellation, clear, edit, drain and scope/policy changes resolve through one
owner. Failed summary or storage cannot silently lose input or create a new
model call. History, export and search use committed messages only. One-shot
plan mode uses the same run and never creates a second planner Agent.

## Consequences and validation
Queue state is ephemeral and separate from durable committed conversation.
Use barriers rather than sleeps to test edit-versus-drain, cancel-versus-commit,
stale revisions, summary failure, bounded queue limits and zero unauthorized
endpoint calls. See [Conversation Input](../user-guide/conversation-input.md).
