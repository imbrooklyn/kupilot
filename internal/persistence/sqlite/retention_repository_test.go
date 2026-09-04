package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRetentionRepositoryUsesInclusiveCategoryCutoffsAndBoundedBatches(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-cutoffs")
	now := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	detailCutoff := now.Add(-30 * 24 * time.Hour)
	readCutoff := now.Add(-90 * 24 * time.Hour)
	writeCutoff := now.Add(-180 * 24 * time.Hour)
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000006001",
		"00000000-0000-7000-8000-000000006002",
		"00000000-0000-7000-8000-000000006003",
		detailCutoff.Add(-time.Hour),
	)

	toolRepository := NewToolInvocationRepository(db)
	modelRepository := NewModelRequestRepository(db)
	evidence := make([]domain.Evidence, 0, 3)
	for index, observedAt := range []time.Time{detailCutoff.Add(-time.Millisecond), detailCutoff, detailCutoff.Add(time.Millisecond)} {
		invocationID := domain.ToolInvocationID([]string{
			"00000000-0000-7000-8000-000000006011",
			"00000000-0000-7000-8000-000000006012",
			"00000000-0000-7000-8000-000000006013",
		}[index])
		evidenceID := domain.EvidenceID([]string{
			"00000000-0000-7000-8000-000000006021",
			"00000000-0000-7000-8000-000000006022",
			"00000000-0000-7000-8000-000000006023",
		}[index])
		invocation := testToolInvocation(invocationID, run, index+1, observedAt.Add(-5*time.Millisecond))
		item := testEvidence(evidenceID, invocation, observedAt)
		invocation.EvidenceCount = 1
		if err := toolRepository.Save(context.Background(), invocation, []domain.Evidence{item}); err != nil {
			t.Fatalf("Save(ToolInvocation %d) error = %v", index, err)
		}
		evidence = append(evidence, item)

		request := testModelRequest(domain.ModelRequestID([]string{
			"00000000-0000-7000-8000-000000006031",
			"00000000-0000-7000-8000-000000006032",
			"00000000-0000-7000-8000-000000006033",
		}[index]), run.ID, index+1, observedAt.Add(-time.Millisecond))
		if err := modelRepository.Save(context.Background(), request); err != nil {
			t.Fatalf("Save(ModelRequest %d) error = %v", index, err)
		}
	}

	diagnosis := testDiagnosis("00000000-0000-7000-8000-000000006041", run, evidence)
	if err := NewDiagnosisRepository(db).Save(context.Background(), diagnosis); err != nil {
		t.Fatalf("Save(Diagnosis) error = %v", err)
	}

	auditRepository := NewAuditRepository(db)
	for index, occurredAt := range []time.Time{readCutoff.Add(-time.Millisecond), readCutoff, readCutoff.Add(time.Millisecond)} {
		event := testAuditEvent(domain.AuditEventID([]string{
			"00000000-0000-7000-8000-000000006051",
			"00000000-0000-7000-8000-000000006052",
			"00000000-0000-7000-8000-000000006053",
		}[index]), run, domain.AuditEventRunCompleted, occurredAt)
		if err := auditRepository.Append(context.Background(), event); err != nil {
			t.Fatalf("Append(read AuditEvent %d) error = %v", index, err)
		}
	}
	for index, occurredAt := range []time.Time{readCutoff.Add(-time.Millisecond), readCutoff, readCutoff.Add(time.Millisecond)} {
		event := testSessionAuditEvent(domain.AuditEventID([]string{
			"00000000-0000-7000-8000-000000006071",
			"00000000-0000-7000-8000-000000006072",
			"00000000-0000-7000-8000-000000006073",
		}[index]), run.SessionID, occurredAt)
		event.Type = domain.AuditEventSessionExportRequested
		event.Actor = domain.AuditActorUser
		if err := auditRepository.Append(context.Background(), event); err != nil {
			t.Fatalf("Append(export AuditEvent %d) error = %v", index, err)
		}
	}
	for index, occurredAt := range []time.Time{writeCutoff.Add(-time.Millisecond), writeCutoff, writeCutoff.Add(time.Millisecond)} {
		event := testAuditEvent(domain.AuditEventID([]string{
			"00000000-0000-7000-8000-000000006061",
			"00000000-0000-7000-8000-000000006062",
			"00000000-0000-7000-8000-000000006063",
		}[index]), run, domain.AuditEventWriteVerified, occurredAt)
		if err := auditRepository.Append(context.Background(), event); err != nil {
			t.Fatalf("Append(write AuditEvent %d) error = %v", index, err)
		}
	}

	repository := NewRetentionRepository(db)
	request := auditcontract.CleanupRequest{Now: now, OperationalDetailRetentionDays: 30, BatchSize: 1}
	var total auditcontract.CleanupResult
	for calls := 0; ; calls++ {
		if calls > 10 {
			t.Fatal("Cleanup did not reach an empty bounded batch")
		}
		result, err := repository.Cleanup(context.Background(), request)
		if err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
		assertCleanupCountsAtMost(t, result, 1)
		total.EvidenceItems += result.EvidenceItems
		total.ToolInvocations += result.ToolInvocations
		total.ModelRequests += result.ModelRequests
		total.ReadAuditEvents += result.ReadAuditEvents
		total.WriteAuditEvents += result.WriteAuditEvents
		total.ApprovalRecords += result.ApprovalRecords
		if !result.More {
			break
		}
	}
	wantTotal := auditcontract.CleanupResult{
		EvidenceItems:    2,
		ToolInvocations:  2,
		ModelRequests:    2,
		ReadAuditEvents:  4,
		WriteAuditEvents: 2,
	}
	if !reflect.DeepEqual(total, wantTotal) {
		t.Fatalf("cleanup totals = %#v, want %#v", total, wantTotal)
	}

	partial, err := NewDiagnosisRepository(db).GetByRunID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByRunID(partial) error = %v", err)
	}
	if partial.EvidenceDetailsState != domain.EvidenceDetailPartial {
		t.Fatalf("Diagnosis Evidence state = %q, want partial", partial.EvidenceDetailsState)
	}
	if !reflect.DeepEqual(partial.ConfirmedFacts, diagnosis.ConfirmedFacts) {
		t.Fatal("retention rewrote historic confirmed facts")
	}
	if _, err := NewEvidenceRepository(db).GetByID(context.Background(), evidence[2].ID); err != nil {
		t.Fatalf("fresh Evidence was removed: %v", err)
	}

	later := request
	later.Now = now.Add(200 * 24 * time.Hour)
	later.BatchSize = auditcontract.MaxCleanupBatchSize
	result, err := repository.Cleanup(context.Background(), later)
	if err != nil {
		t.Fatalf("Cleanup(later) error = %v", err)
	}
	if result.EvidenceItems != 1 || result.ToolInvocations != 1 || result.ModelRequests != 1 || result.ReadAuditEvents != 2 || result.WriteAuditEvents != 1 {
		t.Fatalf("later cleanup result = %#v", result)
	}
	expired, err := NewDiagnosisRepository(db).GetByRunID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByRunID(expired) error = %v", err)
	}
	if expired.EvidenceDetailsState != domain.EvidenceDetailExpired {
		t.Fatalf("Diagnosis Evidence state = %q, want expired", expired.EvidenceDetailsState)
	}
	if !reflect.DeepEqual(expired.ConfirmedFacts, diagnosis.ConfirmedFacts) {
		t.Fatal("expired Evidence caused fabricated or rewritten Diagnosis detail")
	}
}

func TestRetentionRepositoryRemovesOnlyEligibleMinimalSessionShells(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-minimal")
	now := time.Date(2026, time.February, 1, 12, 0, 0, 0, time.UTC)
	readCutoff := now.Add(-90 * 24 * time.Hour)
	sessions := NewSessionRepository(db)
	audits := NewAuditRepository(db)

	emptyID := domain.SessionID("00000000-0000-7000-8000-000000006101")
	expiredID := domain.SessionID("00000000-0000-7000-8000-000000006102")
	freshID := domain.SessionID("00000000-0000-7000-8000-000000006103")
	standardID := domain.SessionID("00000000-0000-7000-8000-000000006104")
	runningID := domain.SessionID("00000000-0000-7000-8000-000000006105")
	for _, value := range []domain.Session{
		testSession(string(emptyID), "", domain.PrivacyModeMinimal, readCutoff),
		testSession(string(expiredID), "", domain.PrivacyModeMinimal, readCutoff),
		testSession(string(freshID), "", domain.PrivacyModeMinimal, readCutoff),
		testSession(string(runningID), "", domain.PrivacyModeMinimal, readCutoff),
		testSession(string(standardID), "Standard Session", domain.PrivacyModeStandard, readCutoff),
	} {
		if err := sessions.Create(context.Background(), value); err != nil {
			t.Fatalf("Create(%s) error = %v", value.ID, err)
		}
	}
	expiredEvent := testSessionAuditEvent("00000000-0000-7000-8000-000000006111", expiredID, readCutoff)
	freshEvent := testSessionAuditEvent("00000000-0000-7000-8000-000000006112", freshID, readCutoff.Add(time.Millisecond))
	for _, event := range []domain.AuditEvent{expiredEvent, freshEvent} {
		if err := audits.Append(context.Background(), event); err != nil {
			t.Fatalf("Append(%s) error = %v", event.ID, err)
		}
	}
	runningMessageID := domain.MessageID("00000000-0000-7000-8000-000000006113")
	runningRunID := domain.AgentRunID("00000000-0000-7000-8000-000000006114")
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO agent_runs (
			id, session_id, request_message_id, status, scope_context,
			scope_namespace, scope_generation, prompt_version,
			tool_catalog_version, started_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, runningRunID, runningID, runningMessageID, "running", "test-context", "test-namespace", 1, "prompt-v1", "tools-v1", readCutoff.UnixMilli()); err != nil {
		t.Fatalf("minimal recovery AgentRun setup error = %v", err)
	}

	result, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now:                            now,
		OperationalDetailRetentionDays: 30,
		BatchSize:                      auditcontract.MaxCleanupBatchSize,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.ReadAuditEvents != 1 || result.MinimalSessions != 2 {
		t.Fatalf("Cleanup() = %#v, want one AuditEvent and two minimal shells", result)
	}
	for _, removed := range []domain.SessionID{emptyID, expiredID} {
		if _, err := sessions.GetByID(context.Background(), removed); err == nil {
			t.Fatalf("minimal Session %s was not removed", removed)
		}
	}
	for _, retained := range []domain.SessionID{freshID, runningID, standardID} {
		if _, err := sessions.GetByID(context.Background(), retained); err != nil {
			t.Fatalf("Session %s was unexpectedly removed: %v", retained, err)
		}
	}
}

func TestRetentionRepositoryRemovesExpiredTerminalApprovalsButPreservesAuthority(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-approvals")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-181 * 24 * time.Hour)
	sessions := NewSessionRepository(db)
	runs := NewAgentRunRepository(db)
	approvals := NewApprovalRepository(db)

	type approvalFixture struct {
		sessionID  domain.SessionID
		messageID  domain.MessageID
		runID      domain.AgentRunID
		approvalID domain.ApprovalID
		state      domain.ApprovalState
		nonceByte  byte
	}
	fixtures := []approvalFixture{
		{
			sessionID: "00000000-0000-7000-8000-000000006121", messageID: "00000000-0000-7000-8000-000000006122",
			runID: "00000000-0000-7000-8000-000000006123", approvalID: "00000000-0000-7000-8000-000000006124",
			state: domain.ApprovalStateRejected, nonceByte: 0x61,
		},
		{
			sessionID: "00000000-0000-7000-8000-000000006131", messageID: "00000000-0000-7000-8000-000000006132",
			runID: "00000000-0000-7000-8000-000000006133", approvalID: "00000000-0000-7000-8000-000000006134",
			state: domain.ApprovalStatePending, nonceByte: 0x62,
		},
		{
			sessionID: "00000000-0000-7000-8000-000000006141", messageID: "00000000-0000-7000-8000-000000006142",
			runID: "00000000-0000-7000-8000-000000006143", approvalID: "00000000-0000-7000-8000-000000006144",
			state: domain.ApprovalStateApproved, nonceByte: 0x63,
		},
	}

	for index, fixture := range fixtures {
		sessionValue := testSession(string(fixture.sessionID), "", domain.PrivacyModeMinimal, requestedAt)
		if err := sessions.Create(context.Background(), sessionValue); err != nil {
			t.Fatalf("Create(Session %d) error = %v", index, err)
		}
		message, running := testRunningPair(fixture.messageID, fixture.runID, fixture.sessionID, requestedAt)
		if err := runs.Begin(context.Background(), message, running); err != nil {
			t.Fatalf("Begin(AgentRun %d) error = %v", index, err)
		}
		if err := runs.Finish(context.Background(), testTerminalRun(running, domain.AgentRunStatusCompleted, requestedAt.Add(time.Millisecond))); err != nil {
			t.Fatalf("Finish(AgentRun %d) error = %v", index, err)
		}

		request := testApprovalRequest(t, fixture.approvalID, running, requestedAt.Add(time.Duration(index)*time.Millisecond), fixture.nonceByte)
		requestedAudit := testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", request.RequestedAt)
		requestedAudit.ID = domain.AuditEventID([]string{
			"00000000-0000-7000-8000-000000006125",
			"00000000-0000-7000-8000-000000006135",
			"00000000-0000-7000-8000-000000006145",
		}[index])
		if err := approvals.CreateWithAudit(context.Background(), request, requestedAudit); err != nil {
			t.Fatalf("CreateWithAudit(Approval %d) error = %v", index, err)
		}
		if fixture.state == domain.ApprovalStatePending {
			continue
		}

		decided := request
		decided.State = fixture.state
		decided.StateChangedAt = request.RequestedAt.Add(time.Second)
		choice := domain.ApprovalDecisionReject
		eventType := domain.AuditEventApprovalRejected
		outcome := domain.AuditOutcomeDenied
		detail := "user_rejected"
		if fixture.state == domain.ApprovalStateApproved {
			decided.StateReason = domain.ApprovalReasonUserApproved
			choice = domain.ApprovalDecisionApprove
			eventType = domain.AuditEventApprovalApproved
			outcome = domain.AuditOutcomeSuccess
			detail = "user_approved"
		} else {
			decided.StateReason = domain.ApprovalReasonUserRejected
		}
		decision := domain.ApprovalDecision{
			RequestID: request.ID, Choice: choice, ShownDigest: request.Digest,
			Nonce: request.Nonce, Actor: domain.ApprovalActorLocalUser,
			Disposition: domain.ReviewDispositionHuman, DecidedAt: decided.StateChangedAt,
		}
		decisionAudit := testApprovalAudit(t, decided, eventType, domain.AuditActorUser, outcome, detail, decided.StateChangedAt)
		decisionAudit.ID = domain.AuditEventID([]string{
			"00000000-0000-7000-8000-000000006126",
			"00000000-0000-7000-8000-000000006136",
			"00000000-0000-7000-8000-000000006146",
		}[index])
		if err := approvals.ResolveWithAudit(context.Background(), request.State, decided, decision, decisionAudit); err != nil {
			t.Fatalf("ResolveWithAudit(Approval %d) error = %v", index, err)
		}
	}

	result, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now: now, OperationalDetailRetentionDays: 30, BatchSize: auditcontract.MaxCleanupBatchSize,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.WriteAuditEvents != 5 || result.ApprovalRecords != 1 || result.MinimalSessions != 1 {
		t.Fatalf("Cleanup() = %#v, want five audits, one terminal approval, and one shell", result)
	}

	var approvalCount, decisionCount int
	if err := db.handle.GetContext(context.Background(), &approvalCount, `SELECT count(id) FROM approvals`); err != nil {
		t.Fatalf("approval count error = %v", err)
	}
	if err := db.handle.GetContext(context.Background(), &decisionCount, `SELECT count(approval_id) FROM approval_decisions`); err != nil {
		t.Fatalf("approval decision count error = %v", err)
	}
	if approvalCount != 2 || decisionCount != 1 {
		t.Fatalf("retained approval/decision counts = %d/%d, want 2/1", approvalCount, decisionCount)
	}
	if _, err := sessions.GetByID(context.Background(), fixtures[0].sessionID); err == nil {
		t.Fatal("minimal Session with expired terminal approval was retained")
	}
	for _, retained := range fixtures[1:] {
		if _, err := sessions.GetByID(context.Background(), retained.sessionID); err != nil {
			t.Fatalf("Session with %s approval was removed: %v", retained.state, err)
		}
	}
}

func TestRetentionRepositoryRemovesExpiredLegacyApprovalArchive(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-legacy-approvals")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-181 * 24 * time.Hour)
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000006171",
		"00000000-0000-7000-8000-000000006172",
		"00000000-0000-7000-8000-000000006173",
		requestedAt,
	)
	approvalID := "00000000-0000-7000-8000-000000006174"
	digest := strings.Repeat("a", 64)
	nonceHash := strings.Repeat("b", 64)
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO legacy_restart_approvals (
			id, run_id, session_id, operation, operation_schema_version,
			policy_version, scope_context, scope_namespace, scope_generation,
			target_api_version, target_kind, target_namespace, deployment_name,
			deployment_uid, template_fingerprint, deployment_generation,
			reason_summary, risk_summary, operation_digest, nonce_hash,
			status, state_reason, requested_at_ms, expires_at_ms,
			state_changed_at_ms
		) VALUES (
			?, ?, ?, 'restart_deployment', 'restart_deployment/v1',
			'restart-deployment-approval/v1', 'test-context', 'test-namespace', 1,
			'apps/v1', 'Deployment', 'test-namespace', 'sample-deployment',
			'deployment-uid', ?, 1, ?, ?, ?, ?,
			'rejected', 'user_rejected', ?, ?, ?
		)
	`, approvalID, run.ID, run.SessionID, strings.Repeat("c", 64), "Safe legacy reason.",
		domain.RestartDeploymentRiskSummary, digest, nonceHash, requestedAt.UnixMilli(),
		requestedAt.Add(time.Minute).UnixMilli(), requestedAt.Add(time.Second).UnixMilli()); err != nil {
		t.Fatalf("legacy approval insert error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO legacy_restart_approval_decisions (
			approval_id, shown_digest, nonce_hash, decision, actor, decided_at_ms
		) VALUES (?, ?, ?, 'reject', 'local_user', ?)
	`, approvalID, digest, nonceHash, requestedAt.Add(time.Second).UnixMilli()); err != nil {
		t.Fatalf("legacy approval decision insert error = %v", err)
	}

	result, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now: now, OperationalDetailRetentionDays: 30, BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.ApprovalRecords != 1 || !result.More {
		t.Fatalf("Cleanup() = %#v, want one bounded legacy approval deletion", result)
	}
	var requests, decisions int
	if err := db.handle.GetContext(context.Background(), &requests, `SELECT count(id) FROM legacy_restart_approvals`); err != nil {
		t.Fatalf("legacy approval count error = %v", err)
	}
	if err := db.handle.GetContext(context.Background(), &decisions, `SELECT count(approval_id) FROM legacy_restart_approval_decisions`); err != nil {
		t.Fatalf("legacy approval decision count error = %v", err)
	}
	if requests != 0 || decisions != 0 {
		t.Fatalf("legacy approval/decision counts = %d/%d, want 0/0", requests, decisions)
	}
}

func TestRetentionRepositoryPreservesMinimalSessionWithRetainedLegacyApproval(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-recent-legacy-approval")
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-179 * 24 * time.Hour)
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000006181")
	messageID := domain.MessageID("00000000-0000-7000-8000-000000006182")
	runID := domain.AgentRunID("00000000-0000-7000-8000-000000006183")
	if err := NewSessionRepository(db).Create(
		context.Background(), testSession(string(sessionID), "", domain.PrivacyModeMinimal, requestedAt),
	); err != nil {
		t.Fatalf("Create(Session) error = %v", err)
	}
	message, running := testRunningPair(messageID, runID, sessionID, requestedAt)
	runs := NewAgentRunRepository(db)
	if err := runs.Begin(context.Background(), message, running); err != nil {
		t.Fatalf("Begin(AgentRun) error = %v", err)
	}
	if err := runs.Finish(context.Background(), testTerminalRun(running, domain.AgentRunStatusCompleted, requestedAt.Add(time.Millisecond))); err != nil {
		t.Fatalf("Finish(AgentRun) error = %v", err)
	}

	approvalID := "00000000-0000-7000-8000-000000006184"
	digest := strings.Repeat("d", 64)
	nonceHash := strings.Repeat("e", 64)
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO legacy_restart_approvals (
			id, run_id, session_id, operation, operation_schema_version,
			policy_version, scope_context, scope_namespace, scope_generation,
			target_api_version, target_kind, target_namespace, deployment_name,
			deployment_uid, template_fingerprint, deployment_generation,
			reason_summary, risk_summary, operation_digest, nonce_hash,
			status, state_reason, requested_at_ms, expires_at_ms,
			state_changed_at_ms
		) VALUES (
			?, ?, ?, 'restart_deployment', 'restart_deployment/v1',
			'restart-deployment-approval/v1', 'test-context', 'test-namespace', 1,
			'apps/v1', 'Deployment', 'test-namespace', 'sample-deployment',
			'deployment-uid', ?, 1, ?, ?, ?, ?,
			'rejected', 'user_rejected', ?, ?, ?
		)
	`, approvalID, runID, sessionID, strings.Repeat("f", 64), "Safe recent legacy reason.",
		domain.RestartDeploymentRiskSummary, digest, nonceHash, requestedAt.UnixMilli(),
		requestedAt.Add(time.Minute).UnixMilli(), requestedAt.Add(time.Second).UnixMilli()); err != nil {
		t.Fatalf("legacy approval insert error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO legacy_restart_approval_decisions (
			approval_id, shown_digest, nonce_hash, decision, actor, decided_at_ms
		) VALUES (?, ?, ?, 'reject', 'local_user', ?)
	`, approvalID, digest, nonceHash, requestedAt.Add(time.Second).UnixMilli()); err != nil {
		t.Fatalf("legacy approval decision insert error = %v", err)
	}

	result, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now: now, OperationalDetailRetentionDays: 30, BatchSize: auditcontract.MaxCleanupBatchSize,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.ApprovalRecords != 0 || result.MinimalSessions != 0 {
		t.Fatalf("Cleanup() = %#v, want retained legacy approval and minimal Session", result)
	}
	if _, err := NewSessionRepository(db).GetByID(context.Background(), sessionID); err != nil {
		t.Fatalf("minimal Session with retained legacy approval was removed: %v", err)
	}
}

func TestRetentionRepositoryHonorsZeroAndLongerOperationalDetailPolicy(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-configured-detail")
	now := time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-40 * 24 * time.Hour)
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000006151",
		"00000000-0000-7000-8000-000000006152",
		"00000000-0000-7000-8000-000000006153",
		observedAt.Add(-time.Hour),
	)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000006154", run, 1, observedAt.Add(-5*time.Millisecond))
	evidence := testEvidence("00000000-0000-7000-8000-000000006155", invocation, observedAt)
	invocation.EvidenceCount = 1
	if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(ToolInvocation) error = %v", err)
	}
	model := testModelRequest("00000000-0000-7000-8000-000000006156", run.ID, 1, observedAt.Add(-time.Millisecond))
	if err := NewModelRequestRepository(db).Save(context.Background(), model); err != nil {
		t.Fatalf("Save(ModelRequest) error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO tool_invocations (
			id, run_id, sequence, tool_name, tool_version, arguments_json,
			arguments_digest, status, started_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "00000000-0000-7000-8000-000000006157", run.ID, 2, "get_resource", "tool-v1", `{}`, domain.SHA256Hex(`{}`), "running", observedAt.UnixMilli()); err != nil {
		t.Fatalf("insert incomplete ToolInvocation error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO model_requests (
			id, run_id, sequence, provider_kind, model, status,
			prompt_version, prompt_fingerprint, started_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "00000000-0000-7000-8000-000000006158", run.ID, 2, "openai_compatible", "test-model", "running", "prompt-v1", domain.SHA256Hex("incomplete-request"), observedAt.UnixMilli()); err != nil {
		t.Fatalf("insert incomplete ModelRequest error = %v", err)
	}
	repository := NewRetentionRepository(db)
	retained, err := repository.Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now:                            now,
		OperationalDetailRetentionDays: 60,
		BatchSize:                      10,
	})
	if err != nil {
		t.Fatalf("Cleanup(longer policy) error = %v", err)
	}
	if retained.EvidenceItems != 0 || retained.ToolInvocations != 0 || retained.ModelRequests != 0 {
		t.Fatalf("longer policy removed operational detail: %#v", retained)
	}
	removed, err := repository.Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now:                            now,
		OperationalDetailRetentionDays: 0,
		BatchSize:                      10,
	})
	if err != nil {
		t.Fatalf("Cleanup(zero policy) error = %v", err)
	}
	if removed.EvidenceItems != 1 || removed.ToolInvocations != 2 || removed.ModelRequests != 2 {
		t.Fatalf("zero policy cleanup = %#v", removed)
	}
}

func TestRetentionRepositoryRollsBackFailuresAndHonorsCancellation(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-rollback")
	now := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-30 * 24 * time.Hour)
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000006201",
		"00000000-0000-7000-8000-000000006202",
		"00000000-0000-7000-8000-000000006203",
		cutoff.Add(-time.Hour),
	)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000006204", run, 1, cutoff.Add(-5*time.Millisecond))
	evidence := testEvidence("00000000-0000-7000-8000-000000006205", invocation, cutoff)
	invocation.EvidenceCount = 1
	if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}
	repository := NewRetentionRepository(db)
	request := auditcontract.CleanupRequest{Now: now, OperationalDetailRetentionDays: 30, BatchSize: 10}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.Cleanup(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Cleanup(cancelled) error = %v, want context.Canceled", err)
	}
	assertRetentionRows(t, db, 1, 1)
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelDeadline()
	_, err := repository.Cleanup(expired, request)
	assertStorageError(t, err, ClassTimeout, "storage_timeout")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Cleanup(expired deadline) error = %v, want context.DeadlineExceeded", err)
	}
	assertRetentionRows(t, db, 1, 1)

	triggerCanary := "synthetic-retention-rollback-canary"
	if _, err := db.handle.ExecContext(context.Background(), `
		CREATE TRIGGER prevent_expired_tool_delete
		BEFORE DELETE ON tool_invocations
		BEGIN
			SELECT RAISE(ABORT, 'synthetic-retention-rollback-canary');
		END
	`); err != nil {
		t.Fatalf("rollback trigger setup error = %v", err)
	}
	_, err = repository.Cleanup(context.Background(), request)
	assertStorageError(t, err, ClassPersistenceUnavailable, "retention_cleanup_failed")
	if strings.Contains(err.Error(), triggerCanary) {
		t.Fatal("retention error disclosed driver text")
	}
	assertRetentionRows(t, db, 1, 1)

	invalid := request
	invalid.BatchSize = 0
	if _, err := repository.Cleanup(context.Background(), invalid); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("Cleanup(invalid) error = %v, want ErrInvalidRepositoryRequest", err)
	}
}

func TestSessionRepositoryDeletionCascadesEverySessionOwnedPersistenceCategory(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "retention-user-delete")
	sessionID := seedCompleteSessionGraph(t, db, "00000000-0000-7000-8000-000000006301")
	if err := NewSessionRepository(db).Delete(context.Background(), sessionID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertSessionGraphRowCount(t, db, 0)
}

func testSessionAuditEvent(id domain.AuditEventID, sessionID domain.SessionID, occurredAt time.Time) domain.AuditEvent {
	return domain.AuditEvent{
		ID:         id,
		SessionID:  &sessionID,
		Type:       domain.AuditEventRunCompleted,
		Actor:      domain.AuditActorSystem,
		Outcome:    domain.AuditOutcomeSuccess,
		Details:    domain.AuditDetails{},
		OccurredAt: occurredAt,
	}
}

func assertCleanupCountsAtMost(t *testing.T, result auditcontract.CleanupResult, limit int64) {
	t.Helper()
	for name, count := range map[string]int64{
		"EvidenceItems":    result.EvidenceItems,
		"ToolInvocations":  result.ToolInvocations,
		"ModelRequests":    result.ModelRequests,
		"ReadAuditEvents":  result.ReadAuditEvents,
		"WriteAuditEvents": result.WriteAuditEvents,
		"ApprovalRecords":  result.ApprovalRecords,
		"MinimalSessions":  result.MinimalSessions,
	} {
		if count > limit {
			t.Errorf("%s deleted %d rows, limit %d", name, count, limit)
		}
	}
}

func assertRetentionRows(t *testing.T, db *DB, wantEvidence, wantTools int) {
	t.Helper()
	var evidenceCount int
	if err := db.handle.GetContext(context.Background(), &evidenceCount, `SELECT count(id) FROM evidence_items`); err != nil {
		t.Fatalf("Evidence count error = %v", err)
	}
	var toolCount int
	if err := db.handle.GetContext(context.Background(), &toolCount, `SELECT count(id) FROM tool_invocations`); err != nil {
		t.Fatalf("ToolInvocation count error = %v", err)
	}
	if evidenceCount != wantEvidence || toolCount != wantTools {
		t.Fatalf("retention rows = Evidence %d, ToolInvocation %d; want %d, %d", evidenceCount, toolCount, wantEvidence, wantTools)
	}
}
