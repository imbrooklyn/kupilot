# ADR-0042: Remember the Last Verified Kubernetes Context

- Status: Accepted
- Date: 2026-08-31
- Amends: ADR-0031 and ADR-0035

## Context

Starting a new Kupilot process without an explicit `context` currently leaves
the TUI without an active Kubernetes scope, even when kubeconfig has a current
Context. Repeatedly selecting the same Context also adds friction to ordinary
operations. Session history cannot solve this problem: a historic Session scope
is display-only, and automatically resuming a Session would violate the
explicit-resume privacy boundary.

Context names can still reveal operational metadata. Remembering one therefore
needs an explicit local-storage contract and must not turn persisted display
data into Kubernetes authority.

## Decision

Kupilot resolves the startup Context candidate in this order:

1. the effective configured `context`, after normal CLI, environment, and file
   precedence;
2. the last Context whose activation completed successfully in this local
   Kupilot Home; and
3. kubeconfig `current-context`.

An effective configured `namespace` wins when present. Otherwise the startup
working Namespace is the literal `default`; Kupilot does not inherit a
kubeconfig Context's Namespace as the implicit startup Namespace.

For a new Session, Kupilot immediately attempts to activate the resolved pair.
The candidate is never authority by itself. Every process start resolves the
Context against the current kubeconfig, constructs a fresh client through the
normal adapter path, verifies the exact Namespace, and commits a new in-memory
scope generation before an AgentRun can start. A missing remembered Context
falls back to kubeconfig `current-context`. A corrupt or unreadable preference
also falls back safely, marks local preference persistence degraded, and is
visible in the TUI. A configured Context that cannot be resolved is an explicit
configuration failure and is not silently replaced.

Kupilot updates the remembered Context only after an exact Context activation
has succeeded. A failed, cancelled, stale-generation, or partially activated
scope does not replace it. A later preference-write failure does not revoke an
already verified in-memory scope, but it is visible as degraded local
persistence and the process does not claim that the choice will survive a
restart.

The preference uses the existing SQLite `settings` table with the fixed key
`scope.last_context`, schema version 1, and a strict bounded JSON object
containing only the Context display name. It stores no Namespace, kubeconfig
path or content, cluster endpoint, credential, client, generation, ResourceRef,
Session identifier, or approval state. Clear-history preserves this global
preference; delete-all-local-state removes it with the database.

A remembered Context never selects or resumes a Session. Bare `kupilot` still
creates a new Session without a history query. Explicit resume still restores
only safe history and candidates, and every selected scope is freshly
activated. Explicit startup scope overrides retain precedence over both the
remembered Context and a resumed Session's historic scope candidate.

## Consequences

The common startup path opens with a verified current scope, and subsequent
processes return to the last successfully used Context without conflating that
convenience with conversation continuity or authorization. The fixed
`default` Namespace makes implicit startup behavior predictable across
kubeconfig Contexts.

The local database now contains one potentially sensitive Kubernetes Context
name even when no resumable Session exists. Users can remove it with the
delete-all-local-state control. If `default` is absent or forbidden, automatic
activation fails visibly and the user can restart with an explicit Namespace.

## Security and privacy impact

The preference is local metadata and is never model content merely because it
was remembered. Normal consent and projection rules still apply if the active
Context name later becomes eligible model context. Persistence errors use
stable content-free failures and must not disclose the stored name or raw JSON.

Loading the preference performs no Kubernetes API, model, Tool, approval, or
executor call. Resolution reads only safe kubeconfig Context metadata. Scope
activation retains the existing generation invalidation, cancellation,
credential confinement, Namespace verification, and late-result rejection
rules.

## Validation

Deterministic tests must prove:

- configured Context, remembered Context, and kubeconfig current Context use
  the documented precedence;
- an absent or stale preference falls back without querying Session history;
- an implicit Namespace is exactly `default`, including when kubeconfig names
  another Namespace;
- only a successfully verified Context is stored;
- cancellation, activation failure, and stale generation perform zero
  preference writes;
- corrupt/read-failed and write-failed preference paths remain visible without
  disclosing stored values;
- a restarted composition reads the durable preference but creates a fresh
  client and scope generation; and
- clear-history preserves the preference while delete-all-local-state removes
  it.

## References

- [Product Contract](../product.md)
- [Scope](../scope.md)
- [Configuration](../configuration.md)
- [Privacy Overview](../privacy-overview.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](0035-use-one-user-managed-home-and-interactive-model-setup.md)
