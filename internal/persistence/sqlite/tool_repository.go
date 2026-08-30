package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	insertToolInvocationSQL = `
		INSERT INTO tool_invocations (
			id, run_id, sequence, tool_name, tool_version, purpose,
			arguments_json, arguments_digest, status, error_class, safe_error,
			result_summary, returned_bytes, evidence_count, truncated,
			started_at_ms, finished_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getToolInvocationByIDSQL = `
		SELECT
			t.id AS id, t.run_id AS run_id, t.sequence AS sequence,
			t.tool_name AS tool_name, t.tool_version AS tool_version,
			t.purpose AS purpose, t.arguments_json AS arguments_json,
			t.arguments_digest AS arguments_digest, t.status AS status,
			t.error_class AS error_class, t.safe_error AS safe_error,
			t.result_summary AS result_summary, t.returned_bytes AS returned_bytes,
			t.evidence_count AS evidence_count, t.truncated AS truncated,
			t.started_at_ms AS started_at_ms, t.finished_at_ms AS finished_at_ms,
			r.scope_context AS scope_context,
			r.scope_namespace AS scope_namespace,
			r.scope_generation AS scope_generation
		FROM tool_invocations AS t
		JOIN agent_runs AS r ON r.id = t.run_id
		WHERE t.id = ?
	`
	listToolInvocationsByRunSQL = `
		SELECT
			t.id AS id, t.run_id AS run_id, t.sequence AS sequence,
			t.tool_name AS tool_name, t.tool_version AS tool_version,
			t.purpose AS purpose, t.arguments_json AS arguments_json,
			t.arguments_digest AS arguments_digest, t.status AS status,
			t.error_class AS error_class, t.safe_error AS safe_error,
			t.result_summary AS result_summary, t.returned_bytes AS returned_bytes,
			t.evidence_count AS evidence_count, t.truncated AS truncated,
			t.started_at_ms AS started_at_ms, t.finished_at_ms AS finished_at_ms,
			r.scope_context AS scope_context,
			r.scope_namespace AS scope_namespace,
			r.scope_generation AS scope_generation
		FROM tool_invocations AS t
		JOIN agent_runs AS r ON r.id = t.run_id
		WHERE t.run_id = ?
		ORDER BY t.sequence, t.id
		LIMIT 256
	`
)

var (
	// ErrToolInvocationNotFound does not disclose Tool arguments or results.
	ErrToolInvocationNotFound = errors.New("the requested ToolInvocation was not found")
)

type toolInvocationRow struct {
	ID              string         `db:"id"`
	RunID           string         `db:"run_id"`
	Sequence        int            `db:"sequence"`
	ToolName        string         `db:"tool_name"`
	ToolVersion     string         `db:"tool_version"`
	Purpose         sql.NullString `db:"purpose"`
	ArgumentsJSON   string         `db:"arguments_json"`
	ArgumentsDigest string         `db:"arguments_digest"`
	Status          string         `db:"status"`
	ErrorClass      sql.NullString `db:"error_class"`
	SafeError       sql.NullString `db:"safe_error"`
	ResultSummary   sql.NullString `db:"result_summary"`
	ReturnedBytes   int            `db:"returned_bytes"`
	EvidenceCount   int            `db:"evidence_count"`
	Truncated       int64          `db:"truncated"`
	StartedAtMS     sql.NullInt64  `db:"started_at_ms"`
	FinishedAtMS    sql.NullInt64  `db:"finished_at_ms"`
	ScopeContext    string         `db:"scope_context"`
	ScopeNamespace  string         `db:"scope_namespace"`
	ScopeGeneration int64          `db:"scope_generation"`
}

// ToolInvocationRepository atomically stores safe invocation metadata and Evidence.
type ToolInvocationRepository struct {
	db *DB
}

// NewToolInvocationRepository binds Tool persistence to one validated database.
func NewToolInvocationRepository(db *DB) *ToolInvocationRepository {
	return &ToolInvocationRepository{db: db}
}

// Save atomically stores one terminal invocation and its accepted Evidence.
func (repository *ToolInvocationRepository) Save(ctx context.Context, invocation domain.ToolInvocation, evidence []domain.Evidence) error {
	if err := repositoryContext(ctx, repository.db, "save_tool_invocation"); err != nil {
		return err
	}
	if err := invocation.Validate(); err != nil {
		return err
	}
	if !invocation.Status.Terminal() || len(evidence) != invocation.EvidenceCount || len(evidence) > 100 {
		return domain.ErrInvalidToolInvocation
	}
	for _, item := range evidence {
		if err := validateInvocationEvidence(invocation, item); err != nil {
			return err
		}
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := ensureRunAllowsDetail(ctx, tx, invocation.RunID); err != nil {
			return err
		}
		run, err := getAgentRun(ctx, tx, invocation.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			return sessioncontract.ErrAgentRunNotFound
		}
		if err != nil {
			return err
		}
		if run.Scope != invocation.Scope {
			return domain.ErrInvalidToolInvocation
		}
		if err := insertToolInvocation(ctx, tx, invocation); err != nil {
			return err
		}
		for _, item := range evidence {
			if err := insertEvidence(ctx, tx, item); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, domain.ErrInvalidToolInvocation) || errors.Is(err, domain.ErrInvalidEvidence) || isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "tool_invocation_save_failed", "save_tool_invocation", "Kupilot could not store safe tool-activity metadata.", err)
	}
	return nil
}

// GetByID returns one exact invocation with scope projected from its owning run.
func (repository *ToolInvocationRepository) GetByID(ctx context.Context, id domain.ToolInvocationID) (domain.ToolInvocation, error) {
	if err := repositoryContext(ctx, repository.db, "get_tool_invocation"); err != nil {
		return domain.ToolInvocation{}, err
	}
	if !id.Valid() {
		return domain.ToolInvocation{}, domain.ErrInvalidToolInvocation
	}
	var row toolInvocationRow
	if err := repository.db.handle.GetContext(ctx, &row, getToolInvocationByIDSQL, id); errors.Is(err, sql.ErrNoRows) {
		return domain.ToolInvocation{}, ErrToolInvocationNotFound
	} else if err != nil {
		return domain.ToolInvocation{}, repositoryFailure(repository.db, "tool_invocation_read_failed", "get_tool_invocation", "Kupilot could not read tool-activity metadata.", err)
	}
	value, err := row.domainToolInvocation()
	if err != nil {
		return domain.ToolInvocation{}, repositoryFailure(repository.db, "tool_invocation_row_invalid", "get_tool_invocation", "Kupilot could not read tool-activity metadata safely.", err)
	}
	return value, nil
}

// ListByRun returns the bounded invocations in stable sequence order.
func (repository *ToolInvocationRepository) ListByRun(ctx context.Context, runID domain.AgentRunID) ([]domain.ToolInvocation, error) {
	if err := repositoryContext(ctx, repository.db, "list_tool_invocations"); err != nil {
		return nil, err
	}
	if !runID.Valid() {
		return nil, domain.ErrInvalidToolInvocation
	}
	rows, err := repository.db.handle.QueryxContext(ctx, listToolInvocationsByRunSQL, runID)
	if err != nil {
		return nil, repositoryFailure(repository.db, "tool_invocation_list_failed", "list_tool_invocations", "Kupilot could not list tool-activity metadata.", err)
	}
	defer rows.Close()
	values := make([]domain.ToolInvocation, 0, 10)
	for rows.Next() {
		var row toolInvocationRow
		if err := rows.StructScan(&row); err != nil {
			return nil, repositoryFailure(repository.db, "tool_invocation_row_invalid", "list_tool_invocations", "Kupilot could not read tool-activity metadata safely.", err)
		}
		value, err := row.domainToolInvocation()
		if err != nil || value.RunID != runID {
			return nil, repositoryFailure(repository.db, "tool_invocation_row_invalid", "list_tool_invocations", "Kupilot could not read tool-activity metadata safely.", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, repositoryFailure(repository.db, "tool_invocation_list_failed", "list_tool_invocations", "Kupilot could not list tool-activity metadata.", err)
	}
	return values, nil
}

func insertToolInvocation(ctx context.Context, tx *sqlx.Tx, invocation domain.ToolInvocation) error {
	_, err := tx.ExecContext(
		ctx,
		insertToolInvocationSQL,
		invocation.ID,
		invocation.RunID,
		invocation.Sequence,
		invocation.Name,
		invocation.Version,
		nullableString(optionalString(invocation.Purpose)),
		invocation.ArgumentsJSON,
		invocation.ArgumentsDigest,
		invocation.Status,
		nullableString(optionalSafeErrorClass(invocation.ErrorClass)),
		nullableString(optionalString(invocation.SafeError)),
		nullableString(optionalString(invocation.ResultSummary)),
		invocation.ReturnedBytes,
		invocation.EvidenceCount,
		boolInteger(invocation.Truncated),
		nullableTime(invocation.StartedAt),
		nullableTime(invocation.FinishedAt),
	)
	return err
}

func (row toolInvocationRow) domainToolInvocation() (domain.ToolInvocation, error) {
	if row.Truncated != 0 && row.Truncated != 1 {
		return domain.ToolInvocation{}, domain.ErrInvalidToolInvocation
	}
	value := domain.ToolInvocation{
		ID:              domain.ToolInvocationID(row.ID),
		RunID:           domain.AgentRunID(row.RunID),
		Sequence:        row.Sequence,
		Name:            domain.ToolName(row.ToolName),
		Version:         row.ToolVersion,
		Purpose:         stringPointer(row.Purpose),
		Scope:           domain.ScopeSnapshot{Context: row.ScopeContext, Namespace: row.ScopeNamespace, Generation: row.ScopeGeneration},
		ArgumentsJSON:   row.ArgumentsJSON,
		ArgumentsDigest: row.ArgumentsDigest,
		Status:          domain.ToolInvocationStatus(row.Status),
		ErrorClass:      safeErrorClassPointer(row.ErrorClass),
		SafeError:       stringPointer(row.SafeError),
		ResultSummary:   stringPointer(row.ResultSummary),
		ReturnedBytes:   row.ReturnedBytes,
		EvidenceCount:   row.EvidenceCount,
		Truncated:       row.Truncated == 1,
		StartedAt:       timePointerFromNull(row.StartedAtMS),
		FinishedAt:      timePointerFromNull(row.FinishedAtMS),
	}
	if err := value.Validate(); err != nil {
		return domain.ToolInvocation{}, err
	}
	return value, nil
}

func validateInvocationEvidence(invocation domain.ToolInvocation, evidence domain.Evidence) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if evidence.RunID != invocation.RunID || evidence.InvocationID != invocation.ID || evidence.Scope != invocation.Scope ||
		evidence.ObservedAt.Before(*invocation.StartedAt) || evidence.ObservedAt.After(*invocation.FinishedAt) {
		return domain.ErrInvalidEvidence
	}
	return nil
}
