package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDiagnosisValidatorRejectsUnregisteredEvidenceAndExecutionClaims(t *testing.T) {
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
		ConfirmedFacts: []domain.ConfirmedFact{
			{Statement: "The projected Pod condition is not Ready.", EvidenceIDs: []domain.EvidenceID{testEvidenceID}},
			{Statement: "A fabricated observation claims a restart occurred.", EvidenceIDs: []domain.EvidenceID{forgedID}},
			{Statement: "An unsupported statement has no Evidence.", EvidenceIDs: nil},
		},
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID, forgedID},
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
	if len(diagnosis.ConfirmedFacts) != 1 || diagnosis.ConfirmedFacts[0].EvidenceIDs[0] != testEvidenceID {
		t.Fatalf("confirmed facts = %#v", diagnosis.ConfirmedFacts)
	}
	if got := diagnosis.Hypotheses[0].SupportingEvidenceIDs; len(got) != 1 || got[0] != testEvidenceID {
		t.Fatalf("hypothesis Evidence IDs = %#v", got)
	}
	if diagnosis.RecommendedActions[0].Executed {
		t.Fatal("execution claim survived validation")
	}
	if len(diagnosis.ValidationWarnings) < 3 {
		t.Fatalf("validation warnings = %#v", diagnosis.ValidationWarnings)
	}
	for _, rejected := range []string{"fabricated observation", "unsupported statement", "restart occurred"} {
		if strings.Contains(diagnosis.AnswerMarkdown, rejected) {
			t.Fatalf("rendered Diagnosis contains rejected claim %q", rejected)
		}
	}
	if !strings.Contains(diagnosis.AnswerMarkdown, string(testEvidenceID)) || !strings.Contains(diagnosis.AnswerMarkdown, "Not executed") {
		t.Fatalf("rendered Diagnosis = %s", diagnosis.AnswerMarkdown)
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
		RecommendedActions: []domain.RecommendedAction{{
			Action: blocked, Risk: "Review is required.", Executed: false,
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if !errors.Is(err, ErrSensitiveModelTextBlocked) || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), blocked) {
		t.Fatalf("ValidateDiagnosis(blocked text) error = %v", err)
	}
	if _, err := ValidateDiagnosis(DiagnosisDraft{}, DiagnosisMetadata{
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
	if _, _, err := BuildToolResultMessage("call-1", unsafeResult); err == nil {
		t.Fatal("unsafe BuildToolResultMessage() error = nil")
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
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{}, DiagnosisMetadata{
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

func TestPromptAndDiagnosisSnapshots(t *testing.T) {
	input := testRunInput(t, "Why is this Pod not Ready?")
	prompt, err := BuildSystemPrompt(input)
	if err != nil {
		t.Fatalf("BuildSystemPrompt() error = %v", err)
	}
	assertSnapshot(t, "agent-policy-system-prompt.txt", prompt)

	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	result := testToolResult(t, call, testEvidenceID, time.UnixMilli(1_000).UTC())
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if _, err := registry.AcceptToolResult(call, result); err != nil {
		t.Fatalf("AcceptToolResult() error = %v", err)
	}
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		ConfirmedFacts: []domain.ConfirmedFact{{Statement: "The projected Pod condition is not Ready.", EvidenceIDs: []domain.EvidenceID{testEvidenceID}}},
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID},
			Confidence:            domain.DiagnosisConfidenceLow,
			Falsifier:             "A later bounded observation reports the Pod Ready.",
		}},
		MissingInformation: []domain.MissingInformation{{
			Kind:   domain.MissingInformationAbsent,
			Detail: "Recent Events were not collected.",
			Impact: "The reason for the readiness state remains uncertain.",
		}},
		RecommendedActions: []domain.RecommendedAction{{
			Action:        "Review the readiness probe and application startup state.",
			Risk:          "Configuration changes can restart Pods.",
			Prerequisites: []string{"Confirm the active Context and Namespace."},
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis() error = %v", err)
	}
	assertSnapshot(t, "agent-policy-diagnosis.md", diagnosis.AnswerMarkdown)
}

func assertSnapshot(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "model", name)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	if got != string(want) {
		t.Fatalf("snapshot %q mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
