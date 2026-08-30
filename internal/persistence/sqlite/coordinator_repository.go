package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

var _ application.SessionLifecyclePersistence = (*SessionRepository)(nil)

const tightenOperationalDetailRetentionSQL = `
	INSERT INTO settings (key, value_json, schema_version, updated_at_ms)
	VALUES (?, ?, ?, ?)
	ON CONFLICT (key) DO UPDATE SET
		value_json = excluded.value_json,
		schema_version = excluded.schema_version,
		updated_at_ms = excluded.updated_at_ms
	WHERE settings.value_json = ?
`

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
			"Kupilot could not durably create the Session.",
			err,
		)
	}
	return nil
}

// LoadOperationalDetailRetention reads the one typed retention preference.
func (repository *SessionRepository) LoadOperationalDetailRetention(ctx context.Context) (int, bool, error) {
	if err := repositoryContext(ctx, repository.db, "read_operational_detail_retention"); err != nil {
		return 0, false, err
	}
	var row settingRow
	err := repository.db.handle.GetContext(ctx, &row, getSettingSQL, domain.SettingOperationalDetailRetentionDays)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, repositoryFailure(
			repository.db,
			"setting_read_failed",
			"read_operational_detail_retention",
			"Kupilot could not read the retention setting.",
			err,
		)
	}
	setting, err := row.domainSetting()
	if err != nil {
		return 0, false, repositoryFailure(
			repository.db,
			"setting_row_invalid",
			"read_operational_detail_retention",
			"Kupilot could not read the retention setting safely.",
			err,
		)
	}
	if setting.Key != domain.SettingOperationalDetailRetentionDays {
		return 0, false, repositoryFailure(
			repository.db,
			"setting_row_invalid",
			"read_operational_detail_retention",
			"Kupilot could not read the retention setting safely.",
			domain.ErrInvalidSetting,
		)
	}
	return int(setting.IntegerValue), true, nil
}

// TightenOperationalDetailRetention atomically compares and lowers the typed setting.
func (repository *SessionRepository) TightenOperationalDetailRetention(
	ctx context.Context,
	update application.RetentionSettingUpdate,
) error {
	if err := repositoryContext(ctx, repository.db, "tighten_operational_detail_retention"); err != nil {
		return err
	}
	if err := update.Validate(); err != nil {
		return err
	}
	setting := domain.Setting{
		Key: domain.SettingOperationalDetailRetentionDays, IntegerValue: int64(update.Days),
		SchemaVersion: 1, UpdatedAt: update.UpdatedAt,
	}
	if setting.Validate() != nil {
		return application.ErrRetentionWouldWiden
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		current := application.DefaultOperationalDetailRetentionDays
		var row settingRow
		readErr := tx.GetContext(ctx, &row, getSettingSQL, domain.SettingOperationalDetailRetentionDays)
		if readErr == nil {
			stored, mapErr := row.domainSetting()
			if mapErr != nil {
				return mapErr
			}
			if stored.Key != domain.SettingOperationalDetailRetentionDays {
				return domain.ErrInvalidSetting
			}
			current = int(stored.IntegerValue)
		} else if !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		if current != update.ExpectedDays {
			return application.ErrRetentionSettingConflict
		}
		if update.Days > current {
			return application.ErrRetentionWouldWiden
		}
		result, execErr := tx.ExecContext(
			ctx,
			tightenOperationalDetailRetentionSQL,
			setting.Key,
			strconv.FormatInt(setting.IntegerValue, 10),
			setting.SchemaVersion,
			setting.UpdatedAt.UTC().UnixMilli(),
			strconv.Itoa(update.ExpectedDays),
		)
		if execErr != nil {
			return execErr
		}
		affected, affectedErr := result.RowsAffected()
		if affectedErr != nil {
			return affectedErr
		}
		if affected != 1 {
			return application.ErrRetentionSettingConflict
		}
		return nil
	})
	if errors.Is(err, application.ErrRetentionSettingConflict) || errors.Is(err, application.ErrRetentionWouldWiden) {
		return err
	}
	if err != nil {
		return repositoryFailure(
			repository.db,
			"retention_setting_failed",
			"tighten_operational_detail_retention",
			"Kupilot could not tighten the retention setting.",
			err,
		)
	}
	return nil
}

// DeleteSessionGraph commits the existing fixed Session cascade transaction.
func (repository *SessionRepository) DeleteSessionGraph(ctx context.Context, id domain.SessionID) error {
	return repository.Delete(ctx, id)
}

// ClearHistory removes every Session graph and every remaining unlinked audit
// in one transaction while preserving settings, consent, and schema metadata.
func (repository *SessionRepository) ClearHistory(ctx context.Context) error {
	if err := repositoryContext(ctx, repository.db, "clear_history"); err != nil {
		return err
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM audit_events`); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return repositoryFailure(
			repository.db,
			"history_clear_failed",
			"clear_history",
			"Kupilot could not clear local Session history.",
			err,
		)
	}
	return nil
}

// BeginWithAudit atomically inserts one running AgentRun, activity time, and
// required start audit. Only standard mode retains the safe request Message.
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
		"Kupilot could not durably start the diagnostic run.",
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
		"Kupilot could not store the completed diagnostic run state.",
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
		"Kupilot could not store the final diagnosis.",
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
			"Kupilot could not store safe tool-activity metadata.",
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
		if !zeroRetentionExpiredDiagnosis(ctx, tx, diagnosis, summary) {
			return ErrDiagnosisEvidenceInvalid
		}
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

func zeroRetentionExpiredDiagnosis(
	ctx context.Context,
	tx *sqlx.Tx,
	diagnosis domain.Diagnosis,
	summary diagnosisEvidenceSummary,
) bool {
	if diagnosis.EvidenceDetailsState != domain.EvidenceDetailExpired ||
		summary.State != domain.EvidenceDetailExpired || summary.Referenced == 0 ||
		summary.Found != 0 || summary.EvidenceCount != 0 {
		return false
	}
	days, err := operationalDetailRetentionDays(ctx, tx, application.DefaultOperationalDetailRetentionDays)
	return err == nil && days == 0
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
