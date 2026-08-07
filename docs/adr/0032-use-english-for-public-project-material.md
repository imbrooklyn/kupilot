# ADR-0032: Use English for Public Project Material

- Status: Accepted
- Date: 2026-08-08

## Context

Architecture, safety rules, CLI behavior, TUI labels, errors, fixtures, and
examples must use consistent terminology for review and testing. Maintaining
several authored-language variants before a localization system exists would
allow security warnings and product contracts to drift.

KuPilot still encounters user questions and Kubernetes or model data containing
arbitrary Unicode. Project language and untrusted data are different concerns.

## Decision

English is the only authored public project language for the initial product.
The following must be written in English:

- README files, public documentation, ADRs, examples, project-authored issue,
  pull-request, and commit text, templates, release notes, and public
  architecture or security reports.
- CLI commands, flags, help, validation messages, and user-facing safe errors.
- TUI labels, consent, risk, approval, degraded-state, execution, verification,
  and recovery text.
- Model system/developer instructions, Tool names and descriptions, Diagnosis
  headings, and project-owned fixture assertions.
- Source identifiers, exported documentation, ordinary code comments, migration
  descriptions, schema comments, and project-owned audit event names.

Development discussion may use another language, but the resulting public
artifact remains English. External names, user input, Kubernetes fields, and
model text are data and may contain arbitrary valid Unicode after normalization;
they are not rewritten merely to satisfy this ADR. The Agent should answer in
the language used by the user's current question when the model can do so, and
falls back to English. This best-effort conversation behavior is not a product
localization or diagnostic-quality promise, and it adds no probabilistic
language detector.

English-only does not mean ASCII-only. Valid Unicode needed for external data,
proper names, and technical notation remains supported and must pass terminal
safety controls. Public credential, kubeconfig, Secret, and raw-output examples
remain prohibited regardless of language.

There is no localization framework, locale negotiation, translated warning,
machine-translation fallback, or per-Session language setting in `v0.1`.

## Consequences

Positive consequences:

- Normative safety and architecture language has one reviewable source.
- CLI/TUI snapshots and stable safe errors have one expected wording set.
- Terminology such as AgentRun, ClusterScope, Evidence, Diagnosis, and
  ToolInvocation remains consistent.
- Localization is not partially implemented in high-risk consent or approval
  flows.

Costs and constraints:

- Non-English users receive no localized product-owned interface or guarantee of
  diagnostic quality.
- Contributors must translate authored public changes into English before they
  are accepted.
- Unicode data still requires rendering and normalization tests independent of
  interface language.

## Alternatives considered

- Publishing bilingual normative documents was rejected because updates could
  produce conflicting security and architecture meanings without a translation
  ownership process.
- Allowing each contributor or component to choose a language was rejected
  because tests, errors, and core terminology would drift.
- Rejecting all non-ASCII or non-English user data was rejected because
  Kubernetes names and application output can legitimately contain Unicode and
  language detection is unreliable.
- Machine-translating warnings at runtime was rejected because consent and
  approval meaning must not depend on an unverified translation service.

## Security and privacy impact

One authored language makes consent, denial, degraded storage, approval,
execution, and verification wording easier to review consistently. Language is
not itself a security control; runtime scope, Tool, endpoint, credential,
retention, and approval enforcement remain authoritative.

External Unicode text remains untrusted and is normalized, bounded, and stripped
of unsafe terminal and bidirectional controls before rendering or model use.

## Validation

Documentation and authored-string checks must:

- Scan public Markdown, project-owned examples, CLI/TUI strings, Tool
  descriptions, safe messages, and code comments for unreviewed non-English
  script characters.
- Construct non-English external-data fixtures with escapes or at runtime so
  project-owned source remains English, while still testing terminal safety.
- Verify public links, terminology, and headings in English during review.
- Keep deterministic English snapshots for consent, approval, degraded storage,
  execution, verification, and recovery states.

Automated character scans cannot judge English clarity; human review remains
required. This ADR does not claim a localization API or library has been
validated.

S03 must establish the repository instruction, S04 must keep CLI/config copy
English, S14 must test the Agent's question-language preference and English
fallback, S15 must keep all TUI-owned copy English without i18n, and S28 must
audit public release documentation. Exact model language quality remains outside
these deterministic gates.

## Revisit triggers

- A funded and owned localization plan covers CLI, TUI, documentation, model
  contract, safe errors, consent, approval, and test maintenance together.
- User research justifies an additional supported diagnostic language with
  deterministic evaluation.
- Public project governance selects another canonical authored language.

## References

- [Glossary](../glossary.md)
- [Product Contract](../product.md)
- [Security Threat Model](../security.md)
- [ADR-0023: Use a Single-Screen Agent-Supervision TUI](0023-use-a-single-screen-agent-supervision-tui.md)
