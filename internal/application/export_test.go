package application

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	exportTestSessionID  domain.SessionID  = "0198a46e-7d2a-7d34-9b6f-2df5f45a3201"
	exportTestEvidenceID domain.EvidenceID = "0198a46e-7d2a-7d34-9b6f-2df5f45a3202"
	exportTestRunID      domain.AgentRunID = "0198a46e-7d2a-7d34-9b6f-2df5f45a3203"
)

func TestExportSummaryUsesVersionedAllowlistAndRemovesSensitiveCanaries(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	credentialCanary := "Bearer export-secret-canary-8a4b2c9d"
	snapshot := SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, Title: "Incident " + credentialCanary,
			PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt:   createdAt, UpdatedAt: createdAt.Add(time.Minute),
			LastScope: &domain.ScopeCandidate{Context: "production", Namespace: "payments"},
		},
		Messages: []ExportMessageRecord{
			{Role: domain.MessageRoleUser, Content: "Inspect failures with " + credentialCanary, CreatedAt: createdAt.Add(time.Second)},
			{Role: domain.MessageRoleAssistant, Content: "The projected answer is bounded.", CreatedAt: createdAt.Add(2 * time.Second)},
		},
		ContextSummary: &domain.SessionContextSummary{
			SessionID: exportTestSessionID, Text: "Historic safe context " + credentialCanary,
			SummaryHash:      domain.SHA256Hex("Historic safe context " + credentialCanary),
			SchemaVersion:    domain.SessionContextSummarySchemaVersion,
			PolicyVersion:    domain.SafeConversationContextPolicyVersion,
			CoveredFirstID:   "0198a46e-7d2a-7d34-9b6f-2df5f45a3211",
			CoveredThroughID: "0198a46e-7d2a-7d34-9b6f-2df5f45a3212",
			CoveredCount:     2, CoveredBytes: 42, CoverageDigest: domain.SHA256Hex("coverage"),
			GeneratedAt: createdAt.Add(30 * time.Second), AgentProfile: "agent",
			AgentOriginHash: domain.SHA256Hex("https://model.example"),
		},
		Diagnoses: []ExportDiagnosisRecord{{
			AnswerMarkdown: "The bounded answer includes " + credentialCanary,
			CreatedAt:      createdAt.Add(3 * time.Second),
			ConfirmedFacts: []domain.ConfirmedFact{{Statement: "A safe fact " + credentialCanary, EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID}}},
			Hypotheses: []domain.Hypothesis{{
				Statement: "A bounded hypothesis", SupportingEvidenceIDs: []domain.EvidenceID{exportTestEvidenceID},
				Confidence: domain.DiagnosisConfidenceLow, Falsifier: "A safe falsifier",
			}},
			MissingInformation: []domain.MissingInformation{{Kind: domain.MissingInformationTruncated, Detail: "More detail is unavailable.", Impact: "Confidence remains limited."}},
			RecommendedActions: []domain.RecommendedAction{{Action: "Review the workload.", Risk: "Read-only review.", Prerequisites: []string{"Verify scope."}}},
			ClaimCoverage: []domain.ClaimEvidenceCoverage{{
				Sequence: 1, Kind: domain.ClaimCurrentObservation,
				Text: "A safe claim " + credentialCanary, TextHash: domain.SHA256Hex("A safe claim " + credentialCanary),
				EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID}, RunID: exportTestRunID,
				Scope:            domain.ScopeSnapshot{Context: "production", Namespace: "payments", Generation: 7},
				PolicyGeneration: 7, State: domain.ClaimCoverageVerified,
			}},
		}},
		Evidence: []ExportEvidenceRecord{{
			ID: exportTestEvidenceID, Category: domain.EvidenceCategoryCondition,
			Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"},
			Fact:     "Ready is false " + credentialCanary, SourcePath: "status.conditions[type=Ready]",
			ObservedAt: createdAt, RedactionCount: 1,
		}},
	}

	summary, err := ProjectExportSummary(snapshot, createdAt.Add(time.Hour), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	content, err := RenderExportSummary(summary, security.NewRedactor())
	if err != nil {
		t.Fatalf("RenderExportSummary() error = %v", err)
	}
	if summary.SchemaVersion != ExportSummarySchemaVersion || !bytes.HasPrefix(content, []byte("# Kupilot Session Summary\n")) {
		t.Fatalf("versioned export = %#v\n%s", summary, content)
	}
	if !bytes.Contains(content, []byte("REDACTED")) || bytes.Contains(content, []byte(credentialCanary)) {
		t.Fatalf("credential processing failed: %s", content)
	}
	if summary.ContextSummary == nil || !summary.ContextSummary.Redacted ||
		!bytes.Contains(content, []byte("## Model context")) ||
		!bytes.Contains(content, []byte("untrusted historic conversation context only")) ||
		!bytes.Contains(content, []byte("#### Claim coverage")) ||
		!bytes.Contains(content, []byte("`current_observation` · `verified`")) {
		t.Fatalf("safe model-context export missing: %#v\n%s", summary.ContextSummary, content)
	}
	for _, deniedLabel := range []string{"Tool input", "Tool result", "Model payload", "Approval digest"} {
		if strings.Contains(string(content), deniedLabel) {
			t.Fatalf("denylisted schema label %q reached export", deniedLabel)
		}
	}
}

func TestExportSummaryRejectsInvalidContextSummary(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	snapshot := SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		ContextSummary: &domain.SessionContextSummary{
			SessionID: "0198a46e-7d2a-7d34-9b6f-2df5f45a3299", Text: "Wrong Session context.",
			SummaryHash:      domain.SHA256Hex("Wrong Session context."),
			SchemaVersion:    domain.SessionContextSummarySchemaVersion,
			PolicyVersion:    domain.SafeConversationContextPolicyVersion,
			CoveredFirstID:   "0198a46e-7d2a-7d34-9b6f-2df5f45a3211",
			CoveredThroughID: "0198a46e-7d2a-7d34-9b6f-2df5f45a3212",
			CoveredCount:     2, CoveredBytes: 42, CoverageDigest: domain.SHA256Hex("coverage"),
			GeneratedAt: createdAt, AgentProfile: "agent",
			AgentOriginHash: domain.SHA256Hex("https://model.example"),
		},
	}
	if _, err := ProjectExportSummary(snapshot, createdAt.Add(time.Second), security.NewRedactor()); !errors.Is(err, ErrInvalidExportSummary) {
		t.Fatalf("ProjectExportSummary(wrong Session context) error = %v, want ErrInvalidExportSummary", err)
	}
}

func TestExportSummaryEscapesMarkdownAndMarksExpiredEvidence(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	snapshot := SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, Title: "# forged heading [link](file:///private/value)\nSchema: forged",
			PrivacyMode: domain.PrivacyModeStandard, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Messages: []ExportMessageRecord{{
			Role: domain.MessageRoleUser, Content: "<script>unsafe</script>\n# not a heading", CreatedAt: createdAt,
		}},
		Diagnoses: []ExportDiagnosisRecord{{
			AnswerMarkdown: "# forged answer\n<script>answer</script>",
			CreatedAt:      createdAt,
			ConfirmedFacts: []domain.ConfirmedFact{{Statement: "Historic fact\n# forged diagnosis", EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID}}},
		}},
	}

	summary, err := ProjectExportSummary(snapshot, createdAt.Add(time.Second), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	content, err := RenderExportSummary(summary, security.NewRedactor())
	if err != nil {
		t.Fatalf("RenderExportSummary() error = %v", err)
	}
	for _, want := range []string{"Schema: `kupilot.export-summary.v3`", "State: expired", `\# forged heading`, `\<script\>`, `\# forged answer`} {
		if !bytes.Contains(content, []byte(want)) {
			t.Fatalf("export missing %q:\n%s", want, content)
		}
	}
	if bytes.Contains(content, []byte("\n# not a heading")) || bytes.Contains(content, []byte("\nSchema: forged")) ||
		bytes.Contains(content, []byte("\n# forged diagnosis")) || len(content) > MaxExportSummaryBytes {
		t.Fatalf("Markdown was not safely bounded:\n%s", content)
	}
}

func TestExportSummaryRejectsUnsafeEvidenceResourceProjection(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	snapshot := SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Messages: []ExportMessageRecord{{
			Role: domain.MessageRoleUser, Content: "Inspect the workload.", CreatedAt: createdAt,
		}},
		Diagnoses: []ExportDiagnosisRecord{{
			AnswerMarkdown: "The workload is unavailable.",
			CreatedAt:      createdAt,
			ConfirmedFacts: []domain.ConfirmedFact{{
				Statement: "The workload is unavailable.", EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID},
			}},
		}},
		Evidence: []ExportEvidenceRecord{{
			ID: exportTestEvidenceID, Category: domain.EvidenceCategoryCondition,
			Resource: domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api`forged",
			},
			Fact: "Ready is false.", ObservedAt: createdAt,
		}},
	}

	if _, err := ProjectExportSummary(snapshot, createdAt.Add(time.Second), security.NewRedactor()); !errors.Is(err, ErrInvalidExportSummary) {
		t.Fatalf("ProjectExportSummary(unsafe resource) error = %v, want ErrInvalidExportSummary", err)
	}
}

func TestExportSummaryAcceptsPartialClusterScopedCRDEvidence(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	snapshot := SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Messages: []ExportMessageRecord{{
			Role: domain.MessageRoleUser, Content: "Inspect the cluster-scoped Widget.", CreatedAt: createdAt,
		}},
		Diagnoses: []ExportDiagnosisRecord{{
			AnswerMarkdown: "The bounded Widget observation is partial.", CreatedAt: createdAt,
			ConfirmedFacts: []domain.ConfirmedFact{{
				Statement: "The Widget was observed.", EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID},
			}},
		}},
		Evidence: []ExportEvidenceRecord{{
			ID: exportTestEvidenceID, Category: domain.EvidenceCategoryResourceStatus,
			Resource: domain.ResourceRef{APIVersion: "example.test/v1", Kind: "Widget", Name: "sample-widget"},
			ResourceType: domain.ResourceType{
				ID: "widgets", Group: "example.test", Version: "v1", Resource: "widgets",
				Kind: "Widget", Scope: domain.ResourceScopeCluster,
			},
			PolicyVersion: domain.ResourcePolicyVersion, PolicyGeneration: 7,
			Fact: "The projected Widget was observed.", SourcePath: "status.state", ObservedAt: createdAt,
			Partial: true,
		}},
	}
	summary, err := ProjectExportSummary(snapshot, createdAt.Add(time.Second), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	content, err := RenderExportSummary(summary, security.NewRedactor())
	if err != nil {
		t.Fatalf("RenderExportSummary() error = %v", err)
	}
	if len(summary.Evidence) != 1 || summary.Evidence[0].State != domain.EvidenceDetailPartial ||
		summary.Evidence[0].Namespace != "" || summary.Evidence[0].ResourceType != snapshot.Evidence[0].ResourceType ||
		!strings.Contains(string(content), "`Widget sample-widget` (`example.test/v1`)") ||
		!strings.Contains(string(content), "`example.test/v1 widgets` (`cluster`)") ||
		!strings.Contains(string(content), "Evidence policy: `kupilot-resource-policy-v1` (generation `7`)") {
		t.Fatalf("custom Evidence export = %#v\n%s", summary.Evidence, content)
	}
}

func TestExportSummaryAcceptsExactObservabilityEvidence(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	snapshot := SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Diagnoses: []ExportDiagnosisRecord{{
			AnswerMarkdown: "The CPU sample was observed.", CreatedAt: createdAt,
			ConfirmedFacts: []domain.ConfirmedFact{{
				Statement: "The CPU sample was observed.", EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID},
			}},
		}},
		Evidence: []ExportEvidenceRecord{{
			ID: exportTestEvidenceID, Category: domain.EvidenceCategoryPrometheus,
			Resource:      domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"},
			PolicyVersion: domain.ObservabilityPolicyVersion, PolicyGeneration: 11,
			Fact:       "The admitted CPU series contains one bounded sample.",
			SourcePath: "api/v1/query_range#pod_cpu_usage", ObservedAt: createdAt,
		}},
	}
	summary, err := ProjectExportSummary(snapshot, createdAt.Add(time.Second), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	content, err := RenderExportSummary(summary, security.NewRedactor())
	if err != nil {
		t.Fatalf("RenderExportSummary() error = %v", err)
	}
	if len(summary.Evidence) != 1 || summary.Evidence[0].PolicyVersion != domain.ObservabilityPolicyVersion ||
		!strings.Contains(string(content), "Evidence policy: `"+domain.ObservabilityPolicyVersion+"` (generation `11`)") ||
		!strings.Contains(string(content), "api/v1/query\\_range\\#pod\\_cpu\\_usage") {
		t.Fatalf("observability Evidence export = %#v\n%s", summary.Evidence, content)
	}

	snapshot.Evidence[0].SourcePath = "api/v1/query_range#model_supplied_query"
	if _, err := ProjectExportSummary(snapshot, createdAt.Add(time.Second), security.NewRedactor()); !errors.Is(err, ErrInvalidExportSummary) {
		t.Fatalf("ProjectExportSummary(unsafe query provenance) error = %v, want ErrInvalidExportSummary", err)
	}
}

func TestExportSummaryEnforcesSourceAndAggregateLimits(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	messages := make([]ExportMessageRecord, MaxExportMessages+1)
	for index := range messages {
		messages[index] = ExportMessageRecord{
			Role: domain.MessageRoleUser, Content: "Bounded message.",
			CreatedAt: createdAt.Add(time.Duration(index) * time.Millisecond),
		}
	}
	summary, err := ProjectExportSummary(SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Messages: messages,
	}, createdAt.Add(time.Hour), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	if len(summary.Messages) != MaxExportMessages || !summary.Truncated {
		t.Fatalf("message source cap = %d truncated=%t", len(summary.Messages), summary.Truncated)
	}

	summary.Messages = []ExportSummaryMessage{{
		Role: domain.MessageRoleUser, Content: strings.Repeat("x", MaxExportSummaryBytes), CreatedAt: createdAt,
	}}
	if _, err := RenderExportSummary(summary, security.NewRedactor()); !errors.Is(err, ErrExportSummaryLimit) {
		t.Fatalf("RenderExportSummary(aggregate limit) error = %v, want ErrExportSummaryLimit", err)
	}
}

func TestExportSummaryPreservesAnswerAboveQuestionLimit(t *testing.T) {
	t.Parallel()

	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	answer := strings.Repeat("a", MaxQuestionBytes+1)
	summary, err := ProjectExportSummary(SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Diagnoses: []ExportDiagnosisRecord{{AnswerMarkdown: answer, CreatedAt: createdAt}},
	}, createdAt.Add(time.Second), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	if summary.Truncated || len(summary.Diagnoses) != 1 || summary.Diagnoses[0].AnswerMarkdown != answer {
		t.Fatalf("answer projection truncated=%t diagnoses=%d answer-bytes=%d", summary.Truncated, len(summary.Diagnoses), len(summary.Diagnoses[0].AnswerMarkdown))
	}
}

func TestExportSummaryRejectsAnUnboundedEvidenceSource(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	evidence := make([]ExportEvidenceRecord, MaxExportEvidence+1)
	for index := range evidence {
		evidence[index] = ExportEvidenceRecord{
			ID:       domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8002-%012d", index+1)),
			Category: domain.EvidenceCategoryCondition,
			Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"},
			Fact:     "Ready is false.", ObservedAt: createdAt,
		}
	}
	_, err := ProjectExportSummary(SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Messages: []ExportMessageRecord{{
			Role: domain.MessageRoleUser, Content: "Inspect the workload.", CreatedAt: createdAt,
		}},
		Evidence: evidence,
	}, createdAt.Add(time.Second), security.NewRedactor())
	if !errors.Is(err, ErrExportSummaryLimit) {
		t.Fatalf("ProjectExportSummary(unbounded Evidence) error = %v, want ErrExportSummaryLimit", err)
	}
}

func TestExportSummaryMarksEvidencePartialWhenExportFieldIsTruncated(t *testing.T) {
	t.Parallel()
	createdAt := time.UnixMilli(1_775_000_000_000).UTC()
	summary, err := ProjectExportSummary(SessionExportSnapshot{
		Session: ExportSessionRecord{
			ID: exportTestSessionID, PrivacyMode: domain.PrivacyModeStandard,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Messages: []ExportMessageRecord{{
			Role: domain.MessageRoleUser, Content: "Inspect the workload.", CreatedAt: createdAt,
		}},
		Diagnoses: []ExportDiagnosisRecord{{
			AnswerMarkdown: "The workload is unavailable.",
			CreatedAt:      createdAt,
			ConfirmedFacts: []domain.ConfirmedFact{{
				Statement: "The workload is unavailable.", EvidenceIDs: []domain.EvidenceID{exportTestEvidenceID},
			}},
		}},
		Evidence: []ExportEvidenceRecord{{
			ID: exportTestEvidenceID, Category: domain.EvidenceCategoryCondition,
			Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "api-0"},
			Fact:     strings.Repeat("x", maxExportEvidenceFactBytes+1), ObservedAt: createdAt,
		}},
	}, createdAt.Add(time.Second), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	if len(summary.Evidence) != 1 || summary.Evidence[0].State != domain.EvidenceDetailPartial ||
		!summary.Evidence[0].Truncated || !summary.Truncated {
		t.Fatalf("truncated Evidence state = %#v document truncated=%t", summary.Evidence, summary.Truncated)
	}
}
