package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	insertAgentRunSQL = `
		INSERT INTO agent_runs (
			id, session_id, request_message_id, retained_request_message_id, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, prompt_version, tool_catalog_version,
			step_count, tool_call_count, model_request_count,
			input_tokens, output_tokens, termination_reason,
			persistence_degraded, started_at_ms, finished_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getAgentRunByIDSQL = `
		SELECT
			id, session_id, request_message_id, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, prompt_version, tool_catalog_version,
			step_count, tool_call_count, model_request_count,
			input_tokens, output_tokens, termination_reason,
			persistence_degraded, started_at_ms, finished_at_ms
		FROM agent_runs
		WHERE id = ?
	`
	listRunningAgentRunsForRecoverySQL = `
		SELECT
			id, session_id, request_message_id, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, prompt_version, tool_catalog_version,
			step_count, tool_call_count, model_request_count,
			input_tokens, output_tokens, termination_reason,
			persistence_degraded, started_at_ms, finished_at_ms
		FROM agent_runs
		WHERE status = 'running'
		ORDER BY id
	`
	countRunningAgentRunsSQL = `
		SELECT count(id)
		FROM agent_runs
		WHERE status = 'running'
	`
	finishAgentRunSQL = `
		UPDATE agent_runs
		SET
			status = ?,
			step_count = ?,
			tool_call_count = ?,
			model_request_count = ?,
			input_tokens = ?,
			output_tokens = ?,
			termination_reason = ?,
			persistence_degraded = ?,
			finished_at_ms = ?
		WHERE id = ? AND status = 'running'
	`
	recoverInterruptedAgentRunsSQL = `
		UPDATE agent_runs
		SET
			status = 'interrupted',
			termination_reason = ?,
			finished_at_ms = CASE
				WHEN started_at_ms IS NOT NULL AND started_at_ms > ? THEN started_at_ms
				ELSE ?
			END
		WHERE status = 'running'
	`
)

var (
	_ sessioncontract.AgentRunStore = (*AgentRunRepository)(nil)
	_ sessioncontract.RunRecovery   = (*AgentRunRepository)(nil)
)

type agentRunRow struct {
	ID                  string         `db:"id"`
	SessionID           string         `db:"session_id"`
	RequestMessageID    string         `db:"request_message_id"`
	Status              string         `db:"status"`
	ScopeContext        string         `db:"scope_context"`
	ScopeNamespace      string         `db:"scope_namespace"`
	ScopeGeneration     int64          `db:"scope_generation"`
	ResourceRefsJSON    sql.NullString `db:"resource_refs_json"`
	PromptVersion       string         `db:"prompt_version"`
	ToolCatalogVersion  string         `db:"tool_catalog_version"`
	StepCount           int            `db:"step_count"`
	ToolCallCount       int            `db:"tool_call_count"`
	ModelRequestCount   int            `db:"model_request_count"`
	InputTokens         sql.NullInt64  `db:"input_tokens"`
	OutputTokens        sql.NullInt64  `db:"output_tokens"`
	TerminationReason   sql.NullString `db:"termination_reason"`
	PersistenceDegraded int64          `db:"persistence_degraded"`
	StartedAtMS         sql.NullInt64  `db:"started_at_ms"`
	FinishedAtMS        sql.NullInt64  `db:"finished_at_ms"`
}

// AgentRunRepository persists bounded run lifecycle metadata and safe Messages.
type AgentRunRepository struct {
	db *DB
}

// NewAgentRunRepository binds run lifecycle operations to one validated database.
func NewAgentRunRepository(db *DB) *AgentRunRepository {
	return &AgentRunRepository{db: db}
}

// Begin atomically inserts one running AgentRun and activity time. Standard
// mode also retains the request Message; minimal mode retains only its opaque
// identity in the run lifecycle record.
func (repository *AgentRunRepository) Begin(ctx context.Context, message domain.Message, run domain.AgentRun) error {
	if err := repositoryContext(ctx, repository.db, "begin_agent_run"); err != nil {
		return err
	}
	if !validBeginPair(message, run) {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		mode, err := activeSessionPrivacyMode(ctx, tx, run.SessionID)
		if err != nil {
			return err
		}
		var runningCount int
		if err := tx.GetContext(ctx, &runningCount, countRunningAgentRunsSQL); err != nil {
			return err
		}
		if runningCount != 0 {
			return sessioncontract.ErrAgentRunConflict
		}
		retainMessage := mode == domain.PrivacyModeStandard
		if retainMessage {
			if err := insertMessage(ctx, tx, message); err != nil {
				return err
			}
		}
		if err := insertAgentRun(ctx, tx, run, retainMessage); err != nil {
			return err
		}
		return touchSession(ctx, tx, run.SessionID, laterTime(message.CreatedAt, *run.StartedAt))
	})
	if isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "agent_run_begin_failed", "begin_agent_run", "Kupilot could not durably start the diagnostic run.", err)
	}
	return nil
}

// GetByID returns one exact strictly mapped AgentRun.
func (repository *AgentRunRepository) GetByID(ctx context.Context, id domain.AgentRunID) (domain.AgentRun, error) {
	if err := repositoryContext(ctx, repository.db, "get_agent_run"); err != nil {
		return domain.AgentRun{}, err
	}
	if !id.Valid() {
		return domain.AgentRun{}, sessioncontract.ErrInvalidRepositoryRequest
	}
	run, err := getAgentRun(ctx, repository.db.handle, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRun{}, sessioncontract.ErrAgentRunNotFound
	}
	if err != nil {
		return domain.AgentRun{}, repositoryFailure(repository.db, "agent_run_row_invalid", "get_agent_run", "Kupilot could not read the diagnostic run safely.", err)
	}
	return run, nil
}

// Finish transitions one matching running AgentRun to a terminal state.
func (repository *AgentRunRepository) Finish(ctx context.Context, run domain.AgentRun) error {
	if err := repositoryContext(ctx, repository.db, "finish_agent_run"); err != nil {
		return err
	}
	if run.Validate() != nil || !run.Status.Terminal() {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		current, err := getAgentRun(ctx, tx, run.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return sessioncontract.ErrAgentRunNotFound
		}
		if err != nil {
			return err
		}
		if domain.ValidateAgentRunTransition(current, run) != nil {
			return sessioncontract.ErrAgentRunConflict
		}
		if err := finishAgentRun(ctx, tx, run); err != nil {
			return err
		}
		return touchSession(ctx, tx, run.SessionID, *run.FinishedAt)
	})
	if isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "agent_run_finish_failed", "finish_agent_run", "Kupilot could not store the completed diagnostic run state.", err)
	}
	return nil
}

// FinishWithMessage atomically stores a final assistant Message and terminal run.
func (repository *AgentRunRepository) FinishWithMessage(ctx context.Context, message domain.Message, run domain.AgentRun) error {
	if err := repositoryContext(ctx, repository.db, "finish_agent_run_with_message"); err != nil {
		return err
	}
	if !validFinishPair(message, run) {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := ensureStandardActiveSession(ctx, tx, run.SessionID); err != nil {
			return err
		}
		current, err := getAgentRun(ctx, tx, run.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return sessioncontract.ErrAgentRunNotFound
		}
		if err != nil {
			return err
		}
		if domain.ValidateAgentRunTransition(current, run) != nil {
			return sessioncontract.ErrAgentRunConflict
		}
		if err := requireNextRunSequence(ctx, tx, run.ID, *message.RunSequence); err != nil {
			return err
		}
		if err := insertMessage(ctx, tx, message); err != nil {
			return err
		}
		if err := finishAgentRun(ctx, tx, run); err != nil {
			return err
		}
		return touchSession(ctx, tx, run.SessionID, laterTime(message.CreatedAt, *run.FinishedAt))
	})
	if isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "agent_run_finish_failed", "finish_agent_run_with_message", "Kupilot could not store the final diagnosis.", err)
	}
	return nil
}

// RecoverInterrupted atomically makes every durable running record terminal.
func (repository *AgentRunRepository) RecoverInterrupted(ctx context.Context, recoveredAt time.Time) (sessioncontract.RecoveryResult, error) {
	if err := repositoryContext(ctx, repository.db, "recover_interrupted_agent_runs"); err != nil {
		return sessioncontract.RecoveryResult{}, err
	}
	if recoveredAt.IsZero() || recoveredAt.UnixMilli() < 0 {
		return sessioncontract.RecoveryResult{}, sessioncontract.ErrInvalidRepositoryRequest
	}
	var interrupted int64
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := validateRunningAgentRunRows(ctx, tx); err != nil {
			return err
		}
		millis := recoveredAt.UTC().UnixMilli()
		result, err := tx.ExecContext(
			ctx,
			recoverInterruptedAgentRunsSQL,
			domain.InterruptedByRestartReason,
			millis,
			millis,
		)
		if err != nil {
			return err
		}
		interrupted, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return sessioncontract.RecoveryResult{}, repositoryFailure(repository.db, "agent_run_recovery_failed", "recover_interrupted_agent_runs", "Kupilot could not recover interrupted diagnostic runs.", err)
	}
	return sessioncontract.RecoveryResult{Interrupted: interrupted}, nil
}

func validBeginPair(message domain.Message, run domain.AgentRun) bool {
	return message.Validate() == nil && run.Validate() == nil &&
		message.Role == domain.MessageRoleUser &&
		message.Status == domain.MessageStatusCommitted &&
		run.Status == domain.AgentRunStatusRunning &&
		message.RunID != nil && *message.RunID == run.ID &&
		message.RunSequence != nil && *message.RunSequence == 0 &&
		message.SessionID == run.SessionID && message.ID == run.RequestMessageID &&
		messageMatchesRun(message, run) &&
		!message.CreatedAt.After(*run.StartedAt)
}

func validFinishPair(message domain.Message, run domain.AgentRun) bool {
	return message.Validate() == nil && run.Validate() == nil && run.Status.Terminal() &&
		message.Role == domain.MessageRoleAssistant &&
		message.Status == domain.MessageStatusCommitted &&
		message.RunID != nil && *message.RunID == run.ID &&
		message.RunSequence != nil && *message.RunSequence >= 1 &&
		message.SessionID == run.SessionID &&
		messageMatchesRun(message, run) &&
		!message.CreatedAt.Before(*run.StartedAt) &&
		!message.CreatedAt.After(*run.FinishedAt)
}

func messageMatchesRun(message domain.Message, run domain.AgentRun) bool {
	if message.Scope == nil || *message.Scope != run.Scope {
		return false
	}
	if message.Resource == nil || run.Resource == nil {
		return message.Resource == nil && run.Resource == nil
	}
	return *message.Resource == *run.Resource
}

func requireNextRunSequence(ctx context.Context, tx *sqlx.Tx, runID domain.AgentRunID, sequence int) error {
	var lastSequence sql.NullInt64
	if err := tx.GetContext(ctx, &lastSequence, `
		SELECT max(run_sequence)
		FROM messages
		WHERE run_id = ?
	`, runID); err != nil {
		return err
	}
	if !lastSequence.Valid || lastSequence.Int64+1 != int64(sequence) {
		return sessioncontract.ErrAgentRunConflict
	}
	return nil
}

func validateRunningAgentRunRows(ctx context.Context, tx *sqlx.Tx) error {
	rows, err := tx.QueryxContext(ctx, listRunningAgentRunsForRecoverySQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row agentRunRow
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		run, err := row.domainAgentRun()
		if err != nil {
			return err
		}
		if run.Status != domain.AgentRunStatusRunning {
			return domain.ErrInvalidAgentRun
		}
	}
	return rows.Err()
}

func insertAgentRun(ctx context.Context, tx *sqlx.Tx, run domain.AgentRun, retainRequestMessage bool) error {
	resourceRefs, err := encodeResourceRefs(run.Resource)
	if err != nil {
		return err
	}
	var retainedRequestMessageID any
	if retainRequestMessage {
		retainedRequestMessageID = run.RequestMessageID
	}
	_, err = tx.ExecContext(
		ctx,
		insertAgentRunSQL,
		run.ID,
		run.SessionID,
		run.RequestMessageID,
		retainedRequestMessageID,
		run.Status,
		run.Scope.Context,
		run.Scope.Namespace,
		run.Scope.Generation,
		nullableString(resourceRefs),
		run.PromptVersion,
		run.ToolCatalogVersion,
		run.StepCount,
		run.ToolCallCount,
		run.ModelRequestCount,
		nullableInt64(run.InputTokens),
		nullableInt64(run.OutputTokens),
		nullableString(optionalString(run.TerminationReason)),
		boolInteger(run.PersistenceDegraded),
		nullableTime(run.StartedAt),
		nullableTime(run.FinishedAt),
	)
	return err
}

func finishAgentRun(ctx context.Context, tx *sqlx.Tx, run domain.AgentRun) error {
	result, err := tx.ExecContext(
		ctx,
		finishAgentRunSQL,
		run.Status,
		run.StepCount,
		run.ToolCallCount,
		run.ModelRequestCount,
		nullableInt64(run.InputTokens),
		nullableInt64(run.OutputTokens),
		nullableString(optionalString(run.TerminationReason)),
		boolInteger(run.PersistenceDegraded),
		run.FinishedAt.UTC().UnixMilli(),
		run.ID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sessioncontract.ErrAgentRunConflict
	}
	return nil
}

func getAgentRun(ctx context.Context, getter strictGetter, id domain.AgentRunID) (domain.AgentRun, error) {
	var row agentRunRow
	if err := getter.GetContext(ctx, &row, getAgentRunByIDSQL, id); err != nil {
		return domain.AgentRun{}, err
	}
	return row.domainAgentRun()
}

func (row agentRunRow) domainAgentRun() (domain.AgentRun, error) {
	resource, err := decodeResourceRefs(row.ResourceRefsJSON)
	if err != nil {
		return domain.AgentRun{}, err
	}
	if row.PersistenceDegraded != 0 && row.PersistenceDegraded != 1 {
		return domain.AgentRun{}, domain.ErrInvalidAgentRun
	}
	value := domain.AgentRun{
		ID:                  domain.AgentRunID(row.ID),
		SessionID:           domain.SessionID(row.SessionID),
		RequestMessageID:    domain.MessageID(row.RequestMessageID),
		Status:              domain.AgentRunStatus(row.Status),
		Scope:               domain.ScopeSnapshot{Context: row.ScopeContext, Namespace: row.ScopeNamespace, Generation: row.ScopeGeneration},
		Resource:            resource,
		PromptVersion:       row.PromptVersion,
		ToolCatalogVersion:  row.ToolCatalogVersion,
		StepCount:           row.StepCount,
		ToolCallCount:       row.ToolCallCount,
		ModelRequestCount:   row.ModelRequestCount,
		InputTokens:         int64Pointer(row.InputTokens),
		OutputTokens:        int64Pointer(row.OutputTokens),
		TerminationReason:   stringPointer(row.TerminationReason),
		PersistenceDegraded: row.PersistenceDegraded == 1,
		StartedAt:           timePointerFromNull(row.StartedAtMS),
		FinishedAt:          timePointerFromNull(row.FinishedAtMS),
	}
	if err := value.Validate(); err != nil {
		return domain.AgentRun{}, err
	}
	return value, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMilli()
}

func timePointerFromNull(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result
}

func boolInteger(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
