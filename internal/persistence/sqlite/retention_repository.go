package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	deleteExpiredEvidenceSQL = `
		DELETE FROM evidence_items
		WHERE id IN (
			SELECT id
			FROM evidence_items
			WHERE observed_at_ms <= ?
			ORDER BY observed_at_ms, id
			LIMIT ?
		)
	`
	deleteExpiredToolInvocationsSQL = `
		DELETE FROM tool_invocations
		WHERE id IN (
			SELECT t.id
			FROM tool_invocations AS t
			JOIN agent_runs AS r ON r.id = t.run_id
			WHERE coalesce(t.finished_at_ms, t.started_at_ms, r.finished_at_ms, r.started_at_ms) IS NOT NULL
				AND coalesce(t.finished_at_ms, t.started_at_ms, r.finished_at_ms, r.started_at_ms) <= ?
				AND NOT EXISTS (
					SELECT 1
					FROM evidence_items AS e
					WHERE e.invocation_id = t.id
				)
			ORDER BY coalesce(t.finished_at_ms, t.started_at_ms, r.finished_at_ms, r.started_at_ms), t.id
			LIMIT ?
		)
	`
	deleteExpiredModelRequestsSQL = `
		DELETE FROM model_requests
		WHERE id IN (
			SELECT id
			FROM model_requests
			WHERE coalesce(finished_at_ms, started_at_ms) <= ?
			ORDER BY coalesce(finished_at_ms, started_at_ms), id
			LIMIT ?
		)
	`
	deleteExpiredReadAuditEventsSQL = `
		DELETE FROM audit_events
		WHERE id IN (
			SELECT id
			FROM audit_events
			WHERE event_type IN (
				'session_created', 'session_deleted', 'session_export_requested',
				'run_started', 'run_completed', 'run_failed', 'run_cancelled',
				'run_timed_out', 'run_stale_scope', 'run_interrupted',
				'scope_changed', 'tool_requested', 'tool_completed', 'tool_denied',
				'model_requested', 'model_completed', 'consent_granted',
				'consent_revoked', 'policy_denied', 'persistence_degraded'
			)
				AND occurred_at_ms <= ?
			ORDER BY occurred_at_ms, id
			LIMIT ?
		)
	`
	deleteExpiredWriteAuditEventsSQL = `
		DELETE FROM audit_events
		WHERE id IN (
			SELECT id
			FROM audit_events
			WHERE event_type IN (
				'approval_requested', 'approval_approved', 'approval_rejected',
				'approval_expired', 'approval_cancelled', 'write_intent',
				'write_attempted', 'write_outcome_unknown', 'write_verified',
				'write_verification_failed'
			)
				AND occurred_at_ms <= ?
			ORDER BY occurred_at_ms, id
			LIMIT ?
		)
	`
	deleteExpiredTerminalApprovalsSQL = `
		DELETE FROM approvals
		WHERE id IN (
			SELECT p.id
			FROM approvals AS p
			WHERE p.status IN ('rejected', 'expired', 'cancelled', 'invalidated', 'consumed')
				AND p.state_changed_at_ms <= ?
				AND NOT EXISTS (
					SELECT 1
					FROM audit_events AS a
					WHERE a.correlation_id = p.id
				)
			ORDER BY p.state_changed_at_ms, p.id
			LIMIT ?
		)
	`
	deleteEmptyMinimalSessionsSQL = `
		DELETE FROM sessions
		WHERE id IN (
			SELECT s.id
			FROM sessions AS s
			WHERE s.privacy_mode = 'minimal'
				AND NOT EXISTS (
					SELECT 1
					FROM audit_events AS a
					WHERE a.session_id = s.id
						OR a.run_id IN (
							SELECT r.id FROM agent_runs AS r WHERE r.session_id = s.id
						)
				)
				AND NOT EXISTS (
					SELECT 1
					FROM approvals AS p
					WHERE p.session_id = s.id
						OR p.run_id IN (
							SELECT r.id FROM agent_runs AS r WHERE r.session_id = s.id
						)
				)
				AND NOT EXISTS (
					SELECT 1
					FROM agent_runs AS r
					WHERE r.session_id = s.id AND r.status IN ('queued', 'running')
				)
			ORDER BY s.updated_at_ms, s.id
			LIMIT ?
		)
	`
)

var _ auditcontract.RetentionCleaner = (*RetentionRepository)(nil)

// RetentionRepository performs one caller-scheduled bounded cleanup transaction.
type RetentionRepository struct {
	db *DB
}

// NewRetentionRepository binds retention operations to one validated database.
func NewRetentionRepository(db *DB) *RetentionRepository {
	return &RetentionRepository{db: db}
}

// Cleanup applies fixed inclusive cutoffs without owning a clock or scheduler.
func (repository *RetentionRepository) Cleanup(ctx context.Context, request auditcontract.CleanupRequest) (auditcontract.CleanupResult, error) {
	if err := repositoryContext(ctx, repository.db, "cleanup_retention"); err != nil {
		return auditcontract.CleanupResult{}, err
	}
	if request.Validate() != nil {
		return auditcontract.CleanupResult{}, auditcontract.ErrInvalidRepositoryRequest
	}
	var committed auditcontract.CleanupResult
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		detailDays, err := operationalDetailRetentionDays(ctx, tx, request.OperationalDetailRetentionDays)
		if err != nil {
			return err
		}
		detailCutoff := request.Now.UTC().Add(-time.Duration(detailDays) * 24 * time.Hour)
		committed.EvidenceItems, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteExpiredEvidenceSQL,
			detailCutoff.UnixMilli(),
			request.BatchSize,
		))
		if err != nil {
			return err
		}
		committed.ToolInvocations, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteExpiredToolInvocationsSQL,
			detailCutoff.UnixMilli(),
			request.BatchSize,
		))
		if err != nil {
			return err
		}
		committed.ModelRequests, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteExpiredModelRequestsSQL,
			detailCutoff.UnixMilli(),
			request.BatchSize,
		))
		if err != nil {
			return err
		}
		committed.ReadAuditEvents, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteExpiredReadAuditEventsSQL,
			request.ReadAuditCutoff().UnixMilli(),
			request.BatchSize,
		))
		if err != nil {
			return err
		}
		committed.WriteAuditEvents, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteExpiredWriteAuditEventsSQL,
			request.WriteAuditCutoff().UnixMilli(),
			request.BatchSize,
		))
		if err != nil {
			return err
		}
		committed.ApprovalRecords, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteExpiredTerminalApprovalsSQL,
			request.WriteAuditCutoff().UnixMilli(),
			request.BatchSize,
		))
		if err != nil {
			return err
		}
		committed.MinimalSessions, err = retentionDeleteResult(tx.ExecContext(
			ctx,
			deleteEmptyMinimalSessionsSQL,
			request.BatchSize,
		))
		return err
	})
	if err != nil {
		return auditcontract.CleanupResult{}, repositoryFailure(repository.db, "retention_cleanup_failed", "cleanup_retention", "KuPilot could not complete retention cleanup.", err)
	}
	limit := int64(request.BatchSize)
	committed.More = committed.EvidenceItems == limit ||
		committed.ToolInvocations == limit ||
		committed.ModelRequests == limit ||
		committed.ReadAuditEvents == limit ||
		committed.WriteAuditEvents == limit ||
		committed.ApprovalRecords == limit ||
		committed.MinimalSessions == limit
	return committed, nil
}

func operationalDetailRetentionDays(ctx context.Context, getter strictGetter, fallback int) (int, error) {
	var row settingRow
	if err := getter.GetContext(ctx, &row, getSettingSQL, domain.SettingOperationalDetailRetentionDays); errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	} else if err != nil {
		return 0, err
	}
	setting, err := row.domainSetting()
	if err != nil {
		return 0, err
	}
	if setting.Key != domain.SettingOperationalDetailRetentionDays {
		return 0, domain.ErrInvalidSetting
	}
	return int(setting.IntegerValue), nil
}

func retentionDeleteResult(result sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
