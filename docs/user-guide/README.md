# Kupilot User Guide

This guide distinguishes the current reachable `v0.4` behavior from the
Accepted `v0.5` target. Named model roles, permission profiles, expanded
capabilities, Session summarization, and generic ActionEnvelope execution are
not reachable until their implementation and tests land.

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
- [Permissions and Controlled Actions](approval.md): `v0.5` permission profiles,
  Reviewer routing, ActionEnvelope, one-attempt execution, and verification,
  plus the currently implemented Deployment restart boundary.
- [Least-Privilege RBAC](../rbac/README.md): exact Kubernetes verbs, resources,
  subresources, and binding guidance.
- [Troubleshooting](../troubleshooting.md): safe recovery from common startup,
  scope, permission, model, privacy, storage, and terminal failures.

The current composition reads only through seven typed bounded capabilities and
admits one supervised exact Deployment restart. The Accepted `v0.5` target adds
typed daily operations, explicitly gated Pod diagnostics and local argv, and a
separate shell risk class without adding generic model authority, autonomous
remediation, or reusable cross-Session approval.
