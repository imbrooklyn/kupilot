package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	approvalcontract "github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestSessionRepositoryExportSnapshotIsAllowlistedAndExcludesRawSourceCanaries(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-export")
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	run := seedStandardRun(
		t, db,
		"00000000-0000-7000-8000-000000008001",
		"00000000-0000-7000-8000-000000008002",
		"00000000-0000-7000-8000-000000008003",
		startedAt,
	)

	credentialCanary := "Bearer repository-export-secret-canary-6a1b2c"
	if _, err := db.handle.ExecContext(context.Background(), `
		UPDATE messages
		SET content = ?, content_hash = ?
		WHERE id = ?
	`, "Inspect the safe projection with "+credentialCanary,
		domain.MessageContentHash("Inspect the safe projection with "+credentialCanary), run.RequestMessageID); err != nil {
		t.Fatalf("credential Message setup error = %v", err)
	}

	rawToolCanary := "raw-tool-result-canary-7b2c3d"
	rawLogCanary := "raw-container-log-canary-8c3d4e"
	invocation := testToolInvocation("00000000-0000-7000-8000-000000008004", run, 1, startedAt.Add(time.Millisecond))
	invocation.ResultSummary = &rawToolCanary
	invocation.Purpose = &rawLogCanary
	evidence := testEvidence("00000000-0000-7000-8000-000000008005", invocation, startedAt.Add(5*time.Millisecond))
	evidence.PolicyVersion = domain.ResourcePolicyVersion
	evidence.PolicyGeneration = 1
	invocation.EvidenceCount = 1
	if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(ToolInvocation) error = %v", err)
	}
	fullModelCanary := "full-model-payload-canary-9d4e5f"
	modelRequest := testModelRequest("00000000-0000-7000-8000-000000008006", run.ID, 1, startedAt.Add(3*time.Millisecond))
	modelRequest.Model = fullModelCanary
	if err := NewModelRequestRepository(db).Save(context.Background(), modelRequest); err != nil {
		t.Fatalf("Save(ModelRequest) error = %v", err)
	}
	approvalCanary := "approval-authority-canary-a5e6f7"
	approvalRequest := testApprovalRequest(
		t,
		"00000000-0000-7000-8000-000000008010",
		run,
		startedAt.Add(4*time.Millisecond),
		0x71,
	)
	approvalRequest.Intent.ReasonSummary = approvalCanary
	digest, err := approvalcontract.OperationDigest(approvalRequest)
	approvalRequest.Digest = digest
	if err != nil || approvalRequest.Validate() != nil {
		t.Fatalf("Approval canary request error/value = %v/%#v", err, approvalRequest)
	}
	approvalAudit := testApprovalAudit(
		t,
		approvalRequest,
		domain.AuditEventApprovalRequested,
		domain.AuditActorAgent,
		domain.AuditOutcomeSuccess,
		"requested",
		approvalRequest.RequestedAt,
	)
	if err := NewApprovalRepository(db).CreateWithAudit(context.Background(), approvalRequest, approvalAudit); err != nil {
		t.Fatalf("CreateWithAudit(Approval) error = %v", err)
	}

	diagnosis := testDiagnosis("00000000-0000-7000-8000-000000008007", run, []domain.Evidence{evidence})
	diagnosis.ValidationWarnings = []string{fullModelCanary}
	claim := "A current readiness problem was structurally cited."
	diagnosis.ClaimCoverage = []domain.ClaimEvidenceCoverage{{
		Sequence: 1, Kind: domain.ClaimCurrentObservation, Text: claim, TextHash: domain.SHA256Hex(claim),
		EvidenceIDs: []domain.EvidenceID{evidence.ID}, RunID: run.ID, Scope: run.Scope,
		PolicyGeneration: 1, State: domain.ClaimCoverageVerified,
	}}
	if err := NewDiagnosisRepository(db).Save(context.Background(), diagnosis); err != nil {
		t.Fatalf("Save(Diagnosis) error = %v", err)
	}
	steer := testMessage(
		"00000000-0000-7000-8000-000000008011",
		run.SessionID,
		&run.ID,
		"Include this committed steer in the safe export.",
		startedAt.Add(6*time.Millisecond),
	)
	steer.RunSequence = testIntPointer(1)
	steer.Scope = &run.Scope
	if err := NewAgentRunRepository(db).AppendRunInput(context.Background(), steer); err != nil {
		t.Fatalf("AppendRunInput(steer) error = %v", err)
	}
	unreferencedInvocation := testToolInvocation("00000000-0000-7000-8000-000000008009", run, 2, startedAt.Add(time.Millisecond))
	unreferenced := make([]domain.Evidence, application.MaxExportEvidence)
	for index := range unreferenced {
		unreferenced[index] = testEvidence(
			domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8002-%012d", index+1)),
			unreferencedInvocation,
			startedAt.Add(2*time.Millisecond),
		)
		unreferenced[index].Fact = fmt.Sprintf("Unreferenced Evidence source canary %03d.", index)
		unreferenced[index].Fingerprint = domain.SHA256Hex(unreferenced[index].Fact)
	}
	unreferencedInvocation.EvidenceCount = len(unreferenced)
	if err := NewToolInvocationRepository(db).Save(context.Background(), unreferencedInvocation, unreferenced); err != nil {
		t.Fatalf("Save(unreferenced ToolInvocation) error = %v", err)
	}
	assistant := testMessage(
		"00000000-0000-7000-8000-000000008008",
		run.SessionID,
		&run.ID,
		"A safe final assistant answer.",
		diagnosis.CreatedAt.Add(time.Millisecond),
	)
	assistant.Role = domain.MessageRoleAssistant
	assistant.RunSequence = testIntPointer(2)
	assistant.Format = domain.MessageFormatMarkdown
	assistant.Scope = &run.Scope
	terminal := testTerminalRun(run, domain.AgentRunStatusCompleted, assistant.CreatedAt)
	if err := NewAgentRunRepository(db).FinishWithMessage(context.Background(), assistant, terminal); err != nil {
		t.Fatalf("FinishWithMessage() error = %v", err)
	}
	contextPage, err := NewMessageRepository(db).ListEligibleModelContext(context.Background(), application.ModelContextPageRequest{
		SessionID: run.SessionID, Limit: 10,
	})
	if err != nil || len(contextPage.Messages) != 3 || contextPage.Messages[1].ID != steer.ID {
		t.Fatalf("ListEligibleModelContext() = %#v/%v", contextPage, err)
	}
	coverageDigest, coveredBytes, err := domain.SessionContextCoverageDigest(contextPage.Messages)
	if err != nil {
		t.Fatalf("SessionContextCoverageDigest() error = %v", err)
	}
	contextSummary := domain.SessionContextSummary{
		SessionID: run.SessionID, Text: "Historic safe context with " + credentialCanary,
		SummaryHash:    domain.SHA256Hex("Historic safe context with " + credentialCanary),
		SchemaVersion:  domain.SessionContextSummarySchemaVersion,
		PolicyVersion:  domain.SafeConversationContextPolicyVersion,
		CoveredFirstID: contextPage.Messages[0].ID, CoveredThroughID: contextPage.Messages[2].ID,
		CoveredCount: 3, CoveredBytes: coveredBytes, CoverageDigest: coverageDigest,
		GeneratedAt: startedAt.Add(30 * time.Minute), AgentProfile: "agent",
		AgentOriginHash: domain.SHA256Hex("https://model.example"),
	}
	if err := NewMessageRepository(db).SaveSessionContextSummary(context.Background(), contextSummary); err != nil {
		t.Fatalf("SaveSessionContextSummary() error = %v", err)
	}

	snapshot, err := NewSessionRepository(db).ReadExportSnapshot(context.Background(), run.SessionID)
	if err != nil {
		t.Fatalf("ReadExportSnapshot() error = %v", err)
	}
	if snapshot.Session.ID != run.SessionID || snapshot.Session.PrivacyMode != domain.PrivacyModeStandard ||
		len(snapshot.Messages) != 3 || snapshot.Messages[1].Role != domain.MessageRoleUser ||
		snapshot.Messages[1].Content != steer.Content || len(snapshot.Diagnoses) != 1 ||
		len(snapshot.Diagnoses[0].ClaimCoverage) != 1 || len(snapshot.Evidence) != 1 ||
		snapshot.ContextSummary == nil || snapshot.ContextSummary.CoverageDigest != coverageDigest {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Evidence[0].Resource.UID != "" || snapshot.Evidence[0].Resource.ResourceVersion != "" {
		t.Fatalf("Evidence exported concurrency metadata: %#v", snapshot.Evidence[0].Resource)
	}

	summary, err := application.ProjectExportSummary(snapshot, startedAt.Add(time.Hour), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary() error = %v", err)
	}
	content, err := application.RenderExportSummary(summary, security.NewRedactor())
	if err != nil {
		t.Fatalf("RenderExportSummary() error = %v", err)
	}
	if !bytes.Contains(content, []byte("REDACTED")) || bytes.Contains(content, []byte(credentialCanary)) {
		t.Fatalf("credential canary result:\n%s", content)
	}
	if !bytes.Contains(content, []byte("## Model context")) ||
		!bytes.Contains(content, []byte(coverageDigest)) ||
		!bytes.Contains(content, []byte("#### Claim coverage")) || !bytes.Contains(content, []byte(claim)) {
		t.Fatalf("model-context coverage missing:\n%s", content)
	}
	for _, canary := range []string{
		rawToolCanary,
		rawLogCanary,
		fullModelCanary,
		approvalCanary,
		string(approvalRequest.Digest),
	} {
		if bytes.Count(content, []byte(canary)) != 0 {
			t.Fatalf("prohibited canary %q reached export", canary)
		}
	}

	if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM evidence_items WHERE id = ?`, evidence.ID); err != nil {
		t.Fatalf("delete Evidence setup error = %v", err)
	}
	expiredSnapshot, err := NewSessionRepository(db).ReadExportSnapshot(context.Background(), run.SessionID)
	if err != nil {
		t.Fatalf("ReadExportSnapshot(expired) error = %v", err)
	}
	expiredSummary, err := application.ProjectExportSummary(expiredSnapshot, startedAt.Add(2*time.Hour), security.NewRedactor())
	if err != nil {
		t.Fatalf("ProjectExportSummary(expired) error = %v", err)
	}
	expiredContent, err := application.RenderExportSummary(expiredSummary, security.NewRedactor())
	if err != nil {
		t.Fatalf("RenderExportSummary(expired) error = %v", err)
	}
	if !strings.Contains(string(expiredContent), "State: expired") || strings.Contains(string(expiredContent), evidence.Fact) {
		t.Fatalf("expired Evidence output:\n%s", expiredContent)
	}
}

func TestSessionRepositoryExportSnapshotRejectsMinimalMissingAndCancelled(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-export-denials")
	repository := NewSessionRepository(db)
	createdAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	minimal := testSession("00000000-0000-7000-8000-000000008101", "", domain.PrivacyModeMinimal, createdAt)
	if err := repository.Create(context.Background(), minimal); err != nil {
		t.Fatalf("Create(minimal) error = %v", err)
	}
	if _, err := repository.ReadExportSnapshot(context.Background(), minimal.ID); !errors.Is(err, application.ErrSessionNotResumable) {
		t.Fatalf("minimal ReadExportSnapshot() error = %v, want ErrSessionNotResumable", err)
	}
	if _, err := repository.ReadExportSnapshot(context.Background(), "00000000-0000-7000-8000-000000008199"); !errors.Is(err, application.ErrSessionExportUnavailable) {
		t.Fatalf("missing ReadExportSnapshot() error = %v, want ErrSessionExportUnavailable", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.ReadExportSnapshot(cancelled, minimal.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ReadExportSnapshot() error = %v, want context.Canceled", err)
	}
}
