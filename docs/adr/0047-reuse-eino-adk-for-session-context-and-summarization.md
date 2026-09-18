# ADR-0047: Use Eino for Session Context and Summarization

- Status: Accepted

## Context
Later turns need retained context without another memory framework or restoring
historical Evidence and execution authority.

## Decision
For every later run with eligible history, Application selects one ordered,
bounded representation of all retained eligible same-Session turns. Translate
once into native Eino messages and include the current question exactly once.
Missing history coverage fails closed with zero model calls.

SQLite safe Messages are the only durable source in standard mode. Minimal mode
uses current-process context only. Resume itself performs no external work and
the next explicit question requires current scope, policy, consent, coverage and
budget checks. Incomplete run groups cannot become model history.

Use Eino summarization middleware with the agent profile and an independent,
reserved, non-streaming, Tool-free, one-attempt budget. Persist only bounded safe
summary text, schema/policy versions, exact covered Message IDs/count/digest/time,
Session/profile/origin identity and truncation/degraded markers. Recent tail
remains Message rows, not a copied generic payload.

Compaction failure, timeout, cancellation, sensitivity, staleness, corrupt coverage
or persistence failure preserves committed state and sends no oversized or
silently truncated request. Manual compaction uses this same path.

Stable Eino currently needs the thin selection/ordering/coverage bridge.
Runner-managed Session support may replace it only in a non-prerelease release
that passes storage, authority, consent, retention, deletion, cancellation,
Tool-pairing and compatibility tests. It must replace, not sit underneath, a
second memory abstraction.

## Consequences and validation
No raw Eino transcript, checkpoint store, tokenizer or general memory manager is
introduced. Test complete retained coverage, exact question count, summaries,
minimal mode, explicit cross-process resume, zero-call failures, cancellation
and SQLite rollback. See [Agent Runtime](../agent-runtime.md) and
[Data Retention](../data-retention.md).
