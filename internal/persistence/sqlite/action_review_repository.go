package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const insertActionReviewSQL = `
	INSERT INTO action_reviews (
		approval_id, model_request_id, profile_name, origin_hash,
		policy_generation, disposition, rationale_summary, error_class,
		occurred_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// AppendActionReview persists only the validated safe recommendation metadata.
func (repository *ApprovalRepository) AppendActionReview(
	ctx context.Context,
	record application.ActionReviewRecord,
) error {
	if err := repositoryContext(ctx, repository.database(), "append_action_review"); err != nil {
		return err
	}
	if record.Validate() != nil {
		return application.ErrApprovalUnavailable
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(
			ctx,
			insertActionReviewSQL,
			record.ApprovalID,
			record.ModelRequestID,
			record.Profile,
			record.OriginHash,
			record.PolicyGeneration,
			record.Disposition,
			nullableText(record.RationaleSummary),
			nullableText(string(record.ErrorClass)),
			record.OccurredAt.UnixMilli(),
		); err != nil {
			return err
		}
		var sessionID string
		if err := tx.GetContext(ctx, &sessionID, `SELECT session_id FROM approvals WHERE id = ?`, record.ApprovalID); errors.Is(err, sql.ErrNoRows) {
			return application.ErrApprovalUnavailable
		} else if err != nil {
			return err
		}
		id := domain.SessionID(sessionID)
		if !id.Valid() {
			return application.ErrApprovalUnavailable
		}
		return touchSession(ctx, tx, id, record.OccurredAt)
	})
	return repository.translateError(
		err,
		"action_review_append_failed",
		"append_action_review",
		"Kupilot could not store the action review safely.",
	)
}
