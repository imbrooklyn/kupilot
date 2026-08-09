package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestModelRequestRepositoryRoundTripsBoundedMetadata(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "model-request-round-trip")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000002001", "00000000-0000-7000-8000-000000002002", "00000000-0000-7000-8000-000000002003", time.UnixMilli(100).UTC())
	repository := NewModelRequestRepository(db)
	first := testModelRequest("00000000-0000-7000-8000-000000002004", run.ID, 1, time.UnixMilli(101).UTC())
	if err := repository.Save(context.Background(), first); err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	got, err := repository.GetByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("GetByID() = %#v, want %#v", got, first)
	}

	errorClass := domain.SafeErrorClassUnavailable
	second := testModelRequest("00000000-0000-7000-8000-000000002005", run.ID, 2, time.UnixMilli(103).UTC())
	second.Status = domain.ModelRequestStatusFailed
	second.ErrorClass = &errorClass
	second.ProviderRequestID = nil
	second.ResponseFingerprint = nil
	second.InputTokens = nil
	second.OutputTokens = nil
	if err := second.Validate(); err != nil {
		t.Fatalf("failed fixture Validate() error = %v", err)
	}
	if err := repository.Save(context.Background(), second); err != nil {
		t.Fatalf("Save(second) error = %v", err)
	}
	listed, err := repository.ListByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListByRun() error = %v", err)
	}
	if !reflect.DeepEqual(listed, []domain.ModelRequestMetadata{first, second}) {
		t.Fatalf("ListByRun() = %#v", listed)
	}
	if _, err := repository.GetByID(context.Background(), "00000000-0000-7000-8000-000000002099"); !errors.Is(err, ErrModelRequestNotFound) {
		t.Fatalf("GetByID(missing) error = %v, want ErrModelRequestNotFound", err)
	}
}

func TestModelRequestRepositoryRejectsMinimalInvalidAndDuplicateMetadata(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "model-request-denials")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000002101", "00000000-0000-7000-8000-000000002102", "00000000-0000-7000-8000-000000002103", time.UnixMilli(110).UTC())
	repository := NewModelRequestRepository(db)
	request := testModelRequest("00000000-0000-7000-8000-000000002104", run.ID, 1, time.UnixMilli(111).UTC())
	if err := repository.Save(context.Background(), request); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}

	boundCanary := "model-bound-value-canary"
	duplicate := request
	duplicate.ID = "00000000-0000-7000-8000-000000002105"
	duplicate.Model = boundCanary
	err := repository.Save(context.Background(), duplicate)
	assertStorageError(t, err, ClassPersistenceUnavailable, "model_request_save_failed")
	if strings.Contains(err.Error(), boundCanary) {
		t.Fatal("duplicate error disclosed a bound model identifier")
	}

	invalid := request
	invalid.ID = "00000000-0000-7000-8000-000000002106"
	invalid.Model = strings.Repeat("m", 129)
	if err := repository.Save(context.Background(), invalid); !errors.Is(err, domain.ErrInvalidModelRequestMetadata) {
		t.Fatalf("Save(invalid) error = %v, want ErrInvalidModelRequestMetadata", err)
	}
	nonterminal := request
	nonterminal.ID = "00000000-0000-7000-8000-000000002108"
	nonterminal.Sequence = 2
	nonterminal.Status = domain.ModelRequestStatusRunning
	nonterminal.FinishedAt = nil
	nonterminal.ResponseFingerprint = nil
	nonterminal.InputTokens = nil
	nonterminal.OutputTokens = nil
	nonterminal.LatencyMilliseconds = nil
	if err := repository.Save(context.Background(), nonterminal); !errors.Is(err, domain.ErrInvalidModelRequestMetadata) {
		t.Fatalf("Save(nonterminal) error = %v, want ErrInvalidModelRequestMetadata", err)
	}

	if _, err := db.handle.ExecContext(context.Background(), `
		UPDATE sessions
		SET title = '', privacy_mode = 'minimal', last_context = NULL,
			last_namespace = NULL, selected_resource_json = NULL, summary = NULL
		WHERE id = ?
	`, run.SessionID); err != nil {
		t.Fatalf("minimal Session setup error = %v", err)
	}
	minimal := testModelRequest("00000000-0000-7000-8000-000000002107", run.ID, 2, time.UnixMilli(113).UTC())
	if err := repository.Save(context.Background(), minimal); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("Save(minimal) error = %v, want ErrDurableContentDisabled", err)
	}
	if _, err := repository.GetByID(context.Background(), minimal.ID); !errors.Is(err, ErrModelRequestNotFound) {
		t.Fatalf("minimal metadata was persisted: %v", err)
	}
}

func TestModelRequestRepositoryRejectsCorruptRowsAndHonorsCancellation(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "model-request-strict")
	run := seedStandardRun(t, db, "00000000-0000-7000-8000-000000002201", "00000000-0000-7000-8000-000000002202", "00000000-0000-7000-8000-000000002203", time.UnixMilli(120).UTC())
	repository := NewModelRequestRepository(db)
	request := testModelRequest("00000000-0000-7000-8000-000000002204", run.ID, 1, time.UnixMilli(121).UTC())
	if err := repository.Save(context.Background(), request); err != nil {
		t.Fatalf("Save(setup) error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("enable ignore_check_constraints error = %v", err)
	}
	rowCanary := "invalid-provider-row-canary"
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE model_requests SET provider_kind = ? WHERE id = ?`, rowCanary, request.ID); err != nil {
		t.Fatalf("corrupt row setup error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatalf("disable ignore_check_constraints error = %v", err)
	}
	_, err := repository.GetByID(context.Background(), request.ID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "model_request_row_invalid")
	if strings.Contains(err.Error(), rowCanary) {
		t.Fatal("row mapping error disclosed stored content")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	other := testModelRequest("00000000-0000-7000-8000-000000002205", run.ID, 2, time.UnixMilli(123).UTC())
	err = repository.Save(cancelled, other)
	assertStorageError(t, err, ClassCancelled, "storage_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Save(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := repository.ListByRun(cancelled, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListByRun(cancelled) error = %v, want context.Canceled", err)
	}
}

func seedStandardRun(t *testing.T, db *DB, sessionID domain.SessionID, messageID domain.MessageID, runID domain.AgentRunID, startedAt time.Time) domain.AgentRun {
	t.Helper()
	sessions := NewSessionRepository(db)
	runs := NewAgentRunRepository(db)
	sessionValue := testSession(string(sessionID), "Repository fixture", domain.PrivacyModeStandard, startedAt.Add(-time.Millisecond))
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create(Session) error = %v", err)
	}
	message, run := testRunningPair(messageID, runID, sessionID, startedAt)
	if err := runs.Begin(context.Background(), message, run); err != nil {
		t.Fatalf("Begin(AgentRun) error = %v", err)
	}
	return run
}

func testModelRequest(id domain.ModelRequestID, runID domain.AgentRunID, sequence int, startedAt time.Time) domain.ModelRequestMetadata {
	finishedAt := startedAt.Add(time.Millisecond)
	endpointHash := domain.SHA256Hex("https://model.invalid")
	providerRequestID := "provider-request"
	responseHash := domain.SHA256Hex("safe-response-derivative")
	inputTokens := int64(11)
	outputTokens := int64(13)
	latency := int64(1)
	return domain.ModelRequestMetadata{
		ID:                  id,
		RunID:               runID,
		Sequence:            sequence,
		ProviderKind:        domain.ModelProviderOpenAICompatible,
		EndpointOriginHash:  &endpointHash,
		Model:               "test-model",
		Status:              domain.ModelRequestStatusSucceeded,
		ProviderRequestID:   &providerRequestID,
		PromptVersion:       "prompt-v1",
		PromptFingerprint:   domain.SHA256Hex("safe-prompt-derivative"),
		ResponseFingerprint: &responseHash,
		InputTokens:         &inputTokens,
		OutputTokens:        &outputTokens,
		LatencyMilliseconds: &latency,
		StartedAt:           startedAt,
		FinishedAt:          &finishedAt,
	}
}
