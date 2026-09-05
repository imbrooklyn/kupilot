package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestAuditRepositoryRoundTripsTypedEventsWithStablePaging(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "audit-round-trip")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000005001", "00000000-0000-7000-8000-000000005002", "00000000-0000-7000-8000-000000005003", time.UnixMilli(400).UTC())
	repository := NewAuditRepository(db)
	events := []domain.AuditEvent{
		testAuditEvent("00000000-0000-7000-8000-000000005004", run, domain.AuditEventRunStarted, time.UnixMilli(401).UTC()),
		testAuditEvent("00000000-0000-7000-8000-000000005005", run, domain.AuditEventToolDenied, time.UnixMilli(402).UTC()),
		testAuditEvent("00000000-0000-7000-8000-000000005006", run, domain.AuditEventRunCompleted, time.UnixMilli(403).UTC()),
	}
	events[1].SessionID = nil
	for _, event := range events {
		if err := repository.Append(context.Background(), event); err != nil {
			t.Fatalf("Append(%s) error = %v", event.ID, err)
		}
	}
	got, err := repository.GetByID(context.Background(), events[1].ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(got, events[1]) {
		t.Fatalf("GetByID() = %#v, want %#v", got, events[1])
	}

	first, err := repository.ListBySession(context.Background(), auditcontract.PageRequest{SessionID: run.SessionID, Limit: 2})
	if err != nil {
		t.Fatalf("ListBySession(first) error = %v", err)
	}
	if gotIDs := auditEventIDs(first.Events); !reflect.DeepEqual(gotIDs, []domain.AuditEventID{events[2].ID, events[1].ID}) {
		t.Fatalf("first page IDs = %v", gotIDs)
	}
	if first.Next == nil {
		t.Fatal("first page Next = nil")
	}
	second, err := repository.ListBySession(context.Background(), auditcontract.PageRequest{SessionID: run.SessionID, Limit: 2, Before: first.Next})
	if err != nil {
		t.Fatalf("ListBySession(second) error = %v", err)
	}
	if gotIDs := auditEventIDs(second.Events); !reflect.DeepEqual(gotIDs, []domain.AuditEventID{events[0].ID}) {
		t.Fatalf("second page IDs = %v", gotIDs)
	}
	if second.Next != nil {
		t.Fatalf("second page Next = %#v", second.Next)
	}
	if _, err := repository.GetByID(context.Background(), "00000000-0000-7000-8000-000000005099"); !errors.Is(err, auditcontract.ErrAuditEventNotFound) {
		t.Fatalf("GetByID(missing) error = %v", err)
	}
}

func TestAuditRepositoryRoundTripsDigestBoundCrossNamespaceDrainPhase(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "audit-cross-namespace-drain")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000005021", "00000000-0000-7000-8000-000000005022", "00000000-0000-7000-8000-000000005023", time.UnixMilli(404).UTC())
	requestedAt := time.UnixMilli(1_700_000_030_000).UTC()
	request := consumedDrainApproval(t, db, run, requestedAt)
	repository := NewAuditRepository(db)
	operation := string(domain.ActionOperationDrainNode)
	event := testAuditEvent(
		"00000000-0000-7000-8000-000000005024", run,
		domain.AuditEventWriteAttempted, requestedAt.Add(3*time.Second),
	)
	event.Outcome = domain.AuditOutcomeSuccess
	event.Subject = &domain.ResourceRef{
		APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod",
		UID: "pod-uid", ResourceVersion: "9",
	}
	event.Details = domain.AuditDetails{Operation: &operation}
	event.CorrelationID = string(request.ID)
	event.IntegrityHash = string(request.Digest)

	if err := repository.AppendWriteResult(context.Background(), event); err != nil {
		t.Fatalf("AppendWriteResult() error = %v", err)
	}
	got, err := repository.GetByID(context.Background(), event.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(got, event) {
		t.Fatalf("GetByID() = %#v, want %#v", got, event)
	}

	for index, test := range []struct {
		name   string
		mutate func(*domain.AuditEvent)
	}{
		{name: "approval", mutate: func(value *domain.AuditEvent) {
			value.CorrelationID = "00000000-0000-7000-8000-000000005027"
		}},
		{name: "digest", mutate: func(value *domain.AuditEvent) {
			value.IntegrityHash = strings.Repeat("c", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			unbound := event
			unbound.ID = domain.AuditEventID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 5_026+index))
			test.mutate(&unbound)
			if err := repository.AppendWriteResult(context.Background(), unbound); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
				t.Fatalf("AppendWriteResult(unbound cross-Namespace phase) error = %v", err)
			}
		})
	}
}

func consumedDrainApproval(t *testing.T, db *DB, run domain.AgentRun, requestedAt time.Time) domain.ApprovalRequest {
	t.Helper()
	scope := domain.ClusterScope{
		Context: run.Scope.Context, Namespace: run.Scope.Namespace, NamespaceAccess: domain.NamespaceAccessAll,
		Generation: run.Scope.Generation, ActivatedAt: time.UnixMilli(404).UTC(),
	}
	podSet, err := domain.NewRemediationTargetSet([]domain.RemediationPlanMember{{
		Role: domain.RemediationMemberDrainPod,
		Resource: domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod",
			UID: "pod-uid", ResourceVersion: "9",
		},
		Fingerprint: domain.ActionDigest(strings.Repeat("b", 64)), Order: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.RemediationActionPlan{
		RunID: run.ID, SessionID: run.SessionID, Scope: scope, PolicyGeneration: 1,
		Operation: domain.ActionOperationDrainNode,
		Target: domain.ActionTarget{
			Resource: domain.ResourceRef{
				APIVersion: "v1", Kind: "Node", Name: "worker-a", UID: "node-uid", ResourceVersion: "8",
			},
			TargetSetDigest: podSet.Digest(), TargetCount: 1,
		},
		Parameters: domain.ActionParameters{
			Kind: domain.ActionParametersDrainPlan, PlanDigest: podSet.Digest(), PlanTargetCount: 1,
			GracePeriodSeconds: domain.RemediationGracePeriodSeconds,
		},
		TargetSet: podSet, ReasonSummary: "Drain the exact approved Pod set.",
		Limits:     domain.ActionLimits{Timeout: domain.TypedRemediationActionTimeout, MaximumItems: 2},
		PreparedAt: requestedAt,
	}
	intent, err := plan.Intent(domain.PermissionProfileAsk)
	if err != nil {
		t.Fatal(err)
	}
	request := pendingRequestForIntent(t, "00000000-0000-7000-8000-000000005025", run, intent, requestedAt, 0x25)
	repository := NewApprovalRepository(db)
	if err := repository.CreateWithAudit(context.Background(), request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested", requestedAt)); err != nil {
		t.Fatal(err)
	}
	approved := request
	approved.State = domain.ApprovalStateApproved
	approved.StateReason = domain.ApprovalReasonUserApproved
	approved.StateChangedAt = requestedAt.Add(time.Second)
	decision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionApprove,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman,
		DecidedAt: approved.StateChangedAt,
	}
	if err := repository.ResolveWithAudit(context.Background(), request.State, approved, decision,
		testApprovalAudit(t, approved, domain.AuditEventApprovalApproved, domain.AuditActorUser, domain.AuditOutcomeSuccess, "user_approved", approved.StateChangedAt)); err != nil {
		t.Fatal(err)
	}
	stored, storedDecision, err := repository.Get(context.Background(), request.ID)
	if err != nil || storedDecision == nil {
		t.Fatalf("Get(approved drain) = %#v/%#v/%v", stored, storedDecision, err)
	}
	consumed := approved
	consumed.State = domain.ApprovalStateConsumed
	consumed.StateReason = domain.ApprovalReasonConsumed
	consumed.StateChangedAt = requestedAt.Add(2 * time.Second)
	storedConsumed := stored
	storedConsumed.State = consumed.State
	storedConsumed.StateReason = consumed.StateReason
	storedConsumed.StateChangedAt = consumed.StateChangedAt
	if err := repository.ConsumeWithAudit(context.Background(), stored, *storedDecision, storedConsumed,
		testApprovalAudit(t, consumed, domain.AuditEventWriteIntent, domain.AuditActorSystem, domain.AuditOutcomeSuccess, "approval_consumed", consumed.StateChangedAt)); err != nil {
		t.Fatal(err)
	}
	return consumed
}

func TestAuditRepositoryAppendWriteResultIsIdempotentAndTypeRestricted(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "write-result-audit")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000005051", "00000000-0000-7000-8000-000000005052", "00000000-0000-7000-8000-000000005053", time.UnixMilli(405).UTC())
	repository := NewAuditRepository(db)
	event := testAuditEvent(
		"00000000-0000-7000-8000-000000005054", run,
		domain.AuditEventWriteAttempted, time.UnixMilli(406).UTC(),
	)
	if err := repository.AppendWriteResult(context.Background(), event); err != nil {
		t.Fatalf("AppendWriteResult(first) error = %v", err)
	}
	if err := repository.AppendWriteResult(context.Background(), event); err != nil {
		t.Fatalf("AppendWriteResult(idempotent retry) error = %v", err)
	}
	var count int
	if err := db.handle.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM audit_events WHERE id = ?`, event.ID); err != nil {
		t.Fatalf("count write result audit error = %v", err)
	}
	if count != 1 {
		t.Fatalf("write result audit rows = %d, want 1", count)
	}
	mismatched := event
	mismatched.Outcome = domain.AuditOutcomeFailure
	if err := repository.AppendWriteResult(context.Background(), mismatched); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("AppendWriteResult(mismatched duplicate) error = %v", err)
	}
	nonWrite := event
	nonWrite.ID = "00000000-0000-7000-8000-000000005055"
	nonWrite.Type = domain.AuditEventRunCompleted
	if err := repository.AppendWriteResult(context.Background(), nonWrite); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("AppendWriteResult(non-write type) error = %v", err)
	}

	batch := make([]domain.AuditEvent, 5)
	for index := range batch {
		batch[index] = testAuditEvent(
			domain.AuditEventID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 5_060+index)),
			run, domain.AuditEventWriteVerified, time.UnixMilli(407).UTC(),
		)
	}
	if err := repository.AppendWriteResults(context.Background(), batch); err != nil {
		t.Fatalf("AppendWriteResults(first) error = %v", err)
	}
	if err := repository.AppendWriteResults(context.Background(), batch); err != nil {
		t.Fatalf("AppendWriteResults(idempotent) error = %v", err)
	}
	if err := db.handle.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM audit_events WHERE occurred_at_ms = ?`, time.UnixMilli(407).UTC().UnixMilli()); err != nil || count != 5 {
		t.Fatalf("batch count/error = %d/%v", count, err)
	}

	conflict := testAuditEvent("00000000-0000-7000-8000-000000005071", run, domain.AuditEventWriteVerified, time.UnixMilli(408).UTC())
	if err := repository.AppendWriteResult(context.Background(), conflict); err != nil {
		t.Fatalf("AppendWriteResult(conflict setup) error = %v", err)
	}
	rollback := []domain.AuditEvent{
		testAuditEvent("00000000-0000-7000-8000-000000005070", run, domain.AuditEventWriteVerified, time.UnixMilli(408).UTC()),
		conflict,
	}
	rollback[1].Outcome = domain.AuditOutcomeSuccess
	if err := repository.AppendWriteResults(context.Background(), rollback); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("AppendWriteResults(mismatched duplicate) error = %v", err)
	}
	if err := db.handle.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM audit_events WHERE id = ?`, rollback[0].ID); err != nil || count != 0 {
		t.Fatalf("partial batch row count/error = %d/%v", count, err)
	}
}

func TestAuditRepositoryEnforcesMinimalAllowlistAndRelationships(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "audit-minimal")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000005101", "00000000-0000-7000-8000-000000005102", "00000000-0000-7000-8000-000000005103", time.UnixMilli(410).UTC())
	repository := NewAuditRepository(db)
	if _, err := db.handle.ExecContext(context.Background(), `
		UPDATE sessions
		SET title = '', privacy_mode = 'minimal', last_context = NULL,
			last_namespace = NULL, selected_resource_json = NULL, summary = NULL
		WHERE id = ?
	`, run.SessionID); err != nil {
		t.Fatalf("minimal Session setup error = %v", err)
	}

	allowed := testAuditEvent("00000000-0000-7000-8000-000000005104", run, domain.AuditEventRunStarted, time.UnixMilli(411).UTC())
	if err := repository.Append(context.Background(), allowed); err != nil {
		t.Fatalf("Append(minimal lifecycle) error = %v", err)
	}
	denied := testAuditEvent("00000000-0000-7000-8000-000000005105", run, domain.AuditEventToolCompleted, time.UnixMilli(412).UTC())
	if err := repository.Append(context.Background(), denied); !errors.Is(err, auditcontract.ErrAuditEventNotEligible) {
		t.Fatalf("Append(minimal Tool detail) error = %v, want ErrAuditEventNotEligible", err)
	}
	if _, err := repository.GetByID(context.Background(), denied.ID); !errors.Is(err, auditcontract.ErrAuditEventNotFound) {
		t.Fatalf("ineligible AuditEvent was persisted: %v", err)
	}
	corruptID := domain.AuditEventID("00000000-0000-7000-8000-000000005108")
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO audit_events (
			id, session_id, run_id, event_type, actor, outcome, details_json, occurred_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, corruptID, run.SessionID, run.ID, "tool_completed", "system", "success", `{}`, 412); err != nil {
		t.Fatalf("ineligible AuditEvent setup error = %v", err)
	}
	_, err := repository.GetByID(context.Background(), corruptID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "audit_event_row_invalid")

	otherSessionID := domain.SessionID("00000000-0000-7000-8000-000000005106")
	other := testSession(string(otherSessionID), "Other Session", domain.PrivacyModeStandard, time.UnixMilli(409).UTC())
	if err := NewSessionRepository(db).Create(context.Background(), other); err != nil {
		t.Fatalf("Create(other Session) error = %v", err)
	}
	mismatched := allowed
	mismatched.ID = "00000000-0000-7000-8000-000000005107"
	mismatched.SessionID = &otherSessionID
	if err := repository.Append(context.Background(), mismatched); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("Append(mismatched relationship) error = %v, want ErrInvalidRepositoryRequest", err)
	}
}

func TestAuditRepositoryRejectsCorruptRowsConstraintDisclosureAndCancellation(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "audit-strict")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000005201", "00000000-0000-7000-8000-000000005202", "00000000-0000-7000-8000-000000005203", time.UnixMilli(420).UTC())
	repository := NewAuditRepository(db)
	event := testAuditEvent("00000000-0000-7000-8000-000000005204", run, domain.AuditEventToolDenied, time.UnixMilli(421).UTC())
	if err := repository.Append(context.Background(), event); err != nil {
		t.Fatalf("Append(setup) error = %v", err)
	}

	rowCanary := "audit-unknown-detail-canary"
	corruptDetails := `{"operation":"execute_tool","` + rowCanary + `":true}`
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE audit_events SET details_json = ? WHERE id = ?`, corruptDetails, event.ID); err != nil {
		t.Fatalf("corrupt AuditEvent setup error = %v", err)
	}
	_, err := repository.GetByID(context.Background(), event.ID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "audit_event_row_invalid")
	if strings.Contains(err.Error(), rowCanary) {
		t.Fatal("AuditEvent row error disclosed stored details")
	}

	boundCanary := "audit-bound-correlation-canary"
	duplicate := event
	duplicate.CorrelationID = boundCanary
	err = repository.Append(context.Background(), duplicate)
	assertStorageError(t, err, ClassPersistenceUnavailable, "audit_event_append_failed")
	if strings.Contains(err.Error(), boundCanary) {
		t.Fatal("AuditEvent constraint error disclosed a bound value")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	other := event
	other.ID = "00000000-0000-7000-8000-000000005205"
	err = repository.Append(cancelled, other)
	assertStorageError(t, err, ClassCancelled, "storage_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := repository.ListBySession(cancelled, auditcontract.PageRequest{SessionID: run.SessionID, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListBySession(cancelled) error = %v, want context.Canceled", err)
	}
	invalidCursor := &auditcontract.Cursor{OccurredAt: time.UnixMilli(421).UTC().Add(time.Nanosecond), ID: event.ID}
	if _, err := repository.ListBySession(context.Background(), auditcontract.PageRequest{SessionID: run.SessionID, Limit: 1, Before: invalidCursor}); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("ListBySession(non-millisecond cursor) error = %v, want ErrInvalidRepositoryRequest", err)
	}
}

func testAuditEvent(id domain.AuditEventID, run domain.AgentRun, eventType domain.AuditEventType, occurredAt time.Time) domain.AuditEvent {
	operation := "execute_tool"
	errorClass := domain.SafeErrorClassPermissionDenied
	toolName := domain.ToolNameGetEvents
	sequence := 1
	detailCode := "read_forbidden"
	sessionID := run.SessionID
	runID := run.ID
	return domain.AuditEvent{
		ID:        id,
		SessionID: &sessionID,
		RunID:     &runID,
		Type:      eventType,
		Actor:     domain.AuditActorSystem,
		Outcome:   domain.AuditOutcomeDenied,
		Scope:     &run.Scope,
		Subject:   &domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: run.Scope.Namespace, Name: "sample-pod"},
		Details: domain.AuditDetails{
			Operation:  &operation,
			ErrorClass: &errorClass,
			ToolName:   &toolName,
			Sequence:   &sequence,
			DetailCode: &detailCode,
		},
		CorrelationID: "command-1",
		IntegrityHash: domain.SHA256Hex("audit-integrity"),
		OccurredAt:    occurredAt,
	}
}

func auditEventIDs(events []domain.AuditEvent) []domain.AuditEventID {
	result := make([]domain.AuditEventID, 0, len(events))
	for _, event := range events {
		result = append(result, event.ID)
	}
	return result
}
