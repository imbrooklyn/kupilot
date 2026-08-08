# ADR-0017: Do Not Persist Full Prompts or Raw Outputs

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot keeps useful local history, but an assembled model prompt and raw
adapter outputs combine data from trust boundaries with different eligibility
rules. They may contain credentials, high-risk values, injected instructions,
unbounded cluster text, transport details, or content that is safe only after a
project-owned projection.

Framework tracing, checkpoints, debug dumps, generic JSON columns, and raw error
logging could accidentally create a second persistence path outside the
reviewed repository contract. Retaining those values for convenience would
also make later deletion and category-specific retention difficult to prove.

The Data Retention Contract already owns lifetimes and deletion behavior. This
ADR defines the narrower eligibility decision for prompts and raw outputs.

## Decision

KuPilot will never persist an assembled prompt or raw external output in
`v0.1`. This zero-day rule also applies to the admitted `v0.2` approval flow
unless a future Accepted ADR explicitly changes the data category.

The prohibited durable forms are:

- Complete system, developer, user-history, Tool, or context messages assembled
  as one model request.
- Raw model requests, responses, stream chunks, headers, protocol events, error
  bodies, or framework callback and checkpoint payloads.
- Raw Tool input or result envelopes, Kubernetes objects, Events, container
  output, or exec credential output.
- Raw application or container logs, stack dumps, arbitrary error chains,
  request dumps, debug bundles, and environment snapshots.
- A serialized copy of any prohibited form inside a text, JSON, metadata,
  attachment, audit, error, tracing, or fallback column.

Persistence may contain only independently eligible project-owned derivatives
defined by the Data Retention Contract, including:

- A committed sanitized user Message and a final locally validated assistant
  Message under standard persistence.
- A structured Diagnosis and concise accepted Evidence projected from an
  allowlisted source.
- Canonical sanitized Tool metadata and bounded safe summaries, never a generic
  ToolResult body.
- Allowlisted model-request metadata, versions, counters, timings, stable safe
  errors, and approved non-reversible fingerprints.
- Bounded allowlisted AuditEvents that contain no arbitrary payload field.

Eligibility is evaluated for each derivative before repository invocation.
Being part of a successful prompt or response does not make a value eligible.
Redaction also does not make a forbidden source eligible.

Raw values may exist in memory only for the shortest lifetime needed by the
owning adapter and ordered safety pipeline. Streams and buffers are bounded and
released on completion, cancellation, scope invalidation, or error. Eino
memory persistence, checkpoints, tracing payload capture, and vendor request or
response logging are not product persistence mechanisms and must remain
disabled or unused.

Standard persistence and minimal-persistence keep their meanings from
ADR-0025. Minimal-persistence stores no user or assistant Message, Diagnosis,
Evidence, Tool detail, model-request detail, final answer, or assembled model
context. A storage or adapter failure never falls back to a raw dump.

No debug flag, environment variable, configuration key, SQL migration, or
support workflow may enable prohibited persistence. Adding such a category
requires an Accepted ADR, threat-model and retention updates, an explicit
schema migration, user-facing privacy review, and deterministic sink tests.

## Consequences

Positive consequences:

- A database or local-log disclosure has a smaller and reviewable content set.
- Retention, deletion, and minimal-persistence behavior can be tested against
  explicit eligible derivatives.
- Model, Eino, Kubernetes, and driver upgrades cannot silently add raw durable
  payloads.
- Debug behavior cannot become an undocumented credential or cluster-data sink.

Costs and constraints:

- A resumed Session cannot reproduce the exact model request or raw provider
  response.
- Some vendor-specific failures are harder to investigate after the owning
  call returns.
- Projectors, safe summaries, fingerprints, and typed metadata require
  deliberate implementation and tests.
- Support procedures must rely on safe correlation metadata and reproducible
  local fixtures rather than uploaded raw dumps.

## Alternatives considered

- Persisting complete prompts for exact replay was rejected because replay
  would mix historic authority, sensitive data, and provider-specific payloads.
- Keeping raw output for a short retention period was rejected because a short
  lifetime does not make a forbidden category safe or reliably erasable.
- Encrypting raw payloads in SQLite was rejected because the project does not
  include an encryption or key-management system and source minimization is the
  accepted control.
- Allowing opt-in debug capture was rejected because configuration mistakes,
  support instructions, and failure paths could bypass the normal sink policy.
- Relying only on a redactor was rejected because forbidden sources must be
  excluded before redaction and a detector cannot prove complete sanitization.

## Security and privacy impact

This ADR reduces durable exposure but does not make eligible conversation
history anonymous or encrypted. Sanitized user Messages, Evidence, Diagnosis,
and safe metadata may still reveal cluster or operational context and remain
subject to owner-only files, retention, and user deletion.

Raw content must also stay out of ordinary logs, AuditEvents, safe errors, TUI
history, crash output, and future diagnostic exports. In-memory presence remains
bounded by the current process and is still protected by source, scope,
projection, sensitive-value, terminal, and model-egress controls.

## Validation

Deterministic tests across logging, schema, repositories, model and Eino
adapters, TUI, audit, and persistence must:

1. Place distinct generated canaries in every prohibited raw source, envelope,
   header, error, stream, callback, and failure path.
2. Use separate expected values for eligible projected derivatives so a safe
   Message or Evidence fact is not confused with a raw envelope.
3. Prove prohibited canaries and complete serialized envelopes are absent from
   every repository input, logical row, database file, WAL, ordinary log,
   AuditEvent, safe error, TUI history, and restart recovery path.
4. Inspect schema and query allowlists for generic text, JSON, attachment,
   debug, trace, checkpoint, request, response, prompt, and raw-output escape
   paths.
5. Exercise success, cancellation, timeout, malformed stream, adapter error,
   persistence failure, and process restart without creating a fallback dump.

Every concrete Eino, model SDK, SQLite driver, tracing, and storage integration
must demonstrate these properties.

## Revisit triggers

- A user-controlled export or diagnostic bundle requires a new durable content
  category.
- A provider contract cannot be supported without retaining protocol state
  across process restart.
- A regulated deployment requires a separately designed encrypted audit or
  evidence store.
- A new telemetry, crash-reporting, synchronization, or remote-support path is
  proposed.

## References

- [Data Retention Contract](../data-retention.md)
- [Security Threat Model](../security.md)
- [ADR-0006: Use Eino Behind an Agent Adapter](0006-use-eino-behind-an-agent-adapter.md)
- [ADR-0008: Use SQLite for Local Persistence](0008-use-sqlite-for-local-persistence.md)
- [ADR-0025: Enforce Data Retention and User Deletion](0025-enforce-data-retention-and-user-deletion.md)
