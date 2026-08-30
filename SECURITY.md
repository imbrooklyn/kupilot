# Security Policy

## Supported boundary

Security reports are accepted for the current `v0.4` source candidate. Kupilot
is a local, single-process, single-user Kubernetes operations Agent with a
typed, bounded read catalog and one supervised Deployment-restart action. The
restart path is composed, but remains behind fresh target preparation,
digest-bound local approval, revalidation, durable pre-write audit, one PATCH
attempt, and separate rollout verification. No published `v0.4` release is
recorded in the [Changelog](CHANGELOG.md). This policy covers the current source
boundary, not an unverified distribution artifact.

The normative controls and residual risks are documented in the
[Security Threat Model](docs/security.md). The
[v0.1 Read-Only Composition Security Review](docs/security-review-v0.1.md) is
historical evidence for controls retained from the earlier boundary; it is not
a complete review of the broader catalog or composed write path. Current
deterministic tests cover typed request allowlists, scope gates, sensitive-data
projections, approval denial paths, and exact external-action counts. This is
not a claim about real production data, live RBAC, or third-party penetration
testing.

## Report a vulnerability privately

Use a private, maintainer-controlled security-reporting channel exposed by the
repository host or maintainers. If no private channel is visible, ask the
maintainers to establish one without including vulnerability details in the
request.

Do not disclose an unpatched vulnerability in a public issue, discussion, pull
request, commit, log, screenshot, or fixture. Never send a real credential,
kubeconfig, Kubernetes Secret, private cluster output, raw container output,
model API key, local database, or application log. Reproduce with synthetic
data and minimize every retained artifact.

A useful report includes:

- The affected revision or published version, if one exists.
- The expected security boundary and the observed violation.
- Impact and preconditions.
- Deterministic reproduction steps using synthetic data.
- Exact model, Kubernetes, Tool, repository, child-process, or executor call and
  sink counts, especially when the expected count is zero.
- A safe contact method for coordinated follow-up.

## Coordinated handling

Maintainers should acknowledge the report through the private channel, confirm
scope, reproduce with synthetic fixtures, assess affected revisions, prepare a
minimal fix and regression evidence, and coordinate disclosure after users can
obtain the fix. This policy does not promise a response deadline, remediation
deadline, CVE assignment, or bug-bounty payment.

Public disclosure should avoid operational data and include the affected
boundary, supported versions, mitigation, fixed version or revision, and the
tests that prevent regression.

## Operator security guidance

- Use the [least-privilege RBAC](docs/rbac/README.md); do not grant Kupilot
  `cluster-admin`.
- Keep configuration, state, database sidecars, and local logs owner-only. Do
  not put the model API key in YAML, argv, history, or a project file.
- Review the exact model destination and enabled categories before accepting
  consent. Container output is disabled by default.
- Set `kubernetes.exec_credentials: deny` when the selected kubeconfig must not
  launch an exec credential program.
- Treat the local SQLite database and log as unencrypted local files. Use
  operating-system disk protection and manage backups and snapshots according
  to organizational policy.
- Stop using Kupilot and report privately if a credential, cross-scope result,
  terminal-control effect, unexpected network path, or unapproved or
  out-of-contract Kubernetes write is observed.

Kupilot does not claim SQLite encryption, tamper resistance, forensic deletion,
protection from a local administrator, sandboxing of kubeconfig exec credential
programs, live RBAC correctness, third-party penetration testing, or guaranteed
model accuracy. See the [Privacy Overview](docs/privacy-overview.md) for the
cloud and local data boundary.
