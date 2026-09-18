# Changelog

## Unreleased — v0.1.0

Kupilot is a local, single-user Kubernetes operations Agent with one
conversational supervision screen.

- Native OpenAI Chat Completions and Responses through one Eino Agent/Runner.
- Explicit Session resume, safe SQLite history and bounded Eino summarization.
- Typed resource, Event, log and metric reads with Evidence-backed answers.
- Exact optional data sources and default-off remote/local diagnostics.
- Deterministic permission profiles, optional Reviewer and digest-bound
  supervised remediation with durable audit and at-most-once attempts.
- Scope and policy isolation, explicit model consent and sensitive-data controls.
- Go 1.27.0, CGO-free macOS/Linux amd64/arm64 builds and version-1 project formats.

No release has been published. Responses uses native non-streaming generation;
raw reasoning is ephemeral. Live endpoint and cluster compatibility require
evidence for the exact configuration. See [Version Scope](docs/scope.md),
[Dependency Compatibility](docs/compatibility.md) and
[Release Process](docs/release.md).
