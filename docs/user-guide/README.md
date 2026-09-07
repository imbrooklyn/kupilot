# Kupilot User Guide

This guide describes the current deterministic `v0.5` implementation boundary.
Named model roles, safe Session context/summarization, expanded diagnostics,
permission routing, the shared ActionEnvelope dispatcher, typed remediation,
and default-off exact local execution are present. This does not claim a live
integration or release artifact.

- [Getting Started](getting-started.md): build, configure, start, and use the
  single-screen TUI.
- [Sessions and Scope](sessions-and-scope.md): new Sessions, explicit resume,
  authoritative Last active, `/sessions`, exact/batch deletion, saved-scope
  conflicts, and ResourceRef revalidation.
- [Steering and Queued Follow-Ups](conversation-input.md): active-run `Enter`,
  FIFO `Tab` queueing, `Alt+Up` edit-last, commitment states, recovery, and
  no-auto-send failures, plus exact cancel and confirmed clear.
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
  plus the currently composed typed remediation and local-process boundaries.
- [Least-Privilege RBAC](../rbac/README.md): exact Kubernetes verbs, resources,
  subresources, and binding guidance.
- [Troubleshooting](../troubleshooting.md): safe recovery from common startup,
  scope, permission, model, privacy, storage, and terminal failures.

The current composition uses fourteen typed bounded diagnostic capabilities and
admits supervised restart, scale, rollback, controller-owned Pod delete,
cordon, uncordon, drain, exact local argv, and a separate shell risk class. It
adds no generic model authority, autonomous remediation, or reusable cross-
Session approval. Remote diagnostics, review-class Pod logs, and optional
Prometheus/Loki reads use the same inline approval supervision and keep the
effectful/source attempt blocked until exact authority is durably consumed.

Fixed `/copy`, `/find`, `/compact`, and `/plan` interactions remain on this one
screen. Copy and search see committed transcript content only. Context pressure
is a byte/message signal, not a token estimate. A plan is bounded ordinary
assistant content and never runs automatically.

`/delete`, `/sessions`, and `/doctor` are fixed local interactions. The same
screen provides typed terminal reasons, claim/Evidence provenance and
navigation, content-free egress/budget state, command availability, a
conservative terminal profile, committed-input reverse search, semantic jumps,
and bounded undo/redo. Strict answers may instead return one to three typed
clarification questions; no clarification or stop label creates authority.
