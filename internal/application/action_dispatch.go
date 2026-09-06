package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	ErrApprovalActionFailed             = errors.New("the approved action failed")
	ErrApprovalActionOutcomeUnknown     = errors.New("the approved action outcome is unknown")
	ErrApprovalActionVerificationFailed = errors.New("the approved action could not be verified")
)

// PrepareActionPolicy rejects an unadmitted, disabled, or profile-denied
// action before an operation-specific Kubernetes or filesystem preparation
// read. Submit re-evaluates the exact immutable envelope after preparation.
func (coordinator *ApprovalCoordinator) PrepareActionPolicy(
	sessionID domain.SessionID,
	scope domain.ScopeSnapshot,
	operation domain.ActionOperation,
	effect domain.CapabilityEffectClass,
	risk domain.RiskClass,
	enabled bool,
) (PermissionPolicy, domain.ClusterScope, error) {
	if coordinator == nil || !sessionID.Valid() || scope.Validate() != nil ||
		!operation.Valid() || !effect.Valid() || !risk.Valid() || risk == domain.RiskDeny ||
		!admittedSupervisedAction(operation) {
		return PermissionPolicy{}, domain.ClusterScope{}, ErrApprovalUnavailable
	}
	status := coordinator.permissions.Status(coordinator.now())
	if status.SessionID == "" {
		if err := coordinator.permissions.BindSession(sessionID); err != nil {
			return PermissionPolicy{}, domain.ClusterScope{}, ErrPermissionStale
		}
	}
	current, ok := coordinator.scope.CurrentScope()
	if !ok || current.Validate() != nil || current.Snapshot() != scope {
		return PermissionPolicy{}, domain.ClusterScope{}, ErrApprovalInvalidated
	}
	policy, evaluation := coordinator.permissions.EvaluateCatalog(sessionID, PermissionEvaluationInput{
		Operation: operation, Effect: effect, Risk: risk,
		CapabilityAdmitted: true, CapabilityEnabled: enabled,
	})
	if evaluation.Validate() != nil || evaluation.Disposition == domain.ReviewDispositionDeny {
		return PermissionPolicy{}, domain.ClusterScope{}, ErrPermissionDenied
	}
	return policy, current, nil
}

func admittedSupervisedAction(operation domain.ActionOperation) bool {
	switch operation {
	case domain.ActionOperationRestartDeployment,
		domain.ActionOperationScaleWorkload,
		domain.ActionOperationRollbackDeployment,
		domain.ActionOperationDeleteOwnedPod,
		domain.ActionOperationCordonNode,
		domain.ActionOperationUncordonNode,
		domain.ActionOperationDrainNode,
		domain.ActionOperationRestrictedLocalArgv,
		domain.ActionOperationShell,
		domain.ActionOperationPodDiagnostic,
		domain.ActionOperationPodExec,
		domain.ActionOperationContainerFileRead,
		domain.ActionOperationDiagnosticPod,
		domain.ActionOperationLogsCurrent,
		domain.ActionOperationLogsPrevious,
		domain.ActionOperationLogsAllContainers,
		domain.ActionOperationLogSearch,
		domain.ActionOperationPrometheusQuery,
		domain.ActionOperationLokiQuery:
		return true
	default:
		return false
	}
}

type toolAuthorizationResult struct {
	envelope domain.ActionEnvelope
	err      error
}

// AuthorizeRemoteDiagnosticAction routes one Tool-owned remote diagnostic
// through the shared human/Reviewer/automatic coordinator. The caller remains
// blocked until the exact request is consumed or safely closed, so no remote
// operation can start while approval is pending.
func (coordinator *ApprovalCoordinator) AuthorizeRemoteDiagnosticAction(
	ctx context.Context,
	policy PermissionPolicy,
	plan domain.RemoteDiagnosticActionPlan,
) (domain.ActionEnvelope, error) {
	if coordinator == nil || ctx == nil || coordinator.remoteDiagnostics == nil ||
		policy.Validate() != nil || plan.Validate() != nil || policy.Generation != plan.PolicyGeneration {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	intent, err := NewRemoteDiagnosticActionIntent(policy, plan)
	if err != nil {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	completion := make(chan toolAuthorizationResult, 1)
	return coordinator.authorizeToolAction(ctx, plan.RunID, plan.SessionID, intent,
		trackedAction{kind: trackedActionRemoteDiagnostic, remote: plan, authorizationCompletion: completion})
}

func (coordinator *ApprovalCoordinator) authorizeToolAction(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	intent domain.ActionIntent,
	action trackedAction,
) (domain.ActionEnvelope, error) {
	sequence, err := coordinator.nextToolActionSequence(runID)
	if err != nil {
		return domain.ActionEnvelope{}, toolAuthorizationError{class: domain.SafeErrorClassInternal}
	}
	request, err := coordinator.submitPreparedAction(ctx, runID, sessionID, sequence, intent, action)
	if err != nil {
		// Programmatic Reviewer resolution may consume the envelope before a
		// final freshness check fails. Preserve that consumed identity together
		// with the error so the Tool can append the required no-attempt outcome;
		// an error-bearing result is never executable authority.
		select {
		case result := <-action.authorizationCompletion:
			if result.envelope.Validate() == nil {
				return result.envelope, result.err
			}
		default:
		}
		return domain.ActionEnvelope{}, toolAuthorizationFailure(ctx, err)
	}
	select {
	case result := <-action.authorizationCompletion:
		if ctx.Err() != nil {
			if result.envelope.Validate() == nil {
				return result.envelope, toolAuthorizationError{class: remoteContextClass(ctx)}
			}
			return domain.ActionEnvelope{}, toolAuthorizationError{class: remoteContextClass(ctx)}
		}
		return result.envelope, result.err
	case <-ctx.Done():
		select {
		case result := <-action.authorizationCompletion:
			if result.envelope.Validate() == nil {
				return result.envelope, toolAuthorizationError{class: remoteContextClass(ctx)}
			}
		default:
		}
		closeContext, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
		_ = coordinator.cancelToolAction(closeContext, request.ID)
		select {
		case result := <-action.authorizationCompletion:
			cancel()
			if result.envelope.Validate() == nil {
				return result.envelope, toolAuthorizationError{class: remoteContextClass(ctx)}
			}
			return domain.ActionEnvelope{}, toolAuthorizationError{class: remoteContextClass(ctx)}
		default:
		}
		select {
		case result := <-action.authorizationCompletion:
			cancel()
			if result.envelope.Validate() == nil {
				return result.envelope, toolAuthorizationError{class: remoteContextClass(ctx)}
			}
			return domain.ActionEnvelope{}, toolAuthorizationError{class: remoteContextClass(ctx)}
		case <-closeContext.Done():
			cancel()
			return domain.ActionEnvelope{}, toolAuthorizationError{class: remoteContextClass(ctx)}
		}
	}
}

func (coordinator *ApprovalCoordinator) nextToolActionSequence(runID domain.AgentRunID) (int64, error) {
	if coordinator == nil || !runID.Valid() {
		return 0, ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.toolSequenceRun != runID {
		coordinator.toolSequenceRun = runID
		coordinator.toolSequence = 0
	}
	if coordinator.toolSequence >= 4096 {
		return 0, ErrApprovalUnavailable
	}
	coordinator.toolSequence++
	return coordinator.toolSequence, nil
}

func (coordinator *ApprovalCoordinator) cancelToolAction(ctx context.Context, requestID domain.ApprovalID) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.closeMatching(ctx, func(tracked trackedApproval) bool {
		return tracked.request.ID == requestID && tracked.action.toolAuthorization()
	}, domain.ApprovalReasonContextCancelled)
}

func toolAuthorizationFailure(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return toolAuthorizationError{class: remoteContextClass(ctx)}
	}
	switch {
	case errors.Is(err, ErrApprovalExpired):
		return toolAuthorizationError{class: domain.SafeErrorClassTimeout}
	case errors.Is(err, ErrApprovalPersistenceUnavailable), errors.Is(err, ErrApprovalResultAuditUnavailable):
		return toolAuthorizationError{class: domain.SafeErrorClassPersistenceUnavailable}
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrPermissionStale):
		return toolAuthorizationError{class: domain.SafeErrorClassPolicyDenied}
	case errors.Is(err, ErrApprovalInvalidated):
		return toolAuthorizationError{class: domain.SafeErrorClassStaleScope}
	default:
		return toolAuthorizationError{class: domain.SafeErrorClassInternal}
	}
}

type toolAuthorizationError struct{ class domain.SafeErrorClass }

func (err toolAuthorizationError) Error() string {
	return "The supervised Tool action was not authorized."
}
func (err toolAuthorizationError) Class() domain.SafeErrorClass {
	if err.class.Valid() {
		return err.class
	}
	return domain.SafeErrorClassInternal
}

func remoteContextClass(ctx context.Context) domain.SafeErrorClass {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.SafeErrorClassTimeout
	}
	return domain.SafeErrorClassCancelled
}

// SubmitRemediationAction retains the full typed target plan in memory while
// persisting only its canonical envelope and parameter digest.
func (coordinator *ApprovalCoordinator) SubmitRemediationAction(
	ctx context.Context,
	sequence int64,
	policy PermissionPolicy,
	plan domain.RemediationActionPlan,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || coordinator.remediation == nil || policy.Validate() != nil ||
		plan.Validate() != nil || policy.Generation != plan.PolicyGeneration {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	intent, err := NewRemediationActionIntent(policy, plan)
	if err != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	return coordinator.submitPreparedAction(ctx, plan.RunID, plan.SessionID, sequence, intent,
		trackedAction{kind: trackedActionRemediation, remediation: plan})
}

// SubmitLocalCommandAction retains one exact policy-owned direct argv plan.
func (coordinator *ApprovalCoordinator) SubmitLocalCommandAction(
	ctx context.Context,
	sequence int64,
	policy PermissionPolicy,
	plan domain.LocalCommandActionPlan,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || coordinator.localProcesses == nil || policy.Validate() != nil ||
		plan.Validate() != nil || policy.Generation != plan.PolicyGeneration {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	intent, err := plan.Intent(policy.Profile)
	if err != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	return coordinator.submitPreparedAction(ctx, plan.RunID, plan.SessionID, sequence, intent,
		trackedAction{kind: trackedActionLocalCommand, command: plan})
}

// SubmitLocalShellAction is deliberately separate from direct argv.
func (coordinator *ApprovalCoordinator) SubmitLocalShellAction(
	ctx context.Context,
	sequence int64,
	policy PermissionPolicy,
	plan domain.LocalShellActionPlan,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || coordinator.localProcesses == nil || policy.Validate() != nil ||
		plan.Validate() != nil || policy.Generation != plan.PolicyGeneration {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	intent, err := plan.Intent(policy.Profile)
	if err != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	return coordinator.submitPreparedAction(ctx, plan.RunID, plan.SessionID, sequence, intent,
		trackedAction{kind: trackedActionLocalShell, shell: plan})
}

// ConsumeApprovedAction is the only shared dispatcher. Restart retains its
// mature rollout path; every other branch is an exact consumer-owned port.
func (coordinator *ApprovalCoordinator) ConsumeApprovedAction(
	ctx context.Context,
	command UICommand,
) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || command.Kind != UICommandApproveAction || command.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	tracked, ok := coordinator.active[command.ApprovalID]
	coordinator.mu.Unlock()
	if !ok {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if tracked.action.kind == trackedActionRestart {
		return coordinator.ConsumeApprovedRestart(ctx, command)
	}
	return coordinator.consumeApprovedDispatched(ctx, command, tracked)
}

func (coordinator *ApprovalCoordinator) consumeApprovedDispatched(
	ctx context.Context,
	command UICommand,
	tracked trackedApproval,
) (UIApprovalResult, error) {
	if err := ctx.Err(); err != nil {
		return UIApprovalResult{}, err
	}
	coordinator.mu.Lock()
	currentTracked, ok := coordinator.active[command.ApprovalID]
	if !ok || currentTracked.request.State != domain.ApprovalStateApproved ||
		currentTracked.request.RunID != command.RunID || currentTracked.consuming ||
		currentTracked.sequence != command.ApprovalSequence ||
		currentTracked.request.Intent.Scope.Generation != command.ExpectedScopeGeneration ||
		currentTracked.request.Intent.PolicyGeneration != command.ExpectedPolicyGeneration ||
		currentTracked.action.kind != tracked.action.kind || !currentTracked.action.matches(currentTracked.request.Intent) {
		coordinator.mu.Unlock()
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	currentTracked.consuming = true
	coordinator.active[command.ApprovalID] = currentTracked
	tracked = currentTracked
	coordinator.mu.Unlock()

	current, currentOK := coordinator.scope.CurrentScope()
	currentScope := domain.ScopeSnapshot{}
	if currentOK {
		currentScope = current.Snapshot()
	}
	claim, claimErr := coordinator.service.Claim(ctx, approval.ConsumeCommand{
		RequestID: command.ApprovalID, ShownDigest: command.ApprovalDigest,
		Nonce: command.ApprovalNonce, CurrentScope: currentScope,
	})
	if claimErr != nil {
		return coordinator.finishClaimFailure(ctx, tracked, claim.Request, claimErr)
	}
	if claim.Validate() != nil || !coordinator.actionCurrent(claim.Request.ActionEnvelope()) ||
		!tracked.action.matches(claim.Request.Intent) {
		return coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
	}
	if err := coordinator.revalidateDispatchedAction(ctx, tracked, claim.Request.Intent); err != nil {
		reason := domain.ApprovalReasonTargetChanged
		if ctx.Err() != nil {
			reason = domain.ApprovalReasonContextCancelled
		}
		return coordinator.invalidateClaim(ctx, tracked, reason)
	}
	if !coordinator.actionCurrent(claim.Request.ActionEnvelope()) {
		return coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
	}
	consumed, consumeErr := coordinator.service.CommitConsume(ctx, claim)
	if consumeErr != nil {
		return coordinator.finishClaimFailure(ctx, tracked, consumed, consumeErr)
	}
	if consumed.Validate() != nil || consumed.State != domain.ApprovalStateConsumed {
		return coordinator.finishClaimFailure(ctx, tracked, consumed, domain.NewApprovalError(domain.ApprovalErrorCodeInternal))
	}
	coordinator.mu.Lock()
	latest, stillTracked := coordinator.active[command.ApprovalID]
	if !stillTracked || latest.request.ID != tracked.request.ID || latest.sequence != tracked.sequence ||
		latest.action.kind != tracked.action.kind {
		coordinator.mu.Unlock()
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	delete(coordinator.active, command.ApprovalID)
	coordinator.mu.Unlock()

	result := projectUIApprovalResult(consumed, tracked.sequence)
	if tracked.action.toolAuthorization() {
		envelope := consumed.ActionEnvelope()
		authorizationErr := coordinator.remotePostConsumeError(ctx, envelope)
		execution := UIActionExecution{
			RequestID: consumed.ID, RunID: consumed.RunID,
			ScopeGeneration: consumed.Intent.Scope.Generation, Sequence: tracked.sequence,
			Digest: consumed.Digest, Operation: consumed.Intent.Operation, Limits: consumed.Intent.Limits,
			Authorization: &UIToolActionAuthorization{State: uiToolActionAuthorized},
		}
		result.ActionExecution = &execution
		if result.Validate() != nil || coordinator.publishClosedRequired(ctx, result) != nil {
			tracked.action.completeAuthorization(domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassInternal})
			return result, ErrApprovalUnavailable
		}
		tracked.action.completeAuthorization(envelope, authorizationErr)
		if authorizationErr != nil {
			return result, ErrPermissionStale
		}
		return result, nil
	}
	execution, resultErr := coordinator.executeDispatchedAction(ctx, tracked, consumed.ActionEnvelope())
	result.ActionExecution = &execution
	if result.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	return result, resultErr
}

func (coordinator *ApprovalCoordinator) remotePostConsumeError(ctx context.Context, envelope domain.ActionEnvelope) error {
	if ctx == nil || ctx.Err() != nil {
		return remoteGateError{class: remoteContextClass(ctx)}
	}
	now := coordinator.now()
	if !validCoordinatorTime(now) {
		return remoteGateError{class: domain.SafeErrorClassInternal}
	}
	if !now.Before(envelope.ExpiresAt) {
		return remoteGateError{class: domain.SafeErrorClassTimeout}
	}
	if !coordinator.actionCurrent(envelope) {
		return remoteGateError{class: domain.SafeErrorClassStaleScope}
	}
	return nil
}

func (coordinator *ApprovalCoordinator) revalidateDispatchedAction(
	ctx context.Context,
	tracked trackedApproval,
	intent domain.ActionIntent,
) error {
	switch tracked.action.kind {
	case trackedActionRemediation:
		if !tracked.action.remediation.MatchesIntent(intent) {
			return ErrRemediationActionInvalid
		}
		return coordinator.remediation.RevalidateRemediationAction(ctx, tracked.action.remediation)
	case trackedActionLocalCommand:
		if !tracked.action.command.MatchesIntent(intent) {
			return ErrLocalActionInvalid
		}
		return coordinator.revalidateLocalPaths(ctx, tracked.action.command.Observation, intent.Parameters)
	case trackedActionLocalShell:
		if !tracked.action.shell.MatchesIntent(intent) {
			return ErrLocalActionInvalid
		}
		return coordinator.revalidateLocalPaths(ctx, tracked.action.shell.Observation, intent.Parameters)
	case trackedActionRemoteDiagnostic:
		if coordinator.remoteDiagnostics == nil || !tracked.action.remote.MatchesIntent(intent) {
			return ErrRemoteDiagnosticActionInvalid
		}
		return coordinator.remoteDiagnostics.RevalidateRemoteDiagnosticAction(ctx, tracked.action.remote)
	case trackedActionObservation:
		if coordinator.observations == nil || !tracked.action.observation.MatchesIntent(intent) {
			return ErrObservationActionInvalid
		}
		return coordinator.observations.RevalidateObservationAction(ctx, tracked.action.observation)
	default:
		return ErrApprovalUnavailable
	}
}

func (coordinator *ApprovalCoordinator) revalidateLocalPaths(
	ctx context.Context,
	want domain.LocalExecutionObservation,
	parameters domain.ActionParameters,
) error {
	observation, err := coordinator.localProcesses.Inspect(ctx, parameters.Executable, parameters.WorkingDirectory)
	if err != nil {
		return err
	}
	if observation != want || observation.ExecutableID != parameters.ExecutableID ||
		observation.WorkingDirectoryID != parameters.WorkingDirectoryID {
		return ErrLocalActionInvalid
	}
	return nil
}

func (coordinator *ApprovalCoordinator) executeDispatchedAction(
	ctx context.Context,
	tracked trackedApproval,
	envelope domain.ActionEnvelope,
) (UIActionExecution, error) {
	event := UIActionExecution{
		RequestID: envelope.RequestID, RunID: envelope.RunID,
		ScopeGeneration: envelope.Intent.Scope.Generation, Sequence: tracked.sequence,
		Digest: envelope.Digest, Operation: envelope.Intent.Operation, Limits: envelope.Intent.Limits,
	}
	if ctx == nil || ctx.Err() != nil || !coordinator.now().Before(envelope.ExpiresAt) ||
		!coordinator.actionCurrent(envelope) {
		return coordinator.notAttemptedAction(event, tracked, envelope, contextOrPolicyClass(ctx))
	}
	executionContext, cancel := context.WithTimeout(ctx, envelope.Intent.Limits.Timeout)
	defer cancel()
	switch tracked.action.kind {
	case trackedActionRemediation:
		attempt, executeErr := coordinator.remediation.ExecuteRemediationAction(executionContext, tracked.action.remediation)
		if attempt.Validate(envelope.Intent.Limits.MaximumItems) != nil {
			attempt = domain.RemediationAttempt{State: domain.RemediationUnknown, AttemptedCount: 1, ErrorClass: domain.SafeErrorClassInvalidExternalResponse}
			executeErr = ErrRemediationActionInvalid
		}
		if err := coordinator.persistRemediationAttemptAudits(tracked, attempt); err != nil {
			result := domain.RemediationResult{Operation: envelope.Intent.Operation, Attempt: attempt}
			if attempt.State == domain.RemediationAccepted {
				result.Verification = domain.RemediationVerificationUnavailable
				result.ErrorClass = domain.SafeErrorClassPersistenceUnavailable
			} else {
				result.Verification = domain.RemediationVerificationNotAttempted
			}
			event.Remediation = &result
			return event, ErrApprovalResultAuditUnavailable
		}
		result := domain.RemediationResult{
			Operation: envelope.Intent.Operation, Attempt: attempt,
			Verification: domain.RemediationVerificationNotAttempted,
		}
		if attempt.State == domain.RemediationAccepted {
			result.Verification, result.ErrorClass = coordinator.verifyRemediation(executionContext, tracked, envelope)
			if result.Verification == domain.RemediationVerified {
				result.VerifiedAt = remediationVerifiedAt(coordinator.now)
			}
		}
		event.Remediation = &result
		if result.Validate(envelope.Intent.Limits.MaximumItems) != nil {
			return event, ErrApprovalActionOutcomeUnknown
		}
		if err := coordinator.persistRemediationVerificationAudit(tracked, result); err != nil {
			return event, ErrApprovalResultAuditUnavailable
		}
		return event, remediationResultError(result, executeErr)
	case trackedActionLocalCommand, trackedActionLocalShell:
		result, executeErr := coordinator.localProcesses.Execute(executionContext, envelope)
		if result.Validate(envelope.Intent.Limits) != nil {
			result = localNotAttempted(domain.SafeErrorClassInvalidExternalResponse)
			executeErr = ErrLocalActionInvalid
		}
		event.LocalProcess = &result
		event.SafeOutput = result.SafeOutput
		if err := coordinator.persistLocalResultAudit(tracked, envelope, result); err != nil {
			return event, ErrApprovalResultAuditUnavailable
		}
		return event, localResultError(result, executeErr)
	default:
		return event, ErrApprovalUnavailable
	}
}

func (coordinator *ApprovalCoordinator) notAttemptedAction(
	event UIActionExecution,
	tracked trackedApproval,
	envelope domain.ActionEnvelope,
	class domain.SafeErrorClass,
) (UIActionExecution, error) {
	if tracked.action.kind == trackedActionRemediation {
		result := domain.RemediationResult{
			Operation:    envelope.Intent.Operation,
			Attempt:      domain.RemediationAttempt{State: domain.RemediationNotAttempted},
			Verification: domain.RemediationVerificationNotAttempted,
		}
		event.Remediation = &result
		if err := coordinator.persistRemediationVerificationAudit(tracked, result); err != nil {
			return event, ErrApprovalResultAuditUnavailable
		}
	} else {
		result := localNotAttempted(class)
		event.LocalProcess, event.SafeOutput = &result, result.SafeOutput
		if err := coordinator.persistLocalResultAudit(tracked, envelope, result); err != nil {
			return event, ErrApprovalResultAuditUnavailable
		}
	}
	return event, ErrApprovalActionFailed
}

func contextOrPolicyClass(ctx context.Context) domain.SafeErrorClass {
	if ctx != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return domain.SafeErrorClassTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return domain.SafeErrorClassCancelled
		}
	}
	return domain.SafeErrorClassStaleScope
}

func localNotAttempted(class domain.SafeErrorClass) domain.LocalCommandResult {
	if !class.Valid() {
		class = domain.SafeErrorClassInternal
	}
	return domain.LocalCommandResult{
		State:        domain.LocalProcessNotAttempted,
		OutputDigest: domain.LocalSafeOutputDigest(""), ErrorClass: class,
	}
}

func (coordinator *ApprovalCoordinator) verifyRemediation(
	ctx context.Context,
	tracked trackedApproval,
	envelope domain.ActionEnvelope,
) (domain.RemediationVerificationState, domain.SafeErrorClass) {
	if !coordinator.actionCurrent(envelope) {
		return domain.RemediationVerificationUnavailable, domain.SafeErrorClassStaleScope
	}
	state, err := coordinator.remediation.VerifyRemediationAction(ctx, tracked.action.remediation)
	if state == domain.RemediationVerified && err == nil && coordinator.actionCurrent(envelope) {
		return state, ""
	}
	if err == nil {
		return domain.RemediationVerificationFailed, domain.SafeErrorClassConflict
	}
	return state, safeApprovalExecutionClass(err)
}

func remediationResultError(result domain.RemediationResult, executeErr error) error {
	switch result.Attempt.State {
	case domain.RemediationUnknown:
		return ErrApprovalActionOutcomeUnknown
	case domain.RemediationFailed, domain.RemediationNotAttempted:
		return ErrApprovalActionFailed
	}
	if executeErr != nil {
		return ErrApprovalActionOutcomeUnknown
	}
	if result.Verification != domain.RemediationVerified {
		return ErrApprovalActionVerificationFailed
	}
	return nil
}

func localResultError(result domain.LocalCommandResult, executeErr error) error {
	switch result.State {
	case domain.LocalProcessExited:
		return executeErr
	case domain.LocalProcessUnknown:
		return ErrApprovalActionOutcomeUnknown
	default:
		return ErrApprovalActionFailed
	}
}

func (coordinator *ApprovalCoordinator) persistRemediationAttemptAudits(
	tracked trackedApproval,
	attempt domain.RemediationAttempt,
) error {
	if attempt.State == domain.RemediationNotAttempted {
		return nil
	}
	for index := 0; index < attempt.AttemptedCount; index++ {
		event, err := coordinator.remediationPhaseAudit(tracked, attempt, index)
		if err != nil || coordinator.persistActionResultAudit(event) != nil {
			return ErrApprovalResultAuditUnavailable
		}
	}
	return nil
}

func (coordinator *ApprovalCoordinator) remediationPhaseAudit(
	tracked trackedApproval,
	attempt domain.RemediationAttempt,
	index int,
) (domain.AuditEvent, error) {
	id, err := coordinator.auditIDs.NewAuditEventID()
	if err != nil || !id.Valid() || index < 0 || index >= attempt.AttemptedCount {
		return domain.AuditEvent{}, ErrApprovalResultAuditUnavailable
	}
	eventType, outcome, detail := domain.AuditEventWriteAttempted, domain.AuditOutcomeFailure, "request_failed"
	if index < attempt.AcceptedCount {
		outcome, detail = domain.AuditOutcomeSuccess, "request_accepted"
	} else if attempt.State == domain.RemediationUnknown {
		eventType, outcome, detail = domain.AuditEventWriteOutcomeUnknown, domain.AuditOutcomeUnknown, "request_outcome_unknown"
	}
	subject, ok := remediationAttemptSubject(tracked.action.remediation, index)
	if !ok {
		return domain.AuditEvent{}, ErrApprovalResultAuditUnavailable
	}
	operation, policy := string(tracked.request.Intent.Operation), tracked.request.Intent.PolicyVersion
	count := int64(index + 1)
	details := domain.AuditDetails{Operation: &operation, DetailCode: &detail, PolicyVersion: &policy, Count: &count}
	if index >= attempt.AcceptedCount && attempt.ErrorClass.Valid() {
		class := attempt.ErrorClass
		details.ErrorClass = &class
	}
	sessionID, runID, scope := tracked.request.SessionID, tracked.request.RunID, tracked.request.Intent.Scope
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType,
		Actor: domain.AuditActorSystem, Outcome: outcome, Scope: &scope, Subject: &subject,
		Details: details, CorrelationID: string(tracked.request.ID), IntegrityHash: string(tracked.request.Digest),
		OccurredAt: coordinator.now(),
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrApprovalResultAuditUnavailable
	}
	return event, nil
}

func remediationAttemptSubject(plan domain.RemediationActionPlan, index int) (domain.ResourceRef, bool) {
	if index == 0 {
		return plan.Target.Resource, true
	}
	current := 1
	for _, member := range plan.TargetSet.Members() {
		if member.Role != domain.RemediationMemberDrainPod {
			continue
		}
		if current == index {
			return member.Resource, true
		}
		current++
	}
	return domain.ResourceRef{}, false
}

func (coordinator *ApprovalCoordinator) persistRemediationVerificationAudit(
	tracked trackedApproval,
	result domain.RemediationResult,
) error {
	id, err := coordinator.auditIDs.NewAuditEventID()
	if err != nil || !id.Valid() {
		return ErrApprovalResultAuditUnavailable
	}
	event, err := RemediationOutcomeAudit(id, tracked.request.ActionEnvelope(), result, coordinator.now())
	if err != nil {
		return ErrApprovalResultAuditUnavailable
	}
	return coordinator.persistActionResultAudit(event)
}

func (coordinator *ApprovalCoordinator) persistLocalResultAudit(
	tracked trackedApproval,
	envelope domain.ActionEnvelope,
	result domain.LocalCommandResult,
) error {
	id, err := coordinator.auditIDs.NewAuditEventID()
	if err != nil || !id.Valid() {
		return ErrApprovalResultAuditUnavailable
	}
	event, err := LocalOutcomeAudit(id, envelope, result, coordinator.now())
	if err != nil {
		return ErrApprovalResultAuditUnavailable
	}
	return coordinator.persistActionResultAudit(event)
}

func (coordinator *ApprovalCoordinator) persistActionResultAudit(event domain.AuditEvent) error {
	if event.Validate() != nil {
		return ErrApprovalResultAuditUnavailable
	}
	for attempt := 0; attempt < maxApprovalResultAuditAttempts; attempt++ {
		operationContext, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
		err := coordinator.resultAudits.AppendWriteResult(operationContext, event)
		cancel()
		if err == nil {
			return nil
		}
	}
	return ErrApprovalResultAuditUnavailable
}

// Ensure UTC result times even when a test clock implementation returns a
// value with equivalent wall time in another location.
func remediationVerifiedAt(now func() time.Time) time.Time {
	return now().UTC().Truncate(time.Millisecond)
}
