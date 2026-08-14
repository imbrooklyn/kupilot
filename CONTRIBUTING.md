# Contributing to KuPilot

Thank you for helping improve KuPilot. Contributions must preserve its narrow,
evidence-first, read-only `v0.1` boundary and the safety properties that make
that boundary reviewable.

## Start with the public contracts

Before proposing or changing behavior, read the documents that govern the
affected area:

- [Product Contract](docs/product.md) and [Version Scope](docs/scope.md)
- [Architecture](docs/architecture.md)
- [Security Threat Model](docs/security.md) and
  [Privacy Overview](docs/privacy-overview.md)
- [Data Retention Contract](docs/data-retention.md)
- Relevant Accepted decisions in [docs/adr](docs/adr)

The root [AGENTS.md](AGENTS.md) contains repository-wide implementation and
review constraints. Accepted public decisions remain authoritative until an
explicit documentation and ADR change replaces them.

For `v0.1`, do not propose a Kubernetes write, shell or kubectl execution,
Secret read, all-Namespace access, generic Kubernetes gateway, dynamic Tool,
plugin system, web service, telemetry path, or autonomous remediation as an
ordinary implementation change. A feature proposal must name a supported
diagnostic need and explain how it helps the Agent gather bounded Evidence or
helps the user supervise that work.

## Report security issues privately

Do not open a public issue for an undisclosed vulnerability. Follow
[SECURITY.md](SECURITY.md), and never attach a credential, kubeconfig, Secret,
private cluster output, raw container output, model API key, or user database to
an issue or pull request.

## Development environment

KuPilot requires Go 1.25.0 or newer. The reproducible repository gates pin Go
1.25.13 and versioned development tools. Build the current platform binary with:

```sh
make build
```

Run the fast gate while developing:

```sh
GOTOOLCHAIN=go1.25.13 make check
```

Before requesting review, run the complete local equivalent of the required
fast and slow gates:

```sh
GOTOOLCHAIN=go1.25.13 make check-all
```

The complete target list, network policy, and hosted job matrix are documented
in [Development and CI Gates](docs/development.md). The reviewed Go toolchain
baseline is recorded in [Dependency Compatibility](docs/compatibility.md). A
cold Go or tool cache and `govulncheck` require network access to their
configured official sources. Do not turn a download or vulnerability-database
failure into a skip.

## Make a focused change

- Keep the diff limited to the requested behavior. Preserve unrelated worktree
  changes and do not include generated binaries, local databases, logs, caches,
  credentials, or editor state.
- Follow the existing layer and consumer-owned port boundaries. Do not add an
  abstraction, package, dependency, schema field, Kubernetes resource, network
  destination, durable field, or Tool without a current admitted consumer and
  the required contract review.
- Keep every I/O and blocking operation Context-aware. Every goroutine needs an
  owner, cancellation path, and bounded termination path.
- Use project-owned concrete types across boundaries. Keep Eino, client-go,
  Bubble Tea, SQL, sqlx, and SQLite driver values inside their documented
  adapters.
- Preserve useful internal causes while translating external failures to stable,
  bounded, non-sensitive errors before they reach a user or sink.
- Use Go formatting and the repository import tool. Do not perform unrelated
  bulk formatting or refactoring.

## Tests and security evidence

Every behavior change needs deterministic tests for applicable success,
failure, cancellation, timeout, limit, stale-state, and sensitive-data paths.
Tests must not require a real cluster, model, public endpoint, credential, or
user state.

Security denials must assert both the safe result and a forbidden external call
or sink count of zero. Kubernetes tests must record the exact verb, resource,
Namespace, subresource, selector, limit, and projection. Concurrency tests must
use fake clocks, channels, or barriers rather than long sleeps.

Documentation examples must use clearly synthetic names and reserved invalid
destinations. Never commit a real key, kubeconfig, cluster name, Namespace,
resource name, IP address, event, log line, or database excerpt.

## Public documentation and language

All project-authored public material is English, including documentation,
identifiers, comments, CLI/TUI copy, schemas, examples, fixtures, changelog
entries, commit messages, and pull-request text. External user or Kubernetes
data may contain valid Unicode, but project-authored test values for other
scripts must be constructed at runtime or with escapes.

Update user documentation, security/privacy text, RBAC, compatibility tables,
and the changelog whenever the implemented public behavior changes. Public
documentation must link only tracked public project files, not local design or
workflow notes.

## Before review

Confirm that:

- The requested behavior is complete and remains inside the admitted version
  boundary.
- Focused tests and every applicable repository gate were actually run.
- Documentation and examples match the real CLI, configuration schema, network
  paths, data categories, permissions, and limits.
- `git status`, the complete `git diff`, and `git diff --check` show no unrelated
  edit, generated artifact, credential, prohibited data, or lost user change.
- Any skipped or failed check and its reason are stated accurately in the pull
  request.

Contributions are provided under the repository's
[Apache License 2.0](LICENSE).
