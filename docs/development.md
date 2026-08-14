# Development and CI Gates

KuPilot's local and hosted gates use the repository `Makefile` as their single
command source. The hosted workflow invokes the same targets contributors run
locally; it does not duplicate test selection or security policy in workflow
steps.

## Required versions

The module's supported minimum remains Go 1.25.0. CI pins Go 1.25.12 as the
reviewed patch version on that minimum-version line. `GOTOOLCHAIN` is set to
`local` in CI so a job cannot silently replace the selected toolchain.
Aggregate gate targets verify that exact Go patch version before running. With
Go toolchain management enabled, the complete local equivalent can be selected
explicitly with `GOTOOLCHAIN=go1.25.12 make check-all`.

The following development tools are installed into the ignored `bin/tools`
tree by versioned Makefile rules. They are development dependencies and do not
enter `go.mod` or the production dependency graph.

| Tool | Version | Gate |
| --- | --- | --- |
| `goimports` | v0.48.0 | Import grouping and Go formatting |
| `golangci-lint` | v2.11.4 | Repository lint configuration |
| `govulncheck` | v1.6.0 | Reachable Go and module vulnerability scan |
| `actionlint` | v1.7.12 | GitHub Actions syntax and semantic checks |

## Local commands

Use these aggregate targets from the repository root:

| Command | Purpose |
| --- | --- |
| `make check` | Fast gate: formatting, imports, vet, lint, workflow lint, unit and integration tests, and a CGO-free build |
| `make check-slow` | Slow gate: race, security, dependency, migration, offline end-to-end, fuzz-seed, vulnerability, and supported cross-build checks |
| `make check-all` | Complete local equivalent of the required fast and slow Linux gates |
| `make platform-smoke` | Full deterministic test suite plus a CGO-free native build |

The component targets `workflow-lint`, `test-security`, `import-guard`,
`dependency-guard`, `test-migration`, `test-e2e`, `test-fuzz-seeds`,
`test-race`, `vuln`, and `cross-build` are available for focused diagnosis.
`make bootstrap-tools` installs all pinned development tools without running a
gate.

Security-denial behavior remains asserted in repository tests, including zero
external action or sink counts. The security target selects the documented
redactor, scope, Slash, endpoint, SQLite/WAL, terminal, model-text, and
Diagnosis-persistence controls. The dependency guard runs the static import and
read-only composition tests before verifying module checksums.

## Hosted CI

The GitHub Actions workflow defines three gate job groups:

- `fast` runs `make check` on Linux amd64.
- `slow` runs `make check-slow` on Linux amd64.
- `platform-smoke` runs `make platform-smoke` on Linux arm64, macOS arm64, and
  macOS amd64.

Linux amd64 is the primary correctness and security gate. The additional Linux
and macOS jobs verify the supported architecture and platform contract.
Windows remains experimental and is not a release gate. Workflow actions are
pinned to immutable commit digests, permissions are read-only, and no workflow
uses `pull_request_target` or uploads runtime databases, WAL sidecars, logs, or
configuration files.

CI and deterministic tests must not use a real cluster, model endpoint,
kubeconfig, API key, or user state. Kubernetes and model transport tests use
narrow fakes or loopback-only fixture servers, while SQLite tests use temporary
database files that are not retained as artifacts.

## Network and vulnerability-database policy

A cold tool or module cache requires access to the configured official Go
module sources. `govulncheck` also requires the current official Go
vulnerability database. A dependency download, tool bootstrap, checksum, or
vulnerability-database availability failure fails the affected gate; it must
not be converted into a skip or ignored result. Retry only after connectivity
or the upstream service is restored.

Tool, action, runner, or Go patch-version updates must remain fixed and be
reviewed together with the Makefile, workflow, and this document. A scanner
finding must be fixed or handled through the project's documented security
decision process; the CI command must not suppress it merely to restore a green
status.

## Work-item and release-gate scope

Work-item acceptance, subsequent work sequencing, and release-candidate
admission are separate decisions. A repository-wide vulnerability gate may
remain open across independently scoped work only when review establishes all
of the following:

- The finding originates entirely in the pinned toolchain or dependency
  baseline rather than the work item's code or dependency changes.
- The work item neither introduces the affected dependency or call path nor
  makes the finding newly reachable.
- The finding does not invalidate the work item's boundary-specific security,
  privacy, migration, or sink tests.
- The scanner remains enabled, the finding remains visible, and remediation is
  owned by release-critical toolchain or dependency work.

Under those conditions, the finding does not reopen independently accepted
work, prevent that work from being committed, or prevent later separately
scoped work from starting. It still blocks a release candidate, tag,
publication, and any claim that the complete repository gate is green. A
finding introduced or made reachable by the current work remains a blocking
failure for that work item and cannot use this separation.

The platform policy is defined by
[ADR-0028](adr/0028-support-macos-and-linux-with-experimental-windows.md).
Security and privacy
requirements remain normative in the [Security Threat Model](security.md),
[Security Review](security-review-v0.1.md), and
[Privacy Overview](privacy-overview.md).
