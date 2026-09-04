package sqlite

import (
	"context"

	"github.com/imbrooklyn/kupilot/internal/application"
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
	_, err := repository.db.handle.ExecContext(
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
	)
	return repository.translateError(
		err,
		"action_review_append_failed",
		"append_action_review",
		"Kupilot could not store the action review safely.",
	)
}
