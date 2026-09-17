# ADR-0063: Establish the Unreleased OpenAI-Only Baseline

- Status: Accepted
- Date: 2026-09-18
- Supersedes: ADR-0055, ADR-0058, and ADR-0059
- Amends: ADR-0008, ADR-0010, ADR-0011, ADR-0022, ADR-0025, ADR-0029,
  ADR-0035, ADR-0038, ADR-0041, ADR-0044, ADR-0046, ADR-0047, ADR-0052,
  ADR-0053, ADR-0056, ADR-0057, and ADR-0061

## Context

Kupilot has not published a supported release. Earlier version labels and
incremental schemas describe development iterations, not compatibility promises.
Keeping their migration and compatibility paths makes the current system harder
to understand. Local Ollama conformance did not establish the reliability needed
for this product, even after correcting native request serialization defects.
Maintaining that provider and its corrections is no longer a product requirement.
This is a support decision based on the tested combination, not a claim that all
Ollama models are incapable of production use.

## Decision

### One supported provider

Support only OpenAI through the existing native Eino Chat Completions and
Responses components. Preserve explicit `api_protocol`, optional reasoning and
sampling, and the existing one Agent/Runner composition in Application. Do not
disable reasoning, emulate provider features, detect models, repair output,
retry, route, or fall back. Remove the native Ollama dependency, request
corrections, NDJSON handling, setup choices, diagnostics, and live test targets.
An OpenAI-compatible service must satisfy the same explicit protocol contract;
there is no separately supported Ollama compatibility route.

The YAML `models.agent` and optional `models.approval_reviewer` slots determine
the role, fixed profile identity, and credential slot. Each supplies its own
endpoint, model, and optional plaintext `api_key`, or its role-specific API-key
environment variable. Remove `name`, `role`, `provider_kind`, `credential_ref`,
`inherit_agent`, `streaming`, and `tool_calling_required` from model configuration.
There is no implicit Reviewer credential or settings inheritance. Remove the
retired `KUPILOT_MODEL_*` environment aliases.

Streaming and Tool availability follow the role and selected protocol. Agent
Chat Completions streams; Agent Responses uses native non-streaming generation
because the pinned streaming component loses encrypted reasoning. Reviewer and
summary calls remain non-streaming and Tool-free. This is fixed composition,
not user-selectable authority. Keep settings with actual consumers: endpoint,
model, API protocol, reasoning effort, response format, optional temperature,
optional output-token ceiling, and bounded request timeout. Omission uses the
documented defaults; null, unknown keys, and removed fields are rejected.

### One initial development baseline

The product version is `v0.1.0`. All project-owned schema, prompt, catalog,
export, and policy format versions start at `1` and remain there until the first
`v0.1.0` release. Correct these unpublished formats in place. Do not create
another application version, schema generation, migration, compatibility reader,
or tag to accommodate local development state. Consolidate SQLite into one
initial checksummed schema with only the current tables and constraints.

This renumbering preserves the current admitted functionality; it does not
restore the historical read-only milestone. Dependency versions and Kubernetes
API versions are external contracts and are unchanged. Scope/policy generations,
message sequences, and optimistic-concurrency versions remain live counters.
ADR numbers and historical evidence identities remain historical records.

Older development files and databases are unsupported. Loading never silently
rewrites configuration, deletes history, or fakes a migration. Correct local
configuration explicitly. Before resetting incompatible local development
storage, stop its owner and preserve a consistent backup. Production startup
continues to reject unknown, corrupt, and checksum-mismatched storage.

After the first release, published schemas require explicit compatibility
decisions and forward migrations. This pre-release reset is not a permanent
permission to rewrite released data contracts.

## Security and privacy

Role- and origin-bound credentials and consent, verified transport, scope and
policy generations, finite budgets, strict Tool binding, Evidence ownership,
runtime-derived claim hashes and ordinals, action authority, and durable commit
barriers remain required. Retained history never restores authority. Removed
configuration switches do not become hidden ways to relax these controls.
Raw model, reasoning, Tool and Evidence payloads remain excluded from ordinary
logs, TUI diagnostics, SQLite and exports under the existing sink contracts.

## Validation

Deterministic tests cover the minimal model configuration, both OpenAI APIs,
reasoning continuity, role-specific credentials, setup/save/load, removed-field
rejection, safe failures, and zero unauthorized calls. Fresh SQLite tests cover
the complete initial schema, constraints, transactions, interrupted-run recovery,
retention, deletion and resume. Unknown and obsolete storage must fail safely.
Current protocol fixtures replace development-version migration fixtures.

Run focused checks followed by the repository's deterministic, race, security,
dependency, lint and platform gates. Hosted CI uses synthetic dependencies and
temporary state. Historical endpoint observations remain historical evidence;
they are not a passing result for this refactor or proof of model quality.
Commit and CI validation do not constitute a release.
