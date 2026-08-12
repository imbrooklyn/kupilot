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
