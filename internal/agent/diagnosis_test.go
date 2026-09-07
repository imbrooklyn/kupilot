package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDiagnosisValidatorRejectsUnregisteredEvidence(t *testing.T) {
	input := testRunInput(t, "Why is this Pod not Ready?")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, time.UnixMilli(1_000).UTC())
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err := registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}
	forgedID := domain.EvidenceID("00000000-0000-7000-8000-000000004099")
	_, err = ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: "The Pod is not Ready. Review its readiness probe before changing the workload.",
		ConfirmedFacts: []domain.ConfirmedFact{
			{Statement: "A fabricated observation claims a restart occurred.", EvidenceIDs: []domain.EvidenceID{forgedID}},
			{Statement: "The projected Pod condition is not Ready.", EvidenceIDs: []domain.EvidenceID{testEvidenceID}},
		},
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID},
			Confidence:            domain.DiagnosisConfidenceLow,
			Falsifier:             "A later bounded observation reports the Pod Ready.",
		}},
		RecommendedActions: []domain.RecommendedAction{{
			Action:   "Review the readiness probe configuration.",
			Risk:     "Configuration changes can restart Pods.",
			Executed: true,
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if !errors.Is(err, ErrInvalidDiagnosisDraft) {
		t.Fatalf("ValidateDiagnosis() error = %v", err)
	}
}

func TestDiagnosisValidatorRejectsInvalidHypothesisReferences(t *testing.T) {
	input := testRunInput(t, "Why is this Pod not Ready?")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, time.UnixMilli(1_000).UTC())
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err := registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}
	forgedID := domain.EvidenceID("00000000-0000-7000-8000-000000004099")
	_, err = ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: "The application may still be starting.",
		ConfirmedFacts: []domain.ConfirmedFact{{
			Statement: "The projected Pod condition is not Ready.", EvidenceIDs: []domain.EvidenceID{testEvidenceID},
		}},
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID, forgedID, testEvidenceID},
			Confidence:            domain.DiagnosisConfidenceHigh,
			Falsifier:             "A later bounded observation reports the Pod Ready.",
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if !errors.Is(err, ErrInvalidDiagnosisDraft) {
		t.Fatalf("ValidateDiagnosis(invalid hypothesis citation) error = %v", err)
	}
}

func TestDiagnosisValidatorSanitizesEveryModelFreeTextField(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, time.UnixMilli(1_000).UTC())
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err := registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}
	canary := strings.Join([]string{"synthetic", "diagnosis", "field", "canary", "4501"}, "-")
	unsafeText := "\x1b[31mObserved token=" + canary + "\x1b[0m\u202e"
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: unsafeText,
		ConfirmedFacts: []domain.ConfirmedFact{{
			Statement: unsafeText, EvidenceIDs: []domain.EvidenceID{testEvidenceID},
		}},
		Hypotheses: []domain.Hypothesis{{
			Statement: unsafeText, SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID},
			Confidence: domain.DiagnosisConfidenceLow, Falsifier: unsafeText,
		}},
		MissingInformation: []domain.MissingInformation{{
			Kind: domain.MissingInformationAbsent, Detail: unsafeText, Impact: unsafeText,
		}},
		RecommendedActions: []domain.RecommendedAction{{
			Action: unsafeText, Risk: unsafeText, Prerequisites: []string{unsafeText}, Executed: false,
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis(sanitized fields) error = %v", err)
	}
	encoded := fmt.Sprintf("%#v", diagnosis)
	if strings.Contains(encoded, canary) || strings.ContainsRune(encoded, '\x1b') || strings.ContainsRune(encoded, '\u202e') ||
		!strings.Contains(encoded, "[REDACTED]") {
		t.Fatalf("sanitized Diagnosis = %#v", diagnosis)
	}
}

func TestDiagnosisValidatorPreservesAnswerMarkdownStructure(t *testing.T) {
	input := testRunInput(t, "Summarize the cluster inventory.")
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	answer := strings.Join([]string{
		"# Cluster overview",
		"",
		"## Nodes",
		"",
		"| Node | State |",
		"| --- | --- |",
		"| worker-0 | Ready |",
		"",
		"## Namespaces",
		"",
		"| Namespace | State |",
		"| --- | --- |",
		"| default | Active |",
		"| system | Active |",
	}, "\r\n")
	want := strings.ReplaceAll(answer, "\r\n", "\n")

	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{AnswerMarkdown: answer}, DiagnosisMetadata{
		ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(),
	}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis(multiline Markdown) error = %v", err)
	}
	if diagnosis.AnswerMarkdown != want {
		t.Fatalf("answer Markdown structure changed\nwant:\n%s\n\ngot:\n%s", want, diagnosis.AnswerMarkdown)
	}
}

func TestDiagnosisValidatorBlocksHighRiskModelTextWithoutSealingEvidence(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	canary := strings.Join([]string{"synthetic", "blocked", "diagnosis", "canary", "4502"}, "-")
	blocked := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\n" +
		canary + "\n" + strings.Join([]string{"-----END", "PRIVATE", "KEY-----"}, " ")
	_, err = ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: blocked,
		RecommendedActions: []domain.RecommendedAction{{
			Action: blocked, Risk: "Review is required.", Executed: false,
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if !errors.Is(err, ErrSensitiveModelTextBlocked) || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), blocked) {
		t.Fatalf("ValidateDiagnosis(blocked text) error = %v", err)
	}
	if _, err := ValidateDiagnosis(DiagnosisDraft{AnswerMarkdown: "No observation was collected."}, DiagnosisMetadata{
		ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(),
	}, registry); err != nil {
		t.Fatalf("blocked draft sealed or changed the Evidence registry: %v", err)
	}
}

func TestEvidenceRegistryRejectsCrossRunAndDuplicateEvidence(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	result := testToolResult(t, call, testEvidenceID, time.UnixMilli(1_000).UTC())
	crossRun := result
	crossRun.Evidence = append([]domain.Evidence(nil), result.Evidence...)
	crossRun.Evidence[0].RunID = "00000000-0000-7000-8000-000000004099"
	if _, err := registry.AcceptToolResult(call, crossRun); err == nil {
		t.Fatal("cross-run AcceptToolResult() error = nil")
	}
	if registry.Len() != 0 {
		t.Fatalf("registry length after rejected result = %d", registry.Len())
	}
	if _, err := registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}
	if _, err := registry.AcceptToolResult(call, result); err == nil {
		t.Fatal("duplicate AcceptToolResult() error = nil")
	}
	if registry.Len() != 1 {
		t.Fatalf("registry length = %d", registry.Len())
	}
}

func TestEvidenceRegistryOwnsObservabilityWindow(t *testing.T) {
	input := testRunInput(t, "Inspect recent Pod events.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, time.UnixMilli(2_000).UTC())
	source := "/api/v1/namespaces/team-a/events"
	observedFrom := time.UnixMilli(1_000).UTC()
	observedThrough := time.UnixMilli(1_500).UTC()
	result.Evidence[0].Category = domain.EvidenceCategoryEvent
	result.Evidence[0].PolicyVersion = domain.ObservabilityPolicyVersion
	result.Evidence[0].PolicyGeneration = call.PolicyGeneration()
	result.Evidence[0].SourcePath = &source
	result.Evidence[0].ObservedFrom = &observedFrom
	result.Evidence[0].ObservedThrough = &observedThrough
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate() error = %v", err)
	}
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err := registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}

	*result.Evidence[0].ObservedFrom = time.UnixMilli(100).UTC()
	*result.Evidence[0].ObservedThrough = time.UnixMilli(200).UTC()
	snapshot, err := registry.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	accepted := snapshot.items[testEvidenceID]
	if accepted.ObservedFrom == nil || accepted.ObservedThrough == nil ||
		!accepted.ObservedFrom.Equal(time.UnixMilli(1_000).UTC()) ||
		!accepted.ObservedThrough.Equal(time.UnixMilli(1_500).UTC()) {
		t.Fatalf("accepted observation window = %v through %v", accepted.ObservedFrom, accepted.ObservedThrough)
	}
}

func TestEvidenceRegistryRejectsUnsafeAndPreactivationEvidence(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")

	unsafeRegistry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry(unsafe) error = %v", err)
	}
	unsafeResult := testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt)
	unsafeResult.Evidence[0].Fact = "Untrusted text" + string(rune(0x202e)) + "must not alter display order."
	if unsafeResult.Validate() == nil {
		t.Fatal("unsafe ToolResult.Validate() error = nil")
	}
	if _, err := unsafeRegistry.AcceptToolResult(call, unsafeResult); err == nil || unsafeRegistry.Len() != 0 {
		t.Fatalf("unsafe AcceptToolResult() error = %v, registry length = %d", err, unsafeRegistry.Len())
	}
	if _, _, err := BuildToolResultContent(unsafeResult); err == nil {
		t.Fatal("unsafe BuildToolResultContent() error = nil")
	}

	preactivationRegistry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry(preactivation) error = %v", err)
	}
	preactivation := testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt.Add(-time.Millisecond))
	if _, err := preactivationRegistry.AcceptToolResult(call, preactivation); err == nil || preactivationRegistry.Len() != 0 {
		t.Fatalf("preactivation AcceptToolResult() error = %v, registry length = %d", err, preactivationRegistry.Len())
	}
}

func TestToolResultEnforcesEvidenceItemCeiling(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt)
	result.Evidence = make([]domain.Evidence, domain.MaxEvidenceItemsPerResult)
	for index := range result.Evidence {
		evidence := testToolResult(t, call, evidenceID(index), input.Scope().ActivatedAt).Evidence[0]
		result.Evidence[index] = evidence
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate(exact Evidence ceiling) error = %v", err)
	}
	over := result
	over.Evidence = append(append([]domain.Evidence(nil), result.Evidence...), testToolResult(t, call, evidenceID(domain.MaxEvidenceItemsPerResult), input.Scope().ActivatedAt).Evidence[0])
	if err := over.Validate(); err == nil {
		t.Fatal("ToolResult.Validate(one-over Evidence ceiling) error = nil")
	}
}

func TestDiagnosisRecordsToolResultLevelTruncationWithoutEvidence(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt)
	result.Status = domain.ToolResultStatusPartial
	result.DataJSON = `{}`
	result.Evidence = nil
	result.Truncation = domain.ToolResultTruncation{
		Truncated:     true,
		Reason:        "item_limit",
		ReturnedCount: 0,
		ReturnedBytes: 2,
	}
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if accepted, err := registry.AcceptToolResult(call, result); err != nil || accepted != 0 {
		t.Fatalf("AcceptToolResult(truncated) = %d, %v", accepted, err)
	}
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{AnswerMarkdown: "The observation was truncated before any item was returned."}, DiagnosisMetadata{
		ID:        testDiagnosisID,
		CreatedAt: input.Scope().ActivatedAt,
	}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis(truncated) error = %v", err)
	}
	if diagnosis.EvidenceDetailsState != domain.EvidenceDetailPartial ||
		!hasMissingKind(diagnosis.MissingInformation, domain.MissingInformationTruncated) {
		t.Fatalf("truncated Diagnosis = %#v", diagnosis)
	}
}

func evidenceID(index int) domain.EvidenceID {
	return domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 4_300+index))
}

func claimCoverageRegistry(t *testing.T) (*EvidenceRegistry, RunInput, domain.EvidenceID, domain.EvidenceID) {
	t.Helper()
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	firstID := testEvidenceID
	secondID := domain.EvidenceID("00000000-0000-7000-8000-000000004098")
	result := testToolResult(t, call, firstID, time.UnixMilli(1_000).UTC())
	result.Evidence[0].PolicyVersion = domain.ResourcePolicyVersion
	result.Evidence[0].PolicyGeneration = input.PolicyGeneration()
	second := result.Evidence[0]
	second.ID = secondID
	second.Fact = "The projected Pod restart count is one."
	second.Fingerprint = domain.SHA256Hex(string(secondID))
	result.Evidence = append(result.Evidence, second)
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope(), input.PolicyGeneration())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err = registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}
	return registry, input, firstID, secondID
}

func validCoverageDraft(sequence int, kind domain.ClaimKind, text string, state domain.ClaimCoverageState, ids ...domain.EvidenceID) ClaimCoverageDraft {
	return ClaimCoverageDraft{
		Sequence: sequence, Kind: kind, Text: text, TextHash: domain.SHA256Hex(text),
		EvidenceIDs: append([]domain.EvidenceID(nil), ids...), State: state,
	}
}

func TestClaimCoverageBindsObservationInferenceUncertaintyAndUnsupportedState(t *testing.T) {
	registry, input, firstID, secondID := claimCoverageRegistry(t)
	coverage := []ClaimCoverageDraft{
		validCoverageDraft(1, domain.ClaimCurrentObservation, "The Pod is not Ready.", domain.ClaimCoverageVerified, firstID),
		validCoverageDraft(2, domain.ClaimInference, "The readiness probe may be failing.", domain.ClaimCoverageSupported, secondID),
		validCoverageDraft(3, domain.ClaimUncertainty, "The probe configuration was not collected.", domain.ClaimCoverageLimited),
		validCoverageDraft(4, domain.ClaimUnsupportedObservation, "No current configuration observation supports a precise cause.", domain.ClaimCoverageUnsupported),
	}
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: "The Pod is not Ready. The exact cause remains uncertain.",
		ConfirmedFacts: []domain.ConfirmedFact{{Statement: coverage[0].Text, EvidenceIDs: []domain.EvidenceID{firstID}}},
		ClaimCoverage:  coverage,
	}, DiagnosisMetadata{
		ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(), PolicyGeneration: input.PolicyGeneration(),
	}, registry)
	if err != nil || len(diagnosis.ClaimCoverage) != 4 {
		t.Fatalf("ValidateDiagnosis(valid coverage) = %#v, %v", diagnosis, err)
	}
	for index, item := range diagnosis.ClaimCoverage {
		if item.Sequence != index+1 || item.RunID != input.RunID() || item.Scope != input.Scope().Snapshot() ||
			item.PolicyGeneration != input.PolicyGeneration() {
			t.Fatalf("bound coverage[%d] = %#v", index, item)
		}
	}
}

func TestClaimCoverageRejectsMalformedOrUnownedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvidenceRegistry, RunInput, domain.EvidenceID, domain.EvidenceID, *[]ClaimCoverageDraft, *DiagnosisMetadata)
	}{
		{
			name: "missing Evidence",
			mutate: func(_ *EvidenceRegistry, _ RunInput, _, _ domain.EvidenceID, coverage *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				(*coverage)[0].EvidenceIDs = nil
			},
		},
		{
			name: "unknown Evidence",
			mutate: func(_ *EvidenceRegistry, _ RunInput, _, _ domain.EvidenceID, coverage *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				(*coverage)[0].EvidenceIDs[0] = "00000000-0000-7000-8000-000000004097"
			},
		},
		{
			name: "duplicate Evidence",
			mutate: func(_ *EvidenceRegistry, _ RunInput, first, _ domain.EvidenceID, coverage *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				(*coverage)[0].EvidenceIDs = []domain.EvidenceID{first, first}
			},
		},
		{
			name: "out of order Evidence",
			mutate: func(_ *EvidenceRegistry, _ RunInput, first, second domain.EvidenceID, coverage *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				(*coverage)[0].EvidenceIDs = []domain.EvidenceID{second, first}
			},
		},
		{
			name: "hash mismatch",
			mutate: func(_ *EvidenceRegistry, _ RunInput, _, _ domain.EvidenceID, coverage *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				(*coverage)[0].TextHash = domain.SHA256Hex("different text")
			},
		},
		{
			name: "sequence gap",
			mutate: func(_ *EvidenceRegistry, _ RunInput, _, _ domain.EvidenceID, coverage *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				(*coverage)[0].Sequence = 2
			},
		},
		{
			name: "stale policy generation",
			mutate: func(_ *EvidenceRegistry, _ RunInput, _, _ domain.EvidenceID, _ *[]ClaimCoverageDraft, metadata *DiagnosisMetadata) {
				metadata.PolicyGeneration++
			},
		},
		{
			name: "cross run Evidence",
			mutate: func(registry *EvidenceRegistry, _ RunInput, first, _ domain.EvidenceID, _ *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				registry.mu.Lock()
				value := registry.items[first]
				value.RunID = "00000000-0000-7000-8000-000000004096"
				registry.items[first] = value
				registry.mu.Unlock()
			},
		},
		{
			name: "cross scope Evidence",
			mutate: func(registry *EvidenceRegistry, _ RunInput, first, _ domain.EvidenceID, _ *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				registry.mu.Lock()
				value := registry.items[first]
				value.Scope.Generation++
				registry.items[first] = value
				registry.mu.Unlock()
			},
		},
		{
			name: "unowned policy Evidence",
			mutate: func(registry *EvidenceRegistry, _ RunInput, first, _ domain.EvidenceID, _ *[]ClaimCoverageDraft, _ *DiagnosisMetadata) {
				registry.mu.Lock()
				value := registry.items[first]
				value.PolicyGeneration = 2
				registry.items[first] = value
				registry.mu.Unlock()
			},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			registry, input, firstID, secondID := claimCoverageRegistry(t)
			coverage := []ClaimCoverageDraft{
				validCoverageDraft(1, domain.ClaimCurrentObservation, "The Pod is not Ready.", domain.ClaimCoverageVerified, firstID),
			}
			metadata := DiagnosisMetadata{
				ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(), PolicyGeneration: input.PolicyGeneration(),
			}
			current.mutate(registry, input, firstID, secondID, &coverage, &metadata)
			_, err := ValidateDiagnosis(DiagnosisDraft{
				AnswerMarkdown: "The Pod is not Ready.", ClaimCoverage: coverage,
			}, metadata, registry)
			if !errors.Is(err, ErrInvalidDiagnosisDraft) {
				t.Fatalf("ValidateDiagnosis() error = %v", err)
			}
		})
	}
}

func TestClaimCoverageLimitsAreExactAndOneOverFailsClosed(t *testing.T) {
	registry, input, firstID, _ := claimCoverageRegistry(t)
	coverage := make([]ClaimCoverageDraft, 100)
	for index := range coverage {
		text := fmt.Sprintf("Unsupported synthetic observation %03d.", index)
		coverage[index] = validCoverageDraft(index+1, domain.ClaimUnsupportedObservation, text, domain.ClaimCoverageUnsupported)
	}
	coverage[0] = validCoverageDraft(1, domain.ClaimCurrentObservation, "The Pod is not Ready.", domain.ClaimCoverageVerified, firstID)
	metadata := DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(), PolicyGeneration: input.PolicyGeneration()}
	if _, err := ValidateDiagnosis(DiagnosisDraft{AnswerMarkdown: "Bounded response.", ClaimCoverage: coverage}, metadata, registry); err != nil {
		t.Fatalf("ValidateDiagnosis(exact claim ceiling) error = %v", err)
	}

	registry, input, firstID, _ = claimCoverageRegistry(t)
	over := append(append([]ClaimCoverageDraft(nil), coverage...),
		validCoverageDraft(101, domain.ClaimUnsupportedObservation, "One over.", domain.ClaimCoverageUnsupported))
	metadata.PolicyGeneration = input.PolicyGeneration()
	if _, err := ValidateDiagnosis(DiagnosisDraft{AnswerMarkdown: "Bounded response.", ClaimCoverage: over}, metadata, registry); !errors.Is(err, ErrInvalidDiagnosisDraft) {
		t.Fatalf("ValidateDiagnosis(one-over claim ceiling) error = %v", err)
	}
}

func TestAnswerCompletenessDistinguishesNegativeAndUnavailableSourceCoverage(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*domain.ToolResult)
		wantState  domain.SourceCoverageState
		wantFresh  domain.EvidenceFreshnessState
		wantReason domain.RunTerminalReason
	}{
		{
			name: "checked absent",
			configure: func(result *domain.ToolResult) {
				result.Evidence = nil
				result.DataJSON = `{"items":[]}`
			},
			wantState: domain.SourceCheckedAbsent, wantFresh: domain.EvidenceFreshnessUnknown, wantReason: domain.RunTerminalCompleted,
		},
		{
			name: "denied",
			configure: func(result *domain.ToolResult) {
				result.Status, result.Evidence, result.DataJSON = domain.ToolResultStatusDenied, nil, `{}`
				result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassPermissionDenied, SafeMessage: "The source was denied."}
			},
			wantState: domain.SourceDenied, wantFresh: domain.EvidenceFreshnessUnknown, wantReason: domain.RunTerminalPolicyDenied,
		},
		{
			name: "timed out",
			configure: func(result *domain.ToolResult) {
				result.Status, result.Evidence, result.DataJSON = domain.ToolResultStatusError, nil, `{}`
				result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassTimeout, Retryable: true, SafeMessage: "The source timed out."}
			},
			wantState: domain.SourceTimedOut, wantFresh: domain.EvidenceFreshnessUnknown, wantReason: domain.RunTerminalSourceUnavailable,
		},
		{
			name: "stale",
			configure: func(result *domain.ToolResult) {
				result.Status, result.Evidence, result.DataJSON = domain.ToolResultStatusError, nil, `{}`
				result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassStaleScope, SafeMessage: "The source became stale."}
			},
			wantState: domain.SourceStale, wantFresh: domain.EvidenceStale, wantReason: domain.RunTerminalStaleGeneration,
		},
		{
			name: "truncated",
			configure: func(result *domain.ToolResult) {
				result.Status, result.Evidence, result.DataJSON = domain.ToolResultStatusPartial, nil, `{}`
				result.Truncation = domain.ToolResultTruncation{Truncated: true, Reason: "item_limit", ReturnedBytes: 2}
			},
			wantState: domain.SourceTruncated, wantFresh: domain.EvidenceFreshnessUnknown, wantReason: domain.RunTerminalPartialResult,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := testRunInput(t, "Inspect the selected Pod.")
			call := testBoundCall(t, input, testInvocationID, "sample-pod")
			result := testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt)
			test.configure(&result)
			if err := result.Validate(); err != nil {
				t.Fatalf("ToolResult.Validate() error = %v", err)
			}
			registry, err := NewEvidenceRegistry(input.RunID(), input.Scope(), input.PolicyGeneration())
			if err != nil {
				t.Fatalf("NewEvidenceRegistry() error = %v", err)
			}
			if _, err := registry.AcceptToolResult(call, result); err != nil {
				t.Fatalf("AcceptToolResult() error = %v", err)
			}
			diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
				AnswerMarkdown: "The bounded source state is reported without inferring omitted objects.", ResponseSchemaVersion: 2,
				SuggestedStopReason: domain.RunTerminalCompleted,
			}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: input.Scope().ActivatedAt, PolicyGeneration: input.PolicyGeneration()}, registry)
			if err != nil || len(diagnosis.Completeness.Sources) != 1 {
				t.Fatalf("ValidateDiagnosis() = %#v, %v", diagnosis, err)
			}
			source := diagnosis.Completeness.Sources[0]
			if source.State != test.wantState || source.Freshness != test.wantFresh ||
				diagnosis.Completeness.StopReason != test.wantReason || source.SourceHash == "" || source.SubjectHash == "" {
				t.Fatalf("source/stop = %#v/%q", source, diagnosis.Completeness.StopReason)
			}
		})
	}
}

func TestAnswerCompletenessMarksExactOlderEvidenceSuperseded(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod twice.")
	firstCall := testBoundCall(t, input, testInvocationID, "sample-pod")
	secondInvocation := domain.ToolInvocationID("00000000-0000-7000-8000-000000004095")
	secondCall := testBoundCall(t, input, secondInvocation, "sample-pod")
	first := testToolResult(t, firstCall, testEvidenceID, input.Scope().ActivatedAt)
	secondID := domain.EvidenceID("00000000-0000-7000-8000-000000004096")
	second := testToolResult(t, secondCall, secondID, input.Scope().ActivatedAt.Add(time.Millisecond))
	first.Evidence[0].PolicyVersion = domain.ResourcePolicyVersion
	first.Evidence[0].PolicyGeneration = input.PolicyGeneration()
	second.Evidence[0].PolicyVersion = domain.ResourcePolicyVersion
	second.Evidence[0].PolicyGeneration = input.PolicyGeneration()
	second.Evidence[0].Fingerprint = domain.SHA256Hex("sample-pod-ready")
	second.Evidence[0].Fact = "The projected Pod condition is Ready."
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope(), input.PolicyGeneration())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err = registry.AcceptToolResult(firstCall, first); err != nil {
		t.Fatalf("AcceptToolResult(first) error = %v", err)
	}
	if _, err = registry.AcceptToolResult(secondCall, second); err != nil {
		t.Fatalf("AcceptToolResult(second) error = %v", err)
	}
	claim := "The Pod is not Ready."
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: claim, ResponseSchemaVersion: 2,
		ConfirmedFacts:      []domain.ConfirmedFact{{Statement: claim, EvidenceIDs: []domain.EvidenceID{testEvidenceID}}},
		ClaimCoverage:       []ClaimCoverageDraft{validCoverageDraft(1, domain.ClaimCurrentObservation, claim, domain.ClaimCoverageVerified, testEvidenceID)},
		SuggestedStopReason: domain.RunTerminalCompleted,
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: second.ObservedAt, PolicyGeneration: input.PolicyGeneration()}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis() error = %v", err)
	}
	older := diagnosis.Completeness.Sources[0]
	if older.Conflict != domain.EvidenceConflictSuperseded || len(older.SupersededByEvidenceIDs) != 1 ||
		older.SupersededByEvidenceIDs[0] != secondID || diagnosis.Completeness.StopReason != domain.RunTerminalConflictingEvidence {
		t.Fatalf("supersession/stop = %#v/%q", older, diagnosis.Completeness.StopReason)
	}
}

func TestTypedClarificationHasNoEvidenceActionOrLimitationAuthority(t *testing.T) {
	input := testRunInput(t, "Help inspect a workload.")
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope(), input.PolicyGeneration())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	request := domain.ClarificationRequest{
		SchemaVersion: domain.AnswerCompletenessSchemaVersion,
		Questions: []domain.ClarificationQuestion{{Sequence: 1, Kind: domain.ClarificationChoice,
			Prompt: "Which workload should be inspected?", Choices: []domain.ClarificationChoiceValue{{ID: "api", Label: "API"}, {ID: "worker", Label: "Worker"}}}},
	}
	answer, err := RenderClarificationMarkdown(request)
	if err != nil {
		t.Fatalf("RenderClarificationMarkdown() error = %v", err)
	}
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: answer, ResponseSchemaVersion: 2, SuggestedStopReason: domain.RunTerminalNeedsUserInput,
		Clarification: &request,
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: input.Scope().ActivatedAt, PolicyGeneration: input.PolicyGeneration()}, registry)
	if err != nil || diagnosis.Clarification == nil || diagnosis.Completeness.StopReason != domain.RunTerminalNeedsUserInput ||
		diagnosis.Completeness.StopReasonBasis != domain.RunTerminalReasonFromClarification ||
		len(diagnosis.Completeness.Claims) != 0 || len(diagnosis.Completeness.Limitations) != 0 {
		t.Fatalf("typed clarification = %#v, %v", diagnosis, err)
	}
}

func TestModelSuggestedStopReasonCannotOverrideAcceptedCoverage(t *testing.T) {
	registry, input, evidenceID, _ := claimCoverageRegistry(t)
	claim := "The Pod is not Ready."
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		AnswerMarkdown: claim, ResponseSchemaVersion: 2,
		ConfirmedFacts: []domain.ConfirmedFact{{Statement: claim, EvidenceIDs: []domain.EvidenceID{evidenceID}}},
		ClaimCoverage: []ClaimCoverageDraft{
			validCoverageDraft(1, domain.ClaimCurrentObservation, claim, domain.ClaimCoverageVerified, evidenceID),
		},
		SuggestedStopReason: domain.RunTerminalPolicyDenied,
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC(), PolicyGeneration: input.PolicyGeneration()}, registry)
	if err != nil || diagnosis.Completeness.StopReason != domain.RunTerminalCompleted ||
		diagnosis.Completeness.StopReasonBasis != domain.RunTerminalReasonFromCoverage {
		t.Fatalf("model-suggested stop reason retained authority: %#v, %v", diagnosis.Completeness, err)
	}
	tampered := diagnosis.Completeness
	tampered.StopReasonBasis = domain.RunTerminalReasonFromRuntimeBudget
	tampered.StopReason = domain.RunTerminalBudgetExhausted
	if tampered.Validate() == nil {
		t.Fatal("runtime-budget basis without a deterministic runtime limitation was accepted")
	}
}
