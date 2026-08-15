# KuPilot User Guide

This guide describes the current, reachable `v0.3` behavior.

- [Getting Started](getting-started.md): build, configure, start, and use the
  single-screen TUI.
- [Sessions and Scope](sessions-and-scope.md): new Sessions, explicit resume,
  saved-scope conflicts, and ResourceRef revalidation.
- [Privacy and Local Data](privacy-and-local-data.md): cloud categories,
  consent, container output, SQLite, local logs, retention, per-Session
  deletion, redacted export, and cleanup.
- [Evidence Details](evidence.md): bounded provenance fields, retained-detail
  states, keyboard flow, and excluded raw data.
- [Configuration](../configuration.md): complete typed YAML, environment, CLI,
  endpoint, path, and logging schema.
- [Diagnostic Capabilities](../diagnostic-capabilities.md): the eight supported
  scenarios and the Evidence required for each.
- [Least-Privilege RBAC](../rbac/README.md): exact Kubernetes verbs, resources,
  subresources, and binding guidance.
- [Troubleshooting](../troubleshooting.md): safe recovery from common startup,
  scope, permission, model, privacy, storage, and terminal failures.

The current `cmd/kupilot` composition is read-only. It does not execute
recommendations and has no shell, kubectl, Pod Exec, write Tool, or approval
dialog.
