# Security Policy

## Supported security boundary

KuPilot security fixes target the maintained `v0.1` product boundary documented
in [`docs/scope.md`](docs/scope.md). That boundary is local, single-process,
single-user, namespaced, and strictly read-only. The admitted `v0.2`
Deployment-restart design is not reachable in the `v0.1` composition and is not
covered as an implemented security property.

The normative threat model is [`docs/security.md`](docs/security.md). The
current threat-to-control-to-test disposition and residual assurance gaps are
recorded in
[`docs/security-review-v0.1.md`](docs/security-review-v0.1.md).

## Reporting a vulnerability

Report a suspected vulnerability through a private, maintainer-controlled
security channel advertised by the repository host or maintainers. If no such
channel is visible, ask the maintainers for a private reporting channel without
including vulnerability details. Do not place an undisclosed vulnerability,
credential, kubeconfig, Secret, private cluster output, or model API key in a
public issue, discussion, log, screenshot, or fixture.

A useful report includes the affected revision or version, the violated
security boundary, impact, deterministic reproduction steps using synthetic
data, and exact forbidden action or sink counts. Minimize retained data and
remove any real credential or user cluster content before sending the report.

## Disclosure and limitations

Coordinate public disclosure with the maintainers after a fix and regression
evidence are available. This policy does not promise a response deadline,
bug-bounty payment, SQLite encryption, tamper resistance, forensic deletion,
live RBAC correctness, or security guarantees for an unimplemented write path.
