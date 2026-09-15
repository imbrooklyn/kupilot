# ADR-0057: Derive Response Metadata and Classify Interaction Failures

- Status: Accepted
- Date: 2026-09-16
- Amends: ADR-0027, ADR-0038, ADR-0049, ADR-0052, and ADR-0056

## Context

A successful Tool read can still end in a rejected final answer. Requiring a
model to repeat ordering metadata or an exact locally rendered clarification
creates avoidable compatibility failures. A single invalid-response message
also hides the distinction between a malformed envelope and an invalid Evidence
binding. ADR-0056 already moved claim hashing into code; that decision remains.

## Decision

Strict final response schema 4 contains `answer_markdown`,
`evidence_citations`, `proposed_actions`, `response_schema_version`, `outcome`,
`limitations`, and `questions`. A citation contains only `claim`, `claim_type`,
and `evidence_ids`. The model expresses intent and selects exact references; it
does not compute hashes, sequence numbers, structural coverage, or stop reasons.

Runtime derives claim, question, choice, and plan-step ordinals from array
positions. It derives claim hashes after safe normalization, as in ADR-0056.
Structural coverage is `verified` for a current observation with accepted
Evidence, `supported` for inference/recommendation with Evidence, `limited` for
inference/recommendation without Evidence and for uncertainty, and `unsupported`
for an explicitly unsupported observation. These labels are structural facts,
never semantic confidence. Uncertainty and unsupported observations cannot cite
Evidence. Current observations without Evidence remain rejected.

Typed questions contain `kind`, `prompt`, and a non-null `choices` array of
objects containing only `label`. Runtime supplies question sequences and local
choice IDs. For `needs_user_input`, `answer_markdown` is an optional candidate
presentation string in meaning only: the field is still required and must be a
bounded safe string, but may be empty. Runtime renders the validated questions.
The candidate cannot override them. Claims, actions, and limitations must be
empty. Any candidate text still passes sensitivity checks before being discarded.
Clarification after a Tool result remains prohibited.

Plan wire schema 2 contains the existing title, steps, limitations, and citations;
steps contain only `description`. Runtime creates the existing durable Plan
schema 1 and its display. No action authority is added.

Object member order, insignificant JSON whitespace, and equivalent JSON string
escapes are presentation differences. Provisional display may be unavailable
when `answer_markdown` is not first; final acceptance does not depend on this
optimization. A unique set of known, same-run, same-generation Evidence IDs may
be sorted into runtime acceptance order. Duplicate references are rejected,
not silently removed. Runtime never guesses a missing claim, Evidence ID,
target, operation, scope, action, or user intent.

Missing and null required fields, duplicate or unknown keys, malformed JSON,
unknown enum values, old wire schemas, excessive nesting, and one-over limits
remain failures. Arrays remain required and non-null. The only nullable value
remains the existing exact action-parameter alternative. Retained assistant
history is reconstructed once in schema 4 with empty authority arrays; stored
older safe diagnoses and manifests remain readable without restoring authority.

An answer that declares no current observation and performs no source read does
not acquire an artificial missing-source failure merely because it is a greeting
or an explanation. Actual source gaps, unsupported observations, conflicts,
partial results, lifecycle stops, and budget stops remain runtime-derived.

Every interaction rejection has a project-owned, content-free stage and reason.
These supplement the existing safe error class and terminal lifecycle reason.
Fixed messages and next actions are selected from code, never from raw errors.
The projection carries no response, Evidence payload, resource, endpoint,
credential, or path. Causes remain wrapped internally. Failure metadata is
current-process diagnostic state, not a second durable conversation store.

Scope, policy, generations, consent, finite reservations, ordered event and Tool
pairing, Evidence ownership, action authority, and durable commit barriers remain
strict. Every accepted Tool result advances the registry revision, including
empty results that change source coverage. A stale snapshot cannot be sealed.
The existing bounded transport observer checks OpenAI SSE finish/usage ordering
before the pinned SDK can merge empty events. It returns the original bytes
unchanged or rejects them; Eino still owns message assembly and Tool pairing.
Repeated finish events, content after finish, missing finish, and invalid usage
ordering have distinct fixed reasons. Partial Evidence produces partial detail
state consistently in runtime and SQLite, without being mislabeled complete.
The native Ollama transport also observes the presence of a non-empty typed
`error` field in a bounded NDJSON record. The pinned client otherwise converts
that provider failure into an untyped error under HTTP 200. Runtime records only
`provider_reported_failure`, never the error value. This is distinct from a
malformed stream and cannot authorize a repair or another request.
Every failure denies queue drain and creates no automatic retry, repair,
fallback, retarget, second Agent, critic, or conversation loop. Successful typed
clarification also keeps the queue pending. Application remains the sole
authority; Eino ADK remains the sole Agent/Runner/ReAct implementation.

## Validation and evidence levels

Deterministic composition fixtures exercise the full runtime and delivery
boundaries, with synthetic Tools, recording transports, and temporary SQLite.
They prove structure, calls, ordering, rejection, cancellation, persistence, and
absence of extra authority. Go coverage measures statements; an explicit
decision matrix records branch outcomes separately.

Explicit bounded local Ollama conformance uses the native pinned Eino adapter,
synthetic Evidence, no Kubernetes, and no retry of failed scenarios. It records
only versions, fixed scenarios, counts, byte/usage measurements, wall time, and
typed outcomes. It proves compatibility only for that exact local target.
Neither scripted fixtures nor local conformance establish real-cluster behavior,
model semantic quality, Reviewer quality, or release readiness.

## Consequences

The wire contract is smaller and easier to follow. Schema 3 model outputs must
be regenerated under the new prompt; they are not heuristically repaired.
Persisted safe content remains the same durable source. Structural validation
still cannot prove that prose is true or that a model declared every claim.

## References

- [Model Compatibility](../model-compatibility.md)
- [Agent Runtime](../agent-runtime.md)
- [Interaction Conformance](../interaction-conformance.md)
- [ADR-0056](0056-derive-claim-integrity-metadata-in-the-runtime.md)
