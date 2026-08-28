# KuPilot v0.1 Security Review

- Review disposition: meets the read-only composition security-review risk
  threshold with no open finding or assurance gap
- Baseline reviewed: 2026-08-28
- Authority: the [Security Threat Model](security.md),
  [Architecture](architecture.md), [Privacy Overview](privacy-overview.md), and
  [Data Retention Contract](data-retention.md)

This review maps every threat in the accepted threat model to implementation
controls and deterministic evidence. It covers the current reachable read-only
composition, which preserves the `v0.1` write-absence invariant. The admitted
but unreachable `v0.2` write threats are included so that write absence is
explicit rather than inferred.

## Conclusion

The review found no credential disclosure, automatic write outside the fixed
Home, cache deletion escape, cross-origin authorization leak, cross-scope
acceptance, terminal-control execution, or reachable Kubernetes write path in
the current composition. Both Medium findings are closed: model-originated free
text is processed before actions and sinks, and Agent plus SQLite use the same
all-accepted-Evidence observation-window invariant.

Both Low assurance gaps are closed. The credential-source matrix distinguishes
the permitted Kubernetes transport use from every prohibited sink, and the
safe-error matrix covers nested, wrapped, joined, and formatted failures across
configuration, Kubernetes, model, Tool, SQLite, Application, and shutdown
aggregation boundaries with exact action counts.

<!-- markdownlint-disable MD013 -->

| Severity | Open review items | Closed review items |
| --- | ---: | ---: |
| Critical | 0 | 0 |
| High | 0 | 0 |
| Medium | 0 | 2 |
| Low | 0 | 2 |

<!-- markdownlint-enable MD013 -->

Status meanings:

- **Pass**: reachable behavior has both implementation and deterministic test
  evidence for the stated threat.
- **Finding**: a deterministic test reproduces behavior that contradicts or
  weakens the required control.
- **Gap**: no bypass was observed, but the required proof is incomplete.
- **Not reachable**: the threat belongs to the admitted `v0.2` write boundary,
  and static composition evidence shows that boundary is absent from the
  current read-only composition.

## Stable composition evidence

- The model-visible catalog in
  [`internal/agent/catalog.go`](../internal/agent/catalog.go) contains exactly
  `get_resource`, `list_resources`, `get_events`, `get_pod_logs`,
  `get_previous_pod_logs`, and `get_related_resources`. Strict catalog and
  schema evidence is in
  [`internal/agent/catalog_test.go`](../internal/agent/catalog_test.go).
- Consumer-owned boundaries are checked by
  [`internal/application/boundary_test.go`](../internal/application/boundary_test.go),
  [`internal/agent/einoadapter_boundary_test.go`](../internal/agent/einoadapter_boundary_test.go),
  [`internal/kube/boundary_test.go`](../internal/kube/boundary_test.go),
  [`internal/tools/boundary_test.go`](../internal/tools/boundary_test.go), and
  [`internal/tui/boundary_test.go`](../internal/tui/boundary_test.go).
  Eino remains at the Agent translation boundary, client-go remains in the
  Kubernetes adapter, Bubble Tea remains in TUI and composition, and sqlx plus
  the pure-Go SQLite driver remain in the SQLite adapter.
- The reachable Kubernetes Tool ports expose fixed typed reads. The `v0.1`
  composition contains no executor, mutation port, approval coordinator, write
  Tool, shell, dynamic client, or generic REST request surface. Static and
  runtime evidence is in
  [`internal/tools/boundary_test.go`](../internal/tools/boundary_test.go) and
  [`cmd/kupilot/composition_test.go`](../cmd/kupilot/composition_test.go).
- SQLite uses the four checksummed forward migrations in
  [`internal/persistence/sqlite/migrations`](../internal/persistence/sqlite/migrations),
  explicit column mappings, fixed SQL, bound values, and the allowlisted schema
  checks in
  [`internal/persistence/sqlite/migrate_test.go`](../internal/persistence/sqlite/migrate_test.go)
  and
  [`internal/persistence/sqlite/session_repository_static_test.go`](../internal/persistence/sqlite/session_repository_static_test.go).
- [`internal/config`](../internal/config) freezes one canonical Home, extracts
  the optional file credential before ordinary decoding, atomically publishes
  only the fixed Home configuration, and clears cache entries through a
  non-following boundary. Model setup and single-runtime replacement remain
  Application-owned, with delivery-only masked input in
  [`internal/tui`](../internal/tui).

## Threat-to-control-to-test-to-status matrix

<!-- markdownlint-disable MD013 -->

| Threat | Control and code evidence | Deterministic test evidence | Status |
| --- | --- | --- | --- |
| T01: Kubernetes credentials reach a prohibited sink, or unsafe kubeconfig permissions are missed | C01, C02, C08, and C10; credential loading and client construction remain in [`internal/kube`](../internal/kube), while SQLite fields are allowlisted | `TestSecurityAssuranceKubernetesCredentialSourceMatrix`, `TestSecurityAssuranceSafeErrorSourceSinkMatrix`, `TestConfigLoaderReportsUnsafePermissionsWithoutChangingOrExposingPath`, and exec-credential strict-denial, environment, output, and cancellation tests | **Pass — SR-003 closed.** Distinct kubeconfig, bearer-token, client-certificate, private-key, exec-stdout, and exec-stderr canaries reach only their permitted adapter use and are absent from every prohibited sink; permissions remain unchanged. |
| T02: the model API key is saved without explicit choice, rendered, retained after failure, inherited, disclosed, or redirected | C01-C03, C10, and C14; pre-Viper file extraction, one-shot opaque environment and TUI sources, masked input, queue cleanup, child-environment filtering, atomic fixed-Home publication, and same-origin transport policy | `TestModelAPIKeyCanaryIsAbsentFromEveryStartupSink`, `TestFileModelAPIKeyCanaryIsExtractedFromOrdinaryConfiguration`, `TestLoadEnvironmentCredentialOverridesFileAndIsUnsetOnce`, `TestUnconfiguredModelSetupMasksCredentialAndEmitsOneTypedRequest`, `TestApplicationRequestFilterAndDrainDestroyRejectedModelSecrets`, `TestCoordinatorModelSetupConstructionFailureKeepsOldRuntime`, redirect tests, and exec-child tests | **Pass.** A synthetic key reaches only the explicitly selected Home file or configured-origin Authorization header. Process-only and failed setup publish no key, while ordinary Config, render/history, errors, logs, audit, SQLite, child, and redirect sinks exclude it. |
| T03: Secret or other high-risk text is read or smuggled through eligible content | C04/C07 model-text processing plus C06 source denial and the ordered egress pipeline in [`internal/agent`](../internal/agent), [`internal/tools`](../internal/tools), and [`internal/security/redactor.go`](../internal/security/redactor.go) | `TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler`, `TestDiagnosisValidatorSanitizesEveryModelFreeTextField`, `TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence`, `TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction`, Kubernetes source-denial tests, `FuzzRedactorSafety`, and the full sink canary integration test | **Pass — SR-001 closed.** Lower-risk eligible text is replaced before downstream use; high-confidence model text is blocked before Tool/Kubernetes action, and denial tests observe zero forbidden calls or sinks. |
| T04: prompt or Tool-result injection changes runtime authority | C04, C05, C07, and C11; authority comes from immutable run state and fixed dispatch in [`internal/agent`](../internal/agent) | `TestSystemPromptDoesNotEmbedQuestionOrToolLanguageInjection`, `TestAdapterRejectsHostileToolSelectionsBeforeHandler`, `TestMaliciousToolOutputCannotAuthorizeAnotherTool`, scope-generation tests, and zero-action Slash denials | **Pass.** Hostile text does not alter catalog, endpoint, scope, budgets, approval state, or write authority. |
| T05: structured Tool input adds scope, Kind, selector, deadline, or larger limits | C04 strict decoding, runtime scope injection, and C09 ceilings in [`internal/agent/catalog.go`](../internal/agent/catalog.go) | `TestToolCatalogIsExactStrictAndScopeFree`, `TestToolCallPolicyRejectsUnknownForbiddenAndInvalidBeforeHandler`, `TestToolCallBindingInjectsScopeCeilingsAndCanonicalDefaults`, and `FuzzBindToolCallStrictSchema` | **Pass.** Invalid authority is rejected before handler I/O; accepted canonical calls retain only runtime-injected scope and ceilings. |
| T06: endpoint confusion, redirects, or error bodies exfiltrate credentials or content | C03 canonical origin, normal TLS, loopback-only HTTP, redirect denial, consent binding, and safe errors | `TestValidateModelEndpointPolicy`, `TestRedirectPolicyAllowsOnlyCanonicalOrigin`, `TestPrivateHTTPSUsesCertificateAndHostnameVerification`, `TestCrossOriginRedirectIsDeniedBeforeAuthorizationCanMove`, `TestHTTPAndTransportErrorCanariesAreBoundedAndDiscarded`, and `TestPrivacyConsentLifecycleBindsOriginCategoriesAndPolicy` | **Pass.** Invalid origins fail locally, authorization does not cross origin, and changed consent tuples prevent content transfer. |
| T07: stale Context or Namespace work reaches a sink | C05 three-gate generation checks in [`internal/application/scope_manager.go`](../internal/application/scope_manager.go), Agent runtime, and Application event acceptance | `TestScopeManagerSwitchContextInvalidatesBeforeCreatingTarget`, `TestScopeManagerDropsBlockedOldGenerationResourceResult`, `TestCoordinatorCancelsOneRunAndRejectsLateEvents`, `TestGetEventsDiscardsLateResultAfterScopeBecomesStale`, `TestGetPodLogsDiscardsRawContentAfterScopeBecomesStale`, and TUI stale-result tests | **Pass.** Before-call stale work performs zero action; in-flight and late work is rejected before Evidence, model, persistence, or current TUI state. |
| T08: broad RBAC permits access outside fixed Kind, relationship, Namespace, or projection policy | C06 task-specific read ports and projections in [`internal/kube`](../internal/kube) | Exact-action tests for resources, Events, logs, and related resources in `tool_resources_test.go`, `tool_events_logs_test.go`, and `tool_related_resources_test.go`; denial tests assert zero forbidden client actions | **Pass.** Recorded requests use only admitted verbs, resources, Namespace, subresources, and limits; relationship traversal stays within fixed nodes, edges, and hops. |
| T09: prose, malformed streams, duplicate calls, or invented Tools bypass structured calling | C04 structured events without text fallback and C09 bounded decoding | `TestCompatibilityFixturesProduceNeutralStreamEvents`, `TestMalformedAndOversizeFixturesHaveOneClassifiedTerminalError`, `TestNeutralModelContractIsMinimalAndRejectsAmbiguity`, hostile Tool selection tests, duplicate Diagnosis tests, and `FuzzBoundedSSEBodyIsChunkIndependent` | **Pass.** Malformed or invented authority produces one classified terminal outcome and no unintended handler call. |
| T10: SQLite leaks excluded data, accepts SQL authority, opens unsafe paths, changes a user-managed mode, or silently loses integrity | C08/C14 fixed safe paths, create-only permissions, allowlisted schema, bound SQL, checksummed migrations, integrity gates, model-text processing, and the all-accepted-Evidence Diagnosis window invariant in [`internal/persistence/sqlite`](../internal/persistence/sqlite) | Database/WAL canary scans, `TestOpenRespectsExistingUserModesAndConfiguresConnectionPragmas`, `TestOpenCreatesPrivateStateAndDatabase`, path tests, `TestRepositorySourcesKeepExplicitSQLBoundary`, migration corruption and unknown-schema tests, Diagnosis window and retention tests, and `TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis` | **Pass — SR-001 and SR-002 closed.** New files use private modes, existing modes remain unchanged, sensitive model values do not reach SQLite, and every accepted or cited Evidence invariant remains same-run validated without false degradation. |
| T11: retention exceeds policy, minimal mode resumes, deletion is partial, or cleanup failure is hidden | C08 plus explicit retention repositories and startup/run gates | cutoff, minimal-shell, cascade, rollback, cancellation, startup maintenance, resume eligibility, and durable-run-start zero-model/Tool-call tests in [`internal/persistence/sqlite`](../internal/persistence/sqlite), [`internal/application`](../internal/application), and [`cmd/kupilot`](../cmd/kupilot) | **Pass.** Cutoffs and cascades are transactional; mandatory failures are visible and fail before prohibited work. |
| T12: external text executes terminal controls, spoofs typed state, or hides scope | C07 local styling, sanitization, bounded render state, typed scope/approval meaning, and fixed Slash registry in [`internal/tui`](../internal/tui) | `TestSanitizeExternalTextRemovesTerminalAndBidiControls`, `TestApplicationTextAndScopeAreSanitizedBeforeRenderState`, golden/render tests, paste and stale-event tests, `TestSlashRegistryIsFixedAndReadOnly`, and `FuzzParseSlashDraftHasNoDynamicAuthority` | **Pass.** Escape, control, bidi, invalid UTF-8, and dynamic Slash inputs do not become terminal or runtime authority. |
| T13: streams, results, logs, recursion, retries, or queues exceed budgets | C09 atomic ceilings, child deadlines, owned cancellation, local projection bounds, and bounded event/render state | `TestRunBudgetEnforcesExactHardLimits`, one-over and concurrent reservation tests, exact Tool result/log/graph limits, event queue and TUI cumulative stream tests, oversize model fixtures, and `FuzzBoundedSSEBodyIsChunkIndependent` | **Pass.** Boundary and one-over cases stop with bounded output, no post-exhaustion call, and one terminal result. |
| T14: raw vendor errors or logs disclose credentials, bodies, SQL, paths, or cluster data | C01 and C10 safe classifications at adapter boundaries | `TestSecurityAssuranceSafeErrorSourceSinkMatrix`, `TestSecurityAssuranceSafeErrorCancellationIdentity`, `TestSecurityAssuranceShutdownAggregationKeepsOnlySafeErrors`, Kubernetes wrapped-error tests, model HTTP/transport error canaries, SQLite safe repository errors, and config value-hiding tests | **Pass — SR-004 closed.** Stable classes and cancellation identity survive direct, nested, wrapped, joined, and formatted paths while model, Tool, TUI, log, audit, SQLite, child-process, CLI, and error sinks exclude the canaries. |
| T15: a hidden Kubernetes write path exists in `v0.1` | C11 absent mutation composition, fixed read-only contracts, and runtime rejection of claimed execution | `TestReadOnlyToolPathContainsNoWriteShellOrGenericKubernetesEscape`, `TestCompositionConstructsOneModelLifecycleAndNoWritePath`, exact Kubernetes request recorders, fixed Slash tests, exact catalog tests, and `TestDiagnosisValidatorRejectsUnregisteredEvidenceAndExecutionClaims` | **Pass.** Static imports and reachable methods expose only admitted reads; all observed requests are read-only and execution prose cannot become a confirmed fact. |
| T16: a future approval is replayed, broadened, stale, or targets a changed Deployment | C11 excludes the C12 coordinator and executor from `v0.1` | `TestCompositionConstructsOneModelLifecycleAndNoWritePath` and Application/Tool/TUI boundary tests | **Not reachable.** No `v0.2` approval state or executor is constructed in the reviewed composition. This is not evidence for a future `v0.2` implementation. |
| T17: database failure bypasses pre-write audit or causes a duplicate write | C11 excludes all write orchestration from `v0.1`; read-only persistence failures follow the explicit degraded-state policy | composition no-write tests and Coordinator durable-start/degraded-storage tests with model and Kubernetes call counts | **Not reachable.** There is no write or pre-write audit path in `v0.1`; reachable read-only persistence failures remain visible and fail closed. |
| T18: kubeconfig exec is model-influenced, shell-launched, leaking, hanging, key-inheriting, or not strictly denied | C02 selected-config ownership, direct launch, clean environment, bounded output, owner cancellation, and strict deny in [`internal/kube`](../internal/kube) | `TestExecCredentialsStrictDenyPerformsZeroLaunches`, `TestExecCredentialsAllowDirectLaunchAndRemoveModelKey`, protocol denial, safe output/failure, and owner-cancellation tests | **Pass.** Strict mode launches nothing; allowed mode owns exact argv without a shell or model key and bounds failure output. |
| T19: model or TUI events bypass Application and invoke an executor | C11 composition isolation, consumer-owned ports, typed UI commands, and fixed Tool dispatch | Agent, Application, Tool, and TUI boundary tests; forged/unknown Slash and model execution-claim tests; composition no-write test | **Pass.** Neither Agent nor TUI can reference an executor, and forged text/events produce no approval or write action. |
| T20: restart issues a broader or repeated write, or conflates acceptance with verification | C11 excludes the C12 operation from `v0.1` | exact catalog, read-only boundary, composition, and request-recorder tests | **Not reachable.** `restart_deployment` and all Kubernetes write methods are absent from the reviewed composition. This does not pre-approve a future implementation. |
| T21: summary export leaks a prohibited source or path, overwrites, follows a link, publishes partial bytes, races deletion, or replays | C08, C10, and C13 fixed projection, two-pass guard, content-free audit, serialization, and atomic no-replace publication | Export projection, filesystem, Application integration, deletion-barrier, restart, and cross-sink canary tests named in the accepted export contract | **Pass.** Only a confirmed new Markdown target receives the bounded versioned projection; denial and race paths leave no partial output or reusable authority. |
| T22: startup or model save writes outside Home, overwrites an external configuration, changes existing modes, follows a managed link, or leaves a partial key file | C14 canonical fixed descendants, read-only external inputs, create-only mode policy, pre-Viper extraction, target revalidation, and atomic publication | `TestResolvePathsUsesOneFixedHomeLayout`, canonical Home-link tests, `TestSaveModelProfileCreatesPrivateHomeConfigAndLoadExtractsCredential`, existing-mode, cancellation, symlink, target-replacement, temporary-cleanup, and file-canary tests | **Pass.** Automatic writes stay under canonical Home, external configuration remains read-only, new paths receive private modes, existing modes are preserved, and failed publication does not change the selected target. |
| T23: cache clearing initializes ordinary services, deletes non-cache data, follows a link, escapes a replacement, creates missing paths, or falsely reports partial completion | C14 fixed short-circuit command and descriptor-relative non-following deletion on supported Unix platforms | `TestCompositionRootCacheClearShortCircuitsOrdinaryStartup`, `TestClearCacheIsIdempotentAndDoesNotCreateHome`, nested/link/non-directory/cancellation tests, and `TestClearCacheEntryReplacementDoesNotEscapeCache` | **Pass.** Only entries below the canonical cache child are removed. Missing cache is unchanged, link targets and displaced entries survive, ordinary composition is untouched, and cancellation returns an incomplete outcome. |

<!-- markdownlint-enable MD013 -->

Threat status totals are 20 Pass, 0 Finding, 0 Gap, and 3 Not reachable.

## Findings

### SR-001: Model-originated free text bypasses sensitive-value processing

- **Severity:** Medium
- **Status:** Closed
- **Affected boundaries:** model-to-runtime, runtime-to-model, Application-to-TUI,
  and Application-to-SQLite

The Agent applies the project-owned text pipeline to every model-provided Tool
purpose, optional name query, and Diagnosis free-text field before downstream
use. Lower-risk matches become the fixed redaction marker. High-confidence
sensitive output is classified and blocked before Tool handler or Kubernetes
I/O, and invalid control-bearing Tool arguments remain denied by the strict
neutral contract. Only the processed canonical Tool selection can enter a later
model request, Application event, rendered TUI, or durable ToolInvocation. Raw
structured Diagnosis deltas are retained only in the bounded decoder; display
progress is code-defined until final validation succeeds.

Deterministic evidence is
`TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler`,
`TestDiagnosisValidatorSanitizesEveryModelFreeTextField`,
`TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence`,
`TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction`, and the
`security_review_finding_SR-001` integration cases in
[`internal/application/coordinator_integration_test.go`](../internal/application/coordinator_integration_test.go).
They assert absence from follow-on model requests, Application events, rendered
TUI, AuditEvents, lifecycle observations, safe errors, and SQLite files.
High-confidence denial also asserts zero ToolInvocations and no increase in the
Kubernetes request count.

### SR-002: Unreferenced accepted Evidence causes persistence degradation

- **Severity:** Medium
- **Status:** Closed
- **Affected boundaries:** Agent Diagnosis validation, SQLite Diagnosis
  persistence, and Application run admission

Agent validation and SQLite durable validation now share one invariant: the
Diagnosis observation window covers all accepted Evidence from the AgentRun,
while the initial detail state also covers accepted Tool-result truncation that
produced no Evidence. Evidence citations remain a separate provenance check,
and every cited ID must exist in that same run. SQLite uses one fixed, bounded
aggregate query for the full run window and truncation state, plus fixed per-ID
queries for citations. Retention can still turn historic detail into `partial`
or `expired` without rewriting the stored Diagnosis text or observation window.

Deterministic evidence is
`TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow`, the
cross-run and retention-state repository tests, and the
`security_review_finding_SR-002` integration case in
[`internal/application/coordinator_integration_test.go`](../internal/application/coordinator_integration_test.go).
The cases cover zero references, one accepted Evidence reference, a subset of
references, truncated Evidence, Tool-result truncation without Evidence,
rejected cross-run references, a durable unreferenced Diagnosis, and a
successful later run without false persistence degradation.

### SR-003: Kubernetes credential-source sink proof

- **Severity:** Low
- **Status:** Closed
- **Affected threats:** T01 and C01/C02/C08/C10
- **Revisit trigger:** any credential-source or prohibited-sink change

`TestSecurityAssuranceKubernetesCredentialSourceMatrix` injects distinct
synthetic values through kubeconfig bytes and path, bearer token, generated
client certificate and private key, and exec standard output and standard
error. It distinguishes the permitted authenticated transport use and direct
credential decoding from project-owned outputs and formatted safe errors, with
exact server, client, and child-process counts. The independent downstream sink
matrix covers model requests, Application events, rendered TUI, ordinary logs,
AuditEvents, SQLite rows and database sidecars, child environments, and CLI
output. Permission tests prove that warnings expose neither content nor path
and do not change mode bits.

### SR-004: Joined and formatted safe-error proof

- **Severity:** Low
- **Status:** Closed
- **Affected threat:** T14 and C01/C10
- **Revisit trigger:** any safe-error boundary, sink, or shutdown-aggregation
  change

`TestSecurityAssuranceSafeErrorSourceSinkMatrix` covers configuration,
Kubernetes, model, Tool, SQLite, and Application boundaries through direct,
nested, wrapped, joined, and formatted errors. It enumerates model requests,
Tool results, Application events, rendered TUI, ordinary logs, AuditEvents,
SQLite rows and database sidecars, child environments, and CLI output while
asserting exact action counts. `TestSecurityAssuranceSafeErrorCancellationIdentity`
preserves cancellation identity without exposing raw causes, and
`TestSecurityAssuranceShutdownAggregationKeepsOnlySafeErrors` proves stable
`errors.Is` and class behavior across ordered, idempotent shutdown aggregation.

## Release decision and limitations

No Critical, High, Medium, or Low finding or assurance gap remains open for the
reviewed read-only composition. The fixed-Home, interactive model setup, and
cache-maintenance changes add no Kubernetes authority. SR-003 and SR-004 are
closed by deterministic synthetic-canary matrices and return to review when a
covered source, sink, or error aggregation boundary changes.

This review uses synthetic canaries, local fakes, request-recording HTTP
fixtures, temporary SQLite databases, static import/composition checks, and
deterministic fuzz seeds. It does not use a real cluster, real model endpoint,
real credential, public network, or third-party penetration test. It does not
claim encryption for SQLite or a locally saved plaintext key, tamper resistance,
forensic deletion, live RBAC correctness, or live-cluster security properties
for the isolated `v0.2` write workflow. Dependency vulnerability scanning is a
separate repository gate rather than evidence for these behavioral controls.

No raw canary value is included in this document. Tests construct their values
locally so that public documentation and fixtures cannot become a source of a
credential-shaped example.
