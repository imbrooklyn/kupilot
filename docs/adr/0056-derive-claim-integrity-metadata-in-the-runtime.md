# ADR-0056: Derive Claim Integrity Metadata in the Runtime

- Status: Accepted
- Date: 2026-09-15
- Amends: ADR-0038, ADR-0043, ADR-0049, ADR-0052, ADR-0054, and ADR-0055

## Context

Strict response schema 2 required the Agent model to calculate the lowercase
SHA-256 digest of each exact claim string. A bounded native Ollama observation
against Ollama 0.34.0 and `gpt-oss:20b` showed a complete Tool call followed by
a structurally valid final response with two current-observation claims and
valid-looking Evidence references, but both model-calculated digests were
wrong. Deterministic validation correctly rejected the mismatch, yet asking a
language model to perform exact cryptographic derivation made an otherwise
useful response unnecessarily fragile.

The digest is integrity metadata over text already present in the response. It
does not express model intent and does not need to cross the model boundary.
Same-run Evidence ownership, scope and policy generation, citation ordering,
coverage state, source coverage, and action authority remain independent
safety decisions and cannot be relaxed to improve provider compatibility.

## Decision

### Strict response schema 3

New Agent finals and reconstructed retained assistant context use strict
response schema 3. Each `evidence_citations` item contains exactly `sequence`,
`claim`, `claim_type`, `evidence_ids`, and `coverage_state`. `claim_hash` is no
longer accepted from model output.

The runtime first strictly decodes and safety-normalizes the bounded claim
text, then computes its lowercase SHA-256 digest. The project-owned durable
claim/Evidence manifest continues to store and validate that digest. Duplicate
claim detection and persistence integrity therefore still use an exact digest,
but the digest is now deterministic local metadata rather than a model claim.

This change does not synthesize Evidence or citations. A current observation
still requires a non-empty, unique, acceptance-ordered set of eligible Evidence
IDs from the exact Run, scope generation, and policy generation. Unknown,
duplicate, cross-run, cross-scope, stale-generation, out-of-order, over-limit,
or unauthorized references still fail closed. Inference, recommendation,
uncertainty, unsupported observation, limitations, conflicts, freshness, and
source coverage retain their existing typed rules.

Durable completeness manifests with response schema 1 or 2 remain readable as
historic data. No raw model response is persisted, so no database migration is
required. Replayed assistant content is reconstructed in schema 3 with empty
historic authority collections.

### Safe failure projection and no retry

The adapter distinguishes a malformed response envelope from a decoded answer
whose claim/Evidence manifest fails deterministic validation. Both remain
content-free `invalid_external_response` failures and expose no model text,
Evidence payload, resource value, endpoint detail, or internal error. Neither
failure starts another model request, repeats a Tool, commits an answer, or
drains queued input.

Provider JSON-object constraints remain explicit profile configuration. This
decision adds no endpoint probe, schema negotiation, fallback, repair call,
automatic retry, response critic, second Agent, or second loop.

## Consequences

Models are responsible for declared claims, classification, citations,
limitations, and proposed actions, but not for mechanically derived integrity
metadata. Deterministic coverage continues to prove structure, ownership,
bounds, and declared reference completeness; it still cannot prove that a
claim semantically follows from Evidence or that the answer is factually
correct.

Changing the wire schema requires all current prompt examples, retained-message
encoding, conformance fixtures, and provider tests to use schema 3. Strict
unknown-field handling intentionally rejects a newly emitted `claim_hash` so a
model cannot mistake that value for authority.

## Security and privacy impact

The runtime hashes only the already admitted normalized claim text in memory.
The durable digest reveals no additional content beyond the existing manifest
contract. Raw provider responses remain excluded from logs, SQLite, export,
and Application or TUI events. All Evidence and action authority gates are
unchanged.

## Validation

Deterministic tests cover:

1. schema 3 answers, clarifications, retained context, plans, actions, and
   Tool-result turns;
2. missing, null, duplicate, unknown, extra legacy-hash, malformed, oversized,
   and out-of-order response members;
3. runtime digest derivation before durable manifest validation;
4. duplicate claims and all missing, duplicate, unknown, stale, cross-run,
   cross-scope, cross-policy, out-of-order, and unauthorized Evidence branches;
5. distinct content-free envelope and coverage failures with exactly one model
   attempt and no automatic Tool or model retry; and
6. an explicitly authorized bounded loopback Ollama run using synthetic Tool
   Evidence and no Kubernetes access.

## References

- [Product Contract](../product.md)
- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [Privacy Overview](../privacy-overview.md)
- [Model Compatibility](../model-compatibility.md)
- [Agent Runtime](../agent-runtime.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [ADR-0049: Bound TUI Observability, Planning, Compaction, and Evidence Coverage](0049-bound-tui-observability-planning-compaction-and-evidence-coverage.md)
- [ADR-0052: Use Typed Agent Outcomes, Evidence Integrity, and Preflight](0052-use-typed-agent-outcomes-evidence-integrity-and-preflight.md)
- [ADR-0054: Preserve Structured Response Compatibility Across Turns](0054-preserve-structured-response-compatibility-across-turns.md)
