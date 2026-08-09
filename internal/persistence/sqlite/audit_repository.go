package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	insertAuditEventSQL = `
		INSERT INTO audit_events (
			id, session_id, run_id, event_type, actor, outcome,
			scope_context, scope_namespace, scope_generation,
			subject_ref_json, details_json, correlation_id,
			integrity_hash, occurred_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getAuditEventByIDSQL = `
		SELECT
			id, session_id, run_id, event_type, actor, outcome,
			scope_context, scope_namespace, scope_generation,
			subject_ref_json, details_json, correlation_id,
			integrity_hash, occurred_at_ms
		FROM audit_events
		WHERE id = ?
	`
	listAuditEventsBySessionSQL = `
		SELECT
			a.id AS id, a.session_id AS session_id, a.run_id AS run_id,
			a.event_type AS event_type, a.actor AS actor, a.outcome AS outcome,
			a.scope_context AS scope_context,
			a.scope_namespace AS scope_namespace,
			a.scope_generation AS scope_generation,
			a.subject_ref_json AS subject_ref_json,
			a.details_json AS details_json, a.correlation_id AS correlation_id,
			a.integrity_hash AS integrity_hash, a.occurred_at_ms AS occurred_at_ms
		FROM audit_events AS a
		WHERE a.session_id = ?
			OR (
				a.session_id IS NULL
				AND EXISTS (
					SELECT 1 FROM agent_runs AS r
					WHERE r.id = a.run_id AND r.session_id = ?
				)
			)
		ORDER BY a.occurred_at_ms DESC, a.id DESC
		LIMIT ?
	`
	listAuditEventsBySessionBeforeSQL = `
		SELECT
			a.id AS id, a.session_id AS session_id, a.run_id AS run_id,
			a.event_type AS event_type, a.actor AS actor, a.outcome AS outcome,
			a.scope_context AS scope_context,
			a.scope_namespace AS scope_namespace,
			a.scope_generation AS scope_generation,
			a.subject_ref_json AS subject_ref_json,
			a.details_json AS details_json, a.correlation_id AS correlation_id,
			a.integrity_hash AS integrity_hash, a.occurred_at_ms AS occurred_at_ms
		FROM audit_events AS a
		WHERE (
				a.session_id = ?
				OR (
					a.session_id IS NULL
					AND EXISTS (
						SELECT 1 FROM agent_runs AS r
						WHERE r.id = a.run_id AND r.session_id = ?
					)
				)
			)
			AND (
				a.occurred_at_ms < ?
				OR (a.occurred_at_ms = ? AND a.id < ?)
			)
		ORDER BY a.occurred_at_ms DESC, a.id DESC
		LIMIT ?
	`
)

var (
	_ auditcontract.EventAppender = (*AuditRepository)(nil)
	_ auditcontract.EventReader   = (*AuditRepository)(nil)
)

type auditEventRow struct {
	ID              string         `db:"id"`
	SessionID       sql.NullString `db:"session_id"`
	RunID           sql.NullString `db:"run_id"`
	EventType       string         `db:"event_type"`
	Actor           string         `db:"actor"`
	Outcome         string         `db:"outcome"`
	ScopeContext    sql.NullString `db:"scope_context"`
	ScopeNamespace  sql.NullString `db:"scope_namespace"`
	ScopeGeneration sql.NullInt64  `db:"scope_generation"`
	SubjectRefJSON  sql.NullString `db:"subject_ref_json"`
	DetailsJSON     string         `db:"details_json"`
	CorrelationID   sql.NullString `db:"correlation_id"`
	IntegrityHash   sql.NullString `db:"integrity_hash"`
	OccurredAtMS    int64          `db:"occurred_at_ms"`
}

// AuditRepository persists only fixed structured AuditEvent projections.
type AuditRepository struct {
	db *DB
}

// NewAuditRepository binds audit operations to one validated database.
func NewAuditRepository(db *DB) *AuditRepository {
	return &AuditRepository{db: db}
}

// Append validates relationships and inserts one immutable AuditEvent.
func (repository *AuditRepository) Append(ctx context.Context, event domain.AuditEvent) error {
	if err := repositoryContext(ctx, repository.db, "append_audit_event"); err != nil {
		return err
	}
	if event.Validate() != nil {
		return auditcontract.ErrInvalidRepositoryRequest
	}
	detailsJSON, err := json.Marshal(event.Details)
	if err != nil {
		return auditcontract.ErrInvalidRepositoryRequest
	}
	subjectJSON, err := encodeSelectedResource(event.Subject)
	if err != nil {
		return auditcontract.ErrInvalidRepositoryRequest
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := validateAuditRelationships(ctx, tx, event, true); err != nil {
			return err
		}
		var scopeContext any
		var scopeNamespace any
		var scopeGeneration any
		if event.Scope != nil {
			scopeContext = event.Scope.Context
			scopeNamespace = event.Scope.Namespace
			scopeGeneration = event.Scope.Generation
		}
		_, err := tx.ExecContext(
			ctx,
			insertAuditEventSQL,
			event.ID,
			nullableSessionID(event.SessionID),
			nullableRunID(event.RunID),
			event.Type,
			event.Actor,
			event.Outcome,
			scopeContext,
			scopeNamespace,
			scopeGeneration,
			nullableString(subjectJSON),
			string(detailsJSON),
			nullableString(event.CorrelationID),
			nullableString(event.IntegrityHash),
			event.OccurredAt.UTC().UnixMilli(),
		)
		return err
	})
	if isAuditContractError(err) || isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "audit_event_append_failed", "append_audit_event", "KuPilot could not store the AuditEvent.", err)
	}
	return nil
}

// GetByID returns one exact strictly mapped AuditEvent.
func (repository *AuditRepository) GetByID(ctx context.Context, id domain.AuditEventID) (domain.AuditEvent, error) {
	if err := repositoryContext(ctx, repository.db, "get_audit_event"); err != nil {
		return domain.AuditEvent{}, err
	}
	if !id.Valid() {
		return domain.AuditEvent{}, auditcontract.ErrInvalidRepositoryRequest
	}
	var row auditEventRow
	if err := repository.db.handle.GetContext(ctx, &row, getAuditEventByIDSQL, id); errors.Is(err, sql.ErrNoRows) {
		return domain.AuditEvent{}, auditcontract.ErrAuditEventNotFound
	} else if err != nil {
		return domain.AuditEvent{}, repositoryFailure(repository.db, "audit_event_read_failed", "get_audit_event", "KuPilot could not read the AuditEvent.", err)
	}
	event, err := row.domainAuditEvent()
	if err != nil {
		return domain.AuditEvent{}, repositoryFailure(repository.db, "audit_event_row_invalid", "get_audit_event", "KuPilot could not read the AuditEvent safely.", err)
	}
	if err := validateAuditRelationships(ctx, repository.db.handle, event, true); err != nil {
		return domain.AuditEvent{}, repositoryFailure(repository.db, "audit_event_row_invalid", "get_audit_event", "KuPilot could not read the AuditEvent safely.", err)
	}
	return event, nil
}

// ListBySession returns one bounded descending page including run-only links.
func (repository *AuditRepository) ListBySession(ctx context.Context, request auditcontract.PageRequest) (auditcontract.Page, error) {
	if err := repositoryContext(ctx, repository.db, "list_session_audit_events"); err != nil {
		return auditcontract.Page{}, err
	}
	if request.Validate() != nil {
		return auditcontract.Page{}, auditcontract.ErrInvalidRepositoryRequest
	}
	limit := request.Limit + 1
	var (
		rows *sqlx.Rows
		err  error
	)
	if request.Before == nil {
		rows, err = repository.db.handle.QueryxContext(ctx, listAuditEventsBySessionSQL, request.SessionID, request.SessionID, limit)
	} else {
		boundary := request.Before.OccurredAt.UTC().UnixMilli()
		rows, err = repository.db.handle.QueryxContext(
			ctx,
			listAuditEventsBySessionBeforeSQL,
			request.SessionID,
			request.SessionID,
			boundary,
			boundary,
			request.Before.ID,
			limit,
		)
	}
	if err != nil {
		return auditcontract.Page{}, repositoryFailure(repository.db, "audit_event_list_failed", "list_session_audit_events", "KuPilot could not list AuditEvents.", err)
	}
	defer rows.Close()

	events := make([]domain.AuditEvent, 0, limit)
	for rows.Next() {
		var row auditEventRow
		if err := rows.StructScan(&row); err != nil {
			return auditcontract.Page{}, repositoryFailure(repository.db, "audit_event_row_invalid", "list_session_audit_events", "KuPilot could not read AuditEvents safely.", err)
		}
		event, err := row.domainAuditEvent()
		if err != nil || event.SessionID != nil && *event.SessionID != request.SessionID {
			return auditcontract.Page{}, repositoryFailure(repository.db, "audit_event_row_invalid", "list_session_audit_events", "KuPilot could not read AuditEvents safely.", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return auditcontract.Page{}, repositoryFailure(repository.db, "audit_event_list_failed", "list_session_audit_events", "KuPilot could not list AuditEvents.", err)
	}
	if err := rows.Close(); err != nil {
		return auditcontract.Page{}, repositoryFailure(repository.db, "audit_event_list_failed", "list_session_audit_events", "KuPilot could not list AuditEvents.", err)
	}
	for _, event := range events {
		if err := validateAuditRelationships(ctx, repository.db.handle, event, true); err != nil {
			return auditcontract.Page{}, repositoryFailure(repository.db, "audit_event_row_invalid", "list_session_audit_events", "KuPilot could not read AuditEvents safely.", err)
		}
	}
	page := auditcontract.Page{Events: events}
	if len(events) > request.Limit {
		page.Events = events[:request.Limit]
		last := page.Events[len(page.Events)-1]
		page.Next = &auditcontract.Cursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return page, nil
}

func (row auditEventRow) domainAuditEvent() (domain.AuditEvent, error) {
	scope, err := decodeScopeSnapshot(row.ScopeContext, row.ScopeNamespace, row.ScopeGeneration)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	subject, err := decodeSelectedResource(row.SubjectRefJSON)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	var details domain.AuditDetails
	if err := decodeStrictJSON(row.DetailsJSON, &details); err != nil {
		return domain.AuditEvent{}, err
	}
	event := domain.AuditEvent{
		ID:            domain.AuditEventID(row.ID),
		SessionID:     sessionIDPointer(row.SessionID),
		RunID:         runIDPointer(row.RunID),
		Type:          domain.AuditEventType(row.EventType),
		Actor:         domain.AuditActor(row.Actor),
		Outcome:       domain.AuditOutcome(row.Outcome),
		Scope:         scope,
		Subject:       subject,
		Details:       details,
		CorrelationID: optionalString(stringPointer(row.CorrelationID)),
		IntegrityHash: optionalString(stringPointer(row.IntegrityHash)),
		OccurredAt:    time.UnixMilli(row.OccurredAtMS).UTC(),
	}
	if err := event.Validate(); err != nil {
		return domain.AuditEvent{}, err
	}
	return event, nil
}

func validateAuditRelationships(ctx context.Context, getter strictGetter, event domain.AuditEvent, enforceEligibility bool) error {
	var sessionID *domain.SessionID
	if event.RunID != nil {
		run, err := getAgentRun(ctx, getter, *event.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			return sessioncontract.ErrAgentRunNotFound
		}
		if err != nil {
			return err
		}
		sessionID = &run.SessionID
		if event.SessionID != nil && *event.SessionID != run.SessionID || event.Scope != nil && *event.Scope != run.Scope {
			return auditcontract.ErrInvalidRepositoryRequest
		}
	} else if event.SessionID != nil {
		sessionID = event.SessionID
	}
	if sessionID == nil {
		return nil
	}
	state, err := getSessionPersistenceState(ctx, getter, *sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return sessioncontract.ErrSessionNotFound
	}
	if err != nil {
		return err
	}
	if enforceEligibility && state.PrivacyMode == string(domain.PrivacyModeMinimal) && !event.Type.AllowedInMinimalPersistence() {
		return auditcontract.ErrAuditEventNotEligible
	}
	if state.PrivacyMode != string(domain.PrivacyModeStandard) && state.PrivacyMode != string(domain.PrivacyModeMinimal) {
		return auditcontract.ErrInvalidRepositoryRequest
	}
	return nil
}

func nullableSessionID(value *domain.SessionID) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableRunID(value *domain.AgentRunID) any {
	if value == nil {
		return nil
	}
	return *value
}

func sessionIDPointer(value sql.NullString) *domain.SessionID {
	if !value.Valid {
		return nil
	}
	result := domain.SessionID(value.String)
	return &result
}

func runIDPointer(value sql.NullString) *domain.AgentRunID {
	if !value.Valid {
		return nil
	}
	result := domain.AgentRunID(value.String)
	return &result
}

func isAuditContractError(err error) bool {
	return errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) ||
		errors.Is(err, auditcontract.ErrAuditEventNotFound) ||
		errors.Is(err, auditcontract.ErrAuditEventNotEligible) ||
		errors.Is(err, auditcontract.ErrSettingNotFound) ||
		errors.Is(err, auditcontract.ErrSettingConflict)
}
