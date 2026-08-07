# ADR-0028: Support macOS and Linux with Experimental Windows

- Status: Accepted
- Date: 2026-08-08

## Context

Terminal lifecycle, filesystem permissions, process signals, kubeconfig lookup,
exec credential children, SQLite locking and sidecars, and static distribution
all vary by operating system and architecture. Claiming every Go target as
supported without running those behaviors would create a false compatibility and
security promise.

The initial project needs a small, explicit platform matrix that a small team can
build and test.

## Decision

The initial supported platform families are macOS and Linux on `amd64` and
`arm64`, running as a local interactive user in a UTF-8-capable terminal.

Windows is experimental and is not part of the formal `v0.1` support or release
gate. BSD variants, mobile systems, browser terminals, remote KuPilot servers,
and cluster-resident or container-only operation are unsupported.

Support means more than compilation. Each platform/architecture combination
must satisfy:

- TUI input, resize, paste, signal/cancellation, alternate-screen, and terminal
  restoration behavior.
- Per-user configuration and data-directory resolution without a repository or
  working-directory state model.
- Owner-only KuPilot data directory, SQLite database, and sidecar permissions.
- Direct exec credential process launch, environment filtering, timeout,
  cancellation, and child reaping.
- Kubeconfig and TLS behavior through the selected client-go version.
- SQLite driver, sqlx, migration, lock, interruption, retention, and delete-all
  behavior.
- Race-enabled unit/contract tests and release cross-builds.

A CGO-free build using a pure-Go SQLite driver is an accepted constraint. The
S06 gate selects the concrete driver. If no candidate passes, the session stops
and platform and packaging consequences require a replacement ADR before CGO is
introduced.

This ADR does not select minimum OS releases, terminal brands, Go versions, or
dependency versions. Those values must be derived from maintained dependencies
and recorded by the session gates below before release claims are published.

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
- A dependency may force a narrower OS or toolchain lower bound after the S04
  evidence gate.

## Alternatives considered

- Claiming all Go targets were rejected because compilation does not verify
  terminal, permission, process, or storage semantics.
- Supporting only the developer's current platform was rejected because Linux
  is a primary Kubernetes workstation and server environment.
- Making Windows a formal first-release target was rejected because permissions,
  signals, PTY, process environment, path, and SQLite behavior require a
  separate validation effort.
- Shipping only a container image was rejected because KuPilot needs the user's
  terminal, kubeconfig, and local Session store and is not a cluster-side Agent.

## Security and privacy impact

Owner-only permission checks and exec-child containment are part of platform
support, not optional best effort. A target that cannot satisfy them is
unsupported until a reviewed equivalent control exists.

KuPilot still relies on operating-system account isolation and disk protection.
Platform support does not imply SQLite encryption, sandboxing of a kubeconfig
exec credential program, or protection from a local administrator.

## Validation gate

S04 must record the Go lower bound and initial cross-builds; S06 must prove the
pure-Go SQLite targets; S09 and S15 must prove exec/client and terminal behavior;
and S29 must complete the release cross-build and dry-run matrix. Before formal
support is published, CI or documented reproducible runs must record:

1. The selected Go lower bound and exact dependency versions from official
   module metadata.
2. Build and unit/contract results for macOS and Linux on `amd64` and `arm64`,
   using native runners for terminal, permission, exec, and SQLite behaviors that
   cross-builds cannot prove.
3. Binary dependency/CGO characteristics and release archive contents.
4. Signal, cancellation, terminal restoration, owner-only files, symlink
   rejection, child reaping, and database sidecar behavior.
5. Linux as the primary CI gate, macOS build/smoke evidence, clearly labeled
   experimental Windows results if produced, and any explicit minimum OS or
   terminal requirements.

No platform-specific version or driver result is claimed verified before that
record exists.

## Revisit triggers

- User demand justifies a Windows or other platform matrix with equivalent
  security controls.
- A mandatory dependency removes support for one accepted architecture.
- A CGO requirement or platform-specific credential store changes packaging.
- KuPilot changes from a local terminal process to another topology.

## References

- [ADR-0001: Use Go](0001-use-go.md)
- [ADR-0005: Use Bubble Tea v2 for the TUI Runtime](0005-use-bubble-tea-v2.md)
- [ADR-0018: Select a Pure-Go SQLite Driver Through an S06 Gate](0018-select-a-pure-go-sqlite-driver-through-an-s06-gate.md)
- [ADR-0020: Contain Kubeconfig Exec Credentials](0020-contain-kubeconfig-exec-credentials.md)
- [Security Threat Model](../security.md)
