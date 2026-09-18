# Architecture Decisions

These Accepted decisions describe the current unreleased v0.1.0 baseline.
Each records rationale, constraints and validation requirements. Acceptance is
not proof that a capability or release has passed its checks.

- [ADR-0001: Use Go and a Fixed Platform Toolchain](0001-use-go.md)
- [ADR-0002: Keep a Local Single-User Agent-First Product](0002-local-single-process-no-server.md)
- [ADR-0013: Compose Native Eino Directly in Application](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0014: Isolate Scope and Policy Generations](0014-cluster-scope-generation-isolation.md)
- [ADR-0020: Confine Kubernetes Access and Exec Credentials](0020-contain-kubeconfig-exec-credentials.md)
- [ADR-0025: Use One Safe SQLite Store](0025-enforce-data-retention-and-user-deletion.md)
- [ADR-0026: Bind Model Roles, Credentials and Consent](0026-require-informed-consent-before-model-transfer.md)
- [ADR-0035: Use One Home and Strict Configuration](0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [ADR-0037: Use a Fixed Capability Catalog and Finite Budgets](0037-adopt-an-operational-capability-catalog.md)
- [ADR-0040: Use One Conversational Supervision Screen](0040-use-a-codex-style-conversational-tui.md)
- [ADR-0044: Route Deterministic Risk Through Permission Profiles](0044-prioritize-daily-operations-and-adopt-permission-profiles.md)
- [ADR-0045: Require Digest-Bound Controlled Execution](0045-admit-controlled-execution-and-remediation.md)
- [ADR-0047: Use Eino for Session Context and Summarization](0047-reuse-eino-adk-for-session-context-and-summarization.md)
- [ADR-0048: Own Steering and Queued Follow-Up Input](0048-own-run-steering-and-queued-follow-up-input.md)
- [ADR-0052: Validate Evidence-Backed Answers and Typed Outcomes](0052-use-typed-agent-outcomes-evidence-integrity-and-preflight.md)
- [ADR-0063: Keep One Unreleased Version-One Baseline](0063-establish-the-unreleased-openai-only-baseline.md)
