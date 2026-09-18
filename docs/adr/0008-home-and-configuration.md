# ADR-0008: Use One Home and Strict Configuration

- Status: Accepted

## Context
Startup and model setup need predictable paths and strict configuration without
repository-local discovery, global mutable configuration or hidden credentials.

## Decision
Use one user-managed Kupilot Home: KUPILOT_HOME or ~/.kupilot. Resolve and
validate it independently of the current working directory. Keep fixed config,
database, log, cache and export descendants. Respect existing permissions and
create new private paths with owner-only modes.

Use Cobra for fixed CLI routing and go.yaml.in/yaml/v3 for strict typed
configuration. Pre-extract credentials into opaque wrappers. Reject duplicate,
unknown, null and wrong-type settings. Configuration schema remains version 1.
Use one loaded version field; no aliases or older-development schema readers.

help, version and cache clear short-circuit before business storage, model,
Kubernetes or TUI startup. An unconfigured Agent enters the one masked setup
flow. Saving is explicit, atomic and limited to the disclosed Home file; never
rewrite an external configuration. Reviewer setup is separate and optional.

Ordinary local diagnostics contain only bounded project-owned classes and
content-free correlation. Explicit sensitive diagnostics are opt-in and have
a visible warning, bounded fields and retention. They still exclude credentials,
raw model traffic, Kubernetes objects and reasoning state.

## Consequences and validation
Strict decoding avoids a general configuration framework and stale aliases.
One Home simplifies ownership and deletion without claiming OS-level isolation.
Test CLI zero-I/O short circuits, precedence, validation, secret extraction,
atomic publication, symlinks, existing permissions, cancellation and setup
teardown. See [Configuration](../configuration.md) and
[Development](../development.md).
