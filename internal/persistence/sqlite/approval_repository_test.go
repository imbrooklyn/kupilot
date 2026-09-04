package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
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
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman, DecidedAt: decidedAt,
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

func TestApprovalRepositoryPersistsOnlyValidatedActionReviewMetadata(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "action-review-metadata")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_050_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x51)
	if err := repository.CreateWithAudit(context.Background(), request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}
	record := application.ActionReviewRecord{
		ModelRequestID: "00000000-0000-7000-8000-000000009050", ApprovalID: request.ID,
		Profile: "approval_reviewer", OriginHash: strings.Repeat("b", 64),
		PolicyGeneration: request.Intent.PolicyGeneration, Disposition: application.ActionReviewDeny,
		RationaleSummary: "The exact bounded policy facts require denial.", OccurredAt: requestedAt.Add(time.Second),
	}
	if err := repository.AppendActionReview(context.Background(), record); err != nil {
		t.Fatalf("AppendActionReview() error = %v", err)
	}
	var stored struct {
		ApprovalID       string         `db:"approval_id"`
		ModelRequestID   string         `db:"model_request_id"`
		Profile          string         `db:"profile_name"`
		OriginHash       string         `db:"origin_hash"`
		PolicyGeneration int64          `db:"policy_generation"`
		Disposition      string         `db:"disposition"`
		Rationale        sql.NullString `db:"rationale_summary"`
		ErrorClass       sql.NullString `db:"error_class"`
		OccurredAtMS     int64          `db:"occurred_at_ms"`
	}
	if err := db.handle.GetContext(context.Background(), &stored, `
		SELECT approval_id, model_request_id, profile_name, origin_hash,
			policy_generation, disposition, rationale_summary, error_class, occurred_at_ms
		FROM action_reviews WHERE model_request_id = ?
	`, record.ModelRequestID); err != nil {
		t.Fatalf("action review query error = %v", err)
	}
	if stored.ApprovalID != string(record.ApprovalID) || stored.ModelRequestID != string(record.ModelRequestID) || stored.Profile != record.Profile ||
		stored.OriginHash != record.OriginHash || stored.PolicyGeneration != int64(record.PolicyGeneration) ||
		stored.Disposition != string(record.Disposition) || !stored.Rationale.Valid ||
		stored.Rationale.String != record.RationaleSummary || stored.ErrorClass.Valid ||
		stored.OccurredAtMS != record.OccurredAt.UnixMilli() {
		t.Fatalf("stored action review = %#v", stored)
	}

	invalid := record
	invalid.ModelRequestID = "00000000-0000-7000-8000-000000009051"
	invalid.RationaleSummary = "-----BEGIN PRIVATE KEY----- blocked -----END PRIVATE KEY-----"
	if err := repository.AppendActionReview(context.Background(), invalid); !errors.Is(err, application.ErrApprovalUnavailable) {
		t.Fatalf("AppendActionReview(sensitive) error = %v", err)
	}
	if err := repository.AppendActionReview(context.Background(), record); err == nil {
		t.Fatal("AppendActionReview(duplicate) error = nil")
	}
	var count int
	if err := db.handle.GetContext(context.Background(), &count, `SELECT count(model_request_id) FROM action_reviews`); err != nil || count != 1 {
		t.Fatalf("action review rows = %d/%v, want 1", count, err)
	}
}

func TestActionAuthoritySchemaChecksMirrorDomainShape(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "action-authority-checks")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_075_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x52)
	if err := repository.CreateWithAudit(context.Background(), request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}

	for _, mutation := range []struct {
		name  string
		query string
		value any
	}{
		{"deny risk", `UPDATE approvals SET risk = ? WHERE id = ?`, "deny"},
		{"lowered effect", `UPDATE approvals SET effect = ? WHERE id = ?`, "safe_read"},
		{"schema mismatch", `UPDATE approvals SET operation_schema_version = ? WHERE id = ?`, "other/v1"},
		{"oversized target", `UPDATE approvals SET target_name = ? WHERE id = ?`, strings.Repeat("n", 254)},
		{"unpaired target set", `UPDATE approvals SET target_set_digest = ? WHERE id = ?`, strings.Repeat("d", 64)},
		{"unknown data bit", `UPDATE approvals SET data_categories = ? WHERE id = ?`, 64},
		{"mixed none network bit", `UPDATE approvals SET network_effects = ? WHERE id = ?`, 3},
		{"tty without stdin", `UPDATE approvals SET tty = ? WHERE id = ?`, 1},
		{"shell enabled", `UPDATE approvals SET shell = ? WHERE id = ?`, 1},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := db.handle.ExecContext(context.Background(), mutation.query, mutation.value, request.ID); err == nil {
				t.Fatal("invalid direct authority mutation succeeded")
			}
		})
	}
	if stored, decision, err := repository.Get(context.Background(), request.ID); err != nil ||
		decision != nil || stored.State != domain.ApprovalStatePending {
		t.Fatalf("request after rejected schema mutations = %#v/%#v/%v", stored, decision, err)
	}

	decidedAt := requestedAt.Add(time.Second)
	rejected := request
	rejected.State = domain.ApprovalStateRejected
	rejected.StateReason = domain.ApprovalReasonUserRejected
	rejected.StateChangedAt = decidedAt
	decision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionReject,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman, DecidedAt: decidedAt,
	}
	if err := repository.ResolveWithAudit(context.Background(), request.State, rejected, decision,
		testApprovalAudit(t, rejected, domain.AuditEventApprovalRejected, domain.AuditActorUser, domain.AuditOutcomeDenied, "user_rejected", decidedAt)); err != nil {
		t.Fatalf("ResolveWithAudit() error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(),
		`UPDATE approval_decisions SET rationale_summary = 'not eligible' WHERE approval_id = ?`, request.ID); err == nil {
		t.Fatal("local-user decision accepted Reviewer-only rationale metadata")
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		UPDATE approval_decisions
		SET actor = 'permission_policy', permission_disposition = 'automatic'
		WHERE approval_id = ?
	`, request.ID); err == nil {
		t.Fatal("permission policy actor accepted a reject decision")
	}
	for _, review := range []struct {
		name             string
		modelRequestID   string
		policyGeneration int64
		errorClass       string
	}{
		{"unknown error class", "00000000-0000-7000-8000-000000009071", int64(request.Intent.PolicyGeneration), "future_error"},
		{"mismatched policy generation", "00000000-0000-7000-8000-000000009072", int64(request.Intent.PolicyGeneration) + 1, "internal"},
	} {
		t.Run(review.name, func(t *testing.T) {
			if _, err := db.handle.ExecContext(context.Background(), `
				INSERT INTO action_reviews (
					approval_id, model_request_id, profile_name, origin_hash,
					policy_generation, disposition, rationale_summary, error_class, occurred_at_ms
				) VALUES (?, ?, 'approval-reviewer', ?, ?, 'failed', NULL, ?, ?)
			`, request.ID, review.modelRequestID, strings.Repeat("b", 64), review.policyGeneration,
				review.errorClass, requestedAt.Add(time.Second).UnixMilli()); err == nil {
				t.Fatal("invalid direct action review insert succeeded")
			}
		})
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
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman, DecidedAt: approved.StateChangedAt,
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

func TestApprovalRepositoryVerifiesAndConsumesApprovedIntentWithAuditAtomically(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "approval-pre-write")
	repository := NewApprovalRepository(db)
	requestedAt := time.UnixMilli(1_700_000_125_000).UTC()
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt, 0x49)
	if err := repository.CreateWithAudit(context.Background(), request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}
	approved := request
	approved.State = domain.ApprovalStateApproved
	approved.StateReason = domain.ApprovalReasonUserApproved
	approved.StateChangedAt = requestedAt.Add(time.Second)
	decision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionApprove,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman, DecidedAt: approved.StateChangedAt,
	}
	if err := repository.ResolveWithAudit(context.Background(), request.State, approved, decision,
		testApprovalAudit(t, approved, domain.AuditEventApprovalApproved, domain.AuditActorUser, domain.AuditOutcomeSuccess, "user_approved", approved.StateChangedAt)); err != nil {
		t.Fatalf("ResolveWithAudit() error = %v", err)
	}
	storedApproved, err := approval.NewStoredRequest(approved)
	if err != nil {
		t.Fatalf("NewStoredRequest(approved) error = %v", err)
	}
	storedDecision, err := approval.NewStoredDecision(decision)
	if err != nil {
		t.Fatalf("NewStoredDecision() error = %v", err)
	}
	if err := repository.VerifyApproved(context.Background(), storedApproved, storedDecision); err != nil {
		t.Fatalf("VerifyApproved() error = %v", err)
	}

	mismatched := storedApproved
	mismatched.Intent.PolicyVersion = "changed-policy"
	if err := repository.VerifyApproved(context.Background(), mismatched, storedDecision); !errors.Is(err, approval.ErrInvalidStoredApproval) {
		t.Fatalf("VerifyApproved(policy mismatch) error = %v", err)
	}

	consumed := approved
	consumed.State = domain.ApprovalStateConsumed
	consumed.StateReason = domain.ApprovalReasonConsumed
	consumed.StateChangedAt = requestedAt.Add(2 * time.Second)
	storedConsumed, err := approval.NewStoredRequest(consumed)
	if err != nil {
		t.Fatalf("NewStoredRequest(consumed) error = %v", err)
	}
	writeIntent := testApprovalAudit(
		t, consumed, domain.AuditEventWriteIntent, domain.AuditActorSystem,
		domain.AuditOutcomeSuccess, "approval_consumed", consumed.StateChangedAt,
	)
	if _, err := db.handle.ExecContext(
		context.Background(),
		`UPDATE approval_decisions SET decided_at_ms = ? WHERE approval_id = ?`,
		storedDecision.DecidedAt.Add(time.Millisecond).UnixMilli(), request.ID,
	); err != nil {
		t.Fatalf("mutate stored decision error = %v", err)
	}
	if err := repository.ConsumeWithAudit(
		context.Background(), storedApproved, storedDecision, storedConsumed, writeIntent,
	); !errors.Is(err, approval.ErrStoredApprovalConflict) {
		t.Fatalf("ConsumeWithAudit(decision mismatch) error = %v", err)
	}
	if _, err := db.handle.ExecContext(
		context.Background(),
		`UPDATE approval_decisions SET decided_at_ms = ? WHERE approval_id = ?`,
		storedDecision.DecidedAt.UnixMilli(), request.ID,
	); err != nil {
		t.Fatalf("restore stored decision error = %v", err)
	}
	duplicateAudit := writeIntent
	duplicateAudit.ID = domain.AuditEventID(approvalTestUUID(9_200))
	if err := repository.ConsumeWithAudit(context.Background(), storedApproved, storedDecision, storedConsumed, duplicateAudit); err == nil {
		t.Fatal("ConsumeWithAudit(duplicate audit) error = nil")
	}
	stored, _, err := repository.Get(context.Background(), request.ID)
	if err != nil || stored.State != domain.ApprovalStateApproved {
		t.Fatalf("state after rolled-back consume = %#v/%v", stored, err)
	}
	var writeIntentCount int
	if err := db.handle.GetContext(context.Background(), &writeIntentCount,
		`SELECT count(id) FROM audit_events WHERE event_type = 'write_intent' AND correlation_id = ?`, request.ID); err != nil {
		t.Fatalf("write_intent count query error = %v", err)
	}
	if writeIntentCount != 0 {
		t.Fatalf("write_intent count after rollback = %d, want 0", writeIntentCount)
	}

	if err := repository.ConsumeWithAudit(context.Background(), storedApproved, storedDecision, storedConsumed, writeIntent); err != nil {
		t.Fatalf("ConsumeWithAudit() error = %v", err)
	}
	stored, _, err = repository.Get(context.Background(), request.ID)
	if err != nil || stored.State != domain.ApprovalStateConsumed || stored.StateReason != domain.ApprovalReasonConsumed {
		t.Fatalf("consumed stored request = %#v/%v", stored, err)
	}
	if err := repository.VerifyApproved(context.Background(), storedApproved, storedDecision); !errors.Is(err, approval.ErrStoredApprovalConflict) {
		t.Fatalf("VerifyApproved(consumed replay) error = %v", err)
	}
	if err := db.handle.GetContext(context.Background(), &writeIntentCount,
		`SELECT count(id) FROM audit_events WHERE event_type = 'write_intent' AND correlation_id = ?`, request.ID); err != nil {
		t.Fatalf("write_intent count query error = %v", err)
	}
	if writeIntentCount != 1 {
		t.Fatalf("write_intent count = %d, want 1", writeIntentCount)
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
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman, DecidedAt: rejected.StateChangedAt,
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
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman, DecidedAt: approvedAt,
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
		request := pending
		if recovered.ID == approved.ID {
			request = approvedNext
		}
		request.State = recovered.State
		request.StateReason = recovered.StateReason
		request.StateChangedAt = recovered.StateChangedAt
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
			Operation:              domain.ActionOperationRestartDeployment,
			OperationSchemaVersion: domain.ActionOperationRestartDeployment.SchemaVersion(),
			PolicyVersion:          domain.ActionPolicyVersion, PermissionProfile: domain.PermissionProfileAsk,
			Risk: domain.RiskReview, Effect: domain.CapabilityEffectClusterMutation, PolicyGeneration: 1,
			Scope: domain.ScopeSnapshot{
				Context: "test-context", Namespace: "test-namespace", Generation: run.Scope.Generation,
			},
			NamespaceAccess: domain.NamespaceAccessCurrent,
			Target: domain.ActionTarget{Resource: domain.ResourceRef{
				APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
				Namespace: "test-namespace", Name: "sample-deployment", UID: "deployment-uid",
				ResourceVersion: "17",
			}, Fingerprint: strings.Repeat("a", 64), Generation: 7},
			Parameters:         domain.ActionParameters{Kind: domain.ActionParametersNone},
			DataCategories:     domain.ActionDataResourceMetadata,
			AllowedSinks:       domain.ActionSinkTerminal | domain.ActionSinkKubernetesAPI,
			NetworkEffects:     domain.ActionNetworkKubernetesAPI,
			Limits:             domain.ActionLimits{Timeout: domain.RestartDeploymentActionTimeout, MaximumItems: 1},
			VerificationPlanID: domain.RestartDeploymentVerificationPlanID,
			ReasonSummary:      "Restart after the bounded diagnosis.", RiskSummary: domain.RestartDeploymentRiskSummary,
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
		domain.AuditEventWriteIntent:       9_700,
	}[eventType]
	if request.ID == testApprovalIDTwo {
		eventNumber++
	}
	operation := string(request.Intent.Operation)
	policy := request.Intent.PolicyVersion
	scope := request.Intent.Scope
	subject := domain.ResourceRef{
		APIVersion:      request.Intent.Target.Resource.APIVersion,
		Kind:            request.Intent.Target.Resource.Kind,
		Namespace:       request.Intent.Target.Resource.Namespace,
		Name:            request.Intent.Target.Resource.Name,
		UID:             request.Intent.Target.Resource.UID,
		ResourceVersion: request.Intent.Target.Resource.ResourceVersion,
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
