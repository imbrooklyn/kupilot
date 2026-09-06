package application

import (
	"context"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func (coordinator *ApprovalCoordinator) resolveAutomaticAction(ctx context.Context, tracked trackedApproval) error {
	actor := domain.ApprovalActorPermissionPolicy
	ruleID := domain.PermissionRuleID("")
	if tracked.route.MatchedRuleID.Valid() {
		actor, ruleID = domain.ApprovalActorSessionRule, tracked.route.MatchedRuleID
	}
	updated, err := coordinator.resolveProgrammatic(
		ctx, tracked, domain.ApprovalDecisionApprove, actor,
		domain.ReviewDispositionAutomatic, ruleID, "", "", "",
	)
	if err != nil {
		return err
	}
	actionResult, err := coordinator.ConsumeApprovedAction(ctx, approvalExecutionCommand(updated, tracked.sequence))
	if tracked.action.kind != trackedActionRestart && !tracked.action.toolAuthorization() && actionResult.Validate() == nil {
		coordinator.publishClosed(ctx, actionResult)
	}
	return err
}

func (coordinator *ApprovalCoordinator) reviewAction(ctx context.Context, tracked trackedApproval) error {
	binding := coordinator.reviewer
	if binding == nil || !binding.valid() || !binding.Available {
		return coordinator.escalateReviewerToHuman(ctx, tracked, UIReviewerStatus{State: UIReviewerEscalated})
	}
	allowed, err := binding.Privacy.AuthorizeModel(ctx)
	if err != nil || !allowed {
		if ctx.Err() != nil {
			_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonContextCancelled)
			return closeErr
		}
		return coordinator.escalateReviewerToHuman(ctx, tracked, UIReviewerStatus{
			State: UIReviewerEscalated, Profile: binding.Profile, OriginHash: binding.Privacy.OriginHash(),
		})
	}
	if !coordinator.actionCurrent(tracked.request.ActionEnvelope()) {
		_, err := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return err
	}
	requestID, err := coordinator.approvalIDs.NewModelRequestID()
	if err != nil || !requestID.Valid() {
		_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return closeErr
	}
	request, err := reviewerRequest(requestID, tracked.request.ActionEnvelope())
	if err != nil {
		_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return closeErr
	}
	reservation, err := binding.Budget.Reserve(ctx)
	if err != nil {
		return coordinator.recordReviewFailureAndEscalate(ctx, tracked, requestID, binding, err)
	}
	reviewContext, cancel := context.WithCancel(ctx)
	coordinator.mu.Lock()
	current, ok := coordinator.active[tracked.request.ID]
	if !ok || current.request.Digest != tracked.request.Digest || current.route.Disposition != domain.ReviewDispositionReviewer {
		coordinator.mu.Unlock()
		cancel()
		return ErrApprovalUnavailable
	}
	current.reviewing, current.reviewCancel = true, cancel
	coordinator.active[tracked.request.ID] = current
	coordinator.mu.Unlock()
	if err := coordinator.publishReviewerState(reviewContext, tracked.request.ID, UIReviewerStatus{
		State: UIReviewerReviewing, Profile: binding.Profile, OriginHash: binding.Privacy.OriginHash(),
	}); err != nil {
		cancel()
		_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return errors.Join(err, closeErr)
	}
	result, reviewErr := binding.Transport.Review(reviewContext, request, reservation)
	cancel()

	coordinator.mu.Lock()
	current, ok = coordinator.active[tracked.request.ID]
	if ok && current.request.Digest == tracked.request.Digest {
		current.reviewing, current.reviewCancel = false, nil
		coordinator.active[tracked.request.ID] = current
	}
	coordinator.mu.Unlock()
	if reviewErr != nil || result.Validate() != nil || result.Risk != tracked.request.Intent.Risk {
		if reviewErr == nil {
			reviewErr = agent.ErrInvalidReviewerData
		}
		return coordinator.recordReviewFailureAndEscalate(ctx, tracked, requestID, binding, reviewErr)
	}
	if !ok || !coordinator.actionCurrent(tracked.request.ActionEnvelope()) {
		return coordinator.persistReview(ActionReviewRecord{
			ModelRequestID: requestID, ApprovalID: tracked.request.ID, Profile: binding.Profile,
			OriginHash: binding.Privacy.OriginHash(), PolicyGeneration: tracked.request.Intent.PolicyGeneration,
			Disposition: ActionReviewCancelled, ErrorClass: domain.SafeErrorClassStaleScope,
			OccurredAt: coordinator.now(),
		})
	}
	disposition := ActionReviewEscalate
	choice := domain.ApprovalDecisionReject
	switch result.Decision {
	case agent.ReviewerDecisionApprove:
		disposition, choice = ActionReviewApprove, domain.ApprovalDecisionApprove
	case agent.ReviewerDecisionDeny:
		disposition = ActionReviewDeny
	case agent.ReviewerDecisionEscalateToUser:
		disposition = ActionReviewEscalate
	default:
		return coordinator.recordReviewFailureAndEscalate(ctx, tracked, requestID, binding, agent.ErrInvalidReviewerData)
	}
	record := ActionReviewRecord{
		ModelRequestID: requestID, ApprovalID: tracked.request.ID, Profile: binding.Profile,
		OriginHash: binding.Privacy.OriginHash(), PolicyGeneration: tracked.request.Intent.PolicyGeneration,
		Disposition: disposition, RationaleSummary: result.Rationale, OccurredAt: coordinator.now(),
	}
	if err := coordinator.persistReview(record); err != nil {
		_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return errors.Join(err, closeErr)
	}
	if disposition == ActionReviewEscalate {
		return coordinator.escalateReviewerToHuman(ctx, tracked, UIReviewerStatus{
			State: UIReviewerEscalated, Profile: binding.Profile, OriginHash: binding.Privacy.OriginHash(),
			RationaleSummary: result.Rationale,
		})
	}
	reviewerState := UIReviewerDenied
	if disposition == ActionReviewApprove {
		reviewerState = UIReviewerApproved
	}
	if err := coordinator.publishReviewerState(ctx, tracked.request.ID, UIReviewerStatus{
		State: reviewerState, Profile: binding.Profile, OriginHash: binding.Privacy.OriginHash(),
		RationaleSummary: result.Rationale,
	}); err != nil {
		_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return errors.Join(err, closeErr)
	}
	updated, err := coordinator.resolveProgrammatic(
		ctx, tracked, choice, domain.ApprovalActorReviewer, domain.ReviewDispositionReviewer,
		"", binding.Profile, binding.Privacy.OriginHash(), result.Rationale,
	)
	if err != nil {
		return err
	}
	if choice == domain.ApprovalDecisionReject {
		return nil
	}
	actionResult, consumeErr := coordinator.ConsumeApprovedAction(ctx, approvalExecutionCommand(updated, tracked.sequence))
	if tracked.action.kind != trackedActionRestart && !tracked.action.toolAuthorization() && actionResult.Validate() == nil {
		coordinator.publishClosed(ctx, actionResult)
	}
	return consumeErr
}

func (coordinator *ApprovalCoordinator) recordReviewFailureAndEscalate(
	ctx context.Context,
	tracked trackedApproval,
	requestID domain.ModelRequestID,
	binding *ReviewerModelBinding,
	cause error,
) error {
	disposition, class := reviewFailureDisposition(cause)
	record := ActionReviewRecord{
		ModelRequestID: requestID, ApprovalID: tracked.request.ID, Profile: binding.Profile,
		OriginHash: binding.Privacy.OriginHash(), PolicyGeneration: tracked.request.Intent.PolicyGeneration,
		Disposition: disposition, ErrorClass: class, OccurredAt: coordinator.now(),
	}
	if err := coordinator.persistReview(record); err != nil {
		_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
		return errors.Join(err, closeErr)
	}
	if ctx.Err() != nil {
		_, err := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonContextCancelled)
		return err
	}
	if disposition == ActionReviewTimedOut {
		deliveryContext, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
		err := coordinator.publishReviewerState(deliveryContext, tracked.request.ID, UIReviewerStatus{
			State: UIReviewerTimedOut, Profile: binding.Profile, OriginHash: binding.Privacy.OriginHash(),
		})
		cancel()
		if err != nil {
			_, closeErr := coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
			return errors.Join(err, closeErr)
		}
	}
	return coordinator.escalateReviewerToHuman(ctx, tracked, UIReviewerStatus{
		State: UIReviewerEscalated, Profile: binding.Profile, OriginHash: binding.Privacy.OriginHash(),
	})
}

func (coordinator *ApprovalCoordinator) persistReview(record ActionReviewRecord) error {
	if coordinator == nil || record.Validate() != nil {
		return ErrApprovalPersistenceUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
	defer cancel()
	if err := coordinator.reviews.AppendActionReview(ctx, record); err != nil {
		return ErrApprovalPersistenceUnavailable
	}
	return nil
}

func (coordinator *ApprovalCoordinator) escalateReviewerToHuman(ctx context.Context, tracked trackedApproval, status UIReviewerStatus) error {
	if err := coordinator.publishReviewerState(ctx, tracked.request.ID, status); err != nil {
		return err
	}
	coordinator.mu.Lock()
	current, ok := coordinator.active[tracked.request.ID]
	if !ok || current.request.Digest != tracked.request.Digest || !coordinator.actionCurrent(tracked.request.ActionEnvelope()) {
		coordinator.mu.Unlock()
		return ErrApprovalUnavailable
	}
	current.reviewing, current.reviewCancel = false, nil
	current.route.Disposition = domain.ReviewDispositionHuman
	current.route.ReasonCode = "reviewer_escalated"
	coordinator.active[tracked.request.ID] = current
	coordinator.mu.Unlock()
	return coordinator.publishApprovalDialog(ctx, current)
}

func (coordinator *ApprovalCoordinator) publishReviewerState(
	ctx context.Context,
	requestID domain.ApprovalID,
	status UIReviewerStatus,
) error {
	if coordinator == nil || ctx == nil || status.valid() == false {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	tracked, ok := coordinator.active[requestID]
	if !ok || tracked.request.State != domain.ApprovalStatePending && tracked.request.State != domain.ApprovalStateApproved {
		coordinator.mu.Unlock()
		return ErrApprovalUnavailable
	}
	tracked.reviewIndex++
	copy := status
	tracked.reviewer = &copy
	tracked.reviewing = status.State == UIReviewerReviewing
	coordinator.active[requestID] = tracked
	event := UIReviewerEvent{
		RequestID: tracked.request.ID, RunID: tracked.request.RunID,
		ScopeGeneration:  tracked.request.Intent.Scope.Generation,
		PolicyGeneration: tracked.request.Intent.PolicyGeneration,
		Sequence:         tracked.sequence, EventIndex: tracked.reviewIndex,
		Digest: tracked.request.Digest, Status: status,
	}
	coordinator.mu.Unlock()
	projection := UIEvent{
		Kind: UIEventReviewerState, RunID: event.RunID, ScopeGeneration: event.ScopeGeneration,
		PolicyGeneration: event.PolicyGeneration, Sequence: event.Sequence, Reviewer: &event,
	}
	if projection.Validate() != nil || coordinator.uiEvents.PublishUIEvent(ctx, projection) != nil {
		return ErrApprovalUnavailable
	}
	return nil
}

func (coordinator *ApprovalCoordinator) resolveProgrammatic(
	ctx context.Context,
	tracked trackedApproval,
	choice domain.ApprovalDecisionChoice,
	actor domain.ApprovalActor,
	disposition domain.ReviewDisposition,
	ruleID domain.PermissionRuleID,
	reviewerProfile string,
	reviewerOriginHash string,
	rationale string,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || ctx == nil || ctx.Err() != nil || !coordinator.actionCurrent(tracked.request.ActionEnvelope()) {
		return domain.ApprovalRequest{}, ErrApprovalInvalidated
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	current, ok := coordinator.active[tracked.request.ID]
	if !ok || current.request.Digest != tracked.request.Digest || current.request.State != domain.ApprovalStatePending {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	scope, scopeOK := coordinator.scope.CurrentScope()
	if !scopeOK || scope.Snapshot() != tracked.request.Intent.Scope {
		return domain.ApprovalRequest{}, ErrApprovalInvalidated
	}
	updated, decision, err := coordinator.service.Decide(ctx, approval.DecisionCommand{
		RequestID: tracked.request.ID, Choice: choice, ShownDigest: tracked.request.Digest,
		Nonce: tracked.request.Nonce, CurrentScope: scope.Snapshot(), Actor: actor, Disposition: disposition,
		RuleID: ruleID, ReviewerProfile: reviewerProfile, ReviewerOriginHash: reviewerOriginHash,
		RationaleSummary: rationale,
	})
	if err != nil {
		return domain.ApprovalRequest{}, ErrApprovalInvalidated
	}
	audit, err := coordinator.newAudit(updated)
	if err != nil || coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.persistence.ResolveWithAudit(operationContext, tracked.request.State, updated, decision, audit)
	}) != nil {
		coordinator.cancelInMemory(updated.ID)
		delete(coordinator.active, tracked.request.ID)
		tracked.action.completeAuthorization(domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable})
		return domain.ApprovalRequest{}, ErrApprovalPersistenceUnavailable
	}
	if updated.State == domain.ApprovalStateApproved {
		current.request = updated
		current.reviewing, current.reviewCancel = false, nil
		coordinator.active[tracked.request.ID] = current
	} else {
		delete(coordinator.active, tracked.request.ID)
		result := projectUIApprovalResult(updated, tracked.sequence)
		if coordinator.publishClosedRequired(ctx, result) != nil {
			tracked.action.completeAuthorization(domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassInternal})
			return domain.ApprovalRequest{}, ErrApprovalUnavailable
		}
		tracked.action.completeAuthorization(domain.ActionEnvelope{}, remoteClosureError(updated))
	}
	return updated, nil
}

func approvalExecutionCommand(request domain.ApprovalRequest, sequence int64) UICommand {
	return UICommand{
		Kind: UICommandApproveAction, RequestID: uint64(sequence), RunID: request.RunID,
		ExpectedScopeGeneration: request.Intent.Scope.Generation, ApprovalID: request.ID,
		ExpectedPolicyGeneration: request.Intent.PolicyGeneration,
		ApprovalDigest:           request.Digest, ApprovalNonce: request.Nonce, ApprovalSequence: sequence,
	}
}
