# ADR-0035: Use One User-Managed Home and Interactive Model Setup

- Status: Accepted
- Date: 2026-08-28
- Supersedes: ADR-0021

## Context

Before the first public release, KuPilot split configuration, SQLite state,
cache, and logs across platform-specific locations and required a complete model
profile plus an environment-only API key before the TUI could open. That made a
local single-user Agent difficult to discover, back up, inspect, reset, and
start. Exact owner-only mode checks also rejected otherwise usable directories
that the user had deliberately selected and manages locally.

There is no released configuration or database layout to migrate. The public
configuration schema remains version 1 and may be corrected in place without a
compatibility reader, legacy path discovery, or data migration.

## Decision

KuPilot has one process-frozen Home. `KUPILOT_HOME` selects it; otherwise it is
`$HOME/.kupilot`. Resolution requires an absolute normalized path, resolves an
existing Home symlink to one canonical target, and rejects roots that cannot be
used safely. Every automatic KuPilot filesystem write is confined to these
fixed children of the canonical Home:

- `config.yaml`
- `state/kupilot.db` and SQLite-owned sidecars
- `cache/`
- `logs/kupilot.log` and bounded rotations

An explicit Session-summary export remains the sole user-selected write outside
Home. `--config` and `KUPILOT_CONFIG_FILE` remain read-only configuration
sources; interactive setup never modifies them and writes only the fixed Home
`config.yaml`.

KuPilot creates a missing default or selected Home and its own missing
directories with mode `0700`, and creates its own missing regular files with
mode `0600`, on supported Unix platforms. It does not chmod or chown an existing
user-selected Home, directory, or regular file merely to enforce an exact mode.
Group or other permission bits may produce a bounded warning but do not by
themselves block startup. Symlink traversal below the canonical Home,
non-regular files, path replacement races, and actual read/write failures still
fail the affected component safely. An unavailable log sink is disabled
visibly; an unavailable cache is bypassed; unavailable durable state still
blocks operations whose contracts require it.

The version 1 YAML schema has no `paths` object and no `model.api_key_source`.
It permits an optional plaintext `model.api_key`. The environment precedence is
`KUPILOT_MODEL_ENDPOINT`, `KUPILOT_MODEL`, and
`KUPILOT_MODEL_API_KEY` over file values. The environment key is read once,
copied into an opaque non-renderable wrapper, and removed from the process
environment; it is never written back. A dedicated extraction boundary removes
the file key before Viper sees configuration bytes. The key is not a normal
Config, Domain, model-content, log, audit, error, SQLite, formatting, or generic
serialization value.

A bare `kupilot` starts the single-screen TUI even when endpoint, model, or key
is absent. The UI reports `model/unconfigured` and opens the fixed model-setup
flow. The flow collects endpoint, model identifier, credential, and one of two
explicit storage choices: `Save locally` or `Use for this run`. The former
discloses that the key is plaintext and not encrypted, then atomically publishes
the Home configuration with a `0600` mode for a newly created file. The latter
keeps the key only in the current runtime. `/model` repeats this flow.

Application owns model setup and replacement. A question cannot reserve or
start a run until one model runtime is ready. Reconfiguration cancels and joins
the active run, validates and constructs the replacement before swapping it,
then closes the old runtime. A changed canonical origin invalidates the prior
consent binding. At every point there is at most one active provider runtime;
there is no provider discovery, fallback, routing, or simultaneous provider
support. Model and Tool call counts remain zero until model configuration,
verified scope, consent, and durable run start have all succeeded.

The fixed `kupilot cache clear` command resolves Home and removes only directory
entries beneath the canonical `cache` child. It does not initialize Viper,
SQLite, logs, Kubernetes, the model, or the TUI. A missing cache is a successful
no-op and is not created. The command never follows a cache symlink and never
removes Home, configuration, state, or logs. It reports partial failure honestly
and does not claim forensic deletion.

## Consequences

Local state becomes predictable and first startup becomes interactive. Users
can choose convenient local permissions and plaintext credential persistence
with an explicit disclosure. Wider existing permissions and plaintext keys may
be exposed to other local principals, backups, snapshots, or filesystem tools;
KuPilot warns but does not claim encryption, secrecy from the host, or forensic
erasure.

The Home resolver, sensitive extractor, atomic writer, runtime replacement, and
cache cleaner require narrow interfaces and deterministic tests. No legacy
locations, legacy schema fields, or old database copies are recognized.

## Security and privacy impact

The local configuration file becomes an admitted credential source and
therefore a sensitive local asset. The value remains confined to the extractor,
opaque wrapper, writer, and same-origin Authorization header. Synthetic canary
tests must prove absence from ordinary Config values, Viper state, rendered UI,
transcript and composer history, Application events, errors, logs, audits,
SQLite, model content, and child environments. Atomic publication must reject
symlinks and replacement races and must never overwrite an explicit external
configuration source.

Permission warnings identify exposure without making exact Unix mode a product
availability gate. On platforms where the mode guarantee is unavailable,
documentation must describe the limitation and tests must not claim otherwise.

## Validation

Deterministic tests must cover:

1. Default and overridden Home resolution, canonicalization, fixed descendants,
   invalid roots, missing creation, existing wider permissions, symlink denial
   below Home, and cancellation.
2. Version 1 strict decoding, removal of retired fields, file and environment
   precedence, one-shot unsetting, plaintext local extraction, serialization
   denial, atomic write failure, and distinct sensitive canaries.
3. Bare unconfigured startup, masked setup input, both save choices, `/model`,
   invalid settings, construction failure, active-run cancellation and join,
   single-runtime swap, origin consent invalidation, and zero forbidden calls.
4. `cache clear` success, absent cache, nested entries, root and cache symlinks,
   path replacement, partial failure, short-circuit initialization counts, and
   proof that configuration, state, logs, and Home remain intact.

## Revisit triggers

- Adding another provider, credential manager, encrypted store, remote config,
  live reload, or simultaneous model runtime.
- Adding another automatic write outside Home or another cache namespace.
- Publishing a release whose data or configuration needs migration.

## References

- [Product Contract](../product.md)
- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [Privacy Overview](../privacy-overview.md)
- [Data Retention Contract](../data-retention.md)
- [ADR-0010: Support One OpenAI-Compatible Model Origin](0010-support-one-openai-compatible-model-origin.md)
- [ADR-0013: Layered Architecture and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0023: Use a Single-Screen Agent-Supervision TUI](0023-use-a-single-screen-agent-supervision-tui.md)
- [ADR-0026: Require Informed Consent Before Model Transfer](0026-require-informed-consent-before-model-transfer.md)
- [ADR-0033: Use Cobra for Fixed CLI Routing and Viper for Configuration](0033-use-cobra-for-cli-and-viper-for-configuration.md)
