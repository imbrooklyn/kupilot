package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

// CreateWithAudit atomically inserts one Session and its creation audit.
func (repository *SessionRepository) CreateWithAudit(
	ctx context.Context,
	session domain.Session,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.db, "create_session_with_audit"); err != nil {
		return err
	}
	if session.Validate() != nil || !validSessionCreationAudit(session, audit) {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	selectedResource, err := encodeSelectedResource(session.SelectedResource)
	if err != nil {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(
			ctx,
			createSessionSQL,
			session.ID,
			session.Title,
			session.Status,
			session.PrivacyMode,
			nullableString(scopeContext(session.LastScope)),
			nullableString(scopeNamespace(session.LastScope)),
			nullableString(selectedResource),
			nullableString(optionalString(session.Summary)),
			session.Version,
			session.CreatedAt.UTC().UnixMilli(),
			session.UpdatedAt.UTC().UnixMilli(),
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return sessioncontract.ErrSessionConflict
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	if isSessionContractError(err) || isAuditContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(
			repository.db,
			"session_create_failed",
			"create_session_with_audit",
			"KuPilot could not durably create the Session.",
			err,
		)
	}
	return nil
}

// BeginWithAudit atomically inserts the safe request Message, running
// AgentRun, activity time, and required start audit.
func (repository *AgentRunRepository) BeginWithAudit(
	ctx context.Context,
	message domain.Message,
	run domain.AgentRun,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.db, "begin_agent_run_with_audit"); err != nil {
		return err
	}
	if !validBeginPair(message, run) || !validRunAudit(run, audit, domain.AuditEventRunStarted) {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := ensureStandardActiveSession(ctx, tx, run.SessionID); err != nil {
			return err
		}
		var runningCount int
		if err := tx.GetContext(ctx, &runningCount, countRunningAgentRunsSQL); err != nil {
			return err
		}
		if runningCount != 0 {
			return sessioncontract.ErrAgentRunConflict
		}
		if err := insertMessage(ctx, tx, message); err != nil {
			return err
		}
		if err := insertAgentRun(ctx, tx, run); err != nil {
			return err
		}
		if err := touchSession(ctx, tx, run.SessionID, laterTime(message.CreatedAt, *run.StartedAt)); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return coordinatedRepositoryResult(
		repository.db,
		err,
		"agent_run_begin_failed",
		"begin_agent_run_with_audit",
		"KuPilot could not durably start the AgentRun.",
	)
}

// FinishWithAudit atomically stores one terminal run without durable Diagnosis
// content and its required terminal audit.
func (repository *AgentRunRepository) FinishWithAudit(
	ctx context.Context,
	run domain.AgentRun,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.db, "finish_agent_run_with_audit"); err != nil {
		return err
	}
	if run.Validate() != nil || !run.Status.Terminal() || !validRunAudit(run, audit, terminalAuditType(run.Status)) {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := finishCoordinatedRun(ctx, tx, run); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return coordinatedRepositoryResult(
		repository.db,
		err,
		"agent_run_finish_failed",
		"finish_agent_run_with_audit",
		"KuPilot could not store the terminal AgentRun state.",
	)
}

// CompleteWithAudit atomically stores a validated Diagnosis, final assistant
// Message, completed AgentRun, and required terminal audit.
func (repository *AgentRunRepository) CompleteWithAudit(
	ctx context.Context,
	diagnosis domain.Diagnosis,
	message domain.Message,
	run domain.AgentRun,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.db, "complete_agent_run_with_audit"); err != nil {
		return err
	}
	if !validFinishPair(message, run) || run.Status != domain.AgentRunStatusCompleted ||
		diagnosis.Validate() != nil || diagnosis.RunID != run.ID || diagnosis.Scope != run.Scope ||
		!validRunAudit(run, audit, domain.AuditEventRunCompleted) {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	encoded, err := encodeCoordinatedDiagnosis(diagnosis)
	if err != nil {
		return domain.ErrInvalidDiagnosis
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
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
		if domain.ValidateAgentRunTransition(current, run) != nil || current.Scope != diagnosis.Scope {
			return sessioncontract.ErrAgentRunConflict
		}
		if err := insertCoordinatedDiagnosis(ctx, tx, diagnosis, encoded); err != nil {
			return err
		}
		if err := insertMessage(ctx, tx, message); err != nil {
			return err
		}
		if err := finishAgentRun(ctx, tx, run); err != nil {
			return err
		}
		if err := touchSession(ctx, tx, run.SessionID, laterTime(message.CreatedAt, *run.FinishedAt)); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return coordinatedRepositoryResult(
		repository.db,
		err,
		"agent_run_finish_failed",
		"complete_agent_run_with_audit",
		"KuPilot could not store the final AgentRun result.",
	)
}

// SaveWithAudit atomically stores one terminal ToolInvocation, its accepted
// Evidence, and the matching terminal Tool audit.
func (repository *ToolInvocationRepository) SaveWithAudit(
	ctx context.Context,
	invocation domain.ToolInvocation,
	evidence []domain.Evidence,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.db, "save_tool_invocation_with_audit"); err != nil {
		return err
	}
	if invocation.Validate() != nil || !invocation.Status.Terminal() ||
		len(evidence) != invocation.EvidenceCount || len(evidence) > 100 ||
		!validToolAudit(invocation, audit) {
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
		if run.Status != domain.AgentRunStatusRunning || run.Scope != invocation.Scope {
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
		return insertAuditEvent(ctx, tx, audit)
	})
	if errors.Is(err, domain.ErrInvalidToolInvocation) || errors.Is(err, domain.ErrInvalidEvidence) ||
		isSessionContractError(err) || isAuditContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(
			repository.db,
			"tool_invocation_save_failed",
			"save_tool_invocation_with_audit",
			"KuPilot could not store safe ToolInvocation metadata.",
			err,
		)
	}
	return nil
}

type coordinatedDiagnosisJSON struct {
	confirmed  string
	hypotheses string
	missing    string
	actions    string
	warnings   string
}

func encodeCoordinatedDiagnosis(diagnosis domain.Diagnosis) (coordinatedDiagnosisJSON, error) {
	confirmed, hypotheses, missing, actions, warnings, err := encodeDiagnosis(diagnosis)
	return coordinatedDiagnosisJSON{
		confirmed: confirmed, hypotheses: hypotheses, missing: missing, actions: actions, warnings: warnings,
	}, err
}

func insertCoordinatedDiagnosis(
	ctx context.Context,
	tx *sqlx.Tx,
	diagnosis domain.Diagnosis,
	encoded coordinatedDiagnosisJSON,
) error {
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
		encoded.confirmed,
		encoded.hypotheses,
		encoded.missing,
		encoded.actions,
		diagnosis.AnswerMarkdown,
		nullableString(encoded.warnings),
		nullableTime(diagnosis.ObservedFrom),
		nullableTime(diagnosis.ObservedTo),
		diagnosis.CreatedAt.UTC().UnixMilli(),
	)
	return err
}

func finishCoordinatedRun(ctx context.Context, tx *sqlx.Tx, run domain.AgentRun) error {
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
}

func validSessionCreationAudit(session domain.Session, audit domain.AuditEvent) bool {
	return audit.Validate() == nil && audit.Type == domain.AuditEventSessionCreated &&
		audit.Actor == domain.AuditActorUser && audit.Outcome == domain.AuditOutcomeSuccess &&
		audit.SessionID != nil && *audit.SessionID == session.ID && audit.RunID == nil &&
		audit.Scope == nil && audit.Subject == nil && !audit.OccurredAt.Before(session.CreatedAt)
}

func validRunAudit(run domain.AgentRun, audit domain.AuditEvent, eventType domain.AuditEventType) bool {
	if eventType == "" || audit.Validate() != nil || audit.Type != eventType || !validRunAuditOutcome(audit) {
		return false
	}
	boundary := run.StartedAt
	if run.Status.Terminal() {
		boundary = run.FinishedAt
	}
	return boundary != nil && !audit.OccurredAt.Before(*boundary) &&
		audit.SessionID != nil && *audit.SessionID == run.SessionID &&
		audit.RunID != nil && *audit.RunID == run.ID &&
		audit.Scope != nil && *audit.Scope == run.Scope && sameOptionalResource(audit.Subject, run.Resource)
}

func validToolAudit(invocation domain.ToolInvocation, audit domain.AuditEvent) bool {
	eventType := domain.AuditEventToolCompleted
	outcome := domain.AuditOutcomeSuccess
	if invocation.Status == domain.ToolInvocationStatusDenied {
		eventType = domain.AuditEventToolDenied
		outcome = domain.AuditOutcomeDenied
	} else if invocation.Status != domain.ToolInvocationStatusSucceeded {
		outcome = domain.AuditOutcomeFailure
	}
	return audit.Validate() == nil && audit.Type == eventType && audit.Actor == domain.AuditActorAgent &&
		audit.Outcome == outcome && invocation.FinishedAt != nil && !audit.OccurredAt.Before(*invocation.FinishedAt) &&
		audit.RunID != nil && *audit.RunID == invocation.RunID &&
		audit.Scope != nil && *audit.Scope == invocation.Scope &&
		audit.Details.ToolName != nil && *audit.Details.ToolName == invocation.Name &&
		audit.Details.Sequence != nil && *audit.Details.Sequence == invocation.Sequence
}

func validRunAuditOutcome(audit domain.AuditEvent) bool {
	if audit.Type == domain.AuditEventRunStarted {
		return audit.Actor == domain.AuditActorUser && audit.Outcome == domain.AuditOutcomeSuccess
	}
	if audit.Type == domain.AuditEventRunCompleted {
		return audit.Actor == domain.AuditActorAgent && audit.Outcome == domain.AuditOutcomeSuccess
	}
	return audit.Actor == domain.AuditActorAgent && audit.Outcome == domain.AuditOutcomeFailure
}

func terminalAuditType(status domain.AgentRunStatus) domain.AuditEventType {
	switch status {
	case domain.AgentRunStatusCompleted:
		return domain.AuditEventRunCompleted
	case domain.AgentRunStatusFailed:
		return domain.AuditEventRunFailed
	case domain.AgentRunStatusCancelled:
		return domain.AuditEventRunCancelled
	case domain.AgentRunStatusTimedOut:
		return domain.AuditEventRunTimedOut
	case domain.AgentRunStatusStaleScope:
		return domain.AuditEventRunStaleScope
	case domain.AgentRunStatusInterrupted:
		return domain.AuditEventRunInterrupted
	default:
		return ""
	}
}

func sameOptionalResource(left, right *domain.ResourceRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func coordinatedRepositoryResult(
	database *DB,
	err error,
	code string,
	operation string,
	safeMessage string,
) error {
	if isSessionContractError(err) || isAuditContractError(err) ||
		errors.Is(err, domain.ErrInvalidDiagnosis) || errors.Is(err, ErrDiagnosisEvidenceInvalid) {
		return err
	}
	if err != nil {
		return repositoryFailure(database, code, operation, safeMessage, err)
	}
	return nil
}
