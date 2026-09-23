# ADR-0015: Validate Evidence-Backed Answers and Typed Outcomes

- Status: Accepted

## Context
Model text can be useful without being evidence or proof that an operation
completed. Deterministic validation must separate these roles.

## Decision
Use free-form Markdown or typed clarification with a strict version-1 envelope.
The model supplies bounded claim text/type and exact Evidence references, not
claim hashes, sequence, coverage state or terminal authority. Runtime derives
normalized claim hashes, order and structural coverage from accepted Evidence.

Keep canonical Evidence UUIDs for runtime, persistence and Evidence inspection. The
model-facing `result.evidence[].id` and `reuse.evidence_ids` use compact opaque
references (`e_` plus 16 hexadecimal digest characters) derived from those IDs.
Before ordinary validation, resolve each reference by exact lookup in the
current run's accepted Evidence only. Reject unknown or ambiguous references;
never accept a UUID as an alternate wire spelling, guess a suffix, repair a
reference, or use historical Evidence. The reference index is derived in memory,
not a new store or authority. This keeps storage identity out of model copying
without weakening full run, invocation, scope and policy checks.

Only deterministic Tool handling creates Evidence. Bind accepted items to the
same run, invocation, complete scope, policy generation, source, safe projection
and observation time. Historical Evidence is display-only. References and
structural coverage do not prove semantic causality. Missing, partial, truncated,
stale or conflicting sources remain explicit limitations.

Validate output for strict schema, references, sensitive values, terminal controls,
bounded size and allowed action proposals before persistence. Proposed actions
remain unexecuted until Application creates fresh local authority. Clarification
cannot create a Tool target or consent.

After sensitive-content screening, remove complete model-emitted private-use
inline citation tokens (`U+E200 cite U+E202 ... U+E201`) from answer Markdown.
Use the same presentation normalization for provisional fragments and the final
answer before persistence, so history, copy and export receive the clean answer.
Recheck the resulting text for sensitive content after token removal.
These tokens do not create or substitute for structured Evidence references;
exact same-run reference validation remains mandatory. Preserve other Unicode
and incomplete or unrelated text rather than guessing citation authority.

Application derives terminal reasons, safe next actions and content-free
preflight projections from actual lifecycle state. Preserve useful internal error
causes but expose only safe project-owned classes and failure stages/reasons.
Raw endpoint strings do not drive policy. Failures are handled at their real
boundaries, with tests of the actual recovery path.

## Consequences and validation
The model retains flexible expression while code owns provenance and authority.
No repair, critic, second Agent or retry hides model capability failures.
Recording and scripted fixtures verify strict decoding, injection resistance,
accepted Evidence, source limitations, clarification, plan-only behavior,
persistence barriers and zero-call denials. Quality fixtures use reviewed
semantic assertions rather than complete prose matching.
See [Evidence Guide](../user-guide/evidence.md) and
[Diagnosis Quality](../quality/diagnosis-baseline.md).
