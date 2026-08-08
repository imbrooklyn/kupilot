package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestAgentRunRepositoryBeginIsAtomicAndMapsNullableFields(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "run-begin")
	sessions := NewSessionRepository(db)
	messages := NewMessageRepository(db)
	repository := NewAgentRunRepository(db)
	base := time.UnixMilli(70_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000501", "Run lifecycle", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	request, run := testRunningPair(
		"00000000-0000-7000-8000-000000000502",
		"00000000-0000-7000-8000-000000000503",
		sessionValue.ID,
		base.Add(time.Millisecond),
	)
	if err := repository.Begin(context.Background(), request, run); err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	gotMessage, err := messages.GetByID(context.Background(), request.ID)
	if err != nil {
		t.Fatalf("GetByID(Message) error = %v", err)
	}
	if !reflect.DeepEqual(gotMessage, request) {
		t.Fatalf("stored Message = %#v, want %#v", gotMessage, request)
	}
	gotRun, err := repository.GetByID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetByID(AgentRun) error = %v", err)
	}
	if !reflect.DeepEqual(gotRun, run) {
		t.Fatalf("stored AgentRun = %#v, want %#v", gotRun, run)
	}

	conflictingRequest, conflictingRun := testRunningPair(
		"00000000-0000-7000-8000-000000000504",
		"00000000-0000-7000-8000-000000000505",
		sessionValue.ID,
		base.Add(2*time.Millisecond),
	)
	if err := repository.Begin(context.Background(), conflictingRequest, conflictingRun); !errors.Is(err, sessioncontract.ErrAgentRunConflict) {
		t.Fatalf("Begin(second active run) error = %v, want ErrAgentRunConflict", err)
	}
	if _, err := messages.GetByID(context.Background(), conflictingRequest.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
		t.Fatalf("active-run conflict left request Message: %v", err)
	}
	terminal := testTerminalRun(run, domain.AgentRunStatusCancelled, base.Add(2*time.Millisecond))
	if err := repository.Finish(context.Background(), terminal); err != nil {
		t.Fatalf("Finish(setup) error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		CREATE TRIGGER fail_agent_run_insert
		BEFORE INSERT ON agent_runs
		WHEN NEW.id = '00000000-0000-7000-8000-000000000507'
		BEGIN
			SELECT RAISE(ABORT, 'synthetic begin rollback canary');
		END
	`); err != nil {
		t.Fatalf("begin rollback trigger setup error = %v", err)
	}
	rollbackRequest, rollbackRun := testRunningPair(
		"00000000-0000-7000-8000-000000000506",
		"00000000-0000-7000-8000-000000000507",
		sessionValue.ID,
		base.Add(3*time.Millisecond),
	)
	err = repository.Begin(context.Background(), rollbackRequest, rollbackRun)
	assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_begin_failed")
	if strings.Contains(err.Error(), "synthetic begin rollback canary") {
		t.Fatal("Begin() error disclosed driver text")
	}
	if _, err := messages.GetByID(context.Background(), rollbackRequest.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
		t.Fatalf("failed Begin left request Message: %v", err)
	}
	if _, err := repository.GetByID(context.Background(), "00000000-0000-7000-8000-000000000599"); !errors.Is(err, sessioncontract.ErrAgentRunNotFound) {
		t.Fatalf("GetByID(missing) error = %v, want ErrAgentRunNotFound", err)
	}
}

func TestAgentRunRepositoryFinishWithMessageIsAtomic(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "run-finish")
	sessions := NewSessionRepository(db)
	messages := NewMessageRepository(db)
	repository := NewAgentRunRepository(db)
	base := time.UnixMilli(80_000).UTC()
	primary := testSession("00000000-0000-7000-8000-000000000601", "Primary run", domain.PrivacyModeStandard, base)
	secondary := testSession("00000000-0000-7000-8000-000000000602", "Secondary run", domain.PrivacyModeStandard, base)
	for _, value := range []domain.Session{primary, secondary} {
		if err := sessions.Create(context.Background(), value); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	request, running := testRunningPair(
		"00000000-0000-7000-8000-000000000603",
		"00000000-0000-7000-8000-000000000604",
		primary.ID,
		base.Add(time.Millisecond),
	)
	if err := repository.Begin(context.Background(), request, running); err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	mismatchedAssistant := testMessage("00000000-0000-7000-8000-000000000607", primary.ID, &running.ID, "Mismatched scope", base.Add(2*time.Millisecond))
	mismatchedAssistant.Role = domain.MessageRoleAssistant
	mismatchedAssistant.Scope = &domain.ScopeSnapshot{Context: "other-context", Namespace: "test-namespace", Generation: 1}
	mismatchedTerminal := testTerminalRun(running, domain.AgentRunStatusCompleted, base.Add(2*time.Millisecond))
	if err := repository.FinishWithMessage(context.Background(), mismatchedAssistant, mismatchedTerminal); !errors.Is(err, sessioncontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("FinishWithMessage(scope mismatch) error = %v, want ErrInvalidRepositoryRequest", err)
	}
	if _, err := messages.GetByID(context.Background(), mismatchedAssistant.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
		t.Fatalf("scope mismatch left assistant Message: %v", err)
	}
	stillRunning, err := repository.GetByID(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("GetByID(after scope mismatch) error = %v", err)
	}
	if stillRunning.Status != domain.AgentRunStatusRunning {
		t.Fatalf("run status after scope mismatch = %q, want running", stillRunning.Status)
	}

	duplicateID := domain.MessageID("00000000-0000-7000-8000-000000000605")
	duplicate := testMessage(duplicateID, secondary.ID, nil, "Existing message", base.Add(2*time.Millisecond))
	if err := messages.Append(context.Background(), duplicate); err != nil {
		t.Fatalf("Append(duplicate setup) error = %v", err)
	}
	failedAssistant := testMessage(duplicateID, primary.ID, &running.ID, "Would roll back", base.Add(3*time.Millisecond))
	failedAssistant.Role = domain.MessageRoleAssistant
	failedAssistant.Scope = &running.Scope
	failedTerminal := testTerminalRun(running, domain.AgentRunStatusCompleted, base.Add(3*time.Millisecond))
	err = repository.FinishWithMessage(context.Background(), failedAssistant, failedTerminal)
	assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_finish_failed")
	stillRunning, err = repository.GetByID(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("GetByID(after rollback) error = %v", err)
	}
	if stillRunning.Status != domain.AgentRunStatusRunning {
		t.Fatalf("run status after rollback = %q, want running", stillRunning.Status)
	}

	assistant := testMessage("00000000-0000-7000-8000-000000000606", primary.ID, &running.ID, "Final validated answer", base.Add(4*time.Millisecond))
	assistant.Role = domain.MessageRoleAssistant
	assistant.Scope = &running.Scope
	terminal := testTerminalRun(running, domain.AgentRunStatusCompleted, base.Add(4*time.Millisecond))
	inputTokens := int64(21)
	outputTokens := int64(34)
	terminal.InputTokens = &inputTokens
	terminal.OutputTokens = &outputTokens
	terminal.StepCount = 2
	terminal.ToolCallCount = 1
	terminal.ModelRequestCount = 2
	if err := repository.FinishWithMessage(context.Background(), assistant, terminal); err != nil {
		t.Fatalf("FinishWithMessage() error = %v", err)
	}
	gotRun, err := repository.GetByID(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("GetByID(finished) error = %v", err)
	}
	if !reflect.DeepEqual(gotRun, terminal) {
		t.Fatalf("finished AgentRun = %#v, want %#v", gotRun, terminal)
	}
	gotAssistant, err := messages.GetByID(context.Background(), assistant.ID)
	if err != nil {
		t.Fatalf("GetByID(assistant) error = %v", err)
	}
	if !reflect.DeepEqual(gotAssistant, assistant) {
		t.Fatalf("stored assistant = %#v, want %#v", gotAssistant, assistant)
	}
}

func TestAgentRunRepositoryRejectsMessageRunBindingMismatchWithoutWrites(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "run-binding-mismatch")
	sessions := NewSessionRepository(db)
	messages := NewMessageRepository(db)
	repository := NewAgentRunRepository(db)
	base := time.UnixMilli(85_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000651", "Run binding", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	tests := []struct {
		name      string
		messageID domain.MessageID
		runID     domain.AgentRunID
		mutate    func(*domain.Message, *domain.AgentRun)
	}{
		{
			name:      "scope",
			messageID: "00000000-0000-7000-8000-000000000652",
			runID:     "00000000-0000-7000-8000-000000000653",
			mutate: func(message *domain.Message, _ *domain.AgentRun) {
				message.Scope = &domain.ScopeSnapshot{Context: "other-context", Namespace: "test-namespace", Generation: 1}
			},
		},
		{
			name:      "resource",
			messageID: "00000000-0000-7000-8000-000000000654",
			runID:     "00000000-0000-7000-8000-000000000655",
			mutate: func(message *domain.Message, run *domain.AgentRun) {
				message.Resource = &domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "message-pod"}
				run.Resource = &domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "run-pod"}
			},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			request, run := testRunningPair(current.messageID, current.runID, sessionValue.ID, base.Add(time.Millisecond))
			current.mutate(&request, &run)
			if err := request.Validate(); err != nil {
				t.Fatalf("Message fixture Validate() error = %v", err)
			}
			if err := run.Validate(); err != nil {
				t.Fatalf("AgentRun fixture Validate() error = %v", err)
			}
			if err := repository.Begin(context.Background(), request, run); !errors.Is(err, sessioncontract.ErrInvalidRepositoryRequest) {
				t.Fatalf("Begin() error = %v, want ErrInvalidRepositoryRequest", err)
			}
			if _, err := messages.GetByID(context.Background(), request.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
				t.Fatalf("binding mismatch left Message: %v", err)
			}
			if _, err := repository.GetByID(context.Background(), run.ID); !errors.Is(err, sessioncontract.ErrAgentRunNotFound) {
				t.Fatalf("binding mismatch left AgentRun: %v", err)
			}
		})
	}
}

func TestAgentRunRepositoryFinishWithoutMessage(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "run-finish-without-message")
	sessions := NewSessionRepository(db)
	repository := NewAgentRunRepository(db)
	base := time.UnixMilli(90_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000701", "Failed run", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	request, running := testRunningPair(
		"00000000-0000-7000-8000-000000000702",
		"00000000-0000-7000-8000-000000000703",
		sessionValue.ID,
		base.Add(time.Millisecond),
	)
	if err := repository.Begin(context.Background(), request, running); err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	reason := "model_request_failed"
	terminal := testTerminalRun(running, domain.AgentRunStatusFailed, base.Add(2*time.Millisecond))
	terminal.TerminationReason = &reason
	terminal.PersistenceDegraded = true
	if err := repository.Finish(context.Background(), terminal); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	got, err := repository.GetByID(context.Background(), running.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(got, terminal) {
		t.Fatalf("finished AgentRun = %#v, want %#v", got, terminal)
	}
	if err := repository.Finish(context.Background(), terminal); !errors.Is(err, sessioncontract.ErrAgentRunConflict) {
		t.Fatalf("repeated Finish() error = %v, want ErrAgentRunConflict", err)
	}
}

func TestAgentRunRepositoryRecoversRunningRunsAndIsIdempotent(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "run-recovery")
	sessions := NewSessionRepository(db)
	repository := NewAgentRunRepository(db)
	base := time.UnixMilli(100_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000801", "Recovery", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var running []domain.AgentRun
	for index := 0; index < 2; index++ {
		requestID := domain.MessageID("00000000-0000-7000-8000-00000000081" + string(rune('1'+index)))
		runID := domain.AgentRunID("00000000-0000-7000-8000-00000000082" + string(rune('1'+index)))
		request, run := testRunningPair(requestID, runID, sessionValue.ID, base.Add(time.Duration(index+1)*time.Millisecond))
		seedRunningPairWithoutActiveGate(t, db, request, run)
		running = append(running, run)
	}
	recoveredAt := base.Add(10 * time.Millisecond)
	result, err := repository.RecoverInterrupted(context.Background(), recoveredAt)
	if err != nil {
		t.Fatalf("RecoverInterrupted() error = %v", err)
	}
	if result.Interrupted != 2 {
		t.Fatalf("Interrupted = %d, want 2", result.Interrupted)
	}
	for _, value := range running {
		got, err := repository.GetByID(context.Background(), value.ID)
		if err != nil {
			t.Fatalf("GetByID(recovered) error = %v", err)
		}
		if got.Status != domain.AgentRunStatusInterrupted || got.FinishedAt == nil || !got.FinishedAt.Equal(recoveredAt) {
			t.Fatalf("recovered AgentRun = %#v", got)
		}
		if got.TerminationReason == nil || *got.TerminationReason != domain.InterruptedByRestartReason {
			t.Fatalf("recovered termination reason = %#v", got.TerminationReason)
		}
	}
	again, err := repository.RecoverInterrupted(context.Background(), recoveredAt.Add(time.Millisecond))
	if err != nil {
		t.Fatalf("RecoverInterrupted(second) error = %v", err)
	}
	if again.Interrupted != 0 {
		t.Fatalf("second Interrupted = %d, want 0", again.Interrupted)
	}
}

func TestAgentRunRepositoryRecoveryRollsBackAndHonorsCancellation(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "run-recovery-rollback")
		sessions := NewSessionRepository(db)
		repository := NewAgentRunRepository(db)
		base := time.UnixMilli(110_000).UTC()
		sessionValue := testSession("00000000-0000-7000-8000-000000000901", "Recovery rollback", domain.PrivacyModeStandard, base)
		if err := sessions.Create(context.Background(), sessionValue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		var runIDs []domain.AgentRunID
		for index := 0; index < 2; index++ {
			requestID := domain.MessageID("00000000-0000-7000-8000-00000000091" + string(rune('1'+index)))
			runID := domain.AgentRunID("00000000-0000-7000-8000-00000000092" + string(rune('1'+index)))
			request, run := testRunningPair(requestID, runID, sessionValue.ID, base.Add(time.Duration(index+1)*time.Millisecond))
			seedRunningPairWithoutActiveGate(t, db, request, run)
			runIDs = append(runIDs, run.ID)
		}
		if _, err := db.handle.ExecContext(context.Background(), `
			CREATE TRIGGER prevent_second_recovery
			BEFORE UPDATE OF status ON agent_runs
			WHEN OLD.id = '00000000-0000-7000-8000-000000000922'
			BEGIN
				SELECT RAISE(ABORT, 'synthetic recovery rollback canary');
			END
		`); err != nil {
			t.Fatalf("recovery trigger setup error = %v", err)
		}
		_, err := repository.RecoverInterrupted(context.Background(), base.Add(10*time.Millisecond))
		assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_recovery_failed")
		if strings.Contains(err.Error(), "synthetic recovery rollback canary") {
			t.Fatal("recovery error disclosed driver text")
		}
		for _, id := range runIDs {
			got, getErr := repository.GetByID(context.Background(), id)
			if getErr != nil {
				t.Fatalf("GetByID() error = %v", getErr)
			}
			if got.Status != domain.AgentRunStatusRunning {
				t.Fatalf("run %q status = %q, want running", id, got.Status)
			}
		}
	})

	t.Run("cancel", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "run-recovery-cancel")
		sessions := NewSessionRepository(db)
		repository := NewAgentRunRepository(db)
		base := time.UnixMilli(120_000).UTC()
		sessionValue := testSession("00000000-0000-7000-8000-000000000a01", "Recovery cancellation", domain.PrivacyModeStandard, base)
		if err := sessions.Create(context.Background(), sessionValue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		request, run := testRunningPair(
			"00000000-0000-7000-8000-000000000a02",
			"00000000-0000-7000-8000-000000000a03",
			sessionValue.ID,
			base.Add(time.Millisecond),
		)
		if err := repository.Begin(context.Background(), request, run); err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := repository.RecoverInterrupted(ctx, base.Add(2*time.Millisecond))
		assertStorageError(t, err, ClassCancelled, "storage_cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RecoverInterrupted() error = %v, want context.Canceled semantics", err)
		}
		got, err := repository.GetByID(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.Status != domain.AgentRunStatusRunning {
			t.Fatalf("run status after cancellation = %q, want running", got.Status)
		}
	})
}

func TestAgentRunRepositoryRecoveryRejectsInvalidDurableRows(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "run-recovery-invalid-row")
	sessions := NewSessionRepository(db)
	repository := NewAgentRunRepository(db)
	base := time.UnixMilli(130_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000b01", "Invalid recovery row", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	messageID := domain.MessageID("00000000-0000-7000-8000-000000000b02")
	content := "Safe recovery fixture"
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO messages (
			id, session_id, role, content, content_format, status,
			content_hash, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, messageID, sessionValue.ID, "user", content, "plain", "committed", domain.MessageContentHash(content), base.UnixMilli()); err != nil {
		t.Fatalf("Message setup error = %v", err)
	}
	invalidRunID := "00000000-0000-6000-8000-000000000b03"
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO agent_runs (
			id, session_id, request_message_id, status,
			scope_context, scope_namespace, scope_generation,
			prompt_version, tool_catalog_version, started_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, invalidRunID, sessionValue.ID, messageID, "running", "test-context", "test-namespace", 1, "prompt-v1", "tools-v1", base.UnixMilli()); err != nil {
		t.Fatalf("invalid AgentRun setup error = %v", err)
	}

	_, err := repository.RecoverInterrupted(context.Background(), base.Add(time.Millisecond))
	assertStorageError(t, err, ClassPersistenceUnavailable, "agent_run_recovery_failed")
	var status string
	if err := db.handle.GetContext(context.Background(), &status, `SELECT status FROM agent_runs WHERE id = ?`, invalidRunID); err != nil {
		t.Fatalf("status query error = %v", err)
	}
	if status != "running" {
		t.Fatalf("invalid row status = %q, want running", status)
	}
}

func testRunningPair(messageID domain.MessageID, runID domain.AgentRunID, sessionID domain.SessionID, startedAt time.Time) (domain.Message, domain.AgentRun) {
	scope := domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 1}
	request := testMessage(messageID, sessionID, &runID, "Safe user request", startedAt)
	request.Scope = &scope
	return request, domain.AgentRun{
		ID:                 runID,
		SessionID:          sessionID,
		RequestMessageID:   messageID,
		Status:             domain.AgentRunStatusRunning,
		Scope:              scope,
		PromptVersion:      "prompt-v1",
		ToolCatalogVersion: "tools-v1",
		StartedAt:          timePointer(startedAt),
	}
}

func testTerminalRun(running domain.AgentRun, status domain.AgentRunStatus, finishedAt time.Time) domain.AgentRun {
	result := running
	result.Status = status
	result.FinishedAt = timePointer(finishedAt)
	return result
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func seedRunningPairWithoutActiveGate(t *testing.T, db *DB, message domain.Message, run domain.AgentRun) {
	t.Helper()
	err := withTx(context.Background(), db.handle, func(tx *sqlx.Tx) error {
		if err := insertMessage(context.Background(), tx, message); err != nil {
			return err
		}
		if err := insertAgentRun(context.Background(), tx, run); err != nil {
			return err
		}
		return touchSession(context.Background(), tx, run.SessionID, *run.StartedAt)
	})
	if err != nil {
		t.Fatalf("running AgentRun setup error = %v", err)
	}
}
