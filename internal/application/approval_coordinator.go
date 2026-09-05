package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	maxApprovalRecoveryBatch       = 1000
	maxApprovalResultAuditAttempts = 3
)

var (
	// ErrApprovalCoordinatorDependency reports an invalid approval composition.
	ErrApprovalCoordinatorDependency = errors.New("Application approval dependencies are invalid")
	// ErrApprovalUnavailable reports an absent, stale, or duplicate request.
	ErrApprovalUnavailable = errors.New("the approval request is unavailable")
	// ErrApprovalBusy reports that one unexecuted request still owns authority.
	ErrApprovalBusy = errors.New("another approval request is still active")
	// ErrApprovalPersistenceUnavailable reports a fail-closed durable transition.
	ErrApprovalPersistenceUnavailable = errors.New("approval persistence is unavailable")
	// ErrApprovalExpired reports a durably closed TTL boundary.
	ErrApprovalExpired = errors.New("the approval request expired")
	// ErrApprovalInvalidated reports a durably closed proof or scope mismatch.
	ErrApprovalInvalidated = errors.New("the approval request was invalidated")
	// ErrApprovalExecutionFailed reports one terminal fixed executor attempt.
	ErrApprovalExecutionFailed = errors.New("the approved Deployment restart request failed")
	// ErrApprovalPatchOutcomeUnknown reports an ambiguous sole PATCH attempt.
	ErrApprovalPatchOutcomeUnknown = errors.New("the Deployment restart PATCH outcome is unknown")
	// ErrApprovalRolloutTimedOut reports an accepted PATCH whose rollout did not
	// reach a terminal conclusion inside the fixed observation window.
	ErrApprovalRolloutTimedOut = errors.New("the Deployment rollout observation timed out")
	// ErrApprovalRolloutFailed reports an accepted PATCH with a fixed rollout failure.
	ErrApprovalRolloutFailed = errors.New("the Deployment rollout failed")
	// ErrApprovalRolloutUnavailable reports an accepted PATCH that could not be verified.
	ErrApprovalRolloutUnavailable = errors.New("the Deployment rollout verification is unavailable")
	// ErrApprovalResultAuditUnavailable reports exhausted bounded post-attempt audit writes.
	ErrApprovalResultAuditUnavailable = errors.New("the approved action result audit is unavailable")
)

// ApprovalLifecycle is the narrow approval service surface used by Application.
type ApprovalLifecycle interface {
	Request(context.Context, approval.RequestCommand) (domain.ApprovalRequest, error)
	Decide(context.Context, approval.DecisionCommand) (domain.ApprovalRequest, domain.ApprovalDecision, error)
	Expire(context.Context, domain.ApprovalID) (domain.ApprovalRequest, error)
	Cancel(context.Context, domain.ApprovalID, domain.ApprovalStateReason) (domain.ApprovalRequest, error)
	Invalidate(context.Context, domain.ApprovalID, domain.ApprovalStateReason) (domain.ApprovalRequest, error)
	Claim(context.Context, approval.ConsumeCommand) (approval.ExecutionClaim, error)
	CommitConsume(context.Context, approval.ExecutionClaim) (domain.ApprovalRequest, error)
}

// ConsumeApprovedRestart owns the typed restart path: durable decision proof,
// fresh target validation, atomic consume/pre-audit, final scope and policy
// checks, and at most one exact executor call.
func (coordinator *ApprovalCoordinator) ConsumeApprovedRestart(
	ctx context.Context,
	command UICommand,
) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || command.Kind != UICommandApproveAction || command.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return UIApprovalResult{}, err
	}
	coordinator.mu.Lock()
	tracked, ok := coordinator.active[command.ApprovalID]
	if !ok || tracked.request.State != domain.ApprovalStateApproved || tracked.request.RunID != command.RunID ||
		tracked.consuming || tracked.sequence != command.ApprovalSequence ||
		tracked.request.Intent.Scope.Generation != command.ExpectedScopeGeneration ||
		tracked.action.kind != trackedActionRestart || !tracked.action.matches(tracked.request.Intent) {
		coordinator.mu.Unlock()
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	tracked.consuming = true
	coordinator.active[command.ApprovalID] = tracked
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
	if claim.Validate() != nil || !coordinator.actionCurrent(claim.Request.ActionEnvelope()) {
		return coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonPolicyChanged)
	}
	observation, revalidateErr := coordinator.restartValidator.RevalidateApprovedRestart(ctx, claim.Request.Intent)
	if revalidateErr != nil {
		reason := domain.ApprovalReasonTargetChanged
		if ctx.Err() != nil {
			reason = domain.ApprovalReasonContextCancelled
		}
		return coordinator.invalidateClaim(ctx, tracked, reason)
	}
	execution, err := approval.NewRestartDeploymentExecution(claim.Request.Intent, observation)
	if err != nil || !coordinator.actionCurrent(claim.Request.ActionEnvelope()) {
		return coordinator.invalidateClaim(ctx, tracked, domain.ApprovalReasonTargetChanged)
	}
	consumed, consumeErr := coordinator.service.CommitConsume(ctx, claim)
	if consumeErr != nil {
		return coordinator.finishClaimFailure(ctx, tracked, consumed, consumeErr)
	}
	if consumed.Validate() != nil || consumed.State != domain.ApprovalStateConsumed {
		return coordinator.finishClaimFailure(ctx, tracked, consumed, domain.NewApprovalError(domain.ApprovalErrorCodeInternal))
	}
	coordinator.mu.Lock()
	currentTracked, stillTracked := coordinator.active[command.ApprovalID]
	if !stillTracked || currentTracked.request.ID != tracked.request.ID || currentTracked.sequence != tracked.sequence {
		coordinator.mu.Unlock()
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	delete(coordinator.active, command.ApprovalID)
	coordinator.mu.Unlock()

	attempt, executeErr := coordinator.executeRestartOnce(ctx, consumed, execution, observation)
	result := projectUIApprovalResult(consumed, tracked.sequence)
	executionResult, resultErr := coordinator.finishRestartExecution(ctx, tracked, attempt, executeErr)
	result.Execution = &executionResult
	if result.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	return result, resultErr
}

func (coordinator *ApprovalCoordinator) actionCurrent(envelope domain.ActionEnvelope) bool {
	if coordinator == nil || !coordinator.permissions.ActionCurrent(envelope) {
		return false
	}
	current, ok := coordinator.scope.CurrentScope()
	return ok && current.Validate() == nil && current.Snapshot() == envelope.Intent.Scope &&
		current.NamespaceAccess == envelope.Intent.NamespaceAccess
}

func (coordinator *ApprovalCoordinator) executeRestartOnce(
	ctx context.Context,
	request domain.ApprovalRequest,
	execution approval.RestartDeploymentExecution,
	observation approval.RestartDeploymentObservation,
) (approval.RestartDeploymentAttempt, error) {
	notAttempted := approval.RestartDeploymentAttempt{State: approval.RestartDeploymentNotAttempted}
	if ctx == nil || ctx.Err() != nil || !coordinator.now().Before(request.ExpiresAt) ||
		!coordinator.actionCurrent(request.ActionEnvelope()) {
		return notAttempted, ErrPermissionStale
	}
	result, err := coordinator.restartExecutor.ExecuteApprovedRestart(ctx, execution)
	if err != nil {
		return classifyRestartDeploymentAttempt(err), err
	}
	if result.Validate() != nil || result.Scope != observation.Scope ||
		result.DeploymentName != observation.DeploymentName || result.DeploymentUID != observation.DeploymentUID ||
		result.PreviousResourceVersion != observation.ResourceVersion || result.ResourceVersion == observation.ResourceVersion ||
		result.TargetGeneration-observation.DeploymentGeneration != 1 {
		return approval.RestartDeploymentAttempt{
			State: approval.RestartDeploymentPatchUnknown, ErrorClass: domain.SafeErrorClassInvalidExternalResponse,
		}, domain.NewApprovalError(domain.ApprovalErrorCodeExecutorFailed)
	}
	attempt := approval.RestartDeploymentAttempt{
		State: approval.RestartDeploymentPatchAccepted,
		Acceptance: approval.RestartDeploymentAcceptance{
			TargetGeneration: result.TargetGeneration, TargetReplicas: result.TargetReplicas,
		},
	}
	if !coordinator.actionCurrent(request.ActionEnvelope()) {
		return attempt, ErrPermissionStale
	}
	return attempt, nil
}

func classifyRestartDeploymentAttempt(err error) approval.RestartDeploymentAttempt {
	errorClass := safeApprovalExecutionClass(err)
	state := approval.RestartDeploymentPatchFailed
	switch errorClass {
	case domain.SafeErrorClassCancelled, domain.SafeErrorClassTimeout, domain.SafeErrorClassUnavailable,
		domain.SafeErrorClassStaleScope, domain.SafeErrorClassInvalidExternalResponse, domain.SafeErrorClassInternal:
		state = approval.RestartDeploymentPatchUnknown
	}
	return approval.RestartDeploymentAttempt{State: state, ErrorClass: errorClass}
}

func (coordinator *ApprovalCoordinator) invalidateClaim(
	ctx context.Context,
	tracked trackedApproval,
	reason domain.ApprovalStateReason,
) (UIApprovalResult, error) {
	var updated domain.ApprovalRequest
	var err error
	if reason.ValidCancellation() {
		updated, err = coordinator.service.Cancel(ctx, tracked.request.ID, reason)
	} else {
		updated, err = coordinator.service.Invalidate(ctx, tracked.request.ID, reason)
	}
	if err != nil && updated.ID == "" {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	return coordinator.finishClaimFailure(ctx, tracked, updated, err)
}

func (coordinator *ApprovalCoordinator) finishClaimFailure(
	ctx context.Context,
	tracked trackedApproval,
	updated domain.ApprovalRequest,
	cause error,
) (UIApprovalResult, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	current, ok := coordinator.active[tracked.request.ID]
	if !ok || current.request.ID != tracked.request.ID || current.sequence != tracked.sequence || updated.ID != tracked.request.ID {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if updated.State == domain.ApprovalStateInvalidated || updated.State == domain.ApprovalStateCancelled ||
		updated.State == domain.ApprovalStateExpired {
		if err := coordinator.persistClosed(ctx, tracked.request.State, updated); err != nil {
			delete(coordinator.active, tracked.request.ID)
			return UIApprovalResult{}, ErrApprovalPersistenceUnavailable
		}
	}
	delete(coordinator.active, tracked.request.ID)
	result := projectUIApprovalResult(updated, tracked.sequence)
	if result.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if errors.Is(cause, approval.ErrPreWritePersistenceUnavailable) {
		return result, ErrApprovalPersistenceUnavailable
	}
	return result, ErrApprovalInvalidated
}

// ApprovalPersistence owns each atomic approval and audit transaction intent.
type ApprovalPersistence interface {
	CreateWithAudit(context.Context, domain.ApprovalRequest, domain.AuditEvent) error
	ResolveWithAudit(context.Context, domain.ApprovalState, domain.ApprovalRequest, domain.ApprovalDecision, domain.AuditEvent) error
	CloseWithAudit(context.Context, domain.ApprovalState, domain.ApprovalRequest, domain.AuditEvent) error
	Get(context.Context, domain.ApprovalID) (approval.StoredRequest, *approval.StoredDecision, error)
	ListRecoverable(context.Context, int) ([]approval.StoredRequest, error)
	RecoverWithAudits(context.Context, []approval.RecoveryTransition) error
}

// ApprovalResultAudits owns idempotent post-attempt write-result records.
type ApprovalResultAudits interface {
	AppendWriteResult(context.Context, domain.AuditEvent) error
}

// ApprovalIdentifierSource supplies application-owned UUIDv7 request IDs.
type ApprovalIdentifierSource interface {
	NewApprovalID() (domain.ApprovalID, error)
	NewModelRequestID() (domain.ModelRequestID, error)
}

// ApprovalCurrentScope supplies the exact currently verified scope.
type ApprovalCurrentScope interface {
	CurrentScope() (domain.ClusterScope, bool)
}

// ApprovalCoordinatorConfig keeps the operation-specific executor at the
// Application boundary; the approval lifecycle never performs external I/O.
type ApprovalCoordinatorConfig struct {
	Service            ApprovalLifecycle
	Persistence        ApprovalPersistence
	ResultAudits       ApprovalResultAudits
	Scope              ApprovalCurrentScope
	ApprovalIDs        ApprovalIdentifierSource
	AuditIDs           AuditIdentifierSource
	UIEvents           UIEventSink
	Rollout            RestartRolloutObserver
	RestartRevalidator approval.RestartDeploymentRevalidator
	RestartExecutor    approval.RestartDeploymentExecutor
	Remediation        RemediationActionExecutor
	LocalProcesses     LocalProcessExecutor
	Permissions        *PermissionManager
	Reviewer           *ReviewerModelBinding
	Reviews            ActionReviewPersistence
	Now                func() time.Time
	PersistenceTimeout time.Duration
}

// ApprovalCoordinator serializes one pending dialog and its possible
// approved-not-executed lifetime, then asks the approval service to consume it.
type ApprovalCoordinator struct {
	mu sync.Mutex

	service          ApprovalLifecycle
	persistence      ApprovalPersistence
	resultAudits     ApprovalResultAudits
	scope            ApprovalCurrentScope
	approvalIDs      ApprovalIdentifierSource
	auditIDs         AuditIdentifierSource
	uiEvents         UIEventSink
	rollout          RestartRolloutObserver
	restartValidator approval.RestartDeploymentRevalidator
	restartExecutor  approval.RestartDeploymentExecutor
	remediation      RemediationActionExecutor
	localProcesses   LocalProcessExecutor
	permissions      *PermissionManager
	reviewer         *ReviewerModelBinding
	reviews          ActionReviewPersistence
	now              func() time.Time
	persistenceLimit time.Duration
	active           map[domain.ApprovalID]trackedApproval
}

type trackedApproval struct {
	request      domain.ApprovalRequest
	sequence     int64
	consuming    bool
	route        PermissionEvaluation
	reviewing    bool
	reviewCancel context.CancelFunc
	action       trackedAction
}

type trackedActionKind uint8

const (
	trackedActionRestart trackedActionKind = iota + 1
	trackedActionRemediation
	trackedActionLocalCommand
	trackedActionLocalShell
)

// trackedAction retains the complete project-owned execution plan only for
// the current process. Persistence receives the canonical parameter digest,
// never raw argv, paths, environment values, or a shell command.
type trackedAction struct {
	kind        trackedActionKind
	remediation domain.RemediationActionPlan
	command     domain.LocalCommandActionPlan
	shell       domain.LocalShellActionPlan
}

func (action trackedAction) matches(intent domain.ActionIntent) bool {
	switch action.kind {
	case trackedActionRestart:
		return intent.ValidateRestartDeployment() == nil
	case trackedActionRemediation:
		return action.remediation.MatchesIntent(intent)
	case trackedActionLocalCommand:
		return action.command.MatchesIntent(intent)
	case trackedActionLocalShell:
		return action.shell.MatchesIntent(intent)
	default:
		return false
	}
}

// NewApprovalCoordinator constructs a fail-closed non-executing coordinator.
func NewApprovalCoordinator(config ApprovalCoordinatorConfig) (*ApprovalCoordinator, error) {
	limit := config.PersistenceTimeout
	if limit == 0 {
		limit = DefaultPersistenceTimeout
	}
	if config.Service == nil || config.Persistence == nil || config.ResultAudits == nil || config.Scope == nil || config.ApprovalIDs == nil ||
		config.AuditIDs == nil || config.UIEvents == nil || config.Rollout == nil || config.RestartRevalidator == nil ||
		config.RestartExecutor == nil || config.Permissions == nil || config.Reviews == nil ||
		config.Now == nil || !validCoordinatorTime(config.Now()) ||
		limit <= 0 || limit > MaxPersistenceTimeout {
		return nil, ErrApprovalCoordinatorDependency
	}
	coordinator := &ApprovalCoordinator{
		service: config.Service, persistence: config.Persistence, resultAudits: config.ResultAudits, scope: config.Scope,
		approvalIDs: config.ApprovalIDs, auditIDs: config.AuditIDs, uiEvents: config.UIEvents,
		rollout: config.Rollout, restartValidator: config.RestartRevalidator,
		restartExecutor: config.RestartExecutor, remediation: config.Remediation,
		localProcesses: config.LocalProcesses, permissions: config.Permissions,
		reviewer: config.Reviewer, reviews: config.Reviews, now: config.Now,
		persistenceLimit: limit, active: make(map[domain.ApprovalID]trackedApproval),
	}
	if config.Reviewer != nil && !config.Reviewer.valid() {
		return nil, ErrApprovalCoordinatorDependency
	}
	if err := config.Permissions.BindInvalidationHooks(coordinator, coordinator); err != nil {
		return nil, ErrApprovalCoordinatorDependency
	}
	return coordinator, nil
}

// PrepareRestartActionPolicy rejects disabled or denied work before the fresh
// Kubernetes target read. It returns only local immutable snapshots.
func (coordinator *ApprovalCoordinator) PrepareRestartActionPolicy(
	sessionID domain.SessionID,
	scope domain.ScopeSnapshot,
) (PermissionPolicy, domain.ClusterScope, error) {
	if coordinator == nil || !sessionID.Valid() || scope.Validate() != nil {
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
		Operation: domain.ActionOperationRestartDeployment, Effect: domain.CapabilityEffectClusterMutation,
		Risk: domain.RiskReview, CapabilityAdmitted: true, CapabilityEnabled: true,
	})
	if evaluation.Disposition == domain.ReviewDispositionDeny || evaluation.Validate() != nil {
		return PermissionPolicy{}, domain.ClusterScope{}, ErrPermissionDenied
	}
	return policy, current, nil
}

// SubmitRestartDeploymentProposal implements the proposal bridge sink. It
// persists request plus audit before making the dialog visible.
func (coordinator *ApprovalCoordinator) SubmitRestartDeploymentProposal(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	sequence int64,
	intent domain.OperationIntent,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || ctx == nil || !runID.Valid() || !sessionID.Valid() ||
		sequence < 1 || sequence > 4096 || intent.ValidateRestartDeployment() != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	return coordinator.submitPreparedAction(
		ctx, runID, sessionID, sequence, intent,
		trackedAction{kind: trackedActionRestart},
	)
}

func (coordinator *ApprovalCoordinator) submitPreparedAction(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	sequence int64,
	intent domain.ActionIntent,
	action trackedAction,
) (domain.ApprovalRequest, error) {
	if coordinator == nil || ctx == nil || !runID.Valid() || !sessionID.Valid() ||
		sequence < 1 || sequence > 4096 || !action.matches(intent) {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	coordinator.mu.Lock()
	if len(coordinator.active) != 0 {
		coordinator.mu.Unlock()
		return domain.ApprovalRequest{}, ErrApprovalBusy
	}
	current, ok := coordinator.scope.CurrentScope()
	if !ok || current.Validate() != nil || current.Snapshot() != intent.Scope ||
		current.NamespaceAccess != intent.NamespaceAccess {
		coordinator.mu.Unlock()
		return domain.ApprovalRequest{}, ErrApprovalInvalidated
	}
	id, err := coordinator.approvalIDs.NewApprovalID()
	if err != nil || !id.Valid() {
		coordinator.mu.Unlock()
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	request, err := coordinator.service.Request(ctx, approval.RequestCommand{
		ID: id, RunID: runID, SessionID: sessionID, Intent: intent,
	})
	if err != nil {
		coordinator.mu.Unlock()
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	route := coordinator.permissions.Evaluate(request.ActionEnvelope(), true, true, coordinator.now())
	if route.Validate() != nil || route.Disposition == domain.ReviewDispositionDeny {
		coordinator.cancelInMemory(request.ID)
		coordinator.mu.Unlock()
		return domain.ApprovalRequest{}, ErrPermissionDenied
	}
	audit, err := coordinator.newAudit(request)
	if err != nil || coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.persistence.CreateWithAudit(operationContext, request, audit)
	}) != nil {
		coordinator.cancelInMemory(request.ID)
		coordinator.mu.Unlock()
		return domain.ApprovalRequest{}, ErrApprovalPersistenceUnavailable
	}
	tracked := trackedApproval{request: request, sequence: sequence, route: route, action: action}
	coordinator.active[request.ID] = tracked
	coordinator.mu.Unlock()

	switch route.Disposition {
	case domain.ReviewDispositionHuman:
		if err := coordinator.publishApprovalDialog(ctx, tracked); err != nil {
			return domain.ApprovalRequest{}, err
		}
	case domain.ReviewDispositionAutomatic:
		if err := coordinator.resolveAutomaticAction(ctx, tracked); err != nil {
			return request, err
		}
	case domain.ReviewDispositionReviewer:
		if err := coordinator.reviewAction(ctx, tracked); err != nil {
			return request, err
		}
	default:
		return domain.ApprovalRequest{}, ErrPermissionDenied
	}
	return request, nil
}

func (coordinator *ApprovalCoordinator) publishApprovalDialog(ctx context.Context, tracked trackedApproval) error {
	request, sequence := tracked.request, tracked.sequence
	projection := projectUIApprovalRequest(request, sequence)
	event := UIEvent{
		Kind: UIEventApprovalRequested, RunID: request.RunID,
		ScopeGeneration: request.Intent.Scope.Generation, Sequence: sequence, Approval: &projection,
	}
	if projection.Validate() != nil || event.Validate() != nil || coordinator.uiEvents.PublishUIEvent(ctx, event) != nil {
		coordinator.mu.Lock()
		delete(coordinator.active, request.ID)
		coordinator.mu.Unlock()
		coordinator.closeAfterDeliveryFailure(ctx, request)
		return ErrApprovalUnavailable
	}
	return nil
}

// Decide maps a typed UI intent to a domain decision and persists it before
// returning a visible result. Approved remains approved-not-executed.
func (coordinator *ApprovalCoordinator) Decide(ctx context.Context, command UICommand) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil ||
		(command.Kind != UICommandApproveAction && command.Kind != UICommandRejectAction) {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return UIApprovalResult{}, err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	tracked, ok := coordinator.active[command.ApprovalID]
	if !ok || tracked.request.RunID != command.RunID || tracked.sequence != command.ApprovalSequence ||
		tracked.request.Intent.Scope.Generation != command.ExpectedScopeGeneration ||
		tracked.route.Disposition != domain.ReviewDispositionHuman {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	current, currentOK := coordinator.scope.CurrentScope()
	currentScope := domain.ScopeSnapshot{}
	if currentOK {
		currentScope = current.Snapshot()
	}
	choice := domain.ApprovalDecisionReject
	if command.Kind == UICommandApproveAction {
		choice = domain.ApprovalDecisionApprove
	}
	updated, decision, decideErr := coordinator.service.Decide(ctx, approval.DecisionCommand{
		RequestID: command.ApprovalID, Choice: choice,
		ShownDigest: command.ApprovalDigest, Nonce: command.ApprovalNonce, CurrentScope: currentScope,
		Actor: domain.ApprovalActorLocalUser, Disposition: domain.ReviewDispositionHuman,
	})
	if decideErr != nil {
		if updated.ID != tracked.request.ID ||
			(updated.State != domain.ApprovalStateExpired && updated.State != domain.ApprovalStateCancelled && updated.State != domain.ApprovalStateInvalidated) {
			return UIApprovalResult{}, ErrApprovalUnavailable
		}
		if err := coordinator.persistClosed(ctx, tracked.request.State, updated); err != nil {
			delete(coordinator.active, command.ApprovalID)
			return UIApprovalResult{}, ErrApprovalPersistenceUnavailable
		}
		delete(coordinator.active, command.ApprovalID)
		result := projectUIApprovalResult(updated, tracked.sequence)
		if updated.State == domain.ApprovalStateExpired {
			return result, ErrApprovalExpired
		}
		return result, ErrApprovalInvalidated
	}
	audit, err := coordinator.newAudit(updated)
	if err != nil || coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.persistence.ResolveWithAudit(operationContext, tracked.request.State, updated, decision, audit)
	}) != nil {
		coordinator.cancelInMemory(updated.ID)
		delete(coordinator.active, command.ApprovalID)
		return UIApprovalResult{}, ErrApprovalPersistenceUnavailable
	}
	if updated.State == domain.ApprovalStateApproved {
		tracked.request = updated
		coordinator.active[command.ApprovalID] = tracked
	} else {
		delete(coordinator.active, command.ApprovalID)
	}
	result := projectUIApprovalResult(updated, tracked.sequence)
	if result.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	return result, nil
}

// Expire closes one tracked request at the exact half-open TTL boundary.
func (coordinator *ApprovalCoordinator) Expire(ctx context.Context, requestID domain.ApprovalID) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || !requestID.Valid() {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	tracked, ok := coordinator.active[requestID]
	if !ok {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	updated, err := coordinator.service.Expire(ctx, requestID)
	if err != nil || updated.State != domain.ApprovalStateExpired {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if err := coordinator.persistClosed(ctx, tracked.request.State, updated); err != nil {
		delete(coordinator.active, requestID)
		return UIApprovalResult{}, ErrApprovalPersistenceUnavailable
	}
	delete(coordinator.active, requestID)
	result := projectUIApprovalResult(updated, tracked.sequence)
	coordinator.publishClosed(ctx, result)
	return result, nil
}

// ExpireCommand checks the complete dialog identity before applying TTL expiry.
func (coordinator *ApprovalCoordinator) ExpireCommand(ctx context.Context, command UICommand) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || command.Kind != UICommandExpireAction || command.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	tracked, ok := coordinator.active[command.ApprovalID]
	valid := ok && tracked.request.RunID == command.RunID && tracked.sequence == command.ApprovalSequence &&
		tracked.request.Intent.Scope.Generation == command.ExpectedScopeGeneration &&
		tracked.request.Digest.Equal(command.ApprovalDigest) && tracked.request.Nonce.Equal(command.ApprovalNonce)
	coordinator.mu.Unlock()
	if !valid {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	return coordinator.Expire(ctx, command.ApprovalID)
}

// CancelRun invalidates every tracked unexecuted request owned by one AgentRun.
func (coordinator *ApprovalCoordinator) CancelRun(ctx context.Context, runID domain.AgentRunID) error {
	if coordinator == nil || ctx == nil || !runID.Valid() {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.closeMatching(ctx, func(tracked trackedApproval) bool {
		return tracked.request.RunID == runID
	}, domain.ApprovalReasonRunCancelled)
}

// InvalidateScope implements the supervised-action scope invalidation hook.
// The caller has already advanced the generation.
func (coordinator *ApprovalCoordinator) InvalidateScope(generation int64) error {
	if coordinator == nil || generation < 1 {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
	defer cancel()
	return coordinator.closeMatching(ctx, func(tracked trackedApproval) bool {
		return tracked.request.Intent.Scope.Generation < generation
	}, domain.ApprovalReasonScopeChanged)
}

// InvalidatePolicy marks every older action stale after the new generation is
// committed. It performs this durable transition before cancellation.
func (coordinator *ApprovalCoordinator) InvalidatePolicy(generation domain.PolicyGeneration) error {
	if coordinator == nil || !generation.Valid() {
		return ErrApprovalUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), coordinator.persistenceLimit)
	defer cancel()
	return coordinator.closeMatching(ctx, func(tracked trackedApproval) bool {
		return tracked.request.Intent.PolicyGeneration < generation
	}, domain.ApprovalReasonPolicyChanged)
}

// CancelPolicyWork runs only after InvalidatePolicy. Reviewer cancellation is
// added to this same owner below; the lifecycle itself has no background work.
func (coordinator *ApprovalCoordinator) CancelPolicyWork(ctx context.Context, _ domain.PolicyGeneration) error {
	if coordinator == nil || ctx == nil {
		return ErrApprovalUnavailable
	}
	return ctx.Err()
}

// InvalidateModelOrigin advances permission policy before Coordinator cancels
// the old AgentRun during a role/origin replacement.
func (coordinator *ApprovalCoordinator) InvalidateModelOrigin(ctx context.Context) error {
	if coordinator == nil || coordinator.permissions == nil {
		return ErrPermissionUnavailable
	}
	return coordinator.permissions.InvalidateOriginPolicy(ctx)
}

// BindSessionAuthority keeps process-local permission rules and actions bound
// to exactly the Session selected by Application.
func (coordinator *ApprovalCoordinator) BindSessionAuthority(ctx context.Context, sessionID domain.SessionID) error {
	if coordinator == nil || ctx == nil || !sessionID.Valid() {
		return ErrPermissionUnavailable
	}
	status := coordinator.permissions.Status(coordinator.now())
	if status.SessionID == "" {
		return coordinator.permissions.BindSession(sessionID)
	}
	if status.SessionID == sessionID {
		return nil
	}
	return coordinator.permissions.ChangeSession(ctx, sessionID)
}

// Recover atomically invalidates every pending or approved-not-executed row.
func (coordinator *ApprovalCoordinator) Recover(ctx context.Context) error {
	if coordinator == nil || ctx == nil {
		return ErrApprovalCoordinatorDependency
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for {
		var stored []approval.StoredRequest
		if err := coordinator.persist(ctx, func(operationContext context.Context) error {
			var listErr error
			stored, listErr = coordinator.persistence.ListRecoverable(operationContext, maxApprovalRecoveryBatch)
			return listErr
		}); err != nil {
			return ErrApprovalPersistenceUnavailable
		}
		if len(stored) == 0 {
			return nil
		}
		now := coordinator.now()
		if !validCoordinatorTime(now) {
			return ErrApprovalCoordinatorDependency
		}
		transitions := make([]approval.RecoveryTransition, 0, len(stored))
		for _, before := range stored {
			after, err := approval.RecoverAfterRestart(before, now)
			if err != nil {
				return ErrApprovalPersistenceUnavailable
			}
			audit, err := coordinator.newStoredAudit(after)
			if err != nil {
				return ErrApprovalPersistenceUnavailable
			}
			transitions = append(transitions, approval.RecoveryTransition{Before: before, After: after, Audit: audit})
		}
		if err := coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.persistence.RecoverWithAudits(operationContext, transitions)
		}); err != nil {
			return ErrApprovalPersistenceUnavailable
		}
		if len(stored) < maxApprovalRecoveryBatch {
			return nil
		}
	}
}

// Get returns a query-only durable-safe approval projection.
func (coordinator *ApprovalCoordinator) Get(
	ctx context.Context,
	requestID domain.ApprovalID,
) (approval.StoredRequest, *approval.StoredDecision, error) {
	if coordinator == nil || ctx == nil || !requestID.Valid() {
		return approval.StoredRequest{}, nil, ErrApprovalUnavailable
	}
	var (
		request  approval.StoredRequest
		decision *approval.StoredDecision
	)
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		var getErr error
		request, decision, getErr = coordinator.persistence.Get(operationContext, requestID)
		return getErr
	}); err != nil {
		return approval.StoredRequest{}, nil, ErrApprovalUnavailable
	}
	return request, decision, nil
}

// Status returns policy and active-action metadata without persistence,
// model, Kubernetes, Tool, reviewer, or executor I/O.
func (coordinator *ApprovalCoordinator) Status() (PermissionStatus, *UIActionStatus) {
	if coordinator == nil || coordinator.permissions == nil {
		return PermissionStatus{}, nil
	}
	permission := coordinator.permissions.Status(coordinator.now())
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for _, tracked := range coordinator.active {
		status := &UIActionStatus{
			RequestID: tracked.request.ID, Operation: tracked.request.Intent.Operation,
			Risk: tracked.request.Intent.Risk, Route: tracked.route.Disposition,
			State: tracked.request.State, ScopeGeneration: tracked.request.Intent.Scope.Generation,
			PolicyGeneration: tracked.request.Intent.PolicyGeneration, Reviewing: tracked.reviewing,
			ExpiresAtMillis: tracked.request.ExpiresAt.UnixMilli(),
		}
		return permission, status
	}
	return permission, nil
}

func (coordinator *ApprovalCoordinator) closeMatching(
	ctx context.Context,
	matches func(trackedApproval) bool,
	reason domain.ApprovalStateReason,
) error {
	var resultErr error
	for id, tracked := range coordinator.active {
		if !matches(tracked) {
			continue
		}
		var (
			updated domain.ApprovalRequest
			err     error
		)
		if reason == domain.ApprovalReasonScopeChanged || reason == domain.ApprovalReasonPolicyChanged {
			updated, err = coordinator.service.Invalidate(ctx, id, reason)
		} else {
			updated, err = coordinator.service.Cancel(ctx, id, reason)
		}
		if err != nil && updated.ID == "" {
			resultErr = ErrApprovalUnavailable
			delete(coordinator.active, id)
			if tracked.reviewCancel != nil {
				tracked.reviewCancel()
			}
			continue
		}
		if updated.State == domain.ApprovalStateConsumed {
			delete(coordinator.active, id)
			if tracked.reviewCancel != nil {
				tracked.reviewCancel()
			}
			continue
		}
		if persistErr := coordinator.persistClosed(ctx, tracked.request.State, updated); persistErr != nil {
			resultErr = ErrApprovalPersistenceUnavailable
		}
		delete(coordinator.active, id)
		if tracked.reviewCancel != nil {
			tracked.reviewCancel()
		}
		coordinator.publishClosed(ctx, projectUIApprovalResult(updated, tracked.sequence))
	}
	return resultErr
}

func (coordinator *ApprovalCoordinator) persistClosed(
	ctx context.Context,
	expected domain.ApprovalState,
	request domain.ApprovalRequest,
) error {
	audit, err := coordinator.newAudit(request)
	if err != nil {
		return err
	}
	return coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.persistence.CloseWithAudit(operationContext, expected, request, audit)
	})
}

func (coordinator *ApprovalCoordinator) newAudit(request domain.ApprovalRequest) (domain.AuditEvent, error) {
	stored, err := approval.NewStoredRequest(request)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	return coordinator.newStoredAudit(stored)
}

func (coordinator *ApprovalCoordinator) newStoredAudit(request approval.StoredRequest) (domain.AuditEvent, error) {
	id, err := coordinator.auditIDs.NewAuditEventID()
	if err != nil || !id.Valid() {
		return domain.AuditEvent{}, ErrApprovalPersistenceUnavailable
	}
	eventType, actor, outcome, detail := approvalAuditProjection(request)
	operation := string(request.Intent.Operation)
	policy := request.Intent.PolicyVersion
	scope := request.Intent.Scope
	sessionID, runID := request.SessionID, request.RunID
	subject := request.Intent.Target.Resource
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType, Actor: actor, Outcome: outcome,
		Scope: &scope, Subject: &subject,
		Details:       domain.AuditDetails{Operation: &operation, DetailCode: &detail, PolicyVersion: &policy},
		CorrelationID: string(request.ID), IntegrityHash: string(request.Digest), OccurredAt: request.StateChangedAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrApprovalPersistenceUnavailable
	}
	return event, nil
}

func approvalAuditProjection(request approval.StoredRequest) (domain.AuditEventType, domain.AuditActor, domain.AuditOutcome, string) {
	switch request.State {
	case domain.ApprovalStatePending:
		return domain.AuditEventApprovalRequested, domain.AuditActorAgent, domain.AuditOutcomeSuccess, "requested"
	case domain.ApprovalStateApproved:
		actor := domain.AuditActorSystem
		if request.StateReason == domain.ApprovalReasonUserApproved {
			actor = domain.AuditActorUser
		}
		return domain.AuditEventApprovalApproved, actor, domain.AuditOutcomeSuccess, string(request.StateReason)
	case domain.ApprovalStateRejected:
		actor := domain.AuditActorSystem
		if request.StateReason == domain.ApprovalReasonUserRejected {
			actor = domain.AuditActorUser
		}
		return domain.AuditEventApprovalRejected, actor, domain.AuditOutcomeDenied, string(request.StateReason)
	case domain.ApprovalStateExpired:
		return domain.AuditEventApprovalExpired, domain.AuditActorSystem, domain.AuditOutcomeDenied, string(request.StateReason)
	default:
		return domain.AuditEventApprovalCancelled, domain.AuditActorSystem, domain.AuditOutcomeDenied, string(request.StateReason)
	}
}

func (coordinator *ApprovalCoordinator) publishClosed(ctx context.Context, result UIApprovalResult) {
	if result.Validate() != nil {
		return
	}
	_ = coordinator.uiEvents.PublishUIEvent(ctx, UIEvent{
		Kind: UIEventApprovalClosed, RunID: result.RunID, ScopeGeneration: result.ScopeGeneration,
		Sequence: result.Sequence, ApprovalResult: &result,
	})
}

func (coordinator *ApprovalCoordinator) closeAfterDeliveryFailure(ctx context.Context, request domain.ApprovalRequest) {
	updated, err := coordinator.service.Cancel(context.Background(), request.ID, domain.ApprovalReasonRunCancelled)
	if err == nil || updated.ID == request.ID {
		_ = coordinator.persistClosed(ctx, request.State, updated)
	}
}

func (coordinator *ApprovalCoordinator) cancelInMemory(requestID domain.ApprovalID) {
	_, _ = coordinator.service.Cancel(context.Background(), requestID, domain.ApprovalReasonContextCancelled)
}

func (coordinator *ApprovalCoordinator) persist(ctx context.Context, operation func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(ctx, coordinator.persistenceLimit)
	defer cancel()
	return operation(operationContext)
}

func approvalErrorCode(err error) domain.ApprovalErrorCode {
	var approvalError *domain.ApprovalError
	if !errors.As(err, &approvalError) {
		return ""
	}
	return approvalError.Code()
}
