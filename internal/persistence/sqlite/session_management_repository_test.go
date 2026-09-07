package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
	contract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestSessionManagementRepositoryOrdersLastActiveAndCommitsExactGraph(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ctx, testStateDir(t), "session-management-exact")
	repository := NewSessionRepository(db)
	base := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	old := testSession("00000000-0000-7000-8000-000000000071", "Old", domain.PrivacyModeStandard, base.Add(-72*time.Hour))
	newest := testSession("00000000-0000-7000-8000-000000000072", "New", domain.PrivacyModeStandard, base.Add(-time.Hour))
	messages := []string{"00000000-0000-7000-8000-000000000079", "00000000-0000-7000-8000-000000000080"}
	for index, session := range []domain.Session{old, newest} {
		if err := repository.Create(ctx, session); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		seedCommittedMessage(t, db, session.ID, messages[index], session.LastActivityAt)
	}
	page, err := repository.ListSessionMetadata(ctx, application.SessionListStoreRequest{Limit: 3})
	if err != nil || len(page.Sessions) != 2 || page.Sessions[0].ID != newest.ID || page.Sessions[1].ID != old.ID {
		t.Fatalf("ListSessionMetadata() = %#v, %v", page, err)
	}
	request := application.SessionDeletionSelectionRequest{
		Kind: application.SessionDeletionExact, SessionID: old.ID, FrozenNow: base, Limit: 1, CurrentSessionID: old.ID,
	}
	snapshot, err := repository.PreviewSessionDeletion(ctx, request)
	if err != nil || len(snapshot.Selected) != 1 || snapshot.Selected[0].ID != old.ID {
		t.Fatalf("PreviewSessionDeletion() = %#v, %v", snapshot, err)
	}
	remaining, err := repository.CommitSessionDeletion(ctx, snapshot)
	if err != nil || remaining != 1 {
		t.Fatalf("CommitSessionDeletion() = %d, %v", remaining, err)
	}
	if _, err := repository.GetByID(ctx, old.ID); !errors.Is(err, contract.ErrSessionNotFound) {
		t.Fatalf("deleted GetByID() error = %v", err)
	}
	var messageCount int
	if err := db.handle.GetContext(ctx, &messageCount, `SELECT count(id) FROM messages WHERE session_id = ?`, old.ID); err != nil || messageCount != 0 {
		t.Fatalf("deleted message count/error = %d/%v", messageCount, err)
	}
}

func TestSessionManagementBatchUsesStrictCutoffProtectsCurrentAndRejectsStale(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ctx, testStateDir(t), "session-management-batch")
	repository := NewSessionRepository(db)
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)
	old := testSession("00000000-0000-7000-8000-000000000081", "Old", domain.PrivacyModeStandard, cutoff.Add(-time.Hour))
	equal := testSession("00000000-0000-7000-8000-000000000082", "Equal", domain.PrivacyModeStandard, cutoff)
	current := testSession("00000000-0000-7000-8000-000000000083", "Current", domain.PrivacyModeStandard, cutoff.Add(-2*time.Hour))
	for _, session := range []domain.Session{old, equal, current} {
		if err := repository.Create(ctx, session); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	request := application.SessionDeletionSelectionRequest{
		Kind: application.SessionDeletionBefore, Cutoff: cutoff, FrozenNow: now, Limit: 2, CurrentSessionID: current.ID,
	}
	snapshot, err := repository.PreviewSessionDeletion(ctx, request)
	if err != nil || snapshot.Matched != 2 || snapshot.Eligible != 1 || snapshot.Protected != 1 ||
		len(snapshot.Selected) != 1 || snapshot.Selected[0].ID != old.ID {
		t.Fatalf("PreviewSessionDeletion() = %#v, %v", snapshot, err)
	}
	if _, err := db.handle.ExecContext(ctx, `UPDATE sessions SET last_activity_at_ms = last_activity_at_ms + 1, updated_at_ms = updated_at_ms + 1 WHERE id = ?`, old.ID); err != nil {
		t.Fatalf("stale setup error = %v", err)
	}
	if _, err := repository.CommitSessionDeletion(ctx, snapshot); !errors.Is(err, application.ErrSessionDeletionStale) {
		t.Fatalf("stale CommitSessionDeletion() error = %v", err)
	}
	for _, id := range []domain.SessionID{old.ID, equal.ID, current.ID} {
		if _, err := repository.GetByID(ctx, id); err != nil {
			t.Fatalf("Session %s changed after stale commit: %v", id, err)
		}
	}
}

func TestSessionManagementBatchProtectsConsumedApprovalUntilTerminalOutcome(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ctx, testStateDir(t), "session-management-consumed-approval")
	sessions := NewSessionRepository(db)
	approvals := NewApprovalRepository(db)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-72 * time.Hour)
	run := seedApprovalRun(t, db, requestedAt)
	request := testApprovalRequest(t, testApprovalIDOne, run, requestedAt.Add(time.Second), 0x52)
	if err := approvals.CreateWithAudit(ctx, request,
		testApprovalAudit(t, request, domain.AuditEventApprovalRequested, domain.AuditActorAgent,
			domain.AuditOutcomeSuccess, "requested", request.RequestedAt)); err != nil {
		t.Fatalf("CreateWithAudit() error = %v", err)
	}
	approved := request
	approved.State = domain.ApprovalStateApproved
	approved.StateReason = domain.ApprovalReasonUserApproved
	approved.StateChangedAt = requestedAt.Add(2 * time.Second)
	decision := domain.ApprovalDecision{
		RequestID: request.ID, Choice: domain.ApprovalDecisionApprove,
		ShownDigest: request.Digest, Nonce: request.Nonce,
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman,
		DecidedAt: approved.StateChangedAt,
	}
	if err := approvals.ResolveWithAudit(ctx, request.State, approved, decision,
		testApprovalAudit(t, approved, domain.AuditEventApprovalApproved, domain.AuditActorUser,
			domain.AuditOutcomeSuccess, "user_approved", approved.StateChangedAt)); err != nil {
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
	consumed := approved
	consumed.State = domain.ApprovalStateConsumed
	consumed.StateReason = domain.ApprovalReasonConsumed
	consumed.StateChangedAt = requestedAt.Add(3 * time.Second)
	storedConsumed, err := approval.NewStoredRequest(consumed)
	if err != nil {
		t.Fatalf("NewStoredRequest(consumed) error = %v", err)
	}
	writeIntent := testApprovalAudit(t, consumed, domain.AuditEventWriteIntent,
		domain.AuditActorSystem, domain.AuditOutcomeSuccess, "approval_consumed", consumed.StateChangedAt)
	if err := approvals.ConsumeWithAudit(ctx, storedApproved, storedDecision, storedConsumed, writeIntent); err != nil {
		t.Fatalf("ConsumeWithAudit() error = %v", err)
	}
	if err := NewAgentRunRepository(db).Finish(ctx,
		testTerminalRun(run, domain.AgentRunStatusCompleted, requestedAt.Add(4*time.Second))); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	selection := application.SessionDeletionSelectionRequest{
		Kind: application.SessionDeletionBefore, Cutoff: now.Add(-24 * time.Hour), FrozenNow: now, Limit: 1,
	}
	assertProtected := func(stage string) {
		t.Helper()
		snapshot, previewErr := sessions.PreviewSessionDeletion(ctx, selection)
		if previewErr != nil || snapshot.Matched != 1 || snapshot.Eligible != 0 || snapshot.Protected != 1 || len(snapshot.Selected) != 0 {
			t.Fatalf("%s PreviewSessionDeletion() = %#v, %v", stage, snapshot, previewErr)
		}
	}
	assertProtected("consumed")

	attempted := writeIntent
	attempted.ID = "00000000-0000-7000-8000-000000009798"
	attempted.Type = domain.AuditEventWriteAttempted
	attempted.Outcome = domain.AuditOutcomeSuccess
	attempted.OccurredAt = requestedAt.Add(5 * time.Second)
	if err := NewAuditRepository(db).AppendWriteResult(ctx, attempted); err != nil {
		t.Fatalf("AppendWriteResult(attempted) error = %v", err)
	}
	assertProtected("attempted without terminal outcome")

	terminal := attempted
	terminal.ID = "00000000-0000-7000-8000-000000009799"
	terminal.Type = domain.AuditEventWriteVerified
	terminal.OccurredAt = requestedAt.Add(6 * time.Second)
	if err := NewAuditRepository(db).AppendWriteResult(ctx, terminal); err != nil {
		t.Fatalf("AppendWriteResult(terminal) error = %v", err)
	}
	snapshot, err := sessions.PreviewSessionDeletion(ctx, selection)
	if err != nil || snapshot.Eligible != 1 || snapshot.Protected != 0 || len(snapshot.Selected) != 1 || snapshot.Selected[0].ID != run.SessionID {
		t.Fatalf("terminal PreviewSessionDeletion() = %#v, %v", snapshot, err)
	}
}

func TestSessionManagementBatchRollbackIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, ctx, testStateDir(t), "session-management-rollback")
	repository := NewSessionRepository(db)
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	first := testSession("00000000-0000-7000-8000-000000000091", "First", domain.PrivacyModeStandard, now.Add(-72*time.Hour))
	second := testSession("00000000-0000-7000-8000-000000000092", "Second", domain.PrivacyModeStandard, now.Add(-48*time.Hour))
	for _, session := range []domain.Session{first, second} {
		if err := repository.Create(ctx, session); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	snapshot, err := repository.PreviewSessionDeletion(ctx, application.SessionDeletionSelectionRequest{
		Kind: application.SessionDeletionBefore, Cutoff: now.Add(-24 * time.Hour), FrozenNow: now, Limit: 2,
	})
	if err != nil || len(snapshot.Selected) != 2 {
		t.Fatalf("PreviewSessionDeletion() = %#v, %v", snapshot, err)
	}
	if _, err := db.handle.ExecContext(ctx, `
		CREATE TRIGGER deny_second_delete
		BEFORE DELETE ON sessions
		WHEN OLD.id = '00000000-0000-7000-8000-000000000092'
		BEGIN SELECT RAISE(ABORT, 'synthetic'); END
	`); err != nil {
		t.Fatalf("trigger setup error = %v", err)
	}
	if _, err := repository.CommitSessionDeletion(ctx, snapshot); err == nil {
		t.Fatal("CommitSessionDeletion() error = nil")
	}
	for _, id := range []domain.SessionID{first.ID, second.ID} {
		if _, err := repository.GetByID(ctx, id); err != nil {
			t.Fatalf("Session %s missing after rollback: %v", id, err)
		}
	}
}
