# ADR-0028: Support macOS and Linux with Experimental Windows

- Status: Accepted
- Date: 2026-08-08
- Amended by: ADR-0035

## Context

Terminal lifecycle, filesystem permissions, process signals, kubeconfig lookup,
exec credential children, SQLite locking and sidecars, and static distribution
all vary by operating system and architecture. Claiming every Go target as
supported without running those behaviors would create a false compatibility and
security promise.

Kupilot needs a small, explicit platform matrix that a small team can
build and test.

## Decision

The supported platform families are macOS and Linux on `amd64` and
`arm64`, running as a local interactive user in a UTF-8-capable terminal.

Windows is experimental and is not part of the formal `v0.1` support or release
gate. BSD variants, mobile systems, browser terminals, remote Kupilot servers,
and cluster-resident or container-only operation are unsupported.

Support means more than compilation. Each platform/architecture combination
must satisfy:

- TUI input, resize, paste, signal/cancellation, alternate-screen, and terminal
  restoration behavior.
- One per-user Kupilot Home selected independently from a repository or working
  directory.
- Owner-only creation modes for new Home, SQLite, and sidecar paths, with
  existing user-managed modes respected.
- Direct exec credential process launch, environment filtering, timeout,
  cancellation, and child reaping.
- Kubeconfig and TLS behavior through the selected client-go version.
- SQLite driver, sqlx, migration, lock, interruption, retention, and delete-all
  behavior.
- Race-enabled unit/contract tests and release cross-builds.

A CGO-free build using a pure-Go SQLite driver is an accepted constraint. The
driver must satisfy ADR-0018. Introducing CGO requires a replacement ADR that
accepts the platform and packaging consequences.

The minimum Go version is 1.25.0. Minimum OS releases and terminal requirements
must be derived from maintained dependencies and verified platform behavior.

## Consequences

Positive consequences:

- Support claims have a finite executable matrix.
- Filesystem, terminal, child-process, and SQLite safety behavior receive real
  platform tests.
- Release packaging remains manageable for a small project.
- Unsupported targets fail as explicit product boundaries rather than untested
  promises.

Costs and constraints:

- Windows users receive only experimental, best-effort results and no `v0.1`
  support promise.
- Four build targets still require continuous testing and release artifacts.
- Some terminal behavior varies even within supported hosts and needs
  conservative rendering fallbacks.
- A dependency may force a narrower OS or toolchain lower bound after review.

## Alternatives considered

- Claiming all Go targets were rejected because compilation does not verify
  terminal, permission, process, or storage semantics.
- Supporting only the developer's current platform was rejected because Linux
  is a primary Kubernetes workstation and server environment.
- Making Windows a formal first-release target was rejected because permissions,
  signals, PTY, process environment, path, and SQLite behavior require a
  separate validation effort.
- Shipping only a container image was rejected because Kupilot needs the user's
  terminal, kubeconfig, and local Session store and is not a cluster-side Agent.

## Security and privacy impact

Owner-only creation modes and exec-child containment are part of platform
support, not optional best effort. Existing user-selected modes remain the
local user's responsibility and are not an availability gate. A target that
cannot satisfy the create-time guarantee is unsupported until a reviewed
equivalent control exists.

Kupilot still relies on operating-system account isolation and disk protection.
Platform support does not imply SQLite encryption, sandboxing of a kubeconfig
exec credential program, or protection from a local administrator.

## Validation

Formal support requires CI or documented reproducible runs that record:

1. The selected Go lower bound and exact dependency versions from official
   module metadata.
2. Build and unit/contract results for macOS and Linux on `amd64` and `arm64`,
   using native runners for terminal, permission, exec, and SQLite behaviors that
   cross-builds cannot prove.
3. Binary dependency/CGO characteristics and release archive contents.
4. Signal, cancellation, terminal restoration, owner-only creation, unchanged
   existing modes, symlink rejection, child reaping, and database sidecar
   behavior.
5. Linux as the primary CI gate, macOS build/smoke evidence, clearly labeled
   experimental Windows results if produced, and any explicit minimum OS or
   terminal requirements.

The release support statement must not include a platform without this record.

## Revisit triggers

- User demand justifies a Windows or other platform matrix with equivalent
  security controls.
- A mandatory dependency removes support for one accepted architecture.
- A CGO requirement or platform-specific credential store changes packaging.
- Kupilot changes from a local terminal process to another topology.

## References

- [ADR-0001: Use Go](0001-use-go.md)
- [ADR-0005: Use Bubble Tea v2 for the TUI Runtime](0005-use-bubble-tea-v2.md)
- [ADR-0018: Require One Pure-Go SQLite Driver](0018-require-one-pure-go-sqlite-driver.md)
- [ADR-0020: Contain Kubeconfig Exec Credentials](0020-contain-kubeconfig-exec-credentials.md)
- [ADR-0035: Use One User-Managed Home and Interactive Model Setup](0035-use-one-user-managed-home-and-interactive-model-setup.md)
- [Security Threat Model](../security.md)
