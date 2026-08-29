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
			SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID, testEvidenceID, forgedID},
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
	if strings.Contains(diagnosis.AnswerMarkdown, string(testEvidenceID)) ||
		strings.Contains(diagnosis.AnswerMarkdown, "Supporting Evidence") ||
		!strings.Contains(diagnosis.AnswerMarkdown, "Not executed") {
		t.Fatalf("rendered Diagnosis = %s", diagnosis.AnswerMarkdown)
	}
}

func TestDiagnosisValidatorDowngradesHypothesisWhenReferencesAreRemoved(t *testing.T) {
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
		ConfirmedFacts: []domain.ConfirmedFact{{
			Statement: "The projected Pod condition is not Ready.", EvidenceIDs: []domain.EvidenceID{testEvidenceID},
		}},
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The application may still be starting.",
			SupportingEvidenceIDs: []domain.EvidenceID{testEvidenceID, forgedID},
			Confidence:            domain.DiagnosisConfidenceHigh,
			Falsifier:             "A later bounded observation reports the Pod Ready.",
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: time.UnixMilli(1_001).UTC()}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis() error = %v", err)
	}
	if got := diagnosis.Hypotheses[0].SupportingEvidenceIDs; len(got) != 1 || got[0] != testEvidenceID {
		t.Fatalf("hypothesis Evidence IDs = %#v", got)
	}
	if got := diagnosis.Hypotheses[0].Confidence; got != domain.DiagnosisConfidenceLow {
		t.Fatalf("hypothesis confidence = %q, want low", got)
	}
	if !hasMissingKind(diagnosis.MissingInformation, domain.MissingInformationUnsupported) {
		t.Fatalf("missing information = %#v", diagnosis.MissingInformation)
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

func TestDiagnosisRendererUsesKubectlStyleTableAndKeepsEvidenceInternal(t *testing.T) {
	input := testRunInput(t, "List Pods in the current Namespace.")
	call, err := BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID: "call-1", Name: domain.ToolNameListResources,
		ArgumentsJSON: `{"health_filter":"any","kind":"Pod","purpose":"List Pods in the current Namespace."}`,
	})
	if err != nil {
		t.Fatalf("BindToolCall() error = %v", err)
	}
	result := testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt)
	ids := make([]domain.EvidenceID, 9)
	result.Evidence = make([]domain.Evidence, len(ids))
	result.ResourceSummaries = make([]domain.ResourceSummary, len(ids))
	facts := make([]domain.ConfirmedFact, len(ids))
	for index := range ids {
		ids[index] = evidenceID(index)
		evidence := testToolResult(t, call, ids[index], input.Scope().ActivatedAt).Evidence[0]
		evidence.Resource.Name = fmt.Sprintf("sample-pod-%d", index+1)
		evidence.Category = domain.EvidenceCategoryResourceStatus
		evidence.Fact = fmt.Sprintf("Pod sample-pod-%d was observed; phase Running; ready 1 of 1.", index+1)
		evidence.Fingerprint = domain.SHA256Hex(evidence.Fact)
		result.Evidence[index] = evidence
		status := domain.ResourceStatus{Phase: "Running", Ready: domain.Count(1), Desired: domain.Count(1)}
		if index == 1 {
			status = domain.ResourceStatus{Phase: "Succeeded", Reason: "Completed", Ready: domain.Count(0), Desired: domain.Count(1)}
		}
		result.ResourceSummaries[index] = domain.ResourceSummary{Reference: evidence.Resource, Status: status}
		facts[index] = domain.ConfirmedFact{
			Statement:   fmt.Sprintf("A validated observation exists for sample-pod-%d.", index+1),
			EvidenceIDs: []domain.EvidenceID{ids[index]},
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate() error = %v", err)
	}
	registry, err := NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		t.Fatalf("NewEvidenceRegistry() error = %v", err)
	}
	if accepted, err := registry.AcceptToolResult(call, result); err != nil || accepted != len(ids) {
		t.Fatalf("AcceptToolResult() = %d, %v", accepted, err)
	}
	diagnosis, err := ValidateDiagnosis(DiagnosisDraft{
		ConfirmedFacts: facts,
		Hypotheses: []domain.Hypothesis{{
			Statement:             "The projected statuses may share a cause.",
			SupportingEvidenceIDs: append([]domain.EvidenceID(nil), ids...),
			Confidence:            domain.DiagnosisConfidenceLow,
			Falsifier:             "A bounded follow-up observation shows unrelated conditions.",
		}},
	}, DiagnosisMetadata{ID: testDiagnosisID, CreatedAt: input.Scope().ActivatedAt.Add(time.Millisecond)}, registry)
	if err != nil {
		t.Fatalf("ValidateDiagnosis() error = %v", err)
	}
	for index, id := range ids {
		if strings.Contains(diagnosis.AnswerMarkdown, string(id)) ||
			!strings.Contains(diagnosis.AnswerMarkdown, fmt.Sprintf("sample-pod-%d", index+1)) {
			t.Fatalf("table row %d or hidden provenance = %s", index+1, diagnosis.AnswerMarkdown)
		}
	}
	if !strings.Contains(diagnosis.AnswerMarkdown, "NAME          READY  STATUS") ||
		!strings.Contains(diagnosis.AnswerMarkdown, "sample-pod-2  0/1    Completed") ||
		strings.Contains(diagnosis.AnswerMarkdown, "was observed") ||
		strings.Contains(diagnosis.AnswerMarkdown, "-----------") ||
		strings.Contains(diagnosis.AnswerMarkdown, "Evidence") ||
		len(diagnosis.ReferencedEvidenceIDs()) != len(ids) {
		t.Fatalf("resource table or typed provenance = %s / %#v", diagnosis.AnswerMarkdown, diagnosis.ReferencedEvidenceIDs())
	}
}

func TestDiagnosisResourceTablesUseKindSpecificReadableColumns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		kind       domain.ResourceKind
		status     domain.ResourceStatus
		wantHeader string
		wantRow    string
	}{
		{
			name: "Deployment", kind: domain.ResourceKindDeployment,
			status: domain.ResourceStatus{
				Ready: domain.Count(2), Desired: domain.Count(3), Available: domain.Count(1), Reason: "Progressing",
			},
			wantHeader: "NAME|READY|AVAILABLE|REASON",
			wantRow:    "sample-deployment|2/3|1/3|Progressing",
		},
		{
			name: "ReplicaSet", kind: domain.ResourceKindReplicaSet,
			status: domain.ResourceStatus{
				Desired: domain.Count(3), Ready: domain.Count(2), Available: domain.Count(1), Reason: "Progressing",
			},
			wantHeader: "NAME|DESIRED|READY|AVAILABLE|REASON",
			wantRow:    "sample-replicaset|3|2|1|Progressing",
		},
		{
			name: "Job", kind: domain.ResourceKindJob,
			status: domain.ResourceStatus{
				Phase: "Running", Desired: domain.Count(3), Succeeded: domain.Count(1), Failed: domain.Count(0), Reason: "Backoff",
			},
			wantHeader: "NAME|STATUS|COMPLETIONS|FAILED|REASON",
			wantRow:    "sample-job|Running|1/3|0|Backoff",
		},
		{
			name: "Service", kind: domain.ResourceKindService,
			status:     domain.ResourceStatus{ServiceType: "ClusterIP"},
			wantHeader: "NAME|TYPE",
			wantRow:    "sample-service|ClusterIP",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := "sample-" + strings.ToLower(tt.name)
			got := renderDiagnosisResourceStatusTable([]diagnosisResourceStatusRow{{summary: domain.ResourceSummary{
				Reference: domain.ResourceRef{
					APIVersion: tt.kind.APIVersion(), Kind: string(tt.kind), Namespace: "test-namespace", Name: name,
				},
				Status: tt.status,
			}}})
			lines := strings.Split(strings.TrimSpace(got), "\n")
			if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), "|") != tt.wantHeader ||
				strings.Join(strings.Fields(lines[1]), "|") != tt.wantRow {
				t.Fatalf("resource table = %q, want header %q and row %q", got, tt.wantHeader, tt.wantRow)
			}
			for _, forbidden := range []string{"Evidence", "was observed", "phase ", "projected."} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("resource table contains implementation prose %q: %q", forbidden, got)
				}
			}
		})
	}
}

func TestDiagnosisRendererBreaksAggregatedBulkFactClauses(t *testing.T) {
	t.Parallel()
	fullwidthSemicolon := string(rune(0xff1b))
	for _, separator := range []string{"; ", fullwidthSemicolon} {
		statement := strings.Join([]string{
			"Pod sample-1 has a projected status",
			"Pod sample-2 has a projected status",
			"Pod sample-3 has a projected status",
			"Pod sample-4 has a projected status",
		}, separator)
		got := markdownConfirmedFactText(statement, 4)
		if strings.Count(got, "\n    ") != 3 || strings.Contains(got, separator+"Pod sample-2") {
			t.Fatalf("markdownConfirmedFactText(%q) = %q", separator, got)
		}
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
