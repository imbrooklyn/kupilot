# ADR-0002: Keep a Local Single-User Agent-First Product

- Status: Accepted

## Context
Operators need bounded assistance and explicit supervision from their own
terminal, without operating another service or granting a model cluster authority.

## Decision
Keep Kupilot local, single-process, single-user and Agent-first, with one active
AgentRun and one verified Kubernetes Context at a time. Use one conversational
screen and one Application authority. The product version is v0.1.0.

Do not become a controller, daemon, scanner, hosted service, telemetry collector,
plugin host, MCP client, retriever, or multi-agent orchestrator. No background
scan, Watch/informer, scheduled execution, cross-Context operation or autonomous
remediation is admitted. Do not fork a cluster-management application.

Project-authored tracked material is English. External Unicode input remains
valid data subject to terminal normalization; it does not justify locale options
or an internationalization framework.

## Consequences and alternatives
A fixed local topology makes credentials, lifetime, scope and deletion ownership
visible. It excludes unattended and multi-user operation. A dashboard or server
would change the product and security model rather than simplify this use case.

## Validation
CLI short-circuit and static import tests enforce the topology. Application,
TUI and process tests enforce one active run, cancellation, bounded join and
single-editor supervision. See [Product Contract](../product.md) and
[Version Scope](../scope.md).
