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
	ErrApprovalResultAuditUnavailable = errors.New("the Deployment restart result audit is unavailable")
)

// ApprovalLifecycle is the narrow approval service surface used by Application.
type ApprovalLifecycle interface {
	Request(context.Context, approval.RequestCommand) (domain.ApprovalRequest, error)
	Decide(context.Context, approval.DecisionCommand) (domain.ApprovalRequest, domain.ApprovalDecision, error)
	Expire(context.Context, domain.ApprovalID) (domain.ApprovalRequest, error)
	Cancel(context.Context, domain.ApprovalID, domain.ApprovalStateReason) (domain.ApprovalRequest, error)
	Invalidate(context.Context, domain.ApprovalID, domain.ApprovalStateReason) (domain.ApprovalRequest, error)
	Consume(context.Context, approval.ConsumeCommand) (approval.ConsumeResult, error)
}

// ConsumeApprovedRestart performs the deterministic post-decision execution
// transition. Application supplies proof and projects the terminal result; it
// never receives an executor, patch, annotation, or client.
func (coordinator *ApprovalCoordinator) ConsumeApprovedRestart(
	ctx context.Context,
	command UICommand,
) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || command.Kind != UICommandApproveRestart || command.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return UIApprovalResult{}, err
	}
	coordinator.mu.Lock()
	tracked, ok := coordinator.active[command.ApprovalID]
	if !ok || tracked.request.State != domain.ApprovalStateApproved || tracked.request.RunID != command.RunID ||
		tracked.consuming || tracked.sequence != command.ApprovalSequence ||
		tracked.request.Intent.Scope.Generation != command.ExpectedScopeGeneration {
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
	consumeResult, consumeErr := coordinator.service.Consume(ctx, approval.ConsumeCommand{
		RequestID: command.ApprovalID, ShownDigest: command.ApprovalDigest,
		Nonce: command.ApprovalNonce, CurrentScope: currentScope,
	})
	updated := consumeResult.ApprovalRequest

	coordinator.mu.Lock()
	currentTracked, stillTracked := coordinator.active[command.ApprovalID]
	if stillTracked && (currentTracked.request.ID != tracked.request.ID || currentTracked.sequence != tracked.sequence) {
		delete(coordinator.active, command.ApprovalID)
		coordinator.mu.Unlock()
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if updated.ID != tracked.request.ID {
		if stillTracked {
			delete(coordinator.active, command.ApprovalID)
		}
		coordinator.mu.Unlock()
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if stillTracked && (updated.State == domain.ApprovalStateInvalidated || updated.State == domain.ApprovalStateCancelled ||
		updated.State == domain.ApprovalStateExpired) {
		if err := coordinator.persistClosed(ctx, tracked.request.State, updated); err != nil {
			delete(coordinator.active, command.ApprovalID)
			coordinator.mu.Unlock()
			return UIApprovalResult{}, ErrApprovalPersistenceUnavailable
		}
	}
	if stillTracked {
		delete(coordinator.active, command.ApprovalID)
	}
	coordinator.mu.Unlock()
	result := projectUIApprovalResult(updated, tracked.sequence)
	if updated.State == domain.ApprovalStateConsumed {
		if consumeResult.Validate() != nil {
			return UIApprovalResult{}, ErrApprovalUnavailable
		}
		execution, executionErr := coordinator.finishRestartExecution(ctx, tracked, consumeResult.Attempt, consumeErr)
		result.Execution = &execution
		if result.Validate() != nil {
			return UIApprovalResult{}, ErrApprovalUnavailable
		}
		return result, executionErr
	}
	if result.Validate() != nil {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if errors.Is(consumeErr, approval.ErrPreWritePersistenceUnavailable) {
		return result, ErrApprovalPersistenceUnavailable
	}
	if updated.State == domain.ApprovalStateInvalidated || updated.State == domain.ApprovalStateCancelled ||
		updated.State == domain.ApprovalStateExpired || approvalErrorCode(consumeErr) == domain.ApprovalErrorCodeStaleScope {
		return result, ErrApprovalInvalidated
	}
	return result, ErrApprovalUnavailable
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
}

// ApprovalCurrentScope supplies the exact currently verified scope.
type ApprovalCurrentScope interface {
	CurrentScope() (domain.ClusterScope, bool)
}

// ApprovalCoordinatorConfig contains no executor. The lifecycle service owns
// its fakeable seam, while Application can only create and resolve authority.
type ApprovalCoordinatorConfig struct {
	Service            ApprovalLifecycle
	Persistence        ApprovalPersistence
	ResultAudits       ApprovalResultAudits
	Scope              ApprovalCurrentScope
	ApprovalIDs        ApprovalIdentifierSource
	AuditIDs           AuditIdentifierSource
	UIEvents           UIEventSink
	Rollout            RestartRolloutObserver
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
	now              func() time.Time
	persistenceLimit time.Duration
	active           map[domain.ApprovalID]trackedApproval
}

type trackedApproval struct {
	request   domain.ApprovalRequest
	sequence  int64
	consuming bool
}

// NewApprovalCoordinator constructs a fail-closed non-executing coordinator.
func NewApprovalCoordinator(config ApprovalCoordinatorConfig) (*ApprovalCoordinator, error) {
	limit := config.PersistenceTimeout
	if limit == 0 {
		limit = DefaultPersistenceTimeout
	}
	if config.Service == nil || config.Persistence == nil || config.ResultAudits == nil || config.Scope == nil || config.ApprovalIDs == nil ||
		config.AuditIDs == nil || config.UIEvents == nil || config.Rollout == nil || config.Now == nil || !validCoordinatorTime(config.Now()) ||
		limit <= 0 || limit > MaxPersistenceTimeout {
		return nil, ErrApprovalCoordinatorDependency
	}
	return &ApprovalCoordinator{
		service: config.Service, persistence: config.Persistence, resultAudits: config.ResultAudits, scope: config.Scope,
		approvalIDs: config.ApprovalIDs, auditIDs: config.AuditIDs, uiEvents: config.UIEvents,
		rollout: config.Rollout, now: config.Now, persistenceLimit: limit, active: make(map[domain.ApprovalID]trackedApproval),
	}, nil
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
		sequence < 1 || sequence > 4096 || intent.Validate() != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return domain.ApprovalRequest{}, err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.active) != 0 {
		return domain.ApprovalRequest{}, ErrApprovalBusy
	}
	current, ok := coordinator.scope.CurrentScope()
	if !ok || current.Snapshot() != intent.Scope {
		return domain.ApprovalRequest{}, ErrApprovalInvalidated
	}
	id, err := coordinator.approvalIDs.NewApprovalID()
	if err != nil || !id.Valid() {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	request, err := coordinator.service.Request(ctx, approval.RequestCommand{
		ID: id, RunID: runID, SessionID: sessionID, Intent: intent,
	})
	if err != nil {
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	audit, err := coordinator.newAudit(request)
	if err != nil || coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.persistence.CreateWithAudit(operationContext, request, audit)
	}) != nil {
		coordinator.cancelInMemory(request.ID)
		return domain.ApprovalRequest{}, ErrApprovalPersistenceUnavailable
	}
	projection := projectUIApprovalRequest(request, sequence)
	event := UIEvent{
		Kind: UIEventApprovalRequested, RunID: request.RunID,
		ScopeGeneration: request.Intent.Scope.Generation, Sequence: sequence, Approval: &projection,
	}
	if projection.Validate() != nil || event.Validate() != nil || coordinator.uiEvents.PublishUIEvent(ctx, event) != nil {
		coordinator.closeAfterDeliveryFailure(ctx, request)
		return domain.ApprovalRequest{}, ErrApprovalUnavailable
	}
	coordinator.active[request.ID] = trackedApproval{request: request, sequence: sequence}
	return request, nil
}

// Decide maps a typed UI intent to a domain decision and persists it before
// returning a visible result. Approved remains approved-not-executed.
func (coordinator *ApprovalCoordinator) Decide(ctx context.Context, command UICommand) (UIApprovalResult, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil ||
		(command.Kind != UICommandApproveRestart && command.Kind != UICommandRejectRestart) {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return UIApprovalResult{}, err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	tracked, ok := coordinator.active[command.ApprovalID]
	if !ok || tracked.request.RunID != command.RunID || tracked.sequence != command.ApprovalSequence ||
		tracked.request.Intent.Scope.Generation != command.ExpectedScopeGeneration {
		return UIApprovalResult{}, ErrApprovalUnavailable
	}
	current, currentOK := coordinator.scope.CurrentScope()
	currentScope := domain.ScopeSnapshot{}
	if currentOK {
		currentScope = current.Snapshot()
	}
	choice := domain.ApprovalDecisionReject
	if command.Kind == UICommandApproveRestart {
		choice = domain.ApprovalDecisionApprove
	}
	updated, decision, decideErr := coordinator.service.Decide(ctx, approval.DecisionCommand{
		RequestID: command.ApprovalID, Choice: choice,
		ShownDigest: command.ApprovalDigest, Nonce: command.ApprovalNonce, CurrentScope: currentScope,
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
	if coordinator == nil || ctx == nil || command.Kind != UICommandExpireRestart || command.Validate() != nil {
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
	}, false)
}

// InvalidateScope implements the scope invalidation hook for a v0.2
// composition. The caller has already advanced the generation.
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
	}, true)
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

func (coordinator *ApprovalCoordinator) closeMatching(
	ctx context.Context,
	matches func(trackedApproval) bool,
	invalidate bool,
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
		if invalidate {
			updated, err = coordinator.service.Invalidate(ctx, id, domain.ApprovalReasonScopeChanged)
		} else {
			updated, err = coordinator.service.Cancel(ctx, id, domain.ApprovalReasonRunCancelled)
		}
		if err != nil && updated.ID == "" {
			resultErr = ErrApprovalUnavailable
			delete(coordinator.active, id)
			continue
		}
		if updated.State == domain.ApprovalStateConsumed {
			delete(coordinator.active, id)
			continue
		}
		if persistErr := coordinator.persistClosed(ctx, tracked.request.State, updated); persistErr != nil {
			resultErr = ErrApprovalPersistenceUnavailable
		}
		delete(coordinator.active, id)
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
	subject := domain.ResourceRef{
		APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
		Namespace: request.Intent.Scope.Namespace, Name: request.Intent.DeploymentName, UID: request.Intent.DeploymentUID,
	}
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
		return domain.AuditEventApprovalApproved, domain.AuditActorUser, domain.AuditOutcomeSuccess, string(request.StateReason)
	case domain.ApprovalStateRejected:
		return domain.AuditEventApprovalRejected, domain.AuditActorUser, domain.AuditOutcomeDenied, string(request.StateReason)
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
