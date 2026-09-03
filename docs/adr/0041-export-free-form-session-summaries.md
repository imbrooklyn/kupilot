# ADR-0041: Export Free-Form Session Summaries

- Status: Accepted
- Date: 2026-08-30
- Supersedes: ADR-0034
- Amended by: ADR-0047

ADR-0047 permits the `v0.5` export to include the versioned safe Session
summary and coverage explanation while retaining all existing source
allowlists, escaping, bounds, explicit confirmation, no-overwrite publication,
and zero external operational I/O. Raw Eino state, prompts, model traffic,
Tool/Exec/log output, Session rules, Reviewer response bytes, and execution
authority remain excluded.

## Context

ADR-0034 admitted an explicit, redacted Markdown export but froze its Diagnosis
projection to the original four collections. ADR-0038 replaces that visible
report contract with a bounded free-form Markdown answer plus verified Evidence
and typed proposed-action metadata. Export must preserve the answer that the
user actually saw without turning Markdown or proposed actions into authority.

## Decision

Kupilot keeps the explicit, target-bound, no-overwrite export flow and advances
the document schema to `kupilot.export-summary.v2`.

The allowlisted Diagnosis projection contains:

- the bounded, sanitized final `answer_markdown`;
- citation-backed confirmed facts and other retained compatibility metadata;
- validation warnings and Evidence availability metadata; and
- proposed operation and safe target display fields, without UID, resource
  version, nonce, digest, fingerprint, approval state, or execution authority.

The renderer places answer Markdown in an escaped quotation block. Content is
data, not document structure: a model-produced heading, link, HTML fragment, or
fenced block cannot forge an export section. Every eligible free-text field
still passes field limits, normalization, sensitive-value handling, and the
aggregate export guard.

All ADR-0034 selection, confirmation, persistence, filesystem, audit,
concurrency, deletion, and prohibited-source rules remain in force. Export is
still local, explicit, single-Session, standard-persistence only, and never
causes model, Kubernetes, Tool, approval, or executor activity.

Previously created `kupilot.export-summary.v1` documents remain standalone user
files. Kupilot does not import, upgrade, discover, overwrite, or delete them.

## Consequences

The portable summary matches the conversational result while retaining a
machine-identifiable schema and deterministic safety boundary. Consumers must
treat v1 and v2 as different document schemas.

The export can still contain operationally sensitive names and conversation
text. It is plaintext and protected only by the selected local filesystem and
operating-system controls.

## Security and privacy impact

Free-form answer text increases the importance of escaping and aggregate
limits. It does not broaden eligible sources. Proposed actions are descriptive
only; the export contains no value that can approve, replay, or execute them.

Tests must prove prohibited canaries are absent from final bytes, audit rows,
safe errors, and temporary files, and must prove every denial path performs
zero filesystem publication and zero external operational calls.

## Validation

Deterministic tests cover schema v2 rendering, answer escaping, typed-action
projection, truncation, two-pass redaction, expired Evidence, no-overwrite
publication, audit-before-write, cancellation, and concurrent Session deletion.

## References

- [ADR-0034: Export Only Versioned Redacted Session Summaries](0034-export-only-versioned-redacted-session-summaries.md)
- [ADR-0038: Use Free-Form Answers with Verified Evidence Metadata](0038-use-free-form-answers-with-verified-evidence-metadata.md)
- [Data Retention Contract](../data-retention.md)
- [Privacy Overview](../privacy-overview.md)
- [ADR-0047: Reuse Eino ADK for Session Context and Summarization](0047-reuse-eino-adk-for-session-context-and-summarization.md)
