# ADR-0033: Use Cobra for Fixed CLI Routing and Viper for Configuration

- Status: Accepted
- Date: 2026-08-08

## Context

Kupilot needs a small fixed command parser and typed configuration loading.
Cobra provides maintained command routing, while Viper provides configuration
source handling. Their broad feature sets must remain narrower than Kupilot's
accepted CLI and configuration contracts.

Using these libraries must not change the accepted product contract. A framework
feature is not an admitted Kupilot feature, and vendor state or values must not
become Application contracts.

## Decision

Kupilot uses `github.com/spf13/cobra v1.10.2` inside `internal/cli` for the fixed
command tree. Cobra does not supersede the fixed command, flag, Session,
privacy, or composition boundaries.

Each parse operation constructs a fresh command tree. The tree contains only
`resume`, `cache clear`, `version`, and `help`; bare execution produces the
typed new-Session intent. Cobra's generated completion command and suggestions
are disabled.
Framework errors are translated to bounded project-owned errors and stable exit
codes. Cobra does not initialize configuration, storage, Kubernetes, a model,
or the TUI, and Cobra types do not cross the CLI delivery boundary. `cache
clear` resolves only the canonical Kupilot Home and removes only entries below
its fixed `cache` child before any ordinary startup dependency is constructed.

Kupilot uses `github.com/spf13/viper v1.21.0` for typed configuration. Each
configuration load constructs and injects an independent Viper instance rather
than using package-level singleton state, then decodes and validates a concrete
project-owned schema before values cross a boundary.

Viper use is limited to the accepted CLI, environment, file, and default
precedence for non-sensitive configuration. Kupilot will not use remote
configuration providers, live watch or hot reload, or a generic `map[string]any`
configuration boundary. A dedicated loader extracts `model.api_key` before
Viper receives sanitized key-free bytes. A dedicated atomic writer may update
the fixed Home configuration file from an Application-owned model-setup use
case; Viper itself does not write configuration. Help, version, and `cache
clear` must return before Viper or any business dependency is initialized.

Only dependencies used by production code belong in `go.mod`.

## Consequences

Positive consequences:

- The fixed CLI grammar uses a maintained parser with deterministic typed output.
- The product has one explicit configuration library and no competing loader.
- Fresh framework instances avoid hidden cross-test and cross-command state.
- Framework features remain narrower than Kupilot's public command and config
  contracts.

Costs and constraints:

- Cobra defaults must be reviewed and disabled when they add commands, output,
  suggestions, or vendor error text.
- Viper's broad feature set and transitive dependencies require graph, license,
  strict-decoding, precedence, and sensitive-data review.
- CLI help and command definitions require tests that prevent framework upgrades
  from widening the public surface.

## Alternatives considered

- A local parser and configuration loader were not selected because Cobra and
  Viper provide maintained implementations while remaining confined to adapter
  boundaries.
- Cobra command generation was rejected because the fixed tree is small and
  generated files would add unnecessary surface.
- Viper package-level helpers were rejected because they create hidden mutable
  global state.
- Remote configuration and hot reload were rejected because they add lifecycle
  and trust surfaces not required by `v0.1`.

## Security and privacy impact

Cobra and Viper are delivery and configuration mechanisms, not authorization
boundaries. Neither library may admit credential value flags, raw kubeconfig,
questions, arbitrary commands, cwd/workspace semantics, dynamic commands, or
additional Session management. Vendor errors and parsed values must not expose
credentials, configuration contents, environment values, or local paths.

Short-circuit tests must prove that help, version, and `cache clear` do not call
the composition start path. Configuration tests must prove that API key handling
bypasses Viper, local publication is bounded and atomic, and invalid or unknown
configuration performs no model, Kubernetes, or database I/O.

## Validation

Cobra v1.10.2 declares Go 1.15 and uses Apache-2.0. Its required pflag v1.0.9
uses BSD-3-Clause, and mousetrap v1.1.0 uses Apache-2.0. Viper v1.21.0 declares
Go 1.23.0, uses MIT, and provides independent instances. These requirements are
compatible with Kupilot's Go 1.25.0 and Apache-2.0 baselines.

Tests must cover the fixed command catalog, completion absence, typed intents,
safe errors, short circuits, exit codes, and sensitive-value rejection. Viper
integration tests must cover strict YAML decoding, unknown-field rejection,
precedence, fixed Home paths, cancellation where applicable, the final
dependency graph and licenses, and absence of sensitive values outside the
dedicated extractor and writer.

## Revisit triggers

- A Cobra upgrade cannot preserve the exact fixed command and safe-error surface.
- Viper cannot provide strict typed configuration without global state or broad
  unneeded behavior.
- Either dependency raises the accepted Go or platform lower bound without a
  reviewed release decision.

## References

- [Architecture](../architecture.md)
- [Security Threat Model](../security.md)
- [ADR-0013: Use Layered Boundaries and Consumer-Owned Ports](0013-layered-architecture-and-consumer-owned-ports.md)
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
