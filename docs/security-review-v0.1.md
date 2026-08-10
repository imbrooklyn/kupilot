# KuPilot v0.1 Security Review

- Review disposition: meets the `v0.1` security-review risk threshold with two
  open Low assurance gaps
- Baseline reviewed: 2026-08-10
- Authority: the [Security Threat Model](security.md),
  [Architecture](architecture.md), [Privacy Overview](privacy-overview.md), and
  [Data Retention Contract](data-retention.md)

This review maps every threat in the accepted threat model to implementation
controls and deterministic evidence. It covers the reachable `v0.1`
composition. The admitted but unreachable `v0.2` write threats are included so
that write absence is explicit rather than inferred.

## Conclusion

The review found no credential disclosure, cross-origin authorization leak,
cross-scope acceptance, terminal-control execution, or reachable Kubernetes
write path in `v0.1`. Both Medium findings are closed: model-originated free
text is processed before actions and sinks, and Agent plus SQLite use the same
all-accepted-Evidence observation-window invariant.

Two Low assurance gaps remain open for the exhaustive credential-source sink
matrix and joined/formatted safe-error coverage. They do not establish a known
bypass and do not block this risk disposition, but they remain explicit
residual risk and are not counted as passing threats.

<!-- markdownlint-disable MD013 -->

| Severity | Open review items | Closed review items |
| --- | ---: | ---: |
| Critical | 0 | 0 |
| High | 0 | 0 |
| Medium | 0 | 2 |
| Low | 2 | 0 |

<!-- markdownlint-enable MD013 -->

Status meanings:

- **Pass**: reachable behavior has both implementation and deterministic test
  evidence for the stated threat.
- **Finding**: a deterministic test reproduces behavior that contradicts or
  weakens the required control.
- **Gap**: no bypass was observed, but the required proof is incomplete.
- **Not reachable**: the threat belongs to the admitted `v0.2` write boundary,
  and static composition evidence shows that boundary is absent from `v0.1`.

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
- SQLite uses the two checksummed forward migrations in
  [`internal/persistence/sqlite/migrations`](../internal/persistence/sqlite/migrations),
  explicit column mappings, fixed SQL, bound values, and the allowlisted schema
  checks in
  [`internal/persistence/sqlite/migrate_test.go`](../internal/persistence/sqlite/migrate_test.go)
  and
  [`internal/persistence/sqlite/session_repository_static_test.go`](../internal/persistence/sqlite/session_repository_static_test.go).

## Threat-to-control-to-test-to-status matrix

<!-- markdownlint-disable MD013 -->

| Threat | Control and code evidence | Deterministic test evidence | Status |
| --- | --- | --- | --- |
| T01: Kubernetes credentials reach a prohibited sink, or unsafe kubeconfig permissions are missed | C01, C02, C08, and C10; credential loading and client construction remain in [`internal/kube`](../internal/kube), while SQLite fields are allowlisted | `TestConfigLoaderReportsUnsafePermissionsWithoutChangingOrExposingPath`, `TestExecCredentialOutputFailureIsBoundedAndSafe`, `TestExecCredentialLaunchFailuresAreSafe`, and the safe-source path in `TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis` | **Gap — SR-003.** Existing tests cover important sources and sinks, but not every credential source across every success/error sink required by T01. |
| T02: the model API key is persisted, rendered, inherited, disclosed, or redirected | C01-C03 and C10; one-shot opaque secret source, child-environment filtering, and same-origin transport policy in [`internal/config`](../internal/config) and [`internal/llm/openaicompat`](../internal/llm/openaicompat) | `TestModelAPIKeyCanaryIsAbsentFromEveryStartupSink`, `TestEnvironmentSecretSourceReadsOnceAndUnsets`, `TestFilterChildEnvironmentRemovesEveryModelAPIKeyEntry`, `TestCredentialCanaryIsBlockedFromRequestAndResponseValues`, `TestCrossOriginRedirectIsDeniedBeforeAuthorizationCanMove`, and `TestExecCredentialsAllowDirectLaunchAndRemoveModelKey` | **Pass.** The synthetic key is confined to the configured-origin authorization header, and denial paths observe zero forbidden forwarding. |
| T03: Secret or other high-risk text is read or smuggled through eligible content | C04/C07 model-text processing plus C06 source denial and the ordered egress pipeline in [`internal/agent`](../internal/agent), [`internal/tools`](../internal/tools), and [`internal/security/redactor.go`](../internal/security/redactor.go) | `TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler`, `TestDiagnosisValidatorSanitizesEveryModelFreeTextField`, `TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence`, `TestAdapterBlocksHighRiskModelTextBeforeDownstreamAction`, Kubernetes source-denial tests, `FuzzRedactorSafety`, and the full sink canary integration test | **Pass — SR-001 closed.** Lower-risk eligible text is replaced before downstream use; high-confidence model text is blocked before Tool/Kubernetes action, and denial tests observe zero forbidden calls or sinks. |
| T04: prompt or Tool-result injection changes runtime authority | C04, C05, C07, and C11; authority comes from immutable run state and fixed dispatch in [`internal/agent`](../internal/agent) | `TestSystemPromptDoesNotEmbedQuestionOrToolLanguageInjection`, `TestAdapterRejectsHostileToolSelectionsBeforeHandler`, `TestMaliciousToolOutputCannotAuthorizeAnotherTool`, scope-generation tests, and zero-action Slash denials | **Pass.** Hostile text does not alter catalog, endpoint, scope, budgets, approval state, or write authority. |
| T05: structured Tool input adds scope, Kind, selector, deadline, or larger limits | C04 strict decoding, runtime scope injection, and C09 ceilings in [`internal/agent/catalog.go`](../internal/agent/catalog.go) | `TestToolCatalogIsExactStrictAndScopeFree`, `TestToolCallPolicyRejectsUnknownForbiddenAndInvalidBeforeHandler`, `TestToolCallBindingInjectsScopeCeilingsAndCanonicalDefaults`, and `FuzzBindToolCallStrictSchema` | **Pass.** Invalid authority is rejected before handler I/O; accepted canonical calls retain only runtime-injected scope and ceilings. |
| T06: endpoint confusion, redirects, or error bodies exfiltrate credentials or content | C03 canonical origin, normal TLS, loopback-only HTTP, redirect denial, consent binding, and safe errors | `TestValidateModelEndpointPolicy`, `TestRedirectPolicyAllowsOnlyCanonicalOrigin`, `TestPrivateHTTPSUsesCertificateAndHostnameVerification`, `TestCrossOriginRedirectIsDeniedBeforeAuthorizationCanMove`, `TestHTTPAndTransportErrorCanariesAreBoundedAndDiscarded`, and `TestPrivacyConsentLifecycleBindsOriginCategoriesAndPolicy` | **Pass.** Invalid origins fail locally, authorization does not cross origin, and changed consent tuples prevent content transfer. |
| T07: stale Context or Namespace work reaches a sink | C05 three-gate generation checks in [`internal/application/scope_manager.go`](../internal/application/scope_manager.go), Agent runtime, and Application event acceptance | `TestScopeManagerSwitchContextInvalidatesBeforeCreatingTarget`, `TestScopeManagerDropsBlockedOldGenerationResourceResult`, `TestCoordinatorCancelsOneRunAndRejectsLateEvents`, `TestGetEventsDiscardsLateResultAfterScopeBecomesStale`, `TestGetPodLogsDiscardsRawContentAfterScopeBecomesStale`, and TUI stale-result tests | **Pass.** Before-call stale work performs zero action; in-flight and late work is rejected before Evidence, model, persistence, or current TUI state. |
| T08: broad RBAC permits access outside fixed Kind, relationship, Namespace, or projection policy | C06 task-specific read ports and projections in [`internal/kube`](../internal/kube) | Exact-action tests for resources, Events, logs, and related resources in `tool_resources_test.go`, `tool_events_logs_test.go`, and `tool_related_resources_test.go`; denial tests assert zero forbidden client actions | **Pass.** Recorded requests use only admitted verbs, resources, Namespace, subresources, and limits; relationship traversal stays within fixed nodes, edges, and hops. |
| T09: prose, malformed streams, duplicate calls, or invented Tools bypass structured calling | C04 structured events without text fallback and C09 bounded decoding | `TestCompatibilityFixturesProduceNeutralStreamEvents`, `TestMalformedAndOversizeFixturesHaveOneClassifiedTerminalError`, `TestNeutralModelContractIsMinimalAndRejectsAmbiguity`, hostile Tool selection tests, duplicate Diagnosis tests, and `FuzzBoundedSSEBodyIsChunkIndependent` | **Pass.** Malformed or invented authority produces one classified terminal outcome and no unintended handler call. |
| T10: SQLite leaks excluded data, accepts SQL authority, opens unsafe paths, or silently loses integrity | C08 fixed safe paths, permissions, allowlisted schema, bound SQL, checksummed migrations, integrity gates, model-text processing, and the all-accepted-Evidence Diagnosis window invariant in [`internal/persistence/sqlite`](../internal/persistence/sqlite) | Database/WAL canary scans, path and mode tests, `TestRepositorySourcesKeepExplicitSQLBoundary`, migration corruption and unknown-schema tests, `TestDiagnosisRepositoryUsesAllAcceptedEvidenceForObservationWindow`, truncation with and without Evidence, cross-run rejection, retention-state tests, and `TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis` | **Pass — SR-001 and SR-002 closed.** Sensitive model values do not reach SQLite, every cited Evidence ID remains same-run validated, and zero, one, subset, and truncated reference sets preserve the exact all-accepted-Evidence window without false degradation. |
| T11: retention exceeds policy, minimal mode resumes, deletion is partial, or cleanup failure is hidden | C08 plus explicit retention repositories and startup/run gates | cutoff, minimal-shell, cascade, rollback, cancellation, startup maintenance, resume eligibility, and durable-run-start zero-model/Tool-call tests in [`internal/persistence/sqlite`](../internal/persistence/sqlite), [`internal/application`](../internal/application), and [`cmd/kupilot`](../cmd/kupilot) | **Pass.** Cutoffs and cascades are transactional; mandatory failures are visible and fail before prohibited work. |
| T12: external text executes terminal controls, spoofs typed state, or hides scope | C07 local styling, sanitization, bounded render state, typed scope/approval meaning, and fixed Slash registry in [`internal/tui`](../internal/tui) | `TestSanitizeExternalTextRemovesTerminalAndBidiControls`, `TestApplicationTextAndScopeAreSanitizedBeforeRenderState`, golden/render tests, paste and stale-event tests, `TestSlashRegistryIsFixedAndReadOnly`, and `FuzzParseSlashDraftHasNoDynamicAuthority` | **Pass.** Escape, control, bidi, invalid UTF-8, and dynamic Slash inputs do not become terminal or runtime authority. |
| T13: streams, results, logs, recursion, retries, or queues exceed budgets | C09 atomic ceilings, child deadlines, owned cancellation, local projection bounds, and bounded event/render state | `TestRunBudgetEnforcesExactHardLimits`, one-over and concurrent reservation tests, exact Tool result/log/graph limits, event queue and TUI cumulative stream tests, oversize model fixtures, and `FuzzBoundedSSEBodyIsChunkIndependent` | **Pass.** Boundary and one-over cases stop with bounded output, no post-exhaustion call, and one terminal result. |
| T14: raw vendor errors or logs disclose credentials, bodies, SQL, paths, or cluster data | C01 and C10 safe classifications at adapter boundaries | Kubernetes wrapped-error tests, model HTTP/transport error canaries, SQLite safe repository errors, config value-hiding tests, and the text-free lifecycle observer in the sink integration test | **Gap — SR-004.** Per-adapter coverage exists, but the required exhaustive joined/formatted-error matrix and complete sink enumeration are absent. |
| T15: a hidden Kubernetes write path exists in `v0.1` | C11 absent mutation composition, fixed read-only contracts, and runtime rejection of claimed execution | `TestReadOnlyToolPathContainsNoWriteShellOrGenericKubernetesEscape`, `TestCompositionConstructsOneModelLifecycleAndNoWritePath`, exact Kubernetes request recorders, fixed Slash tests, exact catalog tests, and `TestDiagnosisValidatorRejectsUnregisteredEvidenceAndExecutionClaims` | **Pass.** Static imports and reachable methods expose only admitted reads; all observed requests are read-only and execution prose cannot become a confirmed fact. |
| T16: a future approval is replayed, broadened, stale, or targets a changed Deployment | C11 excludes the C12 coordinator and executor from `v0.1` | `TestCompositionConstructsOneModelLifecycleAndNoWritePath` and Application/Tool/TUI boundary tests | **Not reachable.** No `v0.2` approval state or executor is constructed in the reviewed composition. This is not evidence for a future `v0.2` implementation. |
| T17: database failure bypasses pre-write audit or causes a duplicate write | C11 excludes all write orchestration from `v0.1`; read-only persistence failures follow the explicit degraded-state policy | composition no-write tests and Coordinator durable-start/degraded-storage tests with model and Kubernetes call counts | **Not reachable.** There is no write or pre-write audit path in `v0.1`; reachable read-only persistence failures remain visible and fail closed. |
| T18: kubeconfig exec is model-influenced, shell-launched, leaking, hanging, key-inheriting, or not strictly denied | C02 selected-config ownership, direct launch, clean environment, bounded output, owner cancellation, and strict deny in [`internal/kube`](../internal/kube) | `TestExecCredentialsStrictDenyPerformsZeroLaunches`, `TestExecCredentialsAllowDirectLaunchAndRemoveModelKey`, protocol denial, safe output/failure, and owner-cancellation tests | **Pass.** Strict mode launches nothing; allowed mode owns exact argv without a shell or model key and bounds failure output. |
| T19: model or TUI events bypass Application and invoke an executor | C11 composition isolation, consumer-owned ports, typed UI commands, and fixed Tool dispatch | Agent, Application, Tool, and TUI boundary tests; forged/unknown Slash and model execution-claim tests; composition no-write test | **Pass.** Neither Agent nor TUI can reference an executor, and forged text/events produce no approval or write action. |
| T20: restart issues a broader or repeated write, or conflates acceptance with verification | C11 excludes the C12 operation from `v0.1` | exact catalog, read-only boundary, composition, and request-recorder tests | **Not reachable.** `restart_deployment` and all Kubernetes write methods are absent from the reviewed composition. This does not pre-approve a future implementation. |

<!-- markdownlint-enable MD013 -->

Threat status totals are 15 Pass, 0 Finding, 2 Gap, and 3 Not reachable.

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

### SR-003: Kubernetes credential-source sink proof is incomplete

- **Severity:** Low
- **Status:** Open assurance gap
- **Affected threats:** T01 and C01/C02/C08/C10
- **Owner:** Kubernetes adapter and security-test maintainers
- **Revisit trigger:** any credential-source or sink change, or a claim that T01
  has exhaustive release-signoff evidence

Existing tests separately cover unsafe kubeconfig permission warnings, model
key child-environment removal, exec credential output failures, safe Kubernetes
errors, SQLite/WAL canary absence, model requests, Application events, rendered
TUI, and text-free lifecycle observations. They do not inject a distinct
synthetic canary at every Kubernetes credential source—kubeconfig bytes, bearer
token, client certificate, private key, exec standard output, and exec standard
error—and enumerate every prohibited sink on both success and failure paths.

Acceptance requires that complete matrix, including formatted safe errors,
ordinary logs, AuditEvents, every eligible SQLite table, database/WAL/SHM
files, and exact external-action counts. The permitted Kubernetes transport use
must be distinguished from every prohibited sink. Permission tests must also
prove warning content contains neither source content nor path and that mode
bits are unchanged.

### SR-004: Joined and formatted safe-error proof is incomplete

- **Severity:** Low
- **Status:** Open assurance gap
- **Affected threat:** T14 and C01/C10
- **Owner:** adapter-boundary and security-test maintainers
- **Revisit trigger:** any safe-error boundary, sink, or shutdown-aggregation
  change, or a claim that T14 has exhaustive release-signoff evidence

Current adapter tests show that representative wrapped Kubernetes, model,
configuration, Tool, and SQLite failures become stable safe classes without
their synthetic values. They do not exhaustively exercise nested `%w`, normal
formatting, and joined failures at every adapter boundary while enumerating the
TUI, model, audit, ordinary-log, and persistent sinks.

Acceptance requires a deterministic table covering configuration, Kubernetes,
model, Tool, SQLite, Application, and shutdown aggregation. Each case must
retain stable `errors.Is`/class behavior, omit distinct synthetic values under
direct formatting, wrapping, and joining, enumerate all reachable sinks, and
assert exact forbidden external-action counts.

## Release decision and limitations

No Critical, High, or Medium finding remains open, so this security review no
longer blocks CI progression for the reviewed `v0.1` composition. SR-003 and
SR-004 remain Low assurance gaps with explicit ownership and revisit triggers.
They do not establish a known bypass, but they cannot be counted as passing
threats or used to claim an exhaustive credential-source or safe-error proof.

This review uses synthetic canaries, local fakes, request-recording HTTP
fixtures, temporary SQLite databases, static import/composition checks, and
deterministic fuzz seeds. It does not use a real cluster, real model endpoint,
real credential, public network, third-party penetration test, or dependency
vulnerability scanner. It does not claim SQLite encryption, tamper resistance,
forensic deletion, live RBAC correctness, or security properties for an
unimplemented `v0.2` write path.

No raw canary value is included in this document. Tests construct their values
locally so that public documentation and fixtures cannot become a source of a
credential-shaped example.
