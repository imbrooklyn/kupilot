package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	insertEvidenceSQL = `
		INSERT INTO evidence_items (
			id, run_id, invocation_id, category, resource_ref_json, fact,
			source_path, severity, resource_version, redaction_count,
			truncated, fingerprint, observed_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getEvidenceByIDSQL = `
		SELECT
			e.id AS id, e.run_id AS run_id, e.invocation_id AS invocation_id,
			e.category AS category, e.resource_ref_json AS resource_ref_json,
			e.fact AS fact, e.source_path AS source_path, e.severity AS severity,
			e.resource_version AS resource_version,
			e.redaction_count AS redaction_count, e.truncated AS truncated,
			e.fingerprint AS fingerprint, e.observed_at_ms AS observed_at_ms,
			r.scope_context AS scope_context,
			r.scope_namespace AS scope_namespace,
			r.scope_generation AS scope_generation,
			t.run_id AS invocation_run_id,
			t.started_at_ms AS invocation_started_at_ms,
			t.finished_at_ms AS invocation_finished_at_ms
		FROM evidence_items AS e
		JOIN agent_runs AS r ON r.id = e.run_id
		JOIN tool_invocations AS t ON t.id = e.invocation_id
		WHERE e.id = ?
	`
	listEvidenceByInvocationSQL = `
		SELECT
			e.id AS id, e.run_id AS run_id, e.invocation_id AS invocation_id,
			e.category AS category, e.resource_ref_json AS resource_ref_json,
			e.fact AS fact, e.source_path AS source_path, e.severity AS severity,
			e.resource_version AS resource_version,
			e.redaction_count AS redaction_count, e.truncated AS truncated,
			e.fingerprint AS fingerprint, e.observed_at_ms AS observed_at_ms,
			r.scope_context AS scope_context,
			r.scope_namespace AS scope_namespace,
			r.scope_generation AS scope_generation,
			t.run_id AS invocation_run_id,
			t.started_at_ms AS invocation_started_at_ms,
			t.finished_at_ms AS invocation_finished_at_ms
		FROM evidence_items AS e
		JOIN agent_runs AS r ON r.id = e.run_id
		JOIN tool_invocations AS t ON t.id = e.invocation_id
		WHERE e.invocation_id = ?
		ORDER BY e.observed_at_ms, e.id
		LIMIT 101
	`
)

var (
	// ErrEvidenceNotFound does not disclose whether supporting detail expired.
	ErrEvidenceNotFound = errors.New("the requested Evidence was not found")
)

type evidenceRow struct {
	ID                     string         `db:"id"`
	RunID                  string         `db:"run_id"`
	InvocationID           string         `db:"invocation_id"`
	Category               string         `db:"category"`
	ResourceRefJSON        string         `db:"resource_ref_json"`
	Fact                   string         `db:"fact"`
	SourcePath             sql.NullString `db:"source_path"`
	Severity               sql.NullString `db:"severity"`
	ResourceVersion        sql.NullString `db:"resource_version"`
	RedactionCount         int            `db:"redaction_count"`
	Truncated              int64          `db:"truncated"`
	Fingerprint            string         `db:"fingerprint"`
	ObservedAtMS           int64          `db:"observed_at_ms"`
	ScopeContext           string         `db:"scope_context"`
	ScopeNamespace         string         `db:"scope_namespace"`
	ScopeGeneration        int64          `db:"scope_generation"`
	InvocationRunID        string         `db:"invocation_run_id"`
	InvocationStartedAtMS  sql.NullInt64  `db:"invocation_started_at_ms"`
	InvocationFinishedAtMS sql.NullInt64  `db:"invocation_finished_at_ms"`
}

// EvidenceRepository reads only accepted concise Evidence derivatives.
type EvidenceRepository struct {
	db *DB
}

// NewEvidenceRepository binds Evidence reads to one validated database.
func NewEvidenceRepository(db *DB) *EvidenceRepository {
	return &EvidenceRepository{db: db}
}

// GetByID returns one exact strictly mapped Evidence item.
func (repository *EvidenceRepository) GetByID(ctx context.Context, id domain.EvidenceID) (domain.Evidence, error) {
	if err := repositoryContext(ctx, repository.db, "get_evidence"); err != nil {
		return domain.Evidence{}, err
	}
	if !id.Valid() {
		return domain.Evidence{}, domain.ErrInvalidEvidence
	}
	var row evidenceRow
	if err := repository.db.handle.GetContext(ctx, &row, getEvidenceByIDSQL, id); errors.Is(err, sql.ErrNoRows) {
		return domain.Evidence{}, ErrEvidenceNotFound
	} else if err != nil {
		return domain.Evidence{}, repositoryFailure(repository.db, "evidence_read_failed", "get_evidence", "KuPilot could not read the supporting observation.", err)
	}
	value, err := row.domainEvidence()
	if err != nil {
		return domain.Evidence{}, repositoryFailure(repository.db, "evidence_row_invalid", "get_evidence", "KuPilot could not read the supporting observation safely.", err)
	}
	return value, nil
}

// ListByInvocation returns at most one Tool result's accepted Evidence items.
func (repository *EvidenceRepository) ListByInvocation(ctx context.Context, invocationID domain.ToolInvocationID) ([]domain.Evidence, error) {
	if err := repositoryContext(ctx, repository.db, "list_invocation_evidence"); err != nil {
		return nil, err
	}
	if !invocationID.Valid() {
		return nil, domain.ErrInvalidEvidence
	}
	rows, err := repository.db.handle.QueryxContext(ctx, listEvidenceByInvocationSQL, invocationID)
	if err != nil {
		return nil, repositoryFailure(repository.db, "evidence_list_failed", "list_invocation_evidence", "KuPilot could not list supporting observations.", err)
	}
	defer rows.Close()
	values := make([]domain.Evidence, 0, 101)
	for rows.Next() {
		var row evidenceRow
		if err := rows.StructScan(&row); err != nil {
			return nil, repositoryFailure(repository.db, "evidence_row_invalid", "list_invocation_evidence", "KuPilot could not read a supporting observation safely.", err)
		}
		value, err := row.domainEvidence()
		if err != nil || value.InvocationID != invocationID {
			return nil, repositoryFailure(repository.db, "evidence_row_invalid", "list_invocation_evidence", "KuPilot could not read a supporting observation safely.", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, repositoryFailure(repository.db, "evidence_list_failed", "list_invocation_evidence", "KuPilot could not list supporting observations.", err)
	}
	if len(values) > 100 {
		return nil, repositoryFailure(repository.db, "evidence_row_invalid", "list_invocation_evidence", "KuPilot could not read a supporting observation safely.", domain.ErrInvalidEvidence)
	}
	return values, nil
}

func insertEvidence(ctx context.Context, tx *sqlx.Tx, evidence domain.Evidence) error {
	resourceJSON, err := encodeSelectedResource(&evidence.Resource)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		insertEvidenceSQL,
		evidence.ID,
		evidence.RunID,
		evidence.InvocationID,
		evidence.Category,
		resourceJSON,
		evidence.Fact,
		nullableString(optionalString(evidence.SourcePath)),
		nullableString(optionalEvidenceSeverity(evidence.Severity)),
		nullableString(evidence.Resource.ResourceVersion),
		evidence.RedactionCount,
		boolInteger(evidence.Truncated),
		evidence.Fingerprint,
		evidence.ObservedAt.UTC().UnixMilli(),
	)
	return err
}

func (row evidenceRow) domainEvidence() (domain.Evidence, error) {
	resource, err := decodeSelectedResource(sql.NullString{String: row.ResourceRefJSON, Valid: true})
	if err != nil || resource == nil || row.Truncated != 0 && row.Truncated != 1 ||
		row.InvocationRunID != row.RunID || row.InvocationStartedAtMS.Valid != row.InvocationFinishedAtMS.Valid {
		return domain.Evidence{}, domain.ErrInvalidEvidence
	}
	if row.ResourceVersion.Valid != (resource.ResourceVersion != "") || row.ResourceVersion.Valid && row.ResourceVersion.String != resource.ResourceVersion {
		return domain.Evidence{}, domain.ErrInvalidEvidence
	}
	value := domain.Evidence{
		ID:             domain.EvidenceID(row.ID),
		RunID:          domain.AgentRunID(row.RunID),
		InvocationID:   domain.ToolInvocationID(row.InvocationID),
		Category:       domain.EvidenceCategory(row.Category),
		Scope:          domain.ScopeSnapshot{Context: row.ScopeContext, Namespace: row.ScopeNamespace, Generation: row.ScopeGeneration},
		Resource:       *resource,
		Fact:           row.Fact,
		SourcePath:     stringPointer(row.SourcePath),
		Severity:       evidenceSeverityPointer(row.Severity),
		RedactionCount: row.RedactionCount,
		Truncated:      row.Truncated == 1,
		Fingerprint:    row.Fingerprint,
		ObservedAt:     time.UnixMilli(row.ObservedAtMS).UTC(),
	}
	if err := value.Validate(); err != nil {
		return domain.Evidence{}, err
	}
	startedAt := timePointerFromNull(row.InvocationStartedAtMS)
	finishedAt := timePointerFromNull(row.InvocationFinishedAtMS)
	if startedAt == nil || finishedAt == nil || value.ObservedAt.Before(*startedAt) || value.ObservedAt.After(*finishedAt) {
		return domain.Evidence{}, domain.ErrInvalidEvidence
	}
	return value, nil
}

func optionalEvidenceSeverity(value *domain.EvidenceSeverity) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func evidenceSeverityPointer(value sql.NullString) *domain.EvidenceSeverity {
	if !value.Valid {
		return nil
	}
	result := domain.EvidenceSeverity(value.String)
	return &result
}
