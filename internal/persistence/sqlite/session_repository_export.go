package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	getExportSessionSQL = `
		SELECT
			id, title, status, privacy_mode, last_context, last_namespace,
			created_at_ms, updated_at_ms
		FROM sessions
		WHERE id = ?
	`
	listExportMessagesSQL = `
		SELECT role, content, created_at_ms
		FROM messages
		WHERE session_id = ?
			AND status = 'committed'
			AND role IN ('user', 'assistant')
		ORDER BY created_at_ms ASC, id ASC
		LIMIT ?
	`
	listExportDiagnosesSQL = `
		SELECT
			d.id AS id, d.run_id AS run_id,
			d.confirmed_json AS confirmed_json,
			d.hypotheses_json AS hypotheses_json,
			d.missing_json AS missing_json,
			d.actions_json AS actions_json,
			d.answer_markdown AS answer_markdown,
			d.created_at_ms AS created_at_ms,
			r.scope_context AS scope_context,
			r.scope_namespace AS scope_namespace,
			r.scope_generation AS scope_generation
		FROM diagnoses AS d
		JOIN agent_runs AS r ON r.id = d.run_id
		WHERE r.session_id = ?
		ORDER BY d.created_at_ms ASC, d.id ASC
		LIMIT ?
	`
	getExportEvidenceSQL = `
		SELECT
			e.id AS id, e.run_id AS run_id, e.invocation_id AS invocation_id,
			e.category AS category, e.resource_ref_json AS resource_ref_json,
			e.fact AS fact, e.source_path AS source_path, e.severity AS severity,
			e.resource_version AS resource_version,
			e.redaction_count AS redaction_count, e.truncated AS truncated,
			e.resource_type_json AS resource_type_json,
			e.resource_policy_version AS resource_policy_version,
			e.policy_generation AS policy_generation, e.partial AS partial,
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
		WHERE r.session_id = ? AND e.id = ?
	`
)

var _ application.SessionExportReader = (*SessionRepository)(nil)

type exportSessionRow struct {
	ID            string         `db:"id"`
	Title         string         `db:"title"`
	Status        string         `db:"status"`
	PrivacyMode   string         `db:"privacy_mode"`
	LastContext   sql.NullString `db:"last_context"`
	LastNamespace sql.NullString `db:"last_namespace"`
	CreatedAtMS   int64          `db:"created_at_ms"`
	UpdatedAtMS   int64          `db:"updated_at_ms"`
}

type exportMessageRow struct {
	Role        string `db:"role"`
	Content     string `db:"content"`
	CreatedAtMS int64  `db:"created_at_ms"`
}

type exportDiagnosisRow struct {
	ID              string `db:"id"`
	RunID           string `db:"run_id"`
	ConfirmedJSON   string `db:"confirmed_json"`
	HypothesesJSON  string `db:"hypotheses_json"`
	MissingJSON     string `db:"missing_json"`
	ActionsJSON     string `db:"actions_json"`
	AnswerMarkdown  string `db:"answer_markdown"`
	CreatedAtMS     int64  `db:"created_at_ms"`
	ScopeContext    string `db:"scope_context"`
	ScopeNamespace  string `db:"scope_namespace"`
	ScopeGeneration int64  `db:"scope_generation"`
}

// ReadExportSnapshot loads one consistent, bounded, explicit export projection.
func (repository *SessionRepository) ReadExportSnapshot(
	ctx context.Context,
	sessionID domain.SessionID,
) (application.SessionExportSnapshot, error) {
	if err := repositoryContext(ctx, repository.db, "read_session_export"); err != nil {
		return application.SessionExportSnapshot{}, err
	}
	if !sessionID.Valid() {
		return application.SessionExportSnapshot{}, application.ErrSessionExportUnavailable
	}
	tx, err := repository.db.handle.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return application.SessionExportSnapshot{}, repositoryFailure(repository.db, "session_export_failed", "read_session_export", "Kupilot could not read the Session export safely.", err)
	}
	defer tx.Rollback()

	snapshot, err := readSessionExportSnapshot(ctx, tx, sessionID)
	if errors.Is(err, application.ErrSessionNotResumable) || errors.Is(err, application.ErrSessionExportUnavailable) {
		return application.SessionExportSnapshot{}, err
	}
	if err != nil {
		return application.SessionExportSnapshot{}, repositoryFailure(repository.db, "session_export_failed", "read_session_export", "Kupilot could not read the Session export safely.", err)
	}
	if err := tx.Commit(); err != nil {
		return application.SessionExportSnapshot{}, repositoryFailure(repository.db, "session_export_failed", "read_session_export", "Kupilot could not read the Session export safely.", err)
	}
	return snapshot, nil
}

func readSessionExportSnapshot(
	ctx context.Context,
	tx *sqlx.Tx,
	sessionID domain.SessionID,
) (application.SessionExportSnapshot, error) {
	var row exportSessionRow
	if err := tx.GetContext(ctx, &row, getExportSessionSQL, sessionID); errors.Is(err, sql.ErrNoRows) {
		return application.SessionExportSnapshot{}, application.ErrSessionExportUnavailable
	} else if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	if row.PrivacyMode == string(domain.PrivacyModeMinimal) {
		return application.SessionExportSnapshot{}, application.ErrSessionNotResumable
	}
	if row.Status != string(domain.SessionStatusActive) || row.PrivacyMode != string(domain.PrivacyModeStandard) {
		return application.SessionExportSnapshot{}, application.ErrSessionExportUnavailable
	}
	lastScope, err := decodeScopeCandidate(row.LastContext, row.LastNamespace)
	if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	snapshot := application.SessionExportSnapshot{Session: application.ExportSessionRecord{
		ID: domain.SessionID(row.ID), Title: row.Title, PrivacyMode: domain.PrivacyMode(row.PrivacyMode),
		LastScope: lastScope, CreatedAt: time.UnixMilli(row.CreatedAtMS).UTC(), UpdatedAt: time.UnixMilli(row.UpdatedAtMS).UTC(),
	}}
	if !snapshot.Session.ID.Valid() || snapshot.Session.CreatedAt.UnixMilli() < 0 ||
		snapshot.Session.UpdatedAt.Before(snapshot.Session.CreatedAt) {
		return application.SessionExportSnapshot{}, domain.ErrInvalidSession
	}

	messages, truncated, err := readExportMessages(ctx, tx, sessionID)
	if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	if len(messages) == 0 {
		return application.SessionExportSnapshot{}, application.ErrSessionExportUnavailable
	}
	snapshot.Messages = messages
	snapshot.Truncated = truncated
	contextSummary, err := readExportContextSummary(ctx, tx, sessionID)
	if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	snapshot.ContextSummary = contextSummary

	diagnoses, truncated, err := readExportDiagnoses(ctx, tx, sessionID)
	if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	snapshot.Diagnoses = diagnoses
	snapshot.Truncated = snapshot.Truncated || truncated

	evidenceIDs, truncated, err := application.ExportEvidenceReferences(diagnoses)
	if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	evidence, err := readExportEvidence(ctx, tx, sessionID, evidenceIDs)
	if err != nil {
		return application.SessionExportSnapshot{}, err
	}
	snapshot.Evidence = evidence
	snapshot.Truncated = snapshot.Truncated || truncated
	return snapshot, nil
}

func readExportContextSummary(
	ctx context.Context,
	tx *sqlx.Tx,
	sessionID domain.SessionID,
) (*domain.SessionContextSummary, error) {
	var row sessionContextSummaryRow
	if err := tx.GetContext(ctx, &row, loadSessionContextSummarySQL, sessionID); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	summary := row.domainSummary()
	if summary.Validate() != nil || summary.SessionID != sessionID {
		return nil, domain.ErrInvalidSessionContextSummary
	}
	return &summary, nil
}

func readExportMessages(ctx context.Context, tx *sqlx.Tx, sessionID domain.SessionID) ([]application.ExportMessageRecord, bool, error) {
	rows, err := tx.QueryxContext(ctx, listExportMessagesSQL, sessionID, application.MaxExportMessages+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]application.ExportMessageRecord, 0, application.MaxExportMessages+1)
	for rows.Next() {
		var row exportMessageRow
		if err := rows.StructScan(&row); err != nil {
			return nil, false, err
		}
		role := domain.MessageRole(row.Role)
		createdAt := time.UnixMilli(row.CreatedAtMS).UTC()
		if (role != domain.MessageRoleUser && role != domain.MessageRoleAssistant) || row.Content == "" || row.CreatedAtMS < 0 {
			return nil, false, domain.ErrInvalidMessage
		}
		values = append(values, application.ExportMessageRecord{Role: role, Content: row.Content, CreatedAt: createdAt})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(values) > application.MaxExportMessages
	if truncated {
		values = values[:application.MaxExportMessages]
	}
	return values, truncated, nil
}

func readExportDiagnoses(ctx context.Context, tx *sqlx.Tx, sessionID domain.SessionID) ([]application.ExportDiagnosisRecord, bool, error) {
	rows, err := tx.QueryxContext(ctx, listExportDiagnosesSQL, sessionID, application.MaxExportDiagnoses+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	values := make([]application.ExportDiagnosisRecord, 0, application.MaxExportDiagnoses+1)
	for rows.Next() {
		var row exportDiagnosisRow
		if err := rows.StructScan(&row); err != nil {
			return nil, false, err
		}
		value, err := row.exportRecord()
		if err != nil {
			return nil, false, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(values) > application.MaxExportDiagnoses
	if truncated {
		values = values[:application.MaxExportDiagnoses]
	}
	return values, truncated, nil
}

func (row exportDiagnosisRow) exportRecord() (application.ExportDiagnosisRecord, error) {
	var confirmed []domain.ConfirmedFact
	if err := decodeStrictJSON(row.ConfirmedJSON, &confirmed); err != nil {
		return application.ExportDiagnosisRecord{}, err
	}
	var hypotheses []domain.Hypothesis
	if err := decodeStrictJSON(row.HypothesesJSON, &hypotheses); err != nil {
		return application.ExportDiagnosisRecord{}, err
	}
	var missing []domain.MissingInformation
	if err := decodeStrictJSON(row.MissingJSON, &missing); err != nil {
		return application.ExportDiagnosisRecord{}, err
	}
	var actions []domain.RecommendedAction
	if err := decodeStrictJSON(row.ActionsJSON, &actions); err != nil {
		return application.ExportDiagnosisRecord{}, err
	}
	createdAt := time.UnixMilli(row.CreatedAtMS).UTC()
	validation := domain.Diagnosis{
		ID: domain.DiagnosisID(row.ID), RunID: domain.AgentRunID(row.RunID),
		Scope:          domain.ScopeSnapshot{Context: row.ScopeContext, Namespace: row.ScopeNamespace, Generation: row.ScopeGeneration},
		ConfirmedFacts: confirmed, Hypotheses: hypotheses, MissingInformation: missing, RecommendedActions: actions,
		AnswerMarkdown: row.AnswerMarkdown, CreatedAt: createdAt,
	}
	if validation.Validate() != nil {
		return application.ExportDiagnosisRecord{}, domain.ErrInvalidDiagnosis
	}
	return application.ExportDiagnosisRecord{
		AnswerMarkdown: row.AnswerMarkdown,
		ConfirmedFacts: confirmed, Hypotheses: hypotheses, MissingInformation: missing,
		RecommendedActions: actions, CreatedAt: createdAt,
	}, nil
}

func readExportEvidence(
	ctx context.Context,
	tx *sqlx.Tx,
	sessionID domain.SessionID,
	evidenceIDs []domain.EvidenceID,
) ([]application.ExportEvidenceRecord, error) {
	values := make([]application.ExportEvidenceRecord, 0, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		var row evidenceRow
		if err := tx.GetContext(ctx, &row, getExportEvidenceSQL, sessionID, evidenceID); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, err
		}
		value, err := row.domainEvidence()
		if err != nil {
			return nil, err
		}
		resource := value.Resource
		resource.UID = ""
		resource.ResourceVersion = ""
		values = append(values, application.ExportEvidenceRecord{
			ID: value.ID, Category: value.Category, Resource: resource, ResourceType: value.ResourceType,
			PolicyVersion: value.PolicyVersion, PolicyGeneration: value.PolicyGeneration, Fact: value.Fact,
			SourcePath: optionalExportString(value.SourcePath), ObservedAt: value.ObservedAt,
			RedactionCount: value.RedactionCount, Truncated: value.Truncated, Partial: value.Partial,
		})
	}
	return values, nil
}

func optionalExportString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
