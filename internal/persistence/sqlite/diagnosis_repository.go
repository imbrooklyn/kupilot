package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	insertDiagnosisSQL = `
		INSERT INTO diagnoses (
			id, run_id, confirmed_json, hypotheses_json, missing_json,
			actions_json, answer_markdown, validation_warnings_json,
			observed_from_ms, observed_to_ms, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getDiagnosisByRunIDSQL = `
		SELECT
			d.id AS id, d.run_id AS run_id, d.confirmed_json AS confirmed_json,
			d.hypotheses_json AS hypotheses_json, d.missing_json AS missing_json,
			d.actions_json AS actions_json, d.answer_markdown AS answer_markdown,
			d.validation_warnings_json AS validation_warnings_json,
			d.observed_from_ms AS observed_from_ms,
			d.observed_to_ms AS observed_to_ms, d.created_at_ms AS created_at_ms,
			r.scope_context AS scope_context,
			r.scope_namespace AS scope_namespace,
			r.scope_generation AS scope_generation
		FROM diagnoses AS d
		JOIN agent_runs AS r ON r.id = d.run_id
		WHERE d.run_id = ?
	`
	getDiagnosisEvidenceStateSQL = `
		SELECT run_id, truncated, observed_at_ms
		FROM evidence_items
		WHERE id = ?
	`
	getDiagnosisEvidenceWindowSQL = `
		SELECT
			COUNT(e.id) AS evidence_count,
			MIN(e.observed_at_ms) AS observed_from_ms,
			MAX(e.observed_at_ms) AS observed_to_ms,
			CASE
				WHEN COALESCE(MAX(e.truncated), 0) = 1 OR COALESCE((
					SELECT MAX(t.truncated)
					FROM tool_invocations AS t
					WHERE t.run_id = ?
				), 0) = 1 THEN 1
				ELSE 0
			END AS truncated
		FROM evidence_items AS e
		WHERE e.run_id = ?
	`
)

var (
	// ErrDiagnosisNotFound does not distinguish expiry from an unknown identifier.
	ErrDiagnosisNotFound = errors.New("the requested Diagnosis was not found")
	// ErrDiagnosisEvidenceInvalid reports inconsistent same-run Evidence references or observation state.
	ErrDiagnosisEvidenceInvalid = errors.New("Diagnosis Evidence references are invalid")
)

type diagnosisRow struct {
	ID                     string         `db:"id"`
	RunID                  string         `db:"run_id"`
	ConfirmedJSON          string         `db:"confirmed_json"`
	HypothesesJSON         string         `db:"hypotheses_json"`
	MissingJSON            string         `db:"missing_json"`
	ActionsJSON            string         `db:"actions_json"`
	AnswerMarkdown         string         `db:"answer_markdown"`
	ValidationWarningsJSON sql.NullString `db:"validation_warnings_json"`
	ObservedFromMS         sql.NullInt64  `db:"observed_from_ms"`
	ObservedToMS           sql.NullInt64  `db:"observed_to_ms"`
	CreatedAtMS            int64          `db:"created_at_ms"`
	ScopeContext           string         `db:"scope_context"`
	ScopeNamespace         string         `db:"scope_namespace"`
	ScopeGeneration        int64          `db:"scope_generation"`
}

type diagnosisEvidenceStateRow struct {
	RunID        string `db:"run_id"`
	Truncated    int64  `db:"truncated"`
	ObservedAtMS int64  `db:"observed_at_ms"`
}

type diagnosisEvidenceWindowRow struct {
	EvidenceCount  int64         `db:"evidence_count"`
	ObservedFromMS sql.NullInt64 `db:"observed_from_ms"`
	ObservedToMS   sql.NullInt64 `db:"observed_to_ms"`
	Truncated      int64         `db:"truncated"`
}

type diagnosisEvidenceSummary struct {
	State         domain.EvidenceDetailState
	Referenced    int
	Found         int
	EvidenceCount int64
	ObservedFrom  *time.Time
	ObservedTo    *time.Time
}

// DiagnosisRepository persists only locally validated structured Diagnoses.
type DiagnosisRepository struct {
	db *DB
}

// NewDiagnosisRepository binds Diagnosis operations to one validated database.
func NewDiagnosisRepository(db *DB) *DiagnosisRepository {
	return &DiagnosisRepository{db: db}
}

// Save verifies same-run Evidence and inserts one immutable Diagnosis.
func (repository *DiagnosisRepository) Save(ctx context.Context, diagnosis domain.Diagnosis) error {
	if err := repositoryContext(ctx, repository.db, "save_diagnosis"); err != nil {
		return err
	}
	if err := diagnosis.Validate(); err != nil {
		return err
	}
	confirmedJSON, hypothesesJSON, missingJSON, actionsJSON, warningsJSON, err := encodeDiagnosis(diagnosis)
	if err != nil {
		return domain.ErrInvalidDiagnosis
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := ensureRunAllowsDetail(ctx, tx, diagnosis.RunID); err != nil {
			return err
		}
		run, err := getAgentRun(ctx, tx, diagnosis.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			return sessioncontract.ErrAgentRunNotFound
		}
		if err != nil {
			return err
		}
		if run.Scope != diagnosis.Scope {
			return domain.ErrInvalidDiagnosis
		}
		summary, err := summarizeDiagnosisEvidence(ctx, tx, diagnosis)
		if err != nil {
			return err
		}
		if summary.Found != summary.Referenced || !diagnosisWindowMatches(diagnosis, summary) ||
			diagnosis.EvidenceDetailsState != "" && diagnosis.EvidenceDetailsState != summary.State {
			return ErrDiagnosisEvidenceInvalid
		}
		_, err = tx.ExecContext(
			ctx,
			insertDiagnosisSQL,
			diagnosis.ID,
			diagnosis.RunID,
			confirmedJSON,
			hypothesesJSON,
			missingJSON,
			actionsJSON,
			diagnosis.AnswerMarkdown,
			nullableString(warningsJSON),
			nullableTime(diagnosis.ObservedFrom),
			nullableTime(diagnosis.ObservedTo),
			diagnosis.CreatedAt.UTC().UnixMilli(),
		)
		return err
	})
	if errors.Is(err, domain.ErrInvalidDiagnosis) || errors.Is(err, ErrDiagnosisEvidenceInvalid) || isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "diagnosis_save_failed", "save_diagnosis", "KuPilot could not store the validated Diagnosis.", err)
	}
	return nil
}

// GetByRunID returns historic text with a derived Evidence detail state.
func (repository *DiagnosisRepository) GetByRunID(ctx context.Context, runID domain.AgentRunID) (domain.Diagnosis, error) {
	if err := repositoryContext(ctx, repository.db, "get_diagnosis"); err != nil {
		return domain.Diagnosis{}, err
	}
	if !runID.Valid() {
		return domain.Diagnosis{}, domain.ErrInvalidDiagnosis
	}
	var row diagnosisRow
	if err := repository.db.handle.GetContext(ctx, &row, getDiagnosisByRunIDSQL, runID); errors.Is(err, sql.ErrNoRows) {
		return domain.Diagnosis{}, ErrDiagnosisNotFound
	} else if err != nil {
		return domain.Diagnosis{}, repositoryFailure(repository.db, "diagnosis_read_failed", "get_diagnosis", "KuPilot could not read the Diagnosis.", err)
	}
	diagnosis, err := row.domainDiagnosis()
	if err != nil || diagnosis.RunID != runID {
		return domain.Diagnosis{}, repositoryFailure(repository.db, "diagnosis_row_invalid", "get_diagnosis", "KuPilot could not read the Diagnosis safely.", err)
	}
	summary, err := summarizeDiagnosisEvidence(ctx, repository.db.handle, diagnosis)
	if err != nil {
		return domain.Diagnosis{}, repositoryFailure(repository.db, "diagnosis_evidence_read_failed", "get_diagnosis", "KuPilot could not determine whether historic observation details remain available.", err)
	}
	diagnosis.EvidenceDetailsState = summary.State
	if err := diagnosis.Validate(); err != nil {
		return domain.Diagnosis{}, repositoryFailure(repository.db, "diagnosis_row_invalid", "get_diagnosis", "KuPilot could not read the Diagnosis safely.", err)
	}
	return diagnosis, nil
}

func encodeDiagnosis(diagnosis domain.Diagnosis) (string, string, string, string, string, error) {
	confirmed, err := json.Marshal(diagnosis.ConfirmedFacts)
	if err != nil {
		return "", "", "", "", "", err
	}
	hypotheses, err := json.Marshal(diagnosis.Hypotheses)
	if err != nil {
		return "", "", "", "", "", err
	}
	missing, err := json.Marshal(diagnosis.MissingInformation)
	if err != nil {
		return "", "", "", "", "", err
	}
	actions, err := json.Marshal(diagnosis.RecommendedActions)
	if err != nil {
		return "", "", "", "", "", err
	}
	warnings := ""
	if diagnosis.ValidationWarnings != nil {
		encoded, err := json.Marshal(diagnosis.ValidationWarnings)
		if err != nil {
			return "", "", "", "", "", err
		}
		warnings = string(encoded)
	}
	return string(confirmed), string(hypotheses), string(missing), string(actions), warnings, nil
}

func (row diagnosisRow) domainDiagnosis() (domain.Diagnosis, error) {
	var confirmed []domain.ConfirmedFact
	if err := decodeStrictJSON(row.ConfirmedJSON, &confirmed); err != nil {
		return domain.Diagnosis{}, err
	}
	var hypotheses []domain.Hypothesis
	if err := decodeStrictJSON(row.HypothesesJSON, &hypotheses); err != nil {
		return domain.Diagnosis{}, err
	}
	var missing []domain.MissingInformation
	if err := decodeStrictJSON(row.MissingJSON, &missing); err != nil {
		return domain.Diagnosis{}, err
	}
	var actions []domain.RecommendedAction
	if err := decodeStrictJSON(row.ActionsJSON, &actions); err != nil {
		return domain.Diagnosis{}, err
	}
	var warnings []string
	if row.ValidationWarningsJSON.Valid {
		if err := decodeStrictJSON(row.ValidationWarningsJSON.String, &warnings); err != nil {
			return domain.Diagnosis{}, err
		}
	}
	value := domain.Diagnosis{
		ID:                 domain.DiagnosisID(row.ID),
		RunID:              domain.AgentRunID(row.RunID),
		Scope:              domain.ScopeSnapshot{Context: row.ScopeContext, Namespace: row.ScopeNamespace, Generation: row.ScopeGeneration},
		ConfirmedFacts:     confirmed,
		Hypotheses:         hypotheses,
		MissingInformation: missing,
		RecommendedActions: actions,
		AnswerMarkdown:     row.AnswerMarkdown,
		ValidationWarnings: warnings,
		ObservedFrom:       timePointerFromNull(row.ObservedFromMS),
		ObservedTo:         timePointerFromNull(row.ObservedToMS),
		CreatedAt:          time.UnixMilli(row.CreatedAtMS).UTC(),
	}
	if err := value.Validate(); err != nil {
		return domain.Diagnosis{}, err
	}
	return value, nil
}

func summarizeDiagnosisEvidence(ctx context.Context, getter strictGetter, diagnosis domain.Diagnosis) (diagnosisEvidenceSummary, error) {
	ids := diagnosis.ReferencedEvidenceIDs()
	var window diagnosisEvidenceWindowRow
	if err := getter.GetContext(ctx, &window, getDiagnosisEvidenceWindowSQL, diagnosis.RunID, diagnosis.RunID); err != nil {
		return diagnosisEvidenceSummary{}, err
	}
	if window.EvidenceCount < 0 || window.Truncated != 0 && window.Truncated != 1 ||
		window.EvidenceCount == 0 && (window.ObservedFromMS.Valid || window.ObservedToMS.Valid) ||
		window.EvidenceCount > 0 && (!window.ObservedFromMS.Valid || !window.ObservedToMS.Valid ||
			window.ObservedFromMS.Int64 < 0 || window.ObservedToMS.Int64 < window.ObservedFromMS.Int64) {
		return diagnosisEvidenceSummary{}, ErrDiagnosisEvidenceInvalid
	}
	summary := diagnosisEvidenceSummary{
		State:         domain.EvidenceDetailAvailable,
		Referenced:    len(ids),
		EvidenceCount: window.EvidenceCount,
	}
	if window.EvidenceCount > 0 {
		observedFrom := time.UnixMilli(window.ObservedFromMS.Int64).UTC()
		observedTo := time.UnixMilli(window.ObservedToMS.Int64).UTC()
		summary.ObservedFrom = &observedFrom
		summary.ObservedTo = &observedTo
	}
	if window.Truncated == 1 {
		summary.State = domain.EvidenceDetailPartial
	}
	for _, id := range ids {
		var row diagnosisEvidenceStateRow
		if err := getter.GetContext(ctx, &row, getDiagnosisEvidenceStateSQL, id); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return diagnosisEvidenceSummary{}, err
		}
		if domain.AgentRunID(row.RunID) != diagnosis.RunID || row.Truncated != 0 && row.Truncated != 1 || row.ObservedAtMS < 0 {
			return diagnosisEvidenceSummary{}, ErrDiagnosisEvidenceInvalid
		}
		summary.Found++
	}
	if (summary.EvidenceCount == 0 && diagnosis.ObservedFrom != nil) ||
		(summary.Referenced > 0 && summary.Found == 0) {
		summary.State = domain.EvidenceDetailExpired
	} else if summary.Found < summary.Referenced || !diagnosisWindowMatches(diagnosis, summary) {
		summary.State = domain.EvidenceDetailPartial
	}
	return summary, nil
}

func diagnosisWindowMatches(diagnosis domain.Diagnosis, summary diagnosisEvidenceSummary) bool {
	if summary.EvidenceCount == 0 {
		return diagnosis.ObservedFrom == nil && diagnosis.ObservedTo == nil
	}
	return diagnosis.ObservedFrom != nil && diagnosis.ObservedTo != nil &&
		diagnosis.ObservedFrom.Equal(*summary.ObservedFrom) && diagnosis.ObservedTo.Equal(*summary.ObservedTo) &&
		!diagnosis.CreatedAt.Before(*diagnosis.ObservedTo)
}
