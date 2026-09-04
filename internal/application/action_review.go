package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

// ActionReviewDisposition is persisted metadata, never execution authority.
type ActionReviewDisposition string

const (
	ActionReviewApprove   ActionReviewDisposition = "approve"
	ActionReviewDeny      ActionReviewDisposition = "deny"
	ActionReviewEscalate  ActionReviewDisposition = "escalate_to_user"
	ActionReviewFailed    ActionReviewDisposition = "failed"
	ActionReviewCancelled ActionReviewDisposition = "cancelled"
	ActionReviewTimedOut  ActionReviewDisposition = "timed_out"
)

func (disposition ActionReviewDisposition) valid() bool {
	switch disposition {
	case ActionReviewApprove, ActionReviewDeny, ActionReviewEscalate,
		ActionReviewFailed, ActionReviewCancelled, ActionReviewTimedOut:
		return true
	default:
		return false
	}
}

// ActionReviewRecord is the minimal safe durable projection. Raw model bytes,
// prompts, endpoint errors, and credentials are deliberately absent.
type ActionReviewRecord struct {
	ModelRequestID   domain.ModelRequestID
	ApprovalID       domain.ApprovalID
	Profile          string
	OriginHash       string
	PolicyGeneration domain.PolicyGeneration
	Disposition      ActionReviewDisposition
	RationaleSummary string
	ErrorClass       domain.SafeErrorClass
	OccurredAt       time.Time
}

func (record ActionReviewRecord) Validate() error {
	if !record.ModelRequestID.Valid() || !record.ApprovalID.Valid() ||
		!domain.ValidModelToken(record.Profile, 128) || !validPrivacyDigest(record.OriginHash) ||
		!record.PolicyGeneration.Valid() || !record.Disposition.valid() || !validCoordinatorTime(record.OccurredAt) {
		return ErrApprovalUnavailable
	}
	switch record.Disposition {
	case ActionReviewApprove, ActionReviewDeny, ActionReviewEscalate:
		processed, err := security.NewRedactor().ProcessLines(record.RationaleSummary, agent.MaxReviewerRationaleBytes)
		if !domain.ValidModelText(record.RationaleSummary, agent.MaxReviewerRationaleBytes, false) || err != nil ||
			processed.Truncated || processed.RedactionCount != 0 || processed.Value != record.RationaleSummary || record.ErrorClass != "" {
			return ErrApprovalUnavailable
		}
	case ActionReviewFailed, ActionReviewCancelled, ActionReviewTimedOut:
		if record.RationaleSummary != "" || !record.ErrorClass.Valid() {
			return ErrApprovalUnavailable
		}
	}
	return nil
}

// ActionReviewPersistence writes one bounded recommendation record.
type ActionReviewPersistence interface {
	AppendActionReview(context.Context, ActionReviewRecord) error
}

func reviewerRequest(
	requestID domain.ModelRequestID,
	envelope domain.ActionEnvelope,
) (agent.ReviewerRequest, error) {
	canonical, err := domain.CanonicalAction(envelope)
	if err != nil || len(canonical) > agent.MaxReviewerActionProjectionBytes {
		return agent.ReviewerRequest{}, ErrApprovalUnavailable
	}
	request := agent.ReviewerRequest{
		RequestID:        requestID,
		UserIntent:       "The local user explicitly selected a permission profile that delegates this reviewable action to the configured approval reviewer.",
		NormalizedAction: string(canonical),
		PolicyFacts: fmt.Sprintf(
			"policy_version=%s\npermission_profile=%s\npolicy_generation=%d\nrisk=%s\neffect=%s\nreviewer_cannot_change_envelope=true\nreviewer_cannot_lower_risk=true",
			envelope.Intent.PolicyVersion, envelope.Intent.PermissionProfile, envelope.Intent.PolicyGeneration,
			envelope.Intent.Risk, envelope.Intent.Effect,
		),
	}
	if request.Validate() != nil {
		return agent.ReviewerRequest{}, ErrApprovalUnavailable
	}
	return request, nil
}

func reviewFailureDisposition(err error) (ActionReviewDisposition, domain.SafeErrorClass) {
	if errors.Is(err, agent.ErrInvalidReviewerData) {
		return ActionReviewFailed, domain.SafeErrorClassInvalidExternalResponse
	}
	if errors.Is(err, context.Canceled) {
		return ActionReviewCancelled, domain.SafeErrorClassCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ActionReviewTimedOut, domain.SafeErrorClassTimeout
	}
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) && classified.Class().Valid() {
		class := classified.Class()
		if class == domain.SafeErrorClassTimeout {
			return ActionReviewTimedOut, class
		}
		if class == domain.SafeErrorClassCancelled {
			return ActionReviewCancelled, class
		}
		return ActionReviewFailed, class
	}
	return ActionReviewFailed, domain.SafeErrorClassInternal
}
