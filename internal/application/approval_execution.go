package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var errApprovalRolloutStaleScope = errors.New("the Deployment rollout scope changed before Application accepted the result")

func (coordinator *ApprovalCoordinator) finishRestartExecution(
	ctx context.Context,
	tracked trackedApproval,
	attempt approval.RestartDeploymentAttempt,
	consumeErr error,
) (UIRestartExecution, error) {
	eventIndex := int64(1)
	switch attempt.State {
	case approval.RestartDeploymentNotAttempted:
		errorClass := safeApprovalExecutionClass(consumeErr)
		if err := coordinator.persistRestartAudit(
			tracked, domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeDenied,
			"patch_not_attempted", errorClass, nil, coordinator.now(),
		); err != nil {
			return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), err
		}
		event := newRestartExecutionEvent(tracked, eventIndex, UIRestartNotAttempted)
		event.ErrorClass = errorClass
		coordinator.publishRestartExecution(ctx, event)
		return event, ErrApprovalExecutionFailed
	case approval.RestartDeploymentPatchFailed:
		if err := coordinator.persistRestartAudit(
			tracked, domain.AuditEventWriteAttempted, domain.AuditOutcomeFailure,
			"patch_failed", attempt.ErrorClass, nil, coordinator.now(),
		); err != nil {
			return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), err
		}
		event := newRestartExecutionEvent(tracked, eventIndex, UIRestartPatchFailed)
		event.ErrorClass = attempt.ErrorClass
		coordinator.publishRestartExecution(ctx, event)
		return event, ErrApprovalExecutionFailed
	case approval.RestartDeploymentPatchUnknown:
		if err := coordinator.persistRestartAudit(
			tracked, domain.AuditEventWriteOutcomeUnknown, domain.AuditOutcomeUnknown,
			"patch_outcome_unknown", attempt.ErrorClass, nil, coordinator.now(),
		); err != nil {
			return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), err
		}
		event := newRestartExecutionEvent(tracked, eventIndex, UIRestartPatchOutcomeUnknown)
		event.ErrorClass = attempt.ErrorClass
		coordinator.publishRestartExecution(ctx, event)
		return event, ErrApprovalPatchOutcomeUnknown
	case approval.RestartDeploymentPatchAccepted:
		acceptedTarget := attempt.Acceptance
		if err := coordinator.persistRestartAudit(
			tracked, domain.AuditEventWriteAttempted, domain.AuditOutcomeSuccess,
			"patch_accepted", "", nil, coordinator.now(),
		); err != nil {
			return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), err
		}
		accepted := newRestartExecutionEvent(tracked, eventIndex, UIRestartPatchAccepted)
		accepted.TargetGeneration = acceptedTarget.TargetGeneration
		accepted.TargetReplicas = acceptedTarget.TargetReplicas
		coordinator.publishRestartExecution(ctx, accepted)
		eventIndex++
		if consumeErr != nil {
			return coordinator.finishUnavailableRollout(ctx, tracked, eventIndex, safeApprovalExecutionClass(consumeErr))
		}
		request := RestartRolloutRequest{
			Scope: tracked.request.Intent.Scope, DeploymentName: tracked.request.Intent.DeploymentName,
			DeploymentUID:    tracked.request.Intent.DeploymentUID,
			TargetGeneration: acceptedTarget.TargetGeneration, TargetReplicas: acceptedTarget.TargetReplicas,
		}
		if !coordinator.approvalScopeMatches(request.Scope) {
			return coordinator.finishUnavailableRollout(ctx, tracked, eventIndex, domain.SafeErrorClassStaleScope)
		}
		sink := &approvalRolloutProgressSink{
			coordinator: coordinator, tracked: tracked, request: request, eventIndex: &eventIndex,
		}
		rollout, observeErr := coordinator.rollout.ObserveRestartRollout(ctx, request, sink)
		if observeErr != nil {
			if errors.Is(observeErr, ErrApprovalResultAuditUnavailable) {
				return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), ErrApprovalResultAuditUnavailable
			}
			return coordinator.finishUnavailableRollout(ctx, tracked, eventIndex, safeApprovalExecutionClass(observeErr))
		}
		if rollout.ObservationCount < sink.lastObservation ||
			(sink.lastObservation > 0 && rollout.State != RestartRolloutTimedOut &&
				rollout.ObservationCount <= sink.lastObservation) {
			return coordinator.finishUnavailableRollout(
				ctx, tracked, eventIndex, domain.SafeErrorClassInvalidExternalResponse,
			)
		}
		return coordinator.finishObservedRollout(ctx, tracked, eventIndex, request, rollout)
	default:
		return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), ErrApprovalResultAuditUnavailable
	}
}

type approvalRolloutProgressSink struct {
	coordinator     *ApprovalCoordinator
	tracked         trackedApproval
	request         RestartRolloutRequest
	eventIndex      *int64
	lastObservation int64
}

func (sink *approvalRolloutProgressSink) PublishRestartRolloutProgress(
	ctx context.Context,
	observation RestartRolloutObservation,
) error {
	if !sink.coordinator.approvalScopeMatches(sink.request.Scope) {
		return errApprovalRolloutStaleScope
	}
	if observation.ValidateFor(sink.request) != nil || observation.State != RestartRolloutProgress {
		return ErrInvalidRestartRollout
	}
	if (sink.lastObservation == 0 && observation.ObservationNumber != 1) ||
		(sink.lastObservation > 0 && observation.ObservationNumber <= sink.lastObservation) {
		return ErrInvalidRestartRollout
	}
	count := observation.ObservationNumber
	if err := sink.coordinator.persistRestartAudit(
		sink.tracked, domain.AuditEventWriteAttempted, domain.AuditOutcomeSuccess,
		"rollout_progress", "", &count, observation.ObservedAt,
	); err != nil {
		return err
	}
	event := rolloutExecutionEvent(sink.tracked, *sink.eventIndex, UIRestartRolloutProgress, observation)
	sink.coordinator.publishRestartExecution(ctx, event)
	sink.lastObservation = observation.ObservationNumber
	*sink.eventIndex = *sink.eventIndex + 1
	return nil
}

func (coordinator *ApprovalCoordinator) finishObservedRollout(
	ctx context.Context,
	tracked trackedApproval,
	eventIndex int64,
	request RestartRolloutRequest,
	result RestartRolloutResult,
) (UIRestartExecution, error) {
	if !coordinator.approvalScopeMatches(request.Scope) {
		return coordinator.finishUnavailableRollout(ctx, tracked, eventIndex, domain.SafeErrorClassStaleScope)
	}
	if request.Scope != tracked.request.Intent.Scope ||
		request.DeploymentName != tracked.request.Intent.DeploymentName ||
		request.DeploymentUID != tracked.request.Intent.DeploymentUID ||
		result.ValidateFor(request) != nil {
		return coordinator.finishUnavailableRollout(
			ctx, tracked, eventIndex, domain.SafeErrorClassInvalidExternalResponse,
		)
	}
	count := result.ObservationCount
	var (
		eventType domain.AuditEventType
		outcome   domain.AuditOutcome
		detail    string
		state     UIRestartExecutionState
		resultErr error
	)
	switch result.State {
	case RestartRolloutSucceeded:
		eventType, outcome, detail = domain.AuditEventWriteVerified, domain.AuditOutcomeSuccess, "rollout_succeeded"
		state = UIRestartRolloutSucceeded
	case RestartRolloutTimedOut:
		eventType, outcome, detail = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeUnknown, "rollout_timed_out"
		state, resultErr = UIRestartRolloutTimedOut, ErrApprovalRolloutTimedOut
	case RestartRolloutFailed:
		eventType, outcome, detail = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeFailure, "rollout_failed"
		state, resultErr = UIRestartRolloutFailed, ErrApprovalRolloutFailed
	default:
		return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), ErrApprovalResultAuditUnavailable
	}
	if err := coordinator.persistRestartAudit(tracked, eventType, outcome, detail, "", &count, result.FinishedAt); err != nil {
		return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), err
	}
	event := newRestartExecutionEvent(tracked, eventIndex, state)
	if result.ObservationCount > 0 {
		event = rolloutExecutionEvent(tracked, eventIndex, state, result.Final)
	}
	coordinator.publishRestartExecution(ctx, event)
	return event, resultErr
}

func (coordinator *ApprovalCoordinator) finishUnavailableRollout(
	ctx context.Context,
	tracked trackedApproval,
	eventIndex int64,
	errorClass domain.SafeErrorClass,
) (UIRestartExecution, error) {
	if err := coordinator.persistRestartAudit(
		tracked, domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeUnknown,
		"rollout_unavailable", errorClass, nil, coordinator.now(),
	); err != nil {
		return coordinator.publishResultAuditFailure(ctx, tracked, eventIndex), err
	}
	event := newRestartExecutionEvent(tracked, eventIndex, UIRestartRolloutUnavailable)
	event.ErrorClass = errorClass
	coordinator.publishRestartExecution(ctx, event)
	return event, ErrApprovalRolloutUnavailable
}

func (coordinator *ApprovalCoordinator) persistRestartAudit(
	tracked trackedApproval,
	eventType domain.AuditEventType,
	outcome domain.AuditOutcome,
	detail string,
	errorClass domain.SafeErrorClass,
	count *int64,
	occurredAt time.Time,
) error {
	id, err := coordinator.auditIDs.NewAuditEventID()
	if err != nil || !id.Valid() {
		return ErrApprovalResultAuditUnavailable
	}
	operation := string(tracked.request.Intent.Operation)
	policy := tracked.request.Intent.PolicyVersion
	scope := tracked.request.Intent.Scope
	sessionID, runID := tracked.request.SessionID, tracked.request.RunID
	subject := domain.ResourceRef{
		APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
		Namespace: scope.Namespace, Name: tracked.request.Intent.DeploymentName, UID: tracked.request.Intent.DeploymentUID,
	}
	details := domain.AuditDetails{
		Operation: &operation, DetailCode: &detail, PolicyVersion: &policy, Count: count,
	}
	if errorClass != "" {
		details.ErrorClass = &errorClass
	}
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType,
		Actor: domain.AuditActorSystem, Outcome: outcome, Scope: &scope, Subject: &subject,
		Details: details, CorrelationID: string(tracked.request.ID), IntegrityHash: string(tracked.request.Digest),
		OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return ErrApprovalResultAuditUnavailable
	}
	for attempt := 0; attempt < maxApprovalResultAuditAttempts; attempt++ {
		operationContext, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
		err = coordinator.resultAudits.AppendWriteResult(operationContext, event)
		cancel()
		if err == nil {
			return nil
		}
	}
	return ErrApprovalResultAuditUnavailable
}

func (coordinator *ApprovalCoordinator) publishResultAuditFailure(
	ctx context.Context,
	tracked trackedApproval,
	eventIndex int64,
) UIRestartExecution {
	event := newRestartExecutionEvent(tracked, eventIndex, UIRestartResultAuditFailed)
	event.ErrorClass = domain.SafeErrorClassPersistenceUnavailable
	coordinator.publishRestartExecution(ctx, event)
	return event
}

func (coordinator *ApprovalCoordinator) publishRestartExecution(ctx context.Context, execution UIRestartExecution) {
	if execution.Validate() != nil {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	deliveryContext, cancel := context.WithTimeout(ctx, coordinator.persistenceLimit)
	defer cancel()
	_ = coordinator.uiEvents.PublishUIEvent(deliveryContext, UIEvent{
		Kind: UIEventRestartExecution, RunID: execution.RunID,
		ScopeGeneration: execution.ScopeGeneration, Sequence: execution.Sequence,
		RestartExecution: &execution,
	})
}

func newRestartExecutionEvent(
	tracked trackedApproval,
	eventIndex int64,
	state UIRestartExecutionState,
) UIRestartExecution {
	return UIRestartExecution{
		RequestID: tracked.request.ID, RunID: tracked.request.RunID,
		ScopeGeneration: tracked.request.Intent.Scope.Generation, Sequence: tracked.sequence,
		EventIndex: eventIndex, Digest: tracked.request.Digest, State: state,
	}
}

func rolloutExecutionEvent(
	tracked trackedApproval,
	eventIndex int64,
	state UIRestartExecutionState,
	observation RestartRolloutObservation,
) UIRestartExecution {
	event := newRestartExecutionEvent(tracked, eventIndex, state)
	event.TargetGeneration = observation.TargetGeneration
	event.ObservedGeneration = observation.ObservedGeneration
	event.UpdatedReplicas = observation.UpdatedReplicas
	event.AvailableReplicas = observation.AvailableReplicas
	event.TargetReplicas = observation.TargetReplicas
	if state == UIRestartRolloutFailed {
		event.FailureCode = observation.FailureCode
	}
	return event
}

func safeApprovalExecutionClass(err error) domain.SafeErrorClass {
	if err == nil {
		return domain.SafeErrorClassInternal
	}
	if errors.Is(err, ErrInvalidRestartRollout) {
		return domain.SafeErrorClassInvalidExternalResponse
	}
	if errors.Is(err, errApprovalRolloutStaleScope) {
		return domain.SafeErrorClassStaleScope
	}
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) && classified.Class().Valid() {
		return classified.Class()
	}
	if errors.Is(err, context.Canceled) {
		return domain.SafeErrorClassCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.SafeErrorClassTimeout
	}
	return domain.SafeErrorClassInternal
}

func (coordinator *ApprovalCoordinator) approvalScopeMatches(expected domain.ScopeSnapshot) bool {
	if coordinator == nil || coordinator.scope == nil || expected.Validate() != nil {
		return false
	}
	current, ok := coordinator.scope.CurrentScope()
	return ok && current.Validate() == nil && current.Snapshot() == expected
}
