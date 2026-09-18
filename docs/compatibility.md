# Dependency Compatibility

## Toolchain and platforms

The module, local gates, CI and release binaries use **Go 1.27.0**.
Production dependencies, development tools and their transitive modules must
build with that exact toolchain. A Go 1.27.1 requirement is ineligible; choose
the newest compatible release and keep automatic toolchain upgrades disabled.

Production builds use CGO_ENABLED=0 for macOS and Linux on amd64 and arm64.
The Go operating-system floors are macOS 13 and Linux kernel 3.2. Windows is
experimental and outside the release gate. Native CI tests lifecycle behavior;
cross-builds alone do not prove terminal, process, filesystem or SQLite support.

See the official [Go 1.27 notes](https://go.dev/doc/go1.27),
[toolchain semantics](https://go.dev/doc/toolchain) and
[minimum requirements](https://go.dev/wiki/MinimumRequirements).

## Pinned production dependencies

go.mod and go.sum are the complete source of exact versions and checksums.
The principal selections are:

| Group | Version | License |
| --- | --- | --- |
| Go | 1.27.0 | BSD-3-Clause |
| Eino | 0.9.19 | Apache-2.0 |
| Eino Chat Completions / Responses | 0.1.13 / 0.2.2 | Apache-2.0 |
| OpenAI Go SDK | 3.50.0 | Apache-2.0 |
| Sonic / loader | 1.15.4 / 0.5.2 | Apache-2.0 |
| Bubble Tea / Bubbles / Lip Gloss | 2.0.9 / 2.2.1 / 2.0.6 | MIT |
| Cobra | 1.10.2 | Apache-2.0 |
| YAML v3 | 3.0.5 | MIT / Apache-2.0 |
| goldmark | 1.8.6 | MIT |
| client-go / api / apimachinery / metrics | 0.37.0 | Apache-2.0 |
| SQLite driver / matching libc | 1.59.0 / 1.75.7 | BSD-3-Clause |
| sqlx | 1.4.0 | MIT |

Sonic 1.15.4 supports Go 1.27 natively on the accepted architectures; no warning
suppression or dependency fork is used. Its required loader 0.5.2 GitHub Release
is marked prerelease despite the unsuffixed module tag. This exact transitive
selection must pass native and cross-platform gates; it is not evidence that all
transitive dependencies are stable releases.

OpenAI SDK 3.51.0 through 3.62.0 have Responses field types incompatible with
stable Eino agenticopenai 0.2.2. SDK 3.50.0 is the newest compatible inspected
release. The native component requires ACL revision
v0.1.18-0.20260527084435-846f52bd97c6; this is explicitly a pseudo-version.
Do not replace stable Eino with an alpha or emulate the missing API.

The Kubernetes modules stay on one minor; k8s.io/streaming is an upstream
transitive module, not another product transport. Keep modernc.org/libc at the
driver-required version. sqlx and the sole SQLite driver remain adapter-confined.

## Pinned development tools

| Tool | Version |
| --- | --- |
| goimports / x/tools | 0.50.0 |
| golangci-lint | 2.13.2 |
| govulncheck / x/vuln | 1.8.0 |
| actionlint | 1.7.12 |
| GoReleaser | 2.18.0 |
| Syft | 1.52.0 |

GoReleaser 2.18.1 and 2.18.2 require Go 1.27.1. Use 2.18.0, including when
building the validation tool; a prebuilt newer-toolchain binary is not a bypass.

## Validation and maintenance

For a dependency change, inspect exact tagged source and tests, module metadata,
licenses, graph differences and applicable release notes. Run focused tests
before make check and make check-slow, plus tagged deterministic contracts,
release configuration and native platform CI. Vulnerability results are current
command output, not a permanent list copied from a previous dependency graph.

Go 1.27 uses the updated encoding/json implementation with v1 compatibility.
Keep strict duplicate/unknown/null decoding, canonical digest and request
fixtures; do not add a compatibility flag merely because error wording changes.

The [Kubernetes](kubernetes-compatibility.md) and
[model](model-compatibility.md) contracts define narrower product admission.
No dependency upgrade enables another provider, source, API, authority or retry.
