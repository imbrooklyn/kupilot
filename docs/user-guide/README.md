# Kupilot User Guide

This guide describes the current, reachable `v0.4` behavior.

- [Getting Started](getting-started.md): build, configure, start, and use the
  single-screen TUI.
- [Sessions and Scope](sessions-and-scope.md): new Sessions, explicit resume,
  saved-scope conflicts, and ResourceRef revalidation.
- [Privacy and Local Data](privacy-and-local-data.md): cloud categories,
  consent, container output, SQLite, local logs, retention, per-Session
  deletion, redacted export, and cleanup.
- [Supporting Observation Details](evidence.md): bounded display fields,
  retained-detail states, keyboard flow, and excluded internal or raw data.
- [Configuration](../configuration.md): complete typed YAML, environment, CLI,
  endpoint, path, and logging schema.
- [Operational and Diagnostic Capabilities](../diagnostic-capabilities.md): the
  typed resource catalog, regression scenarios, Evidence, and action boundary.
- [Deployment Restart Approval](approval.md): exact target preparation, local
  approval, one write attempt, and rollout verification.
- [Least-Privilege RBAC](../rbac/README.md): exact Kubernetes verbs, resources,
  subresources, and binding guidance.
- [Troubleshooting](../troubleshooting.md): safe recovery from common startup,
  scope, permission, model, privacy, storage, and terminal failures.

The current composition reads only through typed bounded capabilities and
admits one supervised exact Deployment restart. It has no shell, kubectl, Pod
Exec, generic write Tool, autonomous remediation, or reusable approval.
