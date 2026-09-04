package sqlite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestCoordinatorRepositoriesRunMinimalSessionWithoutContentPersistence(t *testing.T) {
	stateDir := testStateDir(t)
	database := openTestDB(t, context.Background(), stateDir, "coordinator-minimal-run")
	base := time.UnixMilli(850).UTC()
	session := testSession(
		"00000000-0000-7000-8000-000000008501",
		"",
		domain.PrivacyModeMinimal,
		base,
	)
	if err := NewSessionRepository(database).Create(context.Background(), session); err != nil {
		t.Fatalf("Create(minimal Session) error = %v", err)
	}
	message, run := testRunningPair(
		"00000000-0000-7000-8000-000000008502",
		"00000000-0000-7000-8000-000000008503",
		session.ID,
		base.Add(time.Millisecond),
	)
	contentCanary := strings.Join([]string{"minimal", "content", "must", "remain", "memory", "8501"}, "-")
	message.Content = contentCanary
	message.Hash = domain.MessageContentHash(contentCanary)
	startAudit := coordinatedRunAudit(
		"00000000-0000-7000-8000-000000008504",
		run,
		domain.AuditEventRunStarted,
		base.Add(2*time.Millisecond),
	)
	runs := NewAgentRunRepository(database)
	if err := runs.BeginWithAudit(context.Background(), message, run, startAudit); err != nil {
		t.Fatalf("BeginWithAudit(minimal) error = %v", err)
	}
	stored, err := runs.GetByID(context.Background(), run.ID)
	if err != nil || stored.RequestMessageID != run.RequestMessageID || stored.Status != domain.AgentRunStatusRunning {
		t.Fatalf("GetByID(minimal) = %#v/%v", stored, err)
	}

	invocation := testToolInvocation(
		"00000000-0000-7000-8000-000000008505",
		run,
		1,
		base.Add(3*time.Millisecond),
	)
	invocation.Purpose = &contentCanary
	toolAudit := coordinatedToolAudit(
		"00000000-0000-7000-8000-000000008506",
		run,
		invocation,
		base.Add(9*time.Millisecond),
	)
	if err := NewToolInvocationRepository(database).SaveWithAudit(
		context.Background(), invocation, nil, toolAudit,
	); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("SaveWithAudit(minimal) error = %v, want ErrDurableContentDisabled", err)
	}

	finishedAt := base.Add(10 * time.Millisecond)
	terminal := testTerminalRun(run, domain.AgentRunStatusCompleted, finishedAt)
	diagnosis := domain.Diagnosis{
		ID: domain.DiagnosisID("00000000-0000-7000-8000-000000008507"), RunID: run.ID, Scope: run.Scope,
		AnswerMarkdown: contentCanary, CreatedAt: finishedAt,
	}
	assistant := testMessage(
		"00000000-0000-7000-8000-000000008508",
		run.SessionID,
		&run.ID,
		contentCanary,
		finishedAt,
	)
	assistant.Role = domain.MessageRoleAssistant
	assistant.Format = domain.MessageFormatMarkdown
	assistant.Scope = &run.Scope
	terminalAudit := coordinatedRunAudit(
		"00000000-0000-7000-8000-000000008509",
		terminal,
		domain.AuditEventRunCompleted,
		finishedAt,
	)
	if err := runs.CompleteWithAudit(
		context.Background(), diagnosis, assistant, terminal, terminalAudit,
	); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("CompleteWithAudit(minimal) error = %v, want ErrDurableContentDisabled", err)
	}
	if err := runs.FinishWithAudit(context.Background(), terminal, terminalAudit); err != nil {
		t.Fatalf("FinishWithAudit(minimal) error = %v", err)
	}

	for table, want := range map[string]int{
		"agent_runs":       1,
		"audit_events":     2,
		"messages":         0,
		"tool_invocations": 0,
		"evidence_items":   0,
		"diagnoses":        0,
		"model_requests":   0,
	} {
		var got int
		if err := database.handle.GetContext(context.Background(), &got, "SELECT count(rowid) FROM "+table); err != nil {
			t.Fatalf("count %s error = %v", table, err)
		}
		if got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	var storageBytes []byte
	for _, suffix := range []string{"", "-wal"} {
		content, err := os.ReadFile(filepath.Join(stateDir, databaseFilename+suffix))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", suffix, err)
		}
		storageBytes = append(storageBytes, content...)
	}
	if bytes.Count(storageBytes, []byte(contentCanary)) != 0 {
		t.Fatal("minimal Session content canary reached SQLite or WAL")
	}
}

func TestCoordinatorRepositoryClearHistoryIsAtomicAndPreservesPreferences(t *testing.T) {
	database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-clear-history")
	base := time.UnixMilli(12_000).UTC()
	seedStandardRun(
		t,
		database,
		"00000000-0000-7000-8000-000000012001",
		"00000000-0000-7000-8000-000000012002",
		"00000000-0000-7000-8000-000000012003",
		base,
	)
	second := testSession("00000000-0000-7000-8000-000000012004", "Second", domain.PrivacyModeStandard, base.Add(time.Millisecond))
	if err := NewSessionRepository(database).Create(context.Background(), second); err != nil {
		t.Fatalf("Create(second Session) error = %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO audit_events (id, event_type, actor, outcome, details_json, occurred_at_ms) VALUES ('00000000-0000-7000-8000-000000012005', 'persistence_degraded', 'system', 'failure', '{}', 12005)`,
		`INSERT INTO settings (key, value_json, schema_version, updated_at_ms) VALUES ('operational_detail_retention_days', '14', 1, 12006)`,
		`INSERT INTO privacy_consents (role, policy_version, origin_hash, categories_json, decision, decided_at_ms, schema_version) VALUES ('agent', 'privacy-v1', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '[]', 'accepted', 12007, 2)`,
	} {
		if _, err := database.handle.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("seed preference statement error = %v", err)
		}
	}
	if err := NewScopePreferenceRepository(database).SaveLastContext(context.Background(), application.ScopePreference{
		Context: "development", UpdatedAt: base.Add(8 * time.Millisecond),
	}); err != nil {
		t.Fatalf("seed scope preference error = %v", err)
	}

	repository := NewSessionRepository(database)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repository.ClearHistory(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ClearHistory(cancelled) error = %v", err)
	}
	var before int
	if err := database.handle.GetContext(context.Background(), &before, `SELECT count(id) FROM sessions`); err != nil || before != 2 {
		t.Fatalf("Session count after cancelled clear = %d/%v", before, err)
	}

	if err := repository.ClearHistory(context.Background()); err != nil {
		t.Fatalf("ClearHistory() error = %v", err)
	}
	for table, want := range map[string]int{
		"sessions": 0, "messages": 0, "agent_runs": 0, "model_requests": 0,
		"tool_invocations": 0, "evidence_items": 0, "diagnoses": 0,
		"approvals": 0, "approval_decisions": 0, "audit_events": 0,
		"settings": 2, "privacy_consents": 1, "schema_migrations": 7,
	} {
		var got int
		if err := database.handle.GetContext(context.Background(), &got, "SELECT count(rowid) FROM "+table); err != nil || got != want {
			t.Errorf("%s rows after clear-history = %d/%v, want %d", table, got, err, want)
		}
	}
}

func TestCoordinatorRepositoryClearHistoryRollsBackOnFailure(t *testing.T) {
	database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-clear-history-rollback")
	session := testSession("00000000-0000-7000-8000-000000012101", "Rollback", domain.PrivacyModeStandard, time.UnixMilli(12_100).UTC())
	if err := NewSessionRepository(database).Create(context.Background(), session); err != nil {
		t.Fatalf("Create(Session) error = %v", err)
	}
	if _, err := database.handle.ExecContext(context.Background(), `
		CREATE TRIGGER deny_history_clear
		BEFORE DELETE ON sessions
		BEGIN
			SELECT RAISE(ABORT, 'synthetic history clear denial');
		END
	`); err != nil {
		t.Fatalf("create denial trigger error = %v", err)
	}
	if err := NewSessionRepository(database).ClearHistory(context.Background()); err == nil {
		t.Fatal("ClearHistory() error = nil")
	}
	var count int
	if err := database.handle.GetContext(context.Background(), &count, `SELECT count(id) FROM sessions`); err != nil || count != 1 {
		t.Fatalf("Session count after rollback = %d/%v", count, err)
	}
}

func TestCoordinatorRepositoriesKeepZeroDayDetailOutOfSQLite(t *testing.T) {
	database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-zero-retention")
	run := seedStandardRun(
		t,
		database,
		"00000000-0000-7000-8000-000000009101",
		"00000000-0000-7000-8000-000000009102",
		"00000000-0000-7000-8000-000000009103",
		time.UnixMilli(1_000).UTC(),
	)
	if err := NewSessionRepository(database).TightenOperationalDetailRetention(context.Background(), application.RetentionSettingUpdate{
		ExpectedDays: application.DefaultOperationalDetailRetentionDays,
		Days:         0,
		UpdatedAt:    time.UnixMilli(1_001).UTC(),
	}); err != nil {
		t.Fatalf("TightenOperationalDetailRetention() error = %v", err)
	}

	invocation := testToolInvocation(
		"00000000-0000-7000-8000-000000009104",
		run,
		1,
		time.UnixMilli(1_002).UTC(),
	)
	evidence := testEvidence(
		"00000000-0000-7000-8000-000000009105",
		invocation,
		time.UnixMilli(1_003).UTC(),
	)
	invocation.EvidenceCount = 1
	toolAudit := coordinatedToolAudit(
		"00000000-0000-7000-8000-000000009106",
		run,
		invocation,
		time.UnixMilli(1_008).UTC(),
	)
	tools := NewToolInvocationRepository(database)
	if err := tools.SaveWithAudit(context.Background(), invocation, []domain.Evidence{evidence}, toolAudit); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("SaveWithAudit(zero-day detail) error = %v, want ErrDurableContentDisabled", err)
	}
	if _, err := tools.GetByID(context.Background(), invocation.ID); !errors.Is(err, ErrToolInvocationNotFound) {
		t.Fatalf("zero-day ToolInvocation was persisted: %v", err)
	}
	if _, err := NewEvidenceRepository(database).GetByID(context.Background(), evidence.ID); !errors.Is(err, ErrEvidenceNotFound) {
		t.Fatalf("zero-day Evidence was persisted: %v", err)
	}
	if _, err := NewAuditRepository(database).GetByID(context.Background(), toolAudit.ID); !errors.Is(err, auditcontract.ErrAuditEventNotFound) {
		t.Fatalf("rejected Tool transaction left an audit row: %v", err)
	}

	diagnosis := testDiagnosis("00000000-0000-7000-8000-000000009107", run, []domain.Evidence{evidence})
	diagnosis.EvidenceDetailsState = domain.EvidenceDetailExpired
	finishedAt := time.UnixMilli(1_010).UTC()
	terminal := testTerminalRun(run, domain.AgentRunStatusCompleted, finishedAt)
	terminal.ToolCallCount = 1
	assistant := testMessage(
		"00000000-0000-7000-8000-000000009108",
		run.SessionID,
		&run.ID,
		diagnosis.AnswerMarkdown,
		finishedAt,
	)
	assistant.Role = domain.MessageRoleAssistant
	assistant.Format = domain.MessageFormatMarkdown
	assistant.Scope = &run.Scope
	runAudit := coordinatedRunAudit(
		"00000000-0000-7000-8000-000000009109",
		terminal,
		domain.AuditEventRunCompleted,
		finishedAt,
	)
	if err := NewAgentRunRepository(database).CompleteWithAudit(context.Background(), diagnosis, assistant, terminal, runAudit); err != nil {
		t.Fatalf("CompleteWithAudit(expired detail) error = %v", err)
	}
	stored, err := NewDiagnosisRepository(database).GetByRunID(context.Background(), run.ID)
	if err != nil || stored.EvidenceDetailsState != domain.EvidenceDetailExpired {
		t.Fatalf("stored Diagnosis/state = %#v/%v", stored, err)
	}
}

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

	t.Run("minimal run start", func(t *testing.T) {
		database := openTestDB(t, context.Background(), testStateDir(t), "coordinator-minimal-start-transaction")
		sessions := NewSessionRepository(database)
		runs := NewAgentRunRepository(database)
		messages := NewMessageRepository(database)
		base := time.UnixMilli(915).UTC()
		session := testSession("00000000-0000-7000-8000-000000009015", "", domain.PrivacyModeMinimal, base)
		if err := sessions.Create(context.Background(), session); err != nil {
			t.Fatalf("Create(minimal Session) error = %v", err)
		}
		message, run := testRunningPair(
			"00000000-0000-7000-8000-000000009016",
			"00000000-0000-7000-8000-000000009017",
			session.ID,
			base.Add(time.Millisecond),
		)
		audit := coordinatedRunAudit(
			"00000000-0000-7000-8000-000000009018",
			run,
			domain.AuditEventRunStarted,
			base.Add(2*time.Millisecond),
		)
		installAuditFailureTrigger(t, database)

		err := runs.BeginWithAudit(context.Background(), message, run, audit)
		assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_begin_failed")
		if _, err := messages.GetByID(context.Background(), message.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
			t.Fatalf("failed minimal start left request Message: %v", err)
		}
		if _, err := runs.GetByID(context.Background(), run.ID); !errors.Is(err, sessioncontract.ErrAgentRunNotFound) {
			t.Fatalf("failed minimal start audit left AgentRun: %v", err)
		}
		if _, err := NewAuditRepository(database).GetByID(context.Background(), audit.ID); !errors.Is(err, auditcontract.ErrAuditEventNotFound) {
			t.Fatalf("failed minimal start left AuditEvent: %v", err)
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
