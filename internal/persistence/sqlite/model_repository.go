package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	insertModelRequestSQL = `
		INSERT INTO model_requests (
			id, run_id, sequence, profile_name, model_role, invocation, reserved_cost_units,
			provider_kind, endpoint_origin_hash,
			model, status, error_class, provider_request_id,
			prompt_version, prompt_fingerprint, response_fingerprint,
			input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getModelRequestByIDSQL = `
		SELECT
			id, run_id, sequence, profile_name, model_role, invocation, reserved_cost_units,
			provider_kind, endpoint_origin_hash,
			model, status, error_class, provider_request_id,
			prompt_version, prompt_fingerprint, response_fingerprint,
			input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms
		FROM model_requests
		WHERE id = ?
	`
	listModelRequestsByRunSQL = `
		SELECT
			id, run_id, sequence, profile_name, model_role, invocation, reserved_cost_units,
			provider_kind, endpoint_origin_hash,
			model, status, error_class, provider_request_id,
			prompt_version, prompt_fingerprint, response_fingerprint,
			input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms
		FROM model_requests
		WHERE run_id = ?
		ORDER BY sequence, id
		LIMIT 64
	`
	getRunDetailPersistenceStateSQL = `
		SELECT
			r.session_id AS session_id,
			s.status AS session_status,
			s.privacy_mode AS privacy_mode
		FROM agent_runs AS r
		JOIN sessions AS s ON s.id = r.session_id
		WHERE r.id = ?
	`
)

var (
	// ErrModelRequestNotFound does not disclose any model request content.
	ErrModelRequestNotFound = errors.New("the requested model metadata was not found")
)

type modelRequestRow struct {
	ID                  string         `db:"id"`
	RunID               string         `db:"run_id"`
	Sequence            int            `db:"sequence"`
	ProfileName         string         `db:"profile_name"`
	ModelRole           string         `db:"model_role"`
	Invocation          string         `db:"invocation"`
	ReservedCostUnits   int            `db:"reserved_cost_units"`
	ProviderKind        string         `db:"provider_kind"`
	EndpointOriginHash  sql.NullString `db:"endpoint_origin_hash"`
	Model               string         `db:"model"`
	Status              string         `db:"status"`
	ErrorClass          sql.NullString `db:"error_class"`
	ProviderRequestID   sql.NullString `db:"provider_request_id"`
	PromptVersion       string         `db:"prompt_version"`
	PromptFingerprint   string         `db:"prompt_fingerprint"`
	ResponseFingerprint sql.NullString `db:"response_fingerprint"`
	InputTokens         sql.NullInt64  `db:"input_tokens"`
	OutputTokens        sql.NullInt64  `db:"output_tokens"`
	LatencyMilliseconds sql.NullInt64  `db:"latency_ms"`
	StartedAtMS         int64          `db:"started_at_ms"`
	FinishedAtMS        sql.NullInt64  `db:"finished_at_ms"`
}

type runDetailPersistenceRow struct {
	SessionID     string `db:"session_id"`
	SessionStatus string `db:"session_status"`
	PrivacyMode   string `db:"privacy_mode"`
}

// ModelRequestRepository persists only bounded request metadata and fingerprints.
type ModelRequestRepository struct {
	db *DB
}

// NewModelRequestRepository binds model metadata operations to one database.
func NewModelRequestRepository(db *DB) *ModelRequestRepository {
	return &ModelRequestRepository{db: db}
}

// Save inserts one independently eligible metadata record without any request body.
func (repository *ModelRequestRepository) Save(ctx context.Context, request domain.ModelRequestMetadata) error {
	if err := repositoryContext(ctx, repository.db, "save_model_request"); err != nil {
		return err
	}
	if err := request.Validate(); err != nil || !request.Status.Terminal() {
		return domain.ErrInvalidModelRequestMetadata
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := ensureRunAllowsDetail(ctx, tx, request.RunID); err != nil {
			return err
		}
		_, err := tx.ExecContext(
			ctx,
			insertModelRequestSQL,
			request.ID,
			request.RunID,
			request.Sequence,
			request.ProfileName,
			request.ModelRole,
			request.Invocation,
			request.ReservedCostUnits,
			request.ProviderKind,
			nullableString(optionalString(request.EndpointOriginHash)),
			request.Model,
			request.Status,
			nullableString(optionalSafeErrorClass(request.ErrorClass)),
			nullableString(optionalString(request.ProviderRequestID)),
			request.PromptVersion,
			request.PromptFingerprint,
			nullableString(optionalString(request.ResponseFingerprint)),
			nullableInt64(request.InputTokens),
			nullableInt64(request.OutputTokens),
			nullableInt64(request.LatencyMilliseconds),
			request.StartedAt.UTC().UnixMilli(),
			nullableTime(request.FinishedAt),
		)
		return err
	})
	if isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "model_request_save_failed", "save_model_request", "Kupilot could not store model request metadata.", err)
	}
	return nil
}

// GetByID returns one exact strictly mapped metadata record.
func (repository *ModelRequestRepository) GetByID(ctx context.Context, id domain.ModelRequestID) (domain.ModelRequestMetadata, error) {
	if err := repositoryContext(ctx, repository.db, "get_model_request"); err != nil {
		return domain.ModelRequestMetadata{}, err
	}
	if !id.Valid() {
		return domain.ModelRequestMetadata{}, domain.ErrInvalidModelRequestMetadata
	}
	var row modelRequestRow
	if err := repository.db.handle.GetContext(ctx, &row, getModelRequestByIDSQL, id); errors.Is(err, sql.ErrNoRows) {
		return domain.ModelRequestMetadata{}, ErrModelRequestNotFound
	} else if err != nil {
		return domain.ModelRequestMetadata{}, repositoryFailure(repository.db, "model_request_read_failed", "get_model_request", "Kupilot could not read model request metadata.", err)
	}
	request, err := row.domainModelRequest()
	if err != nil {
		return domain.ModelRequestMetadata{}, repositoryFailure(repository.db, "model_request_row_invalid", "get_model_request", "Kupilot could not read model request metadata safely.", err)
	}
	return request, nil
}

// ListByRun returns the bounded request metadata records in stable order.
func (repository *ModelRequestRepository) ListByRun(ctx context.Context, runID domain.AgentRunID) ([]domain.ModelRequestMetadata, error) {
	if err := repositoryContext(ctx, repository.db, "list_model_requests"); err != nil {
		return nil, err
	}
	if !runID.Valid() {
		return nil, domain.ErrInvalidModelRequestMetadata
	}
	rows, err := repository.db.handle.QueryxContext(ctx, listModelRequestsByRunSQL, runID)
	if err != nil {
		return nil, repositoryFailure(repository.db, "model_request_list_failed", "list_model_requests", "Kupilot could not list model request metadata.", err)
	}
	defer rows.Close()

	values := make([]domain.ModelRequestMetadata, 0, 3)
	for rows.Next() {
		var row modelRequestRow
		if err := rows.StructScan(&row); err != nil {
			return nil, repositoryFailure(repository.db, "model_request_row_invalid", "list_model_requests", "Kupilot could not read model request metadata safely.", err)
		}
		value, err := row.domainModelRequest()
		if err != nil || value.RunID != runID {
			return nil, repositoryFailure(repository.db, "model_request_row_invalid", "list_model_requests", "Kupilot could not read model request metadata safely.", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, repositoryFailure(repository.db, "model_request_list_failed", "list_model_requests", "Kupilot could not list model request metadata.", err)
	}
	return values, nil
}

func (row modelRequestRow) domainModelRequest() (domain.ModelRequestMetadata, error) {
	request := domain.ModelRequestMetadata{
		ID:                  domain.ModelRequestID(row.ID),
		RunID:               domain.AgentRunID(row.RunID),
		Sequence:            row.Sequence,
		ProfileName:         row.ProfileName,
		ModelRole:           domain.ModelRole(row.ModelRole),
		Invocation:          domain.ModelInvocation(row.Invocation),
		ReservedCostUnits:   row.ReservedCostUnits,
		ProviderKind:        domain.ModelProviderKind(row.ProviderKind),
		EndpointOriginHash:  stringPointer(row.EndpointOriginHash),
		Model:               row.Model,
		Status:              domain.ModelRequestStatus(row.Status),
		ErrorClass:          safeErrorClassPointer(row.ErrorClass),
		ProviderRequestID:   stringPointer(row.ProviderRequestID),
		PromptVersion:       row.PromptVersion,
		PromptFingerprint:   row.PromptFingerprint,
		ResponseFingerprint: stringPointer(row.ResponseFingerprint),
		InputTokens:         int64Pointer(row.InputTokens),
		OutputTokens:        int64Pointer(row.OutputTokens),
		LatencyMilliseconds: int64Pointer(row.LatencyMilliseconds),
		StartedAt:           time.UnixMilli(row.StartedAtMS).UTC(),
		FinishedAt:          timePointerFromNull(row.FinishedAtMS),
	}
	if err := request.Validate(); err != nil {
		return domain.ModelRequestMetadata{}, err
	}
	return request, nil
}

func ensureRunAllowsDetail(ctx context.Context, getter strictGetter, runID domain.AgentRunID) error {
	var state runDetailPersistenceRow
	if err := getter.GetContext(ctx, &state, getRunDetailPersistenceStateSQL, runID); errors.Is(err, sql.ErrNoRows) {
		return sessioncontract.ErrAgentRunNotFound
	} else if err != nil {
		return err
	}
	if state.PrivacyMode == string(domain.PrivacyModeMinimal) {
		return sessioncontract.ErrDurableContentDisabled
	}
	if state.PrivacyMode != string(domain.PrivacyModeStandard) || state.SessionStatus != string(domain.SessionStatusActive) {
		return sessioncontract.ErrSessionUnavailable
	}
	days, err := operationalDetailRetentionDays(ctx, getter, application.DefaultOperationalDetailRetentionDays)
	if err != nil {
		return err
	}
	if days == 0 {
		return sessioncontract.ErrDurableContentDisabled
	}
	return nil
}

func optionalSafeErrorClass(value *domain.SafeErrorClass) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func safeErrorClassPointer(value sql.NullString) *domain.SafeErrorClass {
	if !value.Valid {
		return nil
	}
	result := domain.SafeErrorClass(value.String)
	return &result
}
