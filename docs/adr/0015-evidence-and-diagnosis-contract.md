# ADR-0015: Require the Evidence and Diagnosis Contract

- Status: Accepted
- Date: 2026-08-05

## Context

A fluent model response can blur observed facts, inference, missing data, and
advice. Kubernetes data is also a time-bounded snapshot: permissions, output
limits, races, and object changes can make an observation incomplete. KuPilot
must not present model confidence as proof or imply that it found a guaranteed
root cause.

The output contract needs runtime-verifiable provenance rather than relying on
prompt wording or prose conventions.

## Decision

KuPilot will represent observation and interpretation as separate project-owned
models: Evidence and Diagnosis.

Only deterministic local Tool handling creates Evidence. Each accepted Evidence
item binds:

- Its ID, AgentRun ID, and ToolInvocation ID.
- The immutable ClusterScope and observation time.
- A ResourceRef, category, concise observed fact, and safe source path.
- Optional resource version plus redaction, truncation, severity, and
  fingerprint metadata.

Evidence is accepted only after post-result generation validation. Model text,
user text, historic Evidence, and a selected ResourceRef cannot create current
Evidence.

Every Diagnosis has four distinct collections:

1. Confirmed facts. Every item references one or more accepted Evidence IDs
   from the same AgentRun.
2. Hypotheses. Each item remains an inference and may state supporting Evidence,
   bounded confidence, and a way to disprove it.
3. Missing information. This includes absent, forbidden, unsupported, stale,
   conflicting, or truncated observations and explains their impact.
4. Recommended actions. These are user-evaluated next steps with relevant risk
   or prerequisites and are not represented as performed.

The Diagnosis also records the observed ClusterScope, Evidence time window,
creation time, and validation warnings. A model response is a draft. The Agent
adapter validates its structure and references against the runtime Evidence
set. An unsupported confirmed fact is removed or demoted to a hypothesis and
produces a warning.

A successful AgentRun means that KuPilot followed the safety and Evidence
contract. It does not mean that a root cause was found.

## Consequences

Positive consequences:

- Users can distinguish observation from interpretation and advice.
- Confirmed facts have machine-checkable provenance to the current run and
  scope.
- Partial results and permission gaps remain visible instead of being hidden by
  a confident narrative.
- Diagnostic fixtures can use deterministic structural and assertion rubrics.

Costs and constraints:

- Tool handlers must create stable safe Evidence rather than returning only
  prose or vendor objects.
- Model output requires parsing and validation before final rendering.
- Some natural-language drafts will be downgraded or rejected when references
  are absent or invalid.
- Persistence and retention must preserve enough safe provenance to explain a
  historic Diagnosis without treating it as current fact.

## Alternatives considered

- A free-form answer with prompt instructions to cite observations was rejected
  because the runtime cannot verify fact provenance reliably.
- A single `root_cause` field was rejected because incidents may be ambiguous,
  incomplete, or multi-causal.
- Allowing the model to create Evidence was rejected because it collapses the
  observation and inference boundary.
- Treating high-confidence hypotheses as facts was rejected because confidence
  is model self-assessment, not new Evidence.
- Hiding denied or truncated data was rejected because absence affects the
  safety of the Diagnosis.

## Security and privacy impact

Evidence contains only projected, normalized, redacted, and bounded data. It
must not become a route for raw Kubernetes objects, raw logs, credentials, or
unbounded external text to reach the model, TUI, or database.

Evidence provenance reduces unsupported claims but does not guarantee that an
observation is complete, current, or causally explanatory. Observation time,
scope, partial status, and missing information remain visible.

Tool content remains untrusted data. Prompt text can guide interpretation but
cannot authorize reads, create Evidence, or bypass the runtime validator.

## Validation

The model shapes and validator responsibilities are defined in
[Architecture](../architecture.md), and the public
[Product Contract](../product.md) defines the four-part Diagnosis behavior.

Deterministic fixtures and a rubric must cover:

- Required structure and observation metadata.
- Existing same-run Evidence references for every confirmed fact.
- Allowed and forbidden assertions for each diagnostic category.
- Visible permission, truncation, stale-data, and unsupported-capability gaps.
- Recommendations represented as unexecuted.

A live model or an LLM judge is not a CI oracle for this contract.

## Revisit triggers

- Real diagnostic evaluations show that the four collections cannot express a
  required uncertainty or provenance case.
- Evidence retention changes make a historic Diagnosis materially
  uninterpretable.
- A future action workflow needs a separately accepted result contract without
  weakening fact provenance.

## References

- [Architecture](../architecture.md)
- [Product Contract](../product.md)
- [Privacy Overview](../privacy-overview.md)
- [Glossary](../glossary.md)
- [ADR-0014: Isolate Runs with ClusterScope Generation](0014-cluster-scope-generation-isolation.md)
