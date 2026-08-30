# Changelog

This file records notable user-visible changes to Kupilot.

## Unreleased (`v0.4` candidate)

### Changed

- Reframed Kupilot as a conversational Kubernetes operations Agent while
  retaining a local, single-process, single-user architecture and explicit
  rejection of shell, kubectl, dashboard, generic API, and autonomous-control
  behavior.
- Expanded the typed operational catalog to seven structured Tools and stable
  projections for Namespace, Node, common workloads and controllers, storage,
  Ingress, autoscaling, and Pod disruption budgets.
- Added immutable `current` and `all` namespace-access policies. The working
  Namespace remains visible and the model cannot broaden the selected policy.
- Replaced the fixed four-section Diagnosis renderer with bounded free-form
  Markdown, independently validated Evidence citations, and typed proposed
  actions.
- Added `compact`, `balanced`, and `extended` immutable runtime budget profiles;
  `balanced` is the default and `/status` exposes live usage.
- Reworked the TUI toward the public Codex CLI interaction style: terminal
  foreground for primary text, higher contrast, a borderless `›` composer,
  compact inline activity, a minimal scope footer, and detailed local status
  behind `/status`.
- Added inert, width-aware GitHub-Flavored Markdown rendering. Tables use a
  readable grid on wider terminals, fall back to key/value records on narrow
  terminals, and conservatively repair an unambiguous compact one-line form.
- Improved compatible model streaming by treating bounded empty deltas as
  no-ops and allowing fragments for distinct bounded Tool indexes to
  interleave while retaining per-index assembly and all completion gates.
- Made the existing exact Deployment restart approval path reachable from a
  typed model suggestion only after a fresh trusted Deployment read derives
  all digest-bound identity and concurrency data locally.
- Advanced explicit redacted Markdown export to
  `kupilot.export-summary.v2` so it preserves escaped final answer Markdown and
  descriptive proposed-action metadata without approval authority.

### Security

- Secret objects and data, ConfigMap values, environment values, kubeconfig
  content, credentials, raw Kubernetes objects, arbitrary discovered APIs, and
  unbounded logs remain prohibited even under the broader catalog.
- Cross-Namespace calls require the frozen `all` policy and matching Kubernetes
  RBAC. Every request still uses exact typed clients, bounded projection, scope
  generation checks, and deterministic zero-call denial tests.
- A model recommendation cannot supply a Deployment UID, resource version,
  template fingerprint, generation, nonce, digest, patch, or timestamp. Proposal
  preparation performs no Kubernetes write; execution still requires explicit
  local approval, durable pre-write audit, revalidation, and one non-retried
  PATCH attempt.

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
- Strict version 1 YAML configuration, one OpenAI-compatible streaming model
  runtime, masked interactive setup with explicit plaintext-local or
  process-only credential storage, environment overrides, and explicit
  model-transfer consent.
- One fixed user-managed Home for configuration, SQLite state, cache, and
  bounded logs, plus a short-circuiting `kupilot cache clear` command.
- Local SQLite Session history with bounded operational-detail retention,
  private creation modes, respect for wider existing user-managed modes,
  forward checksummed migrations, and interruption recovery.
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
- Kubernetes credentials, Secret data, raw objects, raw container output, raw
  model traffic, and raw Tool results are excluded from model content and
  durable storage by source, projection, and sink contracts. A model key is
  durable only after the explicit plaintext-local choice and only in the fixed
  Home configuration; it remains excluded from SQLite, logs, audit, Session
  content, and model content.
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
