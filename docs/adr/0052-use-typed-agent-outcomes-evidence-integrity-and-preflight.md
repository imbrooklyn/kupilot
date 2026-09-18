# ADR-0052: Validate Evidence-Backed Answers and Typed Outcomes

- Status: Accepted

## Context
Model text can be useful without being evidence or proof that an operation
completed. Deterministic validation must separate these roles.

## Decision
Use free-form Markdown or typed clarification with a strict version-1 envelope.
The model supplies bounded claim text/type and exact Evidence references, not
claim hashes, sequence, coverage state or terminal authority. Runtime derives
normalized claim hashes, order and structural coverage from accepted Evidence.

Only deterministic Tool handling creates Evidence. Bind accepted items to the
same run, invocation, complete scope, policy generation, source, safe projection
and observation time. Historical Evidence is display-only. References and
structural coverage do not prove semantic causality. Missing, partial, truncated,
stale or conflicting sources remain explicit limitations.

Validate output for strict schema, references, sensitive values, terminal controls,
bounded size and allowed action proposals before persistence. Proposed actions
remain unexecuted until Application creates fresh local authority. Clarification
cannot create a Tool target or consent.

Application derives terminal reasons, safe next actions and content-free
preflight projections from actual lifecycle state. Preserve useful internal error
causes but expose only safe project-owned classes and failure stages/reasons.
Raw endpoint strings do not drive policy. Failures are handled at their real
boundaries; an unused parallel recovery table is not verification.

## Consequences and validation
The model retains flexible expression while code owns provenance and authority.
No repair, critic, second Agent or retry hides model capability failures.
Recording and scripted fixtures verify strict decoding, injection resistance,
accepted Evidence, source limitations, clarification, plan-only behavior,
persistence barriers and zero-call denials. Quality fixtures use reviewed
semantic assertions rather than complete prose matching.
See [Evidence Guide](../user-guide/evidence.md) and
[Diagnosis Quality](../quality/diagnosis-baseline.md).
