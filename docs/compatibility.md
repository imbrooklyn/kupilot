# Dependency Compatibility

This document records Kupilot's reviewed dependency-maintenance baseline. It
does not make an untested newer release, operating system, architecture, or
dependency combination supported. Kubernetes API-server and model-protocol
compatibility remain defined by their dedicated public contracts.

## Go toolchain baseline

| Boundary | Supported baseline |
| --- | --- |
| Module language and dependency lower bound | Go 1.25.0 |
| Reproducible CI and release toolchain | Go 1.25.13 |
| Supported build targets | macOS and Linux on `amd64` and `arm64` |
| Go operating-system floor | macOS 12 or newer; Linux kernel 3.2 or newer |
| Toolchain distribution and license | Official Go distribution; BSD-3-Clause |

The `go 1.25.0` directive remains the minimum version required by the selected
module graph. The exact Go 1.25.13 patch is used for repository gates and
release binaries. CI installs that version explicitly and sets `GOTOOLCHAIN` to
`local`; local gate and release commands select `go1.25.13` explicitly. The Go
toolchain has no upstream component `go.mod`; its version selection and the
main module's minimum `go` line instead follow the official
[Go toolchain semantics](https://go.dev/doc/toolchain). The official
[Go license](https://go.dev/LICENSE) is BSD-3-Clause.

Go's release policy supports a major release until two newer major releases
exist and provides critical fixes through patch releases. The
[Go 1.25 release notes](https://go.dev/doc/go1.25) retain the Go 1 compatibility
promise and define the macOS floor. The official
[release history](https://go.dev/doc/devel/release) identifies Go 1.25.13 as a
security patch release, while the
[minimum requirements](https://go.dev/wiki/MinimumRequirements) define the
accepted operating-system floors.

## Security and dependency effect

Go 1.25.13 is required for the current production call graph because it fixes
these reachable standard-library reports:

- [`GO-2026-6218`](https://pkg.go.dev/vuln/GO-2026-6218) in `net/url`;
- [`GO-2026-6090`](https://pkg.go.dev/vuln/GO-2026-6090) in `crypto/tls`;
- [`GO-2026-5972`](https://pkg.go.dev/vuln/GO-2026-5972) in `encoding/asn1`;
- the `net/http` path in
  [`GO-2026-5026`](https://pkg.go.dev/vuln/GO-2026-5026).

The Go patch update changes no module requirement, checksum, production
transitive dependency, dependency license, build tag, or CGO setting. The
production module graph remains pinned by `go.mod` and `go.sum`, Kubernetes
modules remain on one minor, and `modernc.org/sqlite` remains the only
production SQLite driver.

The pinned module graph has four additional package-level reports with no
reachable affected symbol:

- [`GO-2026-5970`](https://pkg.go.dev/vuln/GO-2026-5970) in
  `golang.org/x/text`;
- [`GO-2026-5026`](https://pkg.go.dev/vuln/GO-2026-5026) and
  [`GO-2026-4918`](https://pkg.go.dev/vuln/GO-2026-4918) in
  `golang.org/x/net`;
- [`GO-2026-4514`](https://pkg.go.dev/vuln/GO-2026-4514) in
  `github.com/buger/jsonparser`.

Six more reports affect required `golang.org/x/net` module versions but no
imported package: [`GO-2026-5942`](https://pkg.go.dev/vuln/GO-2026-5942),
[`GO-2026-5030`](https://pkg.go.dev/vuln/GO-2026-5030),
[`GO-2026-5029`](https://pkg.go.dev/vuln/GO-2026-5029),
[`GO-2026-5028`](https://pkg.go.dev/vuln/GO-2026-5028),
[`GO-2026-5027`](https://pkg.go.dev/vuln/GO-2026-5027), and
[`GO-2026-5025`](https://pkg.go.dev/vuln/GO-2026-5025). These modules remain
outside this Go-only maintenance boundary. A newly reachable affected symbol,
a changed official advisory, or a release-candidate policy change reopens the
vulnerability gate and requires a separately reviewed dependency-group
decision.

## Maintenance contract

- Keep the minimum Go version separate from the exact gate and release patch.
- Pin one reviewed Go patch across the Makefile, hosted CI, release build, and
  public maintenance commands. Do not rely on silent toolchain replacement.
- Treat a Go minor-line change or a raised module minimum as a new compatibility
  review. A same-minor security patch may retain the minimum only after the
  complete contract, race, migration, offline end-to-end, vulnerability,
  CGO-free, and supported cross-build gates pass.
- Review official release notes, supported-platform requirements, license,
  vulnerability reports, module-graph diff, and binary metadata before changing
  the pin.
- Roll back only to another supported patch that resolves all reachable
  findings. Go 1.25.12 is not an acceptable release fallback for this baseline.

The supported cluster matrix remains in
[Kubernetes Compatibility](kubernetes-compatibility.md), and the single
provider protocol with explicit Agent and optional Reviewer profiles remains
in [Model Compatibility](model-compatibility.md). Those dependency groups are
not expanded by the Go toolchain baseline.
