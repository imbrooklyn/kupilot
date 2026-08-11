# Changelog

This file records notable user-visible changes to KuPilot. There is no published
release; current behavior is recorded under `Unreleased`.

## Unreleased

### Added

- A local, single-process, single-user Kubernetes TUI centered on supervised,
  evidence-first diagnosis.
- A fixed read-only catalog of six structured Tools for Pod, Deployment,
  ReplicaSet, Job, and Service diagnosis within one verified Namespace.
- Diagnostic guidance and deterministic evaluation for CrashLoopBackOff,
  OOMKilled, ImagePullBackOff, Pod Pending, readiness probe failure, unavailable
  Deployment, failed Job, and Service without a ready Endpoint.
- Explicit Session starts and resume by picker, exact UUIDv7 Session identifier,
  or `--last`, without cwd-based or automatic history selection.
- Strict typed YAML configuration, one OpenAI-compatible streaming model
  profile, ephemeral environment-sourced model credentials, and explicit
  model-transfer consent.
- Local SQLite Session history with bounded operational-detail retention,
  owner-only path controls, forward checksummed migrations, and interruption
  recovery.
- Bounded allowlisted local operational logging with rotation and a disable
  setting.
- Source-build, user, configuration, privacy, security, troubleshooting,
  diagnostic-capability, and least-privilege RBAC documentation.

### Security

- The reachable `v0.1` composition contains no Kubernetes mutation port,
  mutation adapter, write Tool, approval coordinator, shell, kubectl runner, or
  generic Kubernetes request surface.
- Kubernetes credentials, model credentials, Secret data, raw objects, raw
  container output, raw model traffic, and raw Tool results are excluded from
  model content and durable storage by source, projection, and sink contracts.
- Context and Namespace generation checks reject stale work before external I/O,
  after return, and again at Application event acceptance.

### Known limitations

- There is no published binary or package-manager installation recorded here;
  the verified installation path is a source build.
- The public CLI/TUI always starts standard-persistence Sessions and does not
  expose per-Session deletion, clear-history, delete-all, or selection of the
  repository's enforced minimal-persistence mode.
- Diagnosis is limited to the documented eight categories and may end with
  missing information rather than a root cause.
- Windows is experimental and is not part of the supported `v0.1` runtime or CI
  release gate.
