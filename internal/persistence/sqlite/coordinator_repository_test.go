package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestCoordinatorTransactionsRollBackWhenRequiredAuditFails(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-session-transaction")
		sessions := NewSessionRepository(database)
		value := testSession(
			"00000000-0000-7000-8000-000000009001",
			"Atomic Session",
			domain.PrivacyModeStandard,
			time.UnixMilli(900).UTC(),
		)
		audit := coordinatedSessionAudit(
			"00000000-0000-7000-8000-000000009002",
			value,
			time.UnixMilli(901).UTC(),
		)
		installAuditFailureTrigger(t, database)

		err := sessions.CreateWithAudit(context.Background(), value, audit)
		assertStorageError(t, err, ClassPersistenceUnavailable, "session_create_failed")
		if _, err := sessions.GetByID(context.Background(), value.ID); !errors.Is(err, sessioncontract.ErrSessionNotFound) {
			t.Fatalf("failed creation audit left Session: %v", err)
		}
	})

	t.Run("run start", func(t *testing.T) {
		database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-start-transaction")
		sessions := NewSessionRepository(database)
		runs := NewAgentRunRepository(database)
		messages := NewMessageRepository(database)
		base := time.UnixMilli(910).UTC()
		session := testSession("00000000-0000-7000-8000-000000009011", "Atomic start", domain.PrivacyModeStandard, base)
		if err := sessions.Create(context.Background(), session); err != nil {
			t.Fatalf("Create(Session) error = %v", err)
		}
		message, run := testRunningPair(
			"00000000-0000-7000-8000-000000009012",
			"00000000-0000-7000-8000-000000009013",
			session.ID,
			base.Add(time.Millisecond),
		)
		audit := coordinatedRunAudit(
			"00000000-0000-7000-8000-000000009014",
			run,
			domain.AuditEventRunStarted,
			base.Add(2*time.Millisecond),
		)
		installAuditFailureTrigger(t, database)

		err := runs.BeginWithAudit(context.Background(), message, run, audit)
		assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_begin_failed")
		if _, err := messages.GetByID(context.Background(), message.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
			t.Fatalf("failed start audit left request Message: %v", err)
		}
		if _, err := runs.GetByID(context.Background(), run.ID); !errors.Is(err, sessioncontract.ErrAgentRunNotFound) {
			t.Fatalf("failed start audit left AgentRun: %v", err)
		}
	})

	t.Run("tool result", func(t *testing.T) {
		database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-tool-transaction")
		run := seedStandardRun(
			t,
			database,
			"00000000-0000-7000-8000-000000009021",
			"00000000-0000-7000-8000-000000009022",
			"00000000-0000-7000-8000-000000009023",
			time.UnixMilli(920).UTC(),
		)
		tools := NewToolInvocationRepository(database)
		evidenceRepository := NewEvidenceRepository(database)
		invocation := testToolInvocation(
			"00000000-0000-7000-8000-000000009024",
			run,
			1,
			time.UnixMilli(921).UTC(),
		)
		evidence := testEvidence(
			"00000000-0000-7000-8000-000000009025",
			invocation,
			time.UnixMilli(922).UTC(),
		)
		invocation.EvidenceCount = 1
		audit := coordinatedToolAudit(
			"00000000-0000-7000-8000-000000009026",
			run,
			invocation,
			time.UnixMilli(927).UTC(),
		)
		installAuditFailureTrigger(t, database)

		err := tools.SaveWithAudit(context.Background(), invocation, []domain.Evidence{evidence}, audit)
		assertStorageError(t, err, ClassPersistenceUnavailable, "tool_invocation_save_failed")
		if _, err := tools.GetByID(context.Background(), invocation.ID); !errors.Is(err, ErrToolInvocationNotFound) {
			t.Fatalf("failed Tool audit left ToolInvocation: %v", err)
		}
		if _, err := evidenceRepository.GetByID(context.Background(), evidence.ID); !errors.Is(err, ErrEvidenceNotFound) {
			t.Fatalf("failed Tool audit left Evidence: %v", err)
		}
	})

	t.Run("run completion", func(t *testing.T) {
		database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-completion-transaction")
		run := seedStandardRun(
			t,
			database,
			"00000000-0000-7000-8000-000000009031",
			"00000000-0000-7000-8000-000000009032",
			"00000000-0000-7000-8000-000000009033",
			time.UnixMilli(930).UTC(),
		)
		tools := NewToolInvocationRepository(database)
		invocation := testToolInvocation(
			"00000000-0000-7000-8000-000000009034",
			run,
			1,
			time.UnixMilli(931).UTC(),
		)
		evidence := testEvidence(
			"00000000-0000-7000-8000-000000009035",
			invocation,
			time.UnixMilli(932).UTC(),
		)
		invocation.EvidenceCount = 1
		if err := tools.Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
			t.Fatalf("Save(Tool setup) error = %v", err)
		}
		diagnosis := testDiagnosis("00000000-0000-7000-8000-000000009036", run, []domain.Evidence{evidence})
		finishedAt := time.UnixMilli(940).UTC()
		terminal := testTerminalRun(run, domain.AgentRunStatusCompleted, finishedAt)
		terminal.ToolCallCount = 1
		assistant := testMessage(
			"00000000-0000-7000-8000-000000009037",
			run.SessionID,
			&run.ID,
			diagnosis.AnswerMarkdown,
			finishedAt,
		)
		assistant.Role = domain.MessageRoleAssistant
		assistant.Format = domain.MessageFormatMarkdown
		assistant.Scope = &run.Scope
		audit := coordinatedRunAudit(
			"00000000-0000-7000-8000-000000009038",
			terminal,
			domain.AuditEventRunCompleted,
			finishedAt,
		)
		installAuditFailureTrigger(t, database)

		runs := NewAgentRunRepository(database)
		err := runs.CompleteWithAudit(context.Background(), diagnosis, assistant, terminal, audit)
		assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_finish_failed")
		if _, err := NewDiagnosisRepository(database).GetByRunID(context.Background(), run.ID); !errors.Is(err, ErrDiagnosisNotFound) {
			t.Fatalf("failed completion audit left Diagnosis: %v", err)
		}
		if _, err := NewMessageRepository(database).GetByID(context.Background(), assistant.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
			t.Fatalf("failed completion audit left assistant Message: %v", err)
		}
		persisted, err := runs.GetByID(context.Background(), run.ID)
		if err != nil || persisted.Status != domain.AgentRunStatusRunning {
			t.Fatalf("AgentRun after rolled-back completion = %#v/%v", persisted, err)
		}
		if _, err := NewAuditRepository(database).GetByID(context.Background(), audit.ID); !errors.Is(err, auditcontract.ErrAuditEventNotFound) {
			t.Fatalf("failed completion transaction left audit: %v", err)
		}
	})
}

func coordinatedSessionAudit(id domain.AuditEventID, session domain.Session, occurredAt time.Time) domain.AuditEvent {
	sessionID := session.ID
	return domain.AuditEvent{
		ID: id, SessionID: &sessionID, Type: domain.AuditEventSessionCreated,
		Actor: domain.AuditActorUser, Outcome: domain.AuditOutcomeSuccess, OccurredAt: occurredAt,
	}
}

func coordinatedRunAudit(
	id domain.AuditEventID,
	run domain.AgentRun,
	eventType domain.AuditEventType,
	occurredAt time.Time,
) domain.AuditEvent {
	sessionID, runID, scope := run.SessionID, run.ID, run.Scope
	actor := domain.AuditActorAgent
	if eventType == domain.AuditEventRunStarted {
		actor = domain.AuditActorUser
	}
	outcome := domain.AuditOutcomeFailure
	if eventType == domain.AuditEventRunStarted || eventType == domain.AuditEventRunCompleted {
		outcome = domain.AuditOutcomeSuccess
	}
	return domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType,
		Actor: actor, Outcome: outcome, Scope: &scope,
		Subject: cloneTestResource(run.Resource), OccurredAt: occurredAt,
	}
}

func coordinatedToolAudit(
	id domain.AuditEventID,
	run domain.AgentRun,
	invocation domain.ToolInvocation,
	occurredAt time.Time,
) domain.AuditEvent {
	event := coordinatedRunAudit(id, run, domain.AuditEventToolCompleted, occurredAt)
	name, sequence := invocation.Name, invocation.Sequence
	event.Outcome = domain.AuditOutcomeSuccess
	event.Details = domain.AuditDetails{ToolName: &name, Sequence: &sequence}
	return event
}

func cloneTestResource(value *domain.ResourceRef) *domain.ResourceRef {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func installAuditFailureTrigger(t *testing.T, database *DB) {
	t.Helper()
	if _, err := database.handle.ExecContext(context.Background(), `
		CREATE TRIGGER fail_coordinator_audit
		BEFORE INSERT ON audit_events
		BEGIN
			SELECT RAISE(ABORT, 'synthetic coordinated audit rollback');
		END
	`); err != nil {
		t.Fatalf("audit rollback trigger setup error = %v", err)
	}
}
