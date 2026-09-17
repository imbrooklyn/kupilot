# Changelog

No public version has been released. Earlier development labels are superseded
by the initial v0.1.0 baseline; Git history and historical ADRs retain their
original decisions and evidence.

## Unreleased — v0.1.0

### Changed

- Support OpenAI only through native Eino Chat Completions and Responses.
  Remove Ollama dependencies, transport, request repair, setup selection and
  provider-specific integration probes.
- Simplify model configuration to fixed Agent and optional Reviewer slots.
  Derive role, identity, credential ownership, streaming and Tool behavior.
  Remove provider/name/role/credential-reference/inheritance switches and old
  model environment aliases. Reviewer settings and credentials are independent.
- Preserve native reasoning and explicit sampling. Responses uses Generate;
  no error-triggered protocol change, reasoning disablement, retry or fallback.
- Fix the product version at v0.1.0 and all project-owned format, prompt,
  catalog and policy versions at 1 until first publication. Scope/policy
  generations and Session concurrency revisions remain independent counters.
- Consolidate development migrations into one initial SQLite schema. Remove
  historical restart-only archive tables and development migration paths.
  Incompatible local state fails closed and requires an explicit backed-up reset.
- Keep one Application-owned Eino Agent/Runner, one conversation loop and one
  safe SQLite conversation store.
- Apply the existing credential environment filter after merging kubeconfig
  exec settings, so neither current role keys nor retired aliases reach a child.

### Included capabilities

- Bounded typed Kubernetes observations, exact CRD policies, Events, logs,
  metrics and optional policy-bound Prometheus/Loki sources.
- Permission profiles, human or optional Reviewer decisions, immutable
  ActionEnvelopes, durable pre-operation audit and at most one execution attempt.
- Controlled remediation, exact remote diagnostics and default-off local
  execution with independent authority and privacy gates.
- Evidence-backed answers, typed clarification and plan-only output;
  deterministic claim hashes, ordering, coverage and terminal classifications.
- Explicit Session resume, safe retained history, Eino summarization, queue and
  steer handling, local search/export and a single conversational composer.
- Native macOS cursor navigation, bounded command completion and safe terminal
  projection.

### Evidence and limitations

Deterministic fixtures use synthetic Tools, loopback recording transports and
temporary SQLite. They do not establish real-cluster behavior, semantic model
quality or release readiness. Live integrations, model evaluations and release
publication require their own explicit authorization and fresh evidence.
