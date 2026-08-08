# ADR-0033: Use Cobra for Fixed CLI Routing and Viper for Configuration

- Status: Accepted
- Date: 2026-08-08

## Context

KuPilot needs a small fixed command parser and typed configuration loading.
Cobra provides maintained command routing, while Viper provides configuration
source handling. Their broad feature sets must remain narrower than KuPilot's
accepted CLI and configuration contracts.

Using these libraries must not change the accepted product contract. A framework
feature is not an admitted KuPilot feature, and vendor state or values must not
become Application contracts.

## Decision

KuPilot uses `github.com/spf13/cobra v1.10.2` inside `internal/cli` for the fixed
command tree. Cobra does not supersede the fixed command, flag, Session,
privacy, or composition boundaries.

Each parse operation constructs a fresh command tree. The tree contains only
`resume`, `version`, and `help`; bare execution produces the typed new-Session
intent. Cobra's generated completion command and suggestions are disabled.
Framework errors are translated to bounded project-owned errors and stable exit
codes. Cobra does not initialize configuration, storage, Kubernetes, a model,
or the TUI, and Cobra types do not cross the CLI delivery boundary.

KuPilot uses `github.com/spf13/viper v1.21.0` for typed configuration. Each
configuration load constructs and injects an independent Viper instance rather
than using package-level singleton state, then decodes and validates a concrete
project-owned schema before values cross a boundary.

Viper use is limited to the accepted CLI, environment, file, and default
precedence for non-sensitive configuration. KuPilot will not use remote
configuration providers, live watch or hot reload, configuration writes, or a
generic `map[string]any` configuration boundary. Model API keys and other
credential values remain outside Viper and configuration files. Help and version
must return before Viper or any business dependency is initialized.

Only dependencies used by production code belong in `go.mod`.

## Consequences

Positive consequences:

- The fixed CLI grammar uses a maintained parser with deterministic typed output.
- The product has one explicit configuration library and no competing loader.
- Fresh framework instances avoid hidden cross-test and cross-command state.
- Framework features remain narrower than KuPilot's public command and config
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
- Remote configuration, hot reload, and configuration persistence were rejected
  because they add I/O, lifecycle, and trust surfaces not required by `v0.1`.

## Security and privacy impact

Cobra and Viper are delivery and configuration mechanisms, not authorization
boundaries. Neither library may admit credential value flags, raw kubeconfig,
questions, arbitrary commands, cwd/workspace semantics, dynamic commands, or
additional Session management. Vendor errors and parsed values must not expose
credentials, configuration contents, environment values, or local paths.

Short-circuit tests must prove that help and version do not call the composition
start path. Configuration tests must prove that API key handling bypasses Viper
and that invalid or unknown configuration performs no model, Kubernetes, or
database I/O.

## Validation

Cobra v1.10.2 declares Go 1.15 and uses Apache-2.0. Its required pflag v1.0.9
uses BSD-3-Clause, and mousetrap v1.1.0 uses Apache-2.0. Viper v1.21.0 declares
Go 1.23.0, uses MIT, and provides independent instances. These requirements are
compatible with KuPilot's Go 1.25.0 and Apache-2.0 baselines.

Tests must cover the fixed command catalog, completion absence, typed intents,
safe errors, short circuits, exit codes, and sensitive-value rejection. Viper
integration tests must cover strict YAML decoding, unknown-field rejection,
precedence, supported platform paths, cancellation where applicable, the final
dependency graph and licenses, and absence of sensitive values.

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
- [ADR-0021: Use Ephemeral Model API Key Sources](0021-use-ephemeral-model-api-key-sources.md)
- [ADR-0031: Require Explicit CLI Session Resume](0031-require-explicit-cli-session-resume.md)
