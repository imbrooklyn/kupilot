# Release Process

KuPilot releases are built locally from the repository `Makefile`. The release
configuration produces versioned, CGO-free archives for the four supported
macOS and Linux targets. Publishing is a separate, explicitly authorized
maintainer action: the repository has no automated publishing workflow, and
GoReleaser publishing is disabled.

This process must not use a production kubeconfig, model credential, cluster,
local KuPilot database, operational log, or user configuration. See the
[Security Threat Model](security.md), [Privacy Overview](privacy-overview.md),
and [Development and CI Gates](development.md) before preparing a candidate.

## Supported artifacts

The formal `v0.1` release matrix is:

| Operating system | Architecture | Archive |
| --- | --- | --- |
| macOS | `amd64` | `kupilot_0.1.0_darwin_amd64.tar.gz` |
| macOS | `arm64` | `kupilot_0.1.0_darwin_arm64.tar.gz` |
| Linux | `amd64` | `kupilot_0.1.0_linux_amd64.tar.gz` |
| Linux | `arm64` | `kupilot_0.1.0_linux_arm64.tar.gz` |

Windows is experimental and has no `v0.1` release artifact. Each archive
contains exactly `kupilot` and the unmodified Apache-2.0 `LICENSE`. Source
files, local documentation, configuration, databases, SQLite sidecars, logs,
and credentials are not release assets.

Each archive has a sibling SPDX 2.3 JSON SBOM named
`ARCHIVE.tar.gz.spdx.json`. One
`kupilot_0.1.0_checksums.txt` file contains SHA-256 checksums for all four
archives and all four SBOMs. The nine files are the complete upload allowlist;
GoReleaser working metadata, unpackaged binaries, and its effective
configuration are not release assets.

Installation and checksum verification are documented in
[Installing a Release Archive](user-guide/installation.md).

## Pinned release tools

Release tooling is installed into the ignored `bin/tools` tree and does not
enter `go.mod` or the production dependency graph.

| Tool | Version | Purpose | License |
| --- | --- | --- | --- |
| Go | 1.25.12 | Compile and inspect release binaries | BSD-3-Clause |
| GoReleaser | v2.13.3 | Build, archive, and checksum the matrix | MIT |
| Syft | v1.44.0 | Produce archive-level SPDX JSON SBOMs | Apache-2.0 |

These tool versions are selected because they build with the exact Go 1.25.12
gate toolchain. A version update requires review of its Go requirement,
license, configuration schema, generated contents, and reproducibility
behavior.

## Version and build metadata

Release linker flags inject only these non-sensitive values:

- `version`: `v` followed by the requested SemVer value;
- `commit`: the full source commit, displayed as its first 12 lowercase
  hexadecimal characters;
- `built`: the source commit time normalized to UTC RFC 3339.

`built` is deliberately the commit time, not the wall-clock packaging time.
The binary also reports the exact Go version and its target operating system
and architecture. A `v0.1.0` artifact therefore has this output shape:

<!-- markdownlint-disable MD013 -->
```text
kupilot version=v0.1.0 commit=0123456789ab built=2026-01-02T03:04:05Z go=go1.25.12 platform=darwin/arm64
```
<!-- markdownlint-enable MD013 -->

Development builds retain honest `dev` or `unknown` values. Runtime
environment variables cannot override build metadata.

## Local candidate dry run

Start from the repository root with a clean worktree and no release tag
creation or publishing credentials. Run the complete local CI equivalent
before packaging:

```sh
GOTOOLCHAIN=go1.25.12 make check-all
GOTOOLCHAIN=go1.25.12 make release-dry-run RELEASE_VERSION=0.1.0
```

The dry-run target performs the following fixed sequence:

1. Validate the SemVer input and the GoReleaser configuration.
2. Verify the production dependency closure for all four targets with
   `CGO_ENABLED=0`, require `modernc.org/sqlite`, and reject `runtime/cgo`,
   `github.com/mattn/go-sqlite3`, or any selected CgoFiles.
3. Create an isolated temporary workspace and run GoReleaser in snapshot mode.
4. Build all four targets with Go 1.25.12, `-trimpath`, read-only modules, and
   reproducible commit timestamps.
5. Create archives, SPDX SBOMs, and SHA-256 checksums without publishing.
6. Copy only the nine upload-allowlisted files into the printed candidate
   directory.
7. Verify checksums, exact archive membership, embedded target metadata, the
   pure-Go SQLite module, SPDX version, credential-shaped content, private
   source paths, and native `--version` output.

The target refuses to replace an existing repository `dist` path. It uses a
temporary symlink only while GoReleaser is running, removes that link on
success or failure, and leaves the temporary workspace available for review.
Do not distribute a candidate merely because the dry run completed.

From the same commit and pinned toolchain, the four archive bytes and their
archive hashes are reproducible. SPDX documents contain their own generation
time and document namespace, so a regenerated SBOM and the aggregate checksum
file can differ between otherwise equivalent runs. The checksum file from the
candidate being published is authoritative for that candidate.

## Release checklist

### Source and policy

- [ ] The worktree is clean, `HEAD` is the reviewed release commit, and the
  intended version is not already published or associated with another
  commit.
- [ ] `CHANGELOG.md` describes only shipped `v0.1.0` behavior and known
  limitations.
- [ ] `LICENSE` is the standard Apache License 2.0 text and has not been
  rewritten.
- [ ] `go.mod` and `go.sum` are unchanged by the release commands; module
  checksums pass and the production dependency license review has no unresolved
  incompatible or missing-license finding.
- [ ] Binary redistribution notices for every MIT, BSD, ISC, and Apache
  dependency are available with the download in a form approved for
  redistribution. If review requires an attribution bundle, archive contents
  and the checksum/SBOM allowlist have been updated and re-verified before
  publication.
- [ ] The public support matrix agrees with
  [ADR-0028](adr/0028-support-macos-and-linux-with-experimental-windows.md) and
  [Kubernetes Compatibility](kubernetes-compatibility.md).
- [ ] No source or documentation claims a Windows artifact, package-manager
  installation, signed artifact, notarized artifact, or automated publisher.

### Gates and candidate contents

- [ ] `GOTOOLCHAIN=go1.25.12 make check-all` passes with a current
  vulnerability database.
- [ ] `GOTOOLCHAIN=go1.25.12 make release-dry-run
  RELEASE_VERSION=0.1.0` passes from the same commit.
- [ ] A second dry run produces the same SHA-256 value for each of the four
  archives.
- [ ] The candidate directory contains exactly four archives, four sibling
  SPDX JSON documents, and one checksum file.
- [ ] Every checksum verifies and every archive contains only `kupilot` and
  `LICENSE`.
- [ ] Binary metadata reports Go 1.25.12, `CGO_ENABLED=0`, the correct target,
  `modernc.org/sqlite v1.56.0`, and no CGO SQLite driver.
- [ ] Archive, binary-string, and SBOM scans contain no database, WAL/SHM
  sidecar, log, product configuration, key material, private source path, or
  local-only documentation.
- [ ] The SBOM inventory and dependency licenses have been reviewed. An SBOM
  `NOASSERTION` field is not a license approval.

### Platform and smoke checks

- [ ] Both macOS artifacts execute `kupilot --version` on matching `amd64` and
  `arm64` hosts.
- [ ] Both Linux artifacts execute `kupilot --version` on matching `amd64` and
  `arm64` hosts.
- [ ] The offline smoke run below passes without creating user-state files or
  making model or Kubernetes requests.
- [ ] Any live integration smoke uses a separately authorized disposable
  environment, synthetic data, least-privilege RBAC, and a throwaway model
  credential. It is recorded as supplemental evidence, not CI proof.

### Manual publication

- [ ] A maintainer has separately authorized tag creation and publication.
- [ ] The immutable `v0.1.0` tag identifies the exact commit used for the final
  candidate. A tag is never moved or reused.
- [ ] Release notes are derived from the matching `CHANGELOG.md` entry and do
  not promise unsupported behavior.
- [ ] Only the nine allowlisted candidate files are attached to the draft
  release.
- [ ] The attached files are downloaded into a clean directory, the published
  checksum file verifies them, and compatible hosts repeat `--version` and the
  offline smoke.
- [ ] The draft is published only after the download verification passes.

Checksums detect corruption but do not authenticate the publisher. The `v0.1`
process does not create signatures, notarization, provenance attestations, or
package-manager metadata. Apply the repository host's access controls and the
organization's release-approval policy accordingly.

## Manual smoke runbook

### Offline archive smoke

Use a newly created temporary directory containing only one selected archive,
its checksum file, and its sibling SBOM. Verify the selected checksum as shown
in the [installation guide](user-guide/installation.md), then extract the
archive.

On a matching host, run:

```sh
./kupilot --version
./kupilot version
./kupilot help
./kupilot help resume
```

Both version forms must report the expected version, commit, commit time, Go
version, and platform. Help must return without creating a database, contacting
Kubernetes, contacting a model endpoint, or entering the TUI.

For the safe startup-error check, point the platform-specific XDG configuration,
state, and cache variables at an empty temporary tree; set `KUBECONFIG` to an
empty test file; leave `KUPILOT_MODEL_API_KEY` empty; and invoke KuPilot with an
absolute path to a nonexistent configuration file. The command must return a
safe `config_file_unavailable` error, must not echo an environment value, and
must not create a database, WAL/SHM sidecar, or log.

### Separately authorized integration smoke

A live smoke is optional and must not use production data. Provision the test
environment outside this runbook, then use:

- a disposable cluster and Namespace containing only synthetic workloads;
- the documented [least-privilege RBAC](rbac/README.md), never
  `cluster-admin`;
- a dedicated kubeconfig Context with no unrelated credentials;
- a throwaway model credential and an approved test endpoint;
- synthetic questions, Events, labels, annotations, and container output that
  contain no real customer or operational data.

Confirm scope verification, informed consent, bounded read-only Tool calls, a
new Session, explicit resume, and safe cancellation. Record the Kubernetes
requests and require zero write verbs. Remove the disposable namespace,
credential, kubeconfig, local database, WAL/SHM sidecars, logs, and
configuration through the environment owner's approved cleanup process.

## Withdrawal and rollback

Before publication, discard the temporary candidate or delete the unpublished
draft. No rollback action is required because the dry-run configuration cannot
publish.

After publication, do not silently replace an archive, SBOM, checksum file, or
tag. If an artifact is incorrect or unsafe:

1. Stop further downloads by marking the release unavailable and removing its
   downloadable assets through the repository host's audited maintainer
   controls.
2. Preserve the tag and incident evidence; never move or reuse the version.
3. Publish a clear withdrawal notice and follow the private reporting process
   in [SECURITY.md](../SECURITY.md) when security is involved.
4. Revoke any affected publishing credential and review repository access.
   Release artifacts must never contain that credential.
5. Fix the source or process, choose a new patch version, repeat every gate and
   smoke check, and publish a new immutable release.
6. Account for downstream mirrors and caches: withdrawing the repository-host
   assets cannot guarantee deletion of copies already downloaded.

KuPilot has no automatic release rollback, background updater, or mechanism to
remove a binary from a user's machine.
