# Changelog

This file records notable user-visible changes to KuPilot.

## 0.3.0 - 2026-08-16

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
- The isolated `v0.2` `restart_deployment` workflow with a default-reject,
  digest-bound, 60-second, single-use local approval for one exact Deployment.
- Bounded post-PATCH Deployment observation with distinct accepted, progress,
  success, failure, timeout, unavailable, and unknown outcomes in the Approval
  Dialog and structured audit history.
- Bounded Evidence details that preserve run and scope provenance while
  excluding raw Tool results, raw objects, and raw container output.
- Standard and minimal Session modes, one-way operational-detail retention,
  transactional per-Session and clear-history deletion, exact-path local
  database deletion, bounded safe Session discovery, and a versioned redacted
  Markdown summary export in the existing TUI surfaces.
- Deterministic diagnosis provenance and assertion rubrics across all eight
  supported diagnostic categories.
- A released-schema migration matrix covering `v0.1` through `v0.3`, plus
  reproducible offline performance and release-artifact gates.

### Security

- The current read-only composition contains no Kubernetes mutation port,
  mutation adapter, write Tool, approval coordinator, shell, kubectl runner, or
  generic Kubernetes request surface.
- Kubernetes credentials, model credentials, Secret data, raw objects, raw
  container output, raw model traffic, and raw Tool results are excluded from
  model content and durable storage by source, projection, and sink contracts.
- Context and Namespace generation checks reject stale work before external I/O,
  after return, and again at Application event acceptance.
- The `v0.2` executor has one code-generated merge-patch entry point and one
  approval-service caller. Target change, replay, expiry, pre-write audit
  failure, and stale scope produce zero writes; an approved attempt is never
  retried automatically.
- Rollout observation is limited to exact Deployment reads for at most 90
  seconds and 45 observations at a minimum two-second interval. Post-attempt
  audit uses at most three idempotent attempts and fails visibly without
  repeating the Kubernetes write.
- Synthetic credential-source and safe-error matrices cover nested, wrapped,
  joined, and formatted failures across model, Tool, TUI, log, audit, SQLite,
  child-process, CLI, and error sinks with exact external-action counts.

### Known limitations

- There is no published binary or package-manager installation recorded here;
  the verified installation path is a source build.
- Diagnosis is limited to the documented eight categories and may end with
  missing information rather than a root cause.
- Windows is experimental and is not part of the supported `v0.3` runtime or CI
  release gate.
