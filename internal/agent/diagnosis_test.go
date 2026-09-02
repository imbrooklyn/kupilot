package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDiagnosisValidatorRemovesUnregisteredEvidenceAndExecutionClaims(t *testing.T) {
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
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
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
	if err != nil {
		t.Fatalf("ValidateDiagnosis() error = %v", err)
	}
	if len(diagnosis.ConfirmedFacts) != 1 || diagnosis.ConfirmedFacts[0].Statement != "The projected Pod condition is not Ready." ||
		len(diagnosis.ConfirmedFacts[0].EvidenceIDs) != 1 || diagnosis.ConfirmedFacts[0].EvidenceIDs[0] != testEvidenceID {
		t.Fatalf("confirmed facts = %#v", diagnosis.ConfirmedFacts)
	}
	if got := diagnosis.Hypotheses[0].SupportingEvidenceIDs; len(got) != 1 || got[0] != testEvidenceID {
		t.Fatalf("hypothesis Evidence IDs = %#v", got)
	}
	if diagnosis.RecommendedActions[0].Executed {
		t.Fatal("execution claim survived validation")
	}
	if len(diagnosis.ValidationWarnings) != 2 || !hasMissingKind(diagnosis.MissingInformation, domain.MissingInformationUnsupported) {
		t.Fatalf("validation warnings = %#v", diagnosis.ValidationWarnings)
	}
	if diagnosis.AnswerMarkdown != "The Pod is not Ready. Review its readiness probe before changing the workload." ||
		strings.Contains(diagnosis.AnswerMarkdown, string(testEvidenceID)) ||
		strings.Contains(diagnosis.AnswerMarkdown, "## Confirmed facts") {
		t.Fatalf("free-form answer = %s", diagnosis.AnswerMarkdown)
	}
}

func TestDiagnosisValidatorRemovesInvalidHypothesisReferences(t *testing.T) {
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
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
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
	if err != nil {
		t.Fatalf("ValidateDiagnosis(invalid hypothesis citation) error = %v", err)
	}
	if len(diagnosis.Hypotheses) != 1 || diagnosis.Hypotheses[0].Confidence != domain.DiagnosisConfidenceLow ||
		len(diagnosis.Hypotheses[0].SupportingEvidenceIDs) != 1 || diagnosis.Hypotheses[0].SupportingEvidenceIDs[0] != testEvidenceID ||
		len(diagnosis.ValidationWarnings) != 1 || !hasMissingKind(diagnosis.MissingInformation, domain.MissingInformationUnsupported) {
		t.Fatalf("filtered hypothesis diagnosis = %#v", diagnosis)
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
