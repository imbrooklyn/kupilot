package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	testApprovalIDOne domain.ApprovalID = "00000000-0000-7000-8000-000000009001"
	testApprovalIDTwo domain.ApprovalID = "00000000-0000-7000-8000-000000009002"
)

func TestApprovalRepositoryPersistsPendingDecisionExpiryAndSafeQuery(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "approval-lifecycle")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_000_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x41)
	requestedAudit := testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)

	if err := repository.CreateWithAudit(context.Background(), request, requestedAudit); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}
	stored, decision, err := repository.Get(context.Background(), request.ID)
	if err != nil {
		t.Fatalf("Get(pending) error = %v", err)
	}
	if decision != nil || stored.State != domain.ApprovalStatePending || stored.NonceHash != request.Nonce.Hash() {
		t.Fatalf("pending stored request/decision = %#v/%#v", stored, decision)
	}
	if stored.Validate() != nil {
		t.Fatalf("stored pending request is invalid: %#v", stored)
	}

	decidedAt := requestedAt.Add(10 * time.Second)
	approved := request
	approved.State = domain.ApprovalStateApproved
	approved.StateReason = domain.ApprovalReasonUserApproved
	approved.StateChangedAt = decidedAt
	approvalDecision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionApprove,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, DecidedAt: decidedAt,
	}
	approvedAudit := testApprovalAudit(t, approved, domain.AuditEventApprovalApproved, domain.AuditActorUser, domain.AuditOutcomeSuccess, "user_approved", decidedAt)
	if err := repository.ResolveWithAudit(context.Background(), request.State, approved, approvalDecision, approvedAudit); err != nil {
		t.Fatalf("ResolveWithAudit(approve) error = %v", err)
	}
	stored, decision, err = repository.Get(context.Background(), request.ID)
	if err != nil {
		t.Fatalf("Get(approved) error = %v", err)
	}
	if stored.State != domain.ApprovalStateApproved || decision == nil || decision.Choice != domain.ApprovalDecisionApprove ||
		decision.NonceHash != request.Nonce.Hash() || decision.ShownDigest != request.Digest {
		t.Fatalf("approved stored request/decision = %#v/%#v", stored, decision)
	}
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE approval_decisions SET decided_at_ms = ? WHERE approval_id = ?`, request.ExpiresAt.UnixMilli(), request.ID); err != nil {
		t.Fatalf("decision time corruption setup error = %v", err)
	}
	if _, _, err := repository.Get(context.Background(), request.ID); !errors.Is(err, approval.ErrInvalidStoredApproval) {
		t.Fatalf("Get(approved with late decision) error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE approval_decisions SET decided_at_ms = ? WHERE approval_id = ?`, decidedAt.UnixMilli(), request.ID); err != nil {
		t.Fatalf("decision time restore error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `DELETE FROM approval_decisions WHERE approval_id = ?`, request.ID); err != nil {
		t.Fatalf("decision corruption setup error = %v", err)
	}
	if _, _, err := repository.Get(context.Background(), request.ID); !errors.Is(err, approval.ErrInvalidStoredApproval) {
		t.Fatalf("Get(approved without decision) error = %v", err)
	}

	second := testApprovalRequest(t, testApprovalIDTwo, run, requestedAt.Add(time.Millisecond), 0x42)
	if err := repository.CreateWithAudit(context.Background(), second,
		testApprovalAudit(t, second, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", second.RequestedAt)); err != nil {
		t.Fatalf("CreateWithAudit(second) error = %v", err)
	}
	expired := second
	expired.State = domain.ApprovalStateExpired
	expired.StateReason = domain.ApprovalReasonTTLExpired
	expired.StateChangedAt = second.ExpiresAt
	if err := repository.CloseWithAudit(context.Background(), second.State, expired,
		testApprovalAudit(t, expired, domain.AuditEventApprovalExpired, domain.AuditActorSystem, domain.AuditOutcomeDenied, "ttl_expired", expired.StateChangedAt)); err != nil {
		t.Fatalf("CloseWithAudit(expire) error = %v", err)
	}
	stored, decision, err = repository.Get(context.Background(), second.ID)
	if err != nil || decision != nil || stored.State != domain.ApprovalStateExpired {
		t.Fatalf("Get(expired) request/decision/error = %#v/%#v/%v", stored, decision, err)
	}

	var rawNonceMatches int
	if err := db.handle.GetContext(context.Background(), &rawNonceMatches, `
		SELECT count(id) FROM approvals
		WHERE nonce_hash = ? OR nonce_hash = ?
	`, strings.Repeat("A", domain.ApprovalNonceBytes), strings.Repeat("B", domain.ApprovalNonceBytes)); err != nil {
		t.Fatalf("raw nonce query error = %v", err)
	}
	if rawNonceMatches != 0 {
		t.Fatalf("raw nonce row count = %d, want 0", rawNonceMatches)
	}
}

func TestApprovalRepositoryTransactionsRollbackWhenAuditPersistenceFails(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "approval-audit-rollback")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_100_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x43)
	requestedAudit := testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)
	if err := repository.CreateWithAudit(context.Background(), request, requestedAudit); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}

	approved := request
	approved.State = domain.ApprovalStateApproved
	approved.StateReason = domain.ApprovalReasonUserApproved
	approved.StateChangedAt = requestedAt.Add(time.Second)
	decision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionApprove,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, DecidedAt: approved.StateChangedAt,
	}
	duplicateAudit := testApprovalAudit(t, approved, domain.AuditEventApprovalApproved, domain.AuditActorUser, domain.AuditOutcomeSuccess, "user_approved", approved.StateChangedAt)
	duplicateAudit.ID = requestedAudit.ID
	if err := repository.ResolveWithAudit(context.Background(), request.State, approved, decision, duplicateAudit); err == nil {
		t.Fatal("ResolveWithAudit(duplicate audit) error = nil")
	}
	stored, storedDecision, err := repository.Get(context.Background(), request.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.State != domain.ApprovalStatePending || storedDecision != nil {
		t.Fatalf("rolled-back request/decision = %#v/%#v", stored, storedDecision)
	}
}

func TestApprovalRepositoryPersistsRejectedDecision(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "approval-rejection")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_150_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x48)
	if err := repository.CreateWithAudit(context.Background(), request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}
	rejected := request
	rejected.State = domain.ApprovalStateRejected
	rejected.StateReason = domain.ApprovalReasonUserRejected
	rejected.StateChangedAt = requestedAt.Add(time.Second)
	decision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionReject,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, DecidedAt: rejected.StateChangedAt,
	}
	if err := repository.ResolveWithAudit(context.Background(), request.State, rejected, decision,
		testApprovalAudit(t, rejected, domain.AuditEventApprovalRejected, domain.AuditActorUser, domain.AuditOutcomeDenied, "user_rejected", rejected.StateChangedAt)); err != nil {
		t.Fatalf("ResolveWithAudit(reject) error = %v", err)
	}
	stored, storedDecision, err := repository.Get(context.Background(), request.ID)
	if err != nil || stored.State != domain.ApprovalStateRejected || storedDecision == nil ||
		storedDecision.Choice != domain.ApprovalDecisionReject {
		t.Fatalf("Get(rejected) request/decision/error = %#v/%#v/%v", stored, storedDecision, err)
	}
}

func TestApprovalRepositoryRecoveryInvalidatesEveryUnexecutedRequestAtomically(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "approval-recovery")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_200_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	pending := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x44)
	approved := testApprovalRequest(t, testApprovalIDTwo, run, requestedAt.Add(time.Millisecond), 0x45)
	for _, request := range []domain.ApprovalRequest{pending, approved} {
		if err := repository.CreateWithAudit(context.Background(), request,
			testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", request.RequestedAt)); err != nil {
			t.Fatalf("CreateWithAudit(%s) error = %v", request.ID, err)
		}
	}
	approvedAt := requestedAt.Add(2 * time.Second)
	approvedNext := approved
	approvedNext.State = domain.ApprovalStateApproved
	approvedNext.StateReason = domain.ApprovalReasonUserApproved
	approvedNext.StateChangedAt = approvedAt
	decision := domain.ApprovalDecision{
		RequestID: approved.ID, Choice: domain.ApprovalDecisionApprove,
		ShownDigest: approved.Digest, Nonce: approved.Nonce,
		Actor: domain.ApprovalActorLocalUser, DecidedAt: approvedAt,
	}
	if err := repository.ResolveWithAudit(context.Background(), approved.State, approvedNext, decision,
		testApprovalAudit(t, approvedNext, domain.AuditEventApprovalApproved, domain.AuditActorUser, domain.AuditOutcomeSuccess, "user_approved", approvedAt)); err != nil {
		t.Fatalf("ResolveWithAudit(approved) error = %v", err)
	}

	recoverable, err := repository.ListRecoverable(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecoverable() error = %v", err)
	}
	if got := approvalIDs(recoverable); !reflect.DeepEqual(got, []domain.ApprovalID{pending.ID, approved.ID}) {
		t.Fatalf("recoverable IDs = %v", got)
	}
	recoveredAt := requestedAt.Add(3 * time.Second)
	transitions := make([]approval.RecoveryTransition, 0, len(recoverable))
	for index, stored := range recoverable {
		recovered, recoverErr := approval.RecoverAfterRestart(stored, recoveredAt)
		if recoverErr != nil {
			t.Fatalf("RecoverAfterRestart(%s) error = %v", stored.ID, recoverErr)
		}
		request := recovered.RequestWithoutNonce()
		audit := testApprovalAudit(t, request, domain.AuditEventApprovalCancelled, domain.AuditActorSystem, domain.AuditOutcomeDenied, "process_restarted", recoveredAt)
		audit.ID = domain.AuditEventID(approvalTestUUID(9_100 + index))
		transitions = append(transitions, approval.RecoveryTransition{Before: stored, After: recovered, Audit: audit})
	}
	transitions[1].Audit.ID = transitions[0].Audit.ID
	if err := repository.RecoverWithAudits(context.Background(), transitions); err == nil {
		t.Fatal("RecoverWithAudits(duplicate audit) error = nil")
	}
	for _, id := range []domain.ApprovalID{pending.ID, approved.ID} {
		stored, _, getErr := repository.Get(context.Background(), id)
		if getErr != nil || stored.State != map[domain.ApprovalID]domain.ApprovalState{
			pending.ID: domain.ApprovalStatePending, approved.ID: domain.ApprovalStateApproved,
		}[id] {
			t.Fatalf("state after rolled-back recovery for %s = %s/%v", id, stored.State, getErr)
		}
	}
	transitions[1].Audit.ID = domain.AuditEventID(approvalTestUUID(9_102))
	if err := repository.RecoverWithAudits(context.Background(), transitions); err != nil {
		t.Fatalf("RecoverWithAudits() error = %v", err)
	}
	for _, id := range []domain.ApprovalID{pending.ID, approved.ID} {
		stored, _, getErr := repository.Get(context.Background(), id)
		if getErr != nil || stored.State != domain.ApprovalStateCancelled || stored.StateReason != domain.ApprovalReasonProcessRestarted {
			t.Fatalf("recovered state for %s = %#v/%v", id, stored, getErr)
		}
	}
	recoverable, err = repository.ListRecoverable(context.Background(), 10)
	if err != nil || len(recoverable) != 0 {
		t.Fatalf("recoverable after recovery = %#v/%v", recoverable, err)
	}
}

func TestApprovalRepositoryRejectsCancellationAndInvalidStateWithoutChangingRows(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "approval-cancellation")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_300_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x46)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := repository.CreateWithAudit(cancelled, request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateWithAudit(cancelled) error = %v, want context.Canceled", err)
	}
	if _, _, err := repository.Get(context.Background(), request.ID); !errors.Is(err, approval.ErrStoredApprovalNotFound) {
		t.Fatalf("Get(after cancelled create) error = %v", err)
	}
	wrongActorAudit := testApprovalAudit(
		t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt,
	)
	wrongActorAudit.Actor = domain.AuditActorSystem
	if err := repository.CreateWithAudit(context.Background(), request, wrongActorAudit); !errors.Is(err, approval.ErrInvalidStoredApproval) {
		t.Fatalf("CreateWithAudit(wrong actor) error = %v", err)
	}
	if _, _, err := repository.Get(context.Background(), request.ID); !errors.Is(err, approval.ErrStoredApprovalNotFound) {
		t.Fatalf("Get(after wrong audit) error = %v", err)
	}
	otherSessionID := domain.SessionID("00000000-0000-7000-8000-000000009199")
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO sessions (
			id, title, status, privacy_mode, version, created_at_ms, updated_at_ms
		) VALUES (?, 'Other Session', 'active', 'standard', 1, ?, ?)
	`, otherSessionID, requestedAt.UnixMilli(), requestedAt.UnixMilli()); err != nil {
		t.Fatalf("other Session insert error = %v", err)
	}
	mismatchedSession := request
	mismatchedSession.SessionID = otherSessionID
	mismatchedSession.Digest, err = approval.OperationDigest(mismatchedSession)
	if err != nil {
		t.Fatalf("OperationDigest(mismatched Session) error = %v", err)
	}
	if err := repository.CreateWithAudit(context.Background(), mismatchedSession,
		testApprovalAudit(t, mismatchedSession, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); !errors.Is(err, approval.ErrStoredApprovalConflict) {
		t.Fatalf("CreateWithAudit(mismatched Session) error = %v", err)
	}
	if _, _, err := repository.Get(context.Background(), request.ID); !errors.Is(err, approval.ErrStoredApprovalNotFound) {
		t.Fatalf("Get(after mismatched Session) error = %v", err)
	}

	invalid := request
	invalid.Intent.ReasonSummary = strings.Repeat("x", domain.MaxApprovalReasonSummaryBytes+1)
	if err := repository.CreateWithAudit(context.Background(), invalid,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); !errors.Is(err, approval.ErrInvalidStoredApproval) {
		t.Fatalf("CreateWithAudit(invalid) error = %v", err)
	}
}

func seedApprovalRun(t *testing.T, db *DB, at time.Time) domain.AgentRun {
	t.Helper()
	return seedStandardRun(
		t, db,
		domain.SessionID("00000000-0000-7000-8000-000000009100"),
		domain.MessageID("00000000-0000-7000-8000-000000009101"),
		domain.AgentRunID("00000000-0000-7000-8000-000000009102"),
		at,
	)
}

func testApprovalRequest(
	t *testing.T,
	id domain.ApprovalID,
	run domain.AgentRun,
	requestedAt time.Time,
	nonceByte byte,
) domain.ApprovalRequest {
	t.Helper()
	nonce, err := domain.NewApprovalNonce(bytes.Repeat([]byte{nonceByte}, domain.ApprovalNonceBytes))
	if err != nil {
		t.Fatalf("NewApprovalNonce() error = %v", err)
	}
	request := domain.ApprovalRequest{
		ID: id, RunID: run.ID, SessionID: run.SessionID,
		Intent: domain.OperationIntent{
			Operation: domain.ApprovalOperationRestartDeployment,
			Scope: domain.ScopeSnapshot{
				Context: "test-context", Namespace: "test-namespace", Generation: run.Scope.Generation,
			},
			DeploymentName: "sample-deployment", DeploymentUID: "deployment-uid",
			TemplateFingerprint: strings.Repeat("a", 64), DeploymentGeneration: 7,
			PolicyVersion: domain.RestartDeploymentApprovalPolicyVersion,
			ReasonSummary: "Restart after the bounded diagnosis.",
		},
		Nonce: nonce, State: domain.ApprovalStatePending,
		RequestedAt: requestedAt, ExpiresAt: requestedAt.Add(domain.ApprovalExecutionTTL),
		StateChangedAt: requestedAt,
	}
	request.Digest, err = approval.OperationDigest(request)
	if err != nil || request.Validate() != nil {
		t.Fatalf("test ApprovalRequest error/request = %v/%#v", err, request)
	}
	return request
}

func testApprovalAudit(
	t *testing.T,
	request domain.ApprovalRequest,
	eventType domain.AuditEventType,
	actor domain.AuditActor,
	outcome domain.AuditOutcome,
	detail string,
	at time.Time,
) domain.AuditEvent {
	t.Helper()
	eventNumber := map[domain.AuditEventType]int{
		domain.AuditEventApprovalRequested: 9_200,
		domain.AuditEventApprovalApproved:  9_300,
		domain.AuditEventApprovalRejected:  9_400,
		domain.AuditEventApprovalExpired:   9_500,
		domain.AuditEventApprovalCancelled: 9_600,
	}[eventType]
	if request.ID == testApprovalIDTwo {
		eventNumber++
	}
	operation := string(request.Intent.Operation)
	policy := request.Intent.PolicyVersion
	scope := request.Intent.Scope
	subject := domain.ResourceRef{
		APIVersion: domain.RestartDeploymentTargetAPIVersion,
		Kind:       domain.RestartDeploymentTargetKind,
		Namespace:  request.Intent.Scope.Namespace,
		Name:       request.Intent.DeploymentName,
		UID:        request.Intent.DeploymentUID,
	}
	event := domain.AuditEvent{
		ID:        domain.AuditEventID(approvalTestUUID(eventNumber)),
		SessionID: &request.SessionID, RunID: &request.RunID,
		Type: eventType, Actor: actor, Outcome: outcome,
		Scope: &scope, Subject: &subject,
		Details: domain.AuditDetails{
			Operation: &operation, DetailCode: &detail, PolicyVersion: &policy,
		},
		CorrelationID: string(request.ID), IntegrityHash: string(request.Digest), OccurredAt: at,
	}
	if event.Validate() != nil {
		t.Fatalf("test AuditEvent is invalid: %#v", event)
	}
	return event
}

func approvalIDs(values []approval.StoredRequest) []domain.ApprovalID {
	result := make([]domain.ApprovalID, len(values))
	for index := range values {
		result[index] = values[index].ID
	}
	return result
}

func approvalTestUUID(value int) string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", value)
}
