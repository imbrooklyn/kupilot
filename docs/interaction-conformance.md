# Interaction Conformance

This contract separates authority checks, runtime derivation, unambiguous
representation normalization, and presentation. It is governed by
[ADR-0057](adr/0057-derive-response-metadata-and-classify-interaction-failures.md).
Test observations and limits must be reported separately from this target.

## Field audit

| Field | Model responsibility | Runtime responsibility | Rejection |
| --- | --- | --- | --- |
| Final schema and outcome | Exact version 4; answer or typed clarification intent | Dispatch one strict decoder | Old/unknown/missing/null version or outcome |
| Answer Markdown | Candidate bounded safe prose | Normalize safely; render typed clarification instead | Unsafe content, invalid string, excessive size |
| Claims | Text, explicit kind, exact Evidence references | Sequence, normalized hash, structural support, run/scope/policy binding | Unknown kind, duplicate claim, missing support for current observation |
| Evidence IDs | Choose known references without duplication | Validate ownership first, then sort by acceptance order | Missing, unknown, duplicate, foreign, stale IDs |
| Source coverage | No wire field | Derive from accepted Tool outcomes and source gaps | Model-supplied source metadata |
| Clarification | One to three kind/prompt/label-only choice structures | Ordinals, choice IDs, rendering, needs-user-input stop | Missing/null arrays, invalid choices, mixed answer/action/Evidence |
| Plan | Wire version 2, title, description-only steps, limitations, claims | Step ordinals, durable Plan version 1, rendering | Unknown/action fields, invalid line/count/byte bounds |
| Tool call | Fixed Tool and exact typed intent parameters | Invocation ID, canonical binding, scope, budgets, pairing | Malformed/duplicate/unknown call, unauthorized target or capability |
| Retained assistant history | No model reconstruction work | Current strict envelope, empty authority arrays, complete ordered coverage | Missing eligible history, corrupt coverage, oversized representation |
| Stream assembly | Provider protocol events | Pinned Eino concatenation, bounded validation, native call IDs | Malformed/duplicate/out-of-order events, invalid finish/usage |
| Stop reason | No wire field | Derive from lifecycle, source coverage, limitations, clarification | Model-supplied stop field or unsupported provider finish |
| Proposed action | Exact operation, target, parameters, explanatory reason/risk/prerequisites | Risk class, envelope, authority and one-attempt lifecycle | Unknown/missing/null required data, unadmitted target/operation |

JSON member order, whitespace, and equivalent string escaping carry no
authority. Arrays preserve intent order except the validated Evidence set.
Null is not an empty array. The existing exact nullable action parameters are
the only action null alternative. No normalization increases a ceiling or
guesses intent. Every count and byte boundary needs exact-limit and one-over
tests; object keys need missing/null/duplicate/unknown cases independently.

## Failure and recovery rules

The closed reason catalog is in `internal/domain/interaction_failure.go`.
Question-start failures additionally retain the existing exact
`QuestionStartFailureReason` and recovery pair. Model transport errors retain
their existing fixed `ModelErrorCode`. These are complementary typed boundaries.

Every terminal rejection is fail-closed: no new model, Tool, Kubernetes, Reviewer, or
executor call follows it automatically. Calls already performed are recorded
by the runtime budget and scenario assertions; a final binding rejection can
therefore follow successful reads without making their payload authoritative.
Preflight/start-barrier denials make zero runtime calls and commit no input.
After a committed start, failure preserves the committed input and safe prior
history, commits no successful assistant answer, and holds queued input for
explicit recovery. A commit failure marks storage degraded and does not mint
an assistant Message or automatically drain the queue.

Safe diagnostics contain the fixed stage and reason; existing lifecycle and
transport messages remain fixed code-owned sentences. Recovery uses the
existing typed terminal next actions: edit/re-submit explicitly, review
scope/policy/budgets, or run local doctor. No failure reason grants authority.
Successful clarification also denies queue drain. Clean durable completion
alone may start one queued successor through Application.

## Failure-stage and decision matrix

`M/T/K` below means actual model/Tool/Kubernetes calls. `prefix` means only calls
already admitted before the failing boundary: the rejecting branch adds zero
calls. Synthetic composition has `K=0` throughout. Counts are measurements, not
permission. A Tool result marked unavailable or partial may be supplied to the
next ordinary Eino decision; a terminal rejection never schedules that decision.
This existing result feedback is distinct from transport retry or final repair.

All rejection rows deny new authority, deny queue drain, and prohibit local
repair unless the separate field-audit normalization rule explicitly applies.
`input` means the committed safe question and any previously committed steers
remain, while no successful assistant Message is added. `none` means the start
barrier was not crossed. `degraded` preserves the last committed state and
requires explicit storage recovery. Prior history is never rewritten by a
failure. Tool/Evidence metadata already accepted cannot authorize an answer.

| Boundary | Exact diagnostic reasons | Decision and safe next interaction | Calls | Persistence |
| --- | --- | --- | --- | --- |
| Submit / start | Existing `QuestionStartFailureReason` below | Reject before start; recover the editor using the typed recovery action | `0/0/0` | none |
| Request preflight | `request_preflight_rejected` | Reject input/consent/reservation mismatch before the next content request; review current consent or policy | prefix | input |
| Retained context | `retained_context_rejected`, `summary_response_rejected` | Reject incomplete coverage, invalid history translation or unsafe/invalid summary; no current-question-only fallback | prefix | input; last summary unchanged |
| Model invocation | `provider_transport_failed`, `provider_protocol_unsupported`, `provider_reported_failure` | Fixed transport class/code, unsupported profile, or native error record; inspect local doctor/configuration before an explicit question | prefix, at most one attempt per invocation | input |
| Stream assembly | `stream_malformed`, `stream_event_unsupported` | Reject invalid JSON/UTF-8, unsupported message shape, or an untyped provider failure; no reconstruction or reattachment | prefix | input |
| Stream ordering / stop | `stream_finish_duplicate`, `stream_event_after_finish`, `stream_finish_missing`, `stream_usage_invalid`, `provider_stop_reason_invalid` | Reject duplicate/out-of-order/incomplete finish, inconsistent usage or unknown finish; exact finish and optional one usage record are allowed | prefix | input |
| Tool selection | `tool_call_malformed`, `tool_policy_denied`, `tool_pairing_rejected` | Reject malformed closed arguments, hard policy, repeated/cross-bound request IDs or changed bound arguments; no handler call for rejected batch | prefix; rejected Tool adds `0/0` | input |
| Tool execution | `tool_result_rejected`, `tool_cancelled`, `tool_timed_out` | Reject identity/schema/output mismatch or lifecycle failure; no acceptance of late Evidence and no follow-up model request | prefix, one bound handler attempt | input |
| Evidence acceptance | `evidence_acceptance_rejected`, `evidence_registry_changed` | Reject ownership, scope, policy, duplicate ID or stale/sealed registry revision; never infer an absent observation | prefix | input |
| Final decode | `final_json_malformed`, `final_field_duplicate`, `final_field_unknown`, `final_field_missing`, `final_field_null`, `final_schema_unsupported`, `final_shape_invalid`, `final_limit_exceeded` | Reject the exact malformed field class; edit the question or inspect compatibility; no repair call | prefix | input |
| Typed final alternative | `clarification_invalid`, `plan_invalid` | Reject mixed/invalid alternatives and invalid plan/question structure; safe candidate formatting is locally rendered only after validation | prefix | input |
| Claim/Evidence binding | `claim_kind_invalid`, `claim_duplicate`, `current_observation_unsupported`, `evidence_reference_unknown`, `evidence_reference_duplicate`, `evidence_ownership_rejected`, `claim_binding_rejected` | Reject unsupported intent, duplicate normalized claims, absent/unknown references, ownership or completeness mismatch; accepted reads do not bypass this gate | prefix | input |
| Run guards | `sensitive_output_blocked`, `runtime_budget_exhausted`, `run_cancelled`, `run_timed_out`, `generation_stale` | Block sensitive content; stop at finite budget/cancel/deadline/generation boundary; explicit new intent required | prefix | input, or bounded local budget-stop answer where already admitted |
| Durable commit | `persistence_commit_failed` | Zero executor authority, no successful assistant commit, no successor; inspect local storage health | prefix | degraded |
| Application event acceptance | `application_event_rejected` | Reject wrong identity, sequence, pairing or terminal state; no second terminal event | prefix | input |
| Internal invariant | `internal_invariant_failed` | Reject invalid local IDs, clocks, generated state or an adapter returning without an accepted terminal; inspect doctor | prefix | input |

The native `provider_reported_failure` observer recognizes only the typed
non-empty NDJSON `error` field. It does not inspect the error wording. Unknown
errors remain fail-closed and are not retrospectively attributed to that field
without evidence. The original fixed `ModelErrorCode` and `SafeErrorClass`
continue to distinguish transport authentication, rate limit, timeout, redirect,
media, budget and protocol results inside the adapter.

### Start rejection detail

| Existing exact start reasons | Typed recovery | Calls / persistence / queue |
| --- | --- | --- |
| `session_unavailable` | Start or explicitly resume a Session | zero / none / no drain |
| `scope_not_verified`, `scope_generation_stale` | Select or review current scope | zero / none / no drain |
| `selected_resource_stale` | Select the resource again | zero / none / no drain |
| `policy_generation_stale`, `policy_snapshot_invalid` | Review policy | zero / none / no drain |
| `run_active` | Steer or queue against the exact active run | zero new run / none / no drain |
| `run_starting`, `application_operation_active` | Wait for the current local transition | zero new run / none / no drain |
| `persistence_degraded`, `precommit_persistence_failed` | Run local doctor | zero / none / no drain |
| `model_configuration_missing` | Configure the named Agent profile | zero / none / no drain |
| `consent_required` | Review current role/origin/category consent | zero / none / no drain |
| `input_rejected` | Edit recovered input | zero / none / no drain |
| `unknown_safe_failure` | Run local doctor; no raw error interpretation | zero / none / no drain |

Scope/policy invalidation may reject a data event before it can enter the
ordered Application stream. The local rejection reason is retained for a
single failure-only terminal projection. This never accepts the rejected event,
its Evidence, or a successful adapter outcome. Failure/cancel/timeout lifecycle
events carry no result authority and may close an invalidated run.

### Full-composition scenario assertions

`interaction_conformance_test.go` uses the production Eino Agent, loopback HTTP
streaming, code-owned Tool binding, synthetic Namespace reads, the Application
coordinator, real temporary SQLite and TUI event rendering. Each row asserts the
exact terminal reason and diagnostic, call counts, same-run/generation Evidence,
committed rows and one terminal projection. Safe history is checked for duplicate
IDs and absence of wire envelopes. No Kubernetes port is supplied to the Tools.

| Scenario | Terminal reason / diagnostic | M/T/K | Committed Messages |
| --- | --- | --- | --- |
| Greeting | completed | 1/0/0 | 2 |
| One safe read; List to final | completed | 2/1/0 | 2 |
| List to Inspect to final | completed | 3/2/0 | 2 |
| Empty result | completed without fabricated Evidence | 2/1/0 | 2 |
| Partial; truncated | partial_result | 2/1/0 | 2 |
| Unavailable source | source_unavailable | 2/1/0 | 2 |
| Verified current observation | completed with exact citation | 2/1/0 | 2 |
| Two Evidence / two claims | completed | 3/2/0 | 2 |
| Greeting followed by the reported Namespace List/Inspect path | completed; retained history included once | 4/2/0 total | 4 |
| Explicit resume then next Tool question | completed; resume itself has zero external calls | 3/1/0 total | 4 |
| Typed clarification | needs_user_input | 1/0/0 | 2 |
| Plan-only | completed; inert durable Plan schema 1 | 1/0/0 | 2 |
| Cancel; fake-clock timeout | cancelled/run_cancelled; timed_out/run_timed_out | 1/0/0 | 1 |
| Provider transport failure | source_unavailable/provider_transport_failed | 1/0/0 | 1 |
| Tool timeout; malformed result | timed_out/tool_timed_out; failed/tool_result_rejected | 1/1/0 | 1 |
| Stale scope; stale policy | stale_generation/generation_stale | 1/0/0 | 1 |
| Precommit persistence failure | precommit_persistence_failed; no run | 0/0/0 | 0 |
| Final commit failure | persistence_degraded/persistence_commit_failed | 1/0/0 | 1 |
| Two steers across model boundaries, then queued successor | completed; one successor after commit | 4/2/0 | 6 |
| Same steers with rejected final | failed/evidence_reference_unknown; queued input recovered | 3/2/0 | 3 |
| Malformed, duplicate-finish, after-finish provider events | failed with three distinct stream reasons | 1/0/0 | 1 |
| Invalid Evidence reference | failed/evidence_reference_unknown | 2/1/0 | 1 |
| Presentation-only member order | completed | 1/0/0 | 2 |

The explicit-resume composition scenario exercises the accepted resume command
in the same process. Existing `cmd/kupilot/resume_integration_test.go` and the
Session-context contract tests cover the separate process/storage bridge. These
are deterministic fixture claims, not real Kubernetes or model-quality claims.

### Auditable decision coverage

| Changed decisions | Paired test evidence |
| --- | --- |
| Strict final fields present/missing/null/duplicate/unknown, old schema, malformed and one-over | `TestResponseConformanceRequiredFields`, `TestResponseConformanceMalformedAndRetiredFields`, `TestResponseConformanceNestedFields` |
| Claim kind/support derivation; removed wire metadata; 100/101 claims; whitespace/escapes/order | `TestResponseConformanceClaimIntentAndDerivation`, `TestResponseConformanceExactLimitsAndRepresentation` |
| Exact text/Markdown ceiling and one-over; empty/whitespace/different safe clarification candidate | `TestResponseTextLimitsAndClarificationPresentation` |
| Known Evidence order canonicalization vs unknown/duplicate/foreign/stale/duplicate-claim rejection | `diagnosis_test.go` coverage-validator table; `TestInteractionCompositionScenarioMatrix` |
| Direct diagnosis metadata, plan/clarification alternatives, redaction growth, missing internal citation fields | `TestDiagnosisFailureMatrixKeepsDistinctBoundaryReasons`, `TestPlanWireDerivesOrdinalsAndRejectsStructuralAlternatives`, `TestFinalAlternativeAndInternalGrammarFailuresRemainTyped` |
| Empty Tool acceptance changes revision; old snapshot and second seal rejected | `TestEmptyAcceptedSourceInvalidatesEvidenceSnapshot` |
| Partial/truncated detail state agrees with SQLite | `TestInteractionCompositionScenarioMatrix` partial and truncated rows |
| SSE finish, usage, stop, choice count/index, malformed JSON, exact usage ceiling/one-over, EOF/newline | `TestProviderOrderDecisionMatrix`, `TestBoundedSSEOrderObserverPreservesBytesAndEOFRejection` |
| Native provider error vs absent/empty/null/non-string/malformed field; EOF/newline; no content retained | `TestNativeProviderErrorIsTypedBeforePinnedClientLosesItsShape` |
| Tool selection schema and pairing denial before handler | `TestAdapterRejectsHostileToolSelectionsBeforeHandler`, `runtime_policy_test.go`, `model_contract_test.go` |
| Retained message roles, metadata, exact current input; assembled finish and summary normalization | `TestRetainedConversationFailuresNeverReachModelOrTool`, `TestAssembledMessageAndSummaryFailuresHaveExactReasons` |
| Tool feedback identity, duplicate binding, invalid completion, sealed registry | `TestBoundToolResultAndFeedbackCannotChangeIdentity`, `TestToolBindingMalformedAndDuplicateBatchesHaveNoHandlerCalls`, `TestToolResultCannotEnterASealedEvidenceRegistry` |
| Consent return after revocation, storage failure, scope change, or a concurrent accepted event | `TestInteractionConsentReturnRechecksIdentityScopeAndAuthorization` |
| Forced terminal invalid state, interrupted terminal, no ghost rows | `TestInteractionForcedTerminalRejectsInvalidStateWithoutGhostRows` |
| Application preflight allow/deny and zero calls; persistence vs stale rejection | `TestCoordinatorRunPreflightRejectsUnboundInputAndBudgetBeforeModelCall`, `question_start_test.go`, `session_context_test.go` |
| Failure-only terminal after generation rejection; cancel/timeout/transport; queue recovery | `TestInteractionCompositionCancellationTimeoutAndStaleness` |
| Clean commit permits one successor; rejected final permits none; two steers remain exactly once | `TestInteractionCompositionSteersAcrossBoundariesAndQueueDrain` |
| Safe diagnostic catalog vs unknown hostile strings; cause wrapping without rendering | `TestInteractionFailuresAreClosedContentFreeBoundaryValues`, `TestInteractionFailurePreservesCauseWithoutRenderingIt` |
| TUI/doctor fixed diagnostic accepted, unknown diagnostic rejected/hidden | `TestInteractionBoundaryPresentationCannotSelectAuthorityOrExposeUnknownText`, `TestDoctorCompatibilityProjectionIsPinnedTypedAndTamperEvident` |

Go does not report branch coverage. This matrix records explicit tested
decisions and must not be presented as a measured branch percentage. Defensive
JSON-token paths behind complete JSON validation are exercised with direct
internal probes, explicitly distinguished from reachable wire inputs. The
string-only JSON encoder failure and clarification renderer failure after
sanitization/validation remain structural invariants; no provider fixture can
reach them without violating those preceding checks. Whole-package
statement coverage and remaining uncovered changed statements are reported
separately below.

### Measured statement coverage

The same four-package `go test -count=1 -coverprofile` command was run before
implementation at `d722730f4d78acb391c6a667ca8995403077b4d5` and after the
changes, using Go 1.25.13. Profiles were temporary artifacts outside the
repository.

| Package | Before | After |
| --- | --- | --- |
| `internal/agent` | 74.0% | 78.4% |
| `internal/agent/einoadapter` | 78.6% | 81.6% |
| `internal/application` | 70.8% | 71.9% |
| `internal/tui` | 76.4% | 76.4% |

An additional `-coverpkg` run instruments calls across all four packages so
Application composition tests count toward adapter/Agent statement execution.
A diff-to-profile audit checks every instrumented block intersecting a changed
production line: 396 of 397 such blocks were observed. This block audit is not
a branch percentage. The remaining
unreachable changed return is the clarification-render failure after the same
request has already passed sanitization and validation: the fixed question and
choice ceilings are below the fixed Markdown ceiling. The error remains as a
defensive invariant; it is not removed for coverage or described as a tested
external branch.

## Evidence levels

### Native request temperature decisions

ADR-0058 corrects request fidelity independently of model capability. The
following deterministic tests use no model or Kubernetes endpoint:

| Decision outcomes | Tests |
| --- | --- |
| Explicit zero, default 0.1, upper bound 0.2; streaming, summary, review | `TestNativeOllamaTemperatureFieldIsAlwaysExplicit` |
| Empty/nonempty options; present zero/nonzero; whitespace, escaped keys, nested unrelated fields; exact byte preservation | `TestNativeOllamaRequestPreservesAllOtherBytes` |
| Missing/null/array/duplicate options; missing nonzero/null/string/duplicate/mismatched temperature and nonzero underflow; malformed key/value/end and trailing input; zero external calls | `TestNativeOllamaRequestDenialsMakeZeroExternalCalls` |
| Corrected request exactly at the byte ceiling vs one-over with zero calls | `TestNativeOllamaCorrectedRequestByteCeiling` |
| Read failure with wrapped cause; cancellation before/during read; timeout; declared-length mismatch; actual one-over; body closure and zero calls | `TestNativeOllamaRequestReadAndLifecycleDenials` |
| Full Agent completion with one request carrying explicit zero | `TestNativeOllamaRunsFullAgentComposition` |

These checks establish the wire setting, not a model's ability to produce valid
Tool arguments. Provider failures remain failures without automatic retries.

For this correction, Go 1.25.13 statement coverage was measured before changes
at `d11c634c6635440c5784b2fca898b074beac2e1c` and after the correction using
temporary profiles. `internal/agent/einoadapter` increased from 81.6% to 82.2%;
both functions in `ollama_request.go` reached 100% statement coverage. The
other measured packages stayed at 78.4% (`agent`), 71.9% (`application`), and
76.8% (`tui`). This is statement coverage; the decision table above is a
separate manually auditable record, not a measured branch percentage.

### Native bound Tool schema decisions

ADR-0059 extends the existing native request guard, without changing model
output validation. These tests are deterministic and perform no real model,
Tool, or Kubernetes I/O:

| Decision outcomes | Tests |
| --- | --- |
| Full and plan-only bindings; complete and lossy input schemas; exact replacement and unrelated-byte preservation; body closure, ContentLength, GetBody | `TestNativeCatalogPreservesOtherBytesAndHTTPFraming` |
| Missing/null/object/empty/duplicate Tool arrays; missing/one-over/reordered/unbound catalogs; unknown names, changed descriptions/types, unknown Tool/function fields; duplicate type/function/name/description/parameters; missing/null/array/string/malformed parameters; exact error and zero network calls | `TestNativeCatalogEnvelopeDenialsHaveZeroExternalCalls` |
| Final request exactly at its byte ceiling vs one-over; exact framing and zero-call denial | `TestNativeCatalogExactLimitAndOneOver` |
| Full/plan-only binding and rebinding, Stream delegation, invalid rebinding | `TestNativeCatalogBoundViewsDelegateAndFreezeCatalog` |
| Wrapped SDK binding cause; Generate delegation; later caller mutation cannot change the frozen catalog | `TestNativeCatalogBindingPreservesCauseAndGenerateDelegates` |
| Tool-free absent/empty catalog vs malformed JSON | `TestNativeCatalogToolFreeAndMalformedHelpers` |
| Cancellation after restoration and before acceptance; no corrected request escapes | `TestNativeCatalogCancellationBeforeCorrectedBodyAcceptance` |
| Cancellation before/during read, timeout, read failure, length mismatch and actual one-over; closed body and zero calls | `TestNativeOllamaRequestReadAndLifecycleDenials` |
| Full Agent greeting followed by retained Namespace question; exact settings, current-question uniqueness, all 14 schema comparisons; safe provider rejection, two fixture model calls, zero Tool/Kubernetes calls | `TestNativeOllamaRequestFidelityAudit`, `TestNativeOllamaRequestAuditComparator` |
| Three test-only request variants preserve history, authority instructions and intended catalog; valid strict Tool binding, exactly one fixture call each, fixed parser-error observation | Tagged `TestNativeOllamaSelectionProbeFixture` (also race checked) |

The request boundary keeps existing typed reasons: an unsupported native
request envelope uses `model_invocation/provider_protocol_unsupported` (the
existing transport-validation class, before actual I/O); corrected-byte excess uses
`runtime_budget_exhausted`, and lifecycle failures use `run_cancelled` or
`run_timed_out`. No request denial adds model/Tool/Kubernetes calls. Successful
restoration grants no authority and schedules no additional call. Failure
persistence and queue rules are unchanged from the matrix above.

Fresh Go 1.25.13 statement coverage for this schema correction increased
`internal/agent/einoadapter` from **82.2% to 82.8%**. Together with ADR-0058 the
change from the clean base is **81.6% to 82.8%**. Both new production files,
`ollama_request.go` and `ollama_catalog.go`, have **100% statement coverage**,
including every explicit error-return statement. `agent` remains **78.4%**,
`application` **71.9%**, and `tui` **76.8%**. Profiles are temporary artifacts.
Go does not measure branch coverage: the table is the manual decision record,
not a branch percentage or a claim that every legacy branch is covered.

The bounded local comparison is separate evidence: full requests passed 0/3,
single-Tool requests 3/3, and requests without final protocol text 2/3. It
executed no Tool or Kubernetes call and is not an end-to-end Agent success.
Exact versions, failures, usage and timings are in
[Model Compatibility](model-compatibility.md#bounded-native-first-call-comparison).

### Interpretation

Go `-coverprofile` is statement coverage, not native branch coverage. A decision
matrix must name tests for both outcomes of changed decisions and explicit
error returns. Deterministic full-composition tests use recording transports,
synthetic Tools/Evidence, and temporary SQLite; they do not prove real-cluster
integration or semantic answer quality. Opt-in bounded local Ollama conformance
records only version/model/scenario, call and byte/usage counts, wall time, and
typed structural outcomes. Release evidence and real-model quality evaluation
are separate and cannot be inferred from either fixture class.
