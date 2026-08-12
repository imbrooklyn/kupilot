package approval

import (
	"context"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Clock provides the authoritative UTC instant for approval policy.
type Clock interface {
	Now() time.Time
}

// NonceSource creates one opaque nonce for each immutable request.
type NonceSource interface {
	NewNonce(context.Context) (domain.ApprovalNonce, error)
}

// RestartDeploymentExecutor is the sole fixed executor seam. Its input cannot
// carry a patch, YAML, annotation, timestamp, resource version, or another Kind.
type RestartDeploymentExecutor interface {
	ExecuteApprovedRestart(context.Context, domain.OperationIntent) error
}

// ApprovalService is the narrow lifecycle port intended for later Application
// orchestration. It is not wired into the v0.1 composition.
type ApprovalService interface {
	Request(context.Context, RequestCommand) (domain.ApprovalRequest, error)
	Decide(context.Context, DecisionCommand) (domain.ApprovalRequest, domain.ApprovalDecision, error)
	Expire(context.Context, domain.ApprovalID) (domain.ApprovalRequest, error)
	Cancel(context.Context, domain.ApprovalID, domain.ApprovalStateReason) (domain.ApprovalRequest, error)
	Invalidate(context.Context, domain.ApprovalID, domain.ApprovalStateReason) (domain.ApprovalRequest, error)
	Consume(context.Context, ConsumeCommand) (domain.ApprovalRequest, error)
}

// ServiceConfig supplies controlled policy dependencies without infrastructure.
type ServiceConfig struct {
	Clock    Clock
	Nonces   NonceSource
	Executor RestartDeploymentExecutor
}

// RequestCommand contains only application-owned identity and a closed intent.
type RequestCommand struct {
	ID        domain.ApprovalID
	RunID     domain.AgentRunID
	SessionID domain.SessionID
	Intent    domain.OperationIntent
}

// DecisionCommand carries exactly the proof returned by one visible dialog.
type DecisionCommand struct {
	RequestID    domain.ApprovalID
	Choice       domain.ApprovalDecisionChoice
	ShownDigest  domain.ApprovalDigest
	Nonce        domain.ApprovalNonce
	CurrentScope domain.ScopeSnapshot
}

// ConsumeCommand carries the same proof plus the current immutable scope.
type ConsumeCommand struct {
	RequestID    domain.ApprovalID
	ShownDigest  domain.ApprovalDigest
	Nonce        domain.ApprovalNonce
	CurrentScope domain.ScopeSnapshot
}

// Service owns process-local one-time state. Process loss therefore grants no
// authority; later persistence recovery must explicitly cancel old requests.
type Service struct {
	mu       sync.Mutex
	clock    Clock
	nonces   NonceSource
	executor RestartDeploymentExecutor
	records  map[domain.ApprovalID]*approvalRecord
}

type approvalRecord struct {
	request  domain.ApprovalRequest
	decision *domain.ApprovalDecision
}

// NewService constructs the pure approval coordinator and its fixed fakeable seam.
func NewService(config ServiceConfig) (*Service, error) {
	if config.Clock == nil || config.Nonces == nil || config.Executor == nil {
		return nil, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	return &Service{
		clock:    config.Clock,
		nonces:   config.Nonces,
		executor: config.Executor,
		records:  make(map[domain.ApprovalID]*approvalRecord),
	}, nil
}

// Request creates one pending request with an exact 60-second lifetime.
func (service *Service) Request(ctx context.Context, command RequestCommand) (domain.ApprovalRequest, error) {
	if err := contextApprovalError(ctx); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if service == nil {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	if err := validateRequestCommand(command); err != nil {
		return domain.ApprovalRequest{}, err
	}

	service.mu.Lock()
	_, exists := service.records[command.ID]
	service.mu.Unlock()
	if exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestConflict)
	}

	now, err := currentApprovalTime(service.clock)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	nonce, err := service.nonces.NewNonce(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
		}
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInternal)
	}
	if !nonce.Valid() {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInternal)
	}
	if err := contextApprovalError(ctx); err != nil {
		return domain.ApprovalRequest{}, err
	}
	request := domain.ApprovalRequest{
		ID:             command.ID,
		RunID:          command.RunID,
		SessionID:      command.SessionID,
		Intent:         command.Intent,
		Nonce:          nonce,
		State:          domain.ApprovalStatePending,
		RequestedAt:    now,
		ExpiresAt:      now.Add(domain.ApprovalExecutionTTL),
		StateChangedAt: now,
	}
	request.Digest, err = OperationDigest(request)
	if err != nil || request.Validate() != nil {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if _, exists := service.records[command.ID]; exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestConflict)
	}
	service.records[command.ID] = &approvalRecord{request: request}
	return request, nil
}

// Decide applies one default-reject local decision after checking every proof.
func (service *Service) Decide(
	ctx context.Context,
	command DecisionCommand,
) (domain.ApprovalRequest, domain.ApprovalDecision, error) {
	if service == nil {
		return domain.ApprovalRequest{}, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	if err := validateRequestIdentity(command.RequestID); err != nil {
		return domain.ApprovalRequest{}, domain.ApprovalDecision{}, err
	}
	now, err := currentApprovalTime(service.clock)
	if err != nil {
		return domain.ApprovalRequest{}, domain.ApprovalDecision{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[command.RequestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	if record.request.State.Terminated() {
		return record.request, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if expiredAt(record.request, now) {
		updated := service.expireLocked(record, now)
		return updated, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeExpired)
	}
	if contextApprovalError(ctx) != nil {
		updated := service.cancelLocked(record, now, domain.ApprovalReasonContextCancelled)
		return updated, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	if record.request.State == domain.ApprovalStateApproved {
		updated := service.invalidateLocked(record, now, domain.ApprovalReasonDecisionReplayed)
		return updated, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeDecisionReplayed)
	}
	if !command.Choice.Valid() {
		updated := service.invalidateLocked(record, now, domain.ApprovalReasonDecisionReplayed)
		return updated, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	if proofCode, reason := verifyProof(record.request, command.ShownDigest, command.Nonce, command.CurrentScope); proofCode != "" {
		updated := service.invalidateLocked(record, now, reason)
		return updated, domain.ApprovalDecision{}, domain.NewApprovalError(proofCode)
	}

	action := actionReject
	nextReason := domain.ApprovalReasonUserRejected
	if command.Choice == domain.ApprovalDecisionApprove {
		action = actionApprove
		nextReason = domain.ApprovalReasonUserApproved
	}
	next, ok := nextState(record.request.State, action)
	if !ok {
		return record.request, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	decision := domain.ApprovalDecision{
		RequestID:   record.request.ID,
		Choice:      command.Choice,
		ShownDigest: command.ShownDigest,
		Nonce:       command.Nonce,
		Actor:       domain.ApprovalActorLocalUser,
		DecidedAt:   now,
	}
	if decision.Validate() != nil {
		updated := service.invalidateLocked(record, now, domain.ApprovalReasonDecisionReplayed)
		return updated, domain.ApprovalDecision{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	record.request.State = next
	record.request.StateReason = nextReason
	record.request.StateChangedAt = now
	record.decision = &decision
	return record.request, decision, nil
}

// Expire closes one pending or approved request at or after the exact boundary.
func (service *Service) Expire(ctx context.Context, requestID domain.ApprovalID) (domain.ApprovalRequest, error) {
	if service == nil {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	if err := validateRequestIdentity(requestID); err != nil {
		return domain.ApprovalRequest{}, err
	}
	now, err := currentApprovalTime(service.clock)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	if record.request.State.Terminated() {
		return record.request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if expiredAt(record.request, now) {
		return service.expireLocked(record, now), nil
	}
	if contextApprovalError(ctx) != nil {
		updated := service.cancelLocked(record, now, domain.ApprovalReasonContextCancelled)
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	return record.request, domain.NewApprovalError(domain.ApprovalErrorCodeNotExpired)
}

// Cancel closes one pending or approved request for a fixed local reason.
func (service *Service) Cancel(
	ctx context.Context,
	requestID domain.ApprovalID,
	reason domain.ApprovalStateReason,
) (domain.ApprovalRequest, error) {
	if service == nil {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	if err := validateRequestIdentity(requestID); err != nil {
		return domain.ApprovalRequest{}, err
	}
	if !reason.ValidCancellation() {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	now, err := currentApprovalTime(service.clock)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	if record.request.State.Terminated() {
		return record.request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if expiredAt(record.request, now) {
		return service.expireLocked(record, now), domain.NewApprovalError(domain.ApprovalErrorCodeExpired)
	}
	if contextApprovalError(ctx) != nil {
		reason = domain.ApprovalReasonContextCancelled
	}
	updated := service.cancelLocked(record, now, reason)
	if reason == domain.ApprovalReasonContextCancelled {
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	return updated, nil
}

// Invalidate closes one pending or approved request after a proof or scope
// mismatch. It does not invoke the executor.
func (service *Service) Invalidate(
	ctx context.Context,
	requestID domain.ApprovalID,
	reason domain.ApprovalStateReason,
) (domain.ApprovalRequest, error) {
	if service == nil {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	if err := validateRequestIdentity(requestID); err != nil || !validInvalidationReason(reason) {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	now, err := currentApprovalTime(service.clock)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	if record.request.State.Terminated() {
		return record.request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if expiredAt(record.request, now) {
		return service.expireLocked(record, now), domain.NewApprovalError(domain.ApprovalErrorCodeExpired)
	}
	if contextApprovalError(ctx) != nil {
		updated := service.cancelLocked(record, now, domain.ApprovalReasonContextCancelled)
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	return service.invalidateLocked(record, now, reason), nil
}

// Consume atomically spends one approved request before invoking the fixed seam.
// A consumed request is never made available for an automatic retry.
func (service *Service) Consume(ctx context.Context, command ConsumeCommand) (domain.ApprovalRequest, error) {
	if service == nil {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	if err := validateRequestIdentity(command.RequestID); err != nil {
		return domain.ApprovalRequest{}, err
	}
	now, err := currentApprovalTime(service.clock)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}

	service.mu.Lock()
	record, exists := service.records[command.RequestID]
	if !exists {
		service.mu.Unlock()
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	if record.request.State.Terminated() {
		request := record.request
		service.mu.Unlock()
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if expiredAt(record.request, now) {
		updated := service.expireLocked(record, now)
		service.mu.Unlock()
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeExpired)
	}
	if contextApprovalError(ctx) != nil {
		updated := service.cancelLocked(record, now, domain.ApprovalReasonContextCancelled)
		service.mu.Unlock()
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	if record.request.State != domain.ApprovalStateApproved {
		request := record.request
		service.mu.Unlock()
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if record.decision == nil || record.decision.Choice != domain.ApprovalDecisionApprove {
		updated := service.invalidateLocked(record, now, domain.ApprovalReasonDecisionReplayed)
		service.mu.Unlock()
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	if proofCode, reason := verifyProof(record.request, command.ShownDigest, command.Nonce, command.CurrentScope); proofCode != "" {
		updated := service.invalidateLocked(record, now, reason)
		service.mu.Unlock()
		return updated, domain.NewApprovalError(proofCode)
	}
	if !record.decision.ShownDigest.Equal(command.ShownDigest) || !record.decision.Nonce.Equal(command.Nonce) {
		updated := service.invalidateLocked(record, now, domain.ApprovalReasonDecisionReplayed)
		service.mu.Unlock()
		return updated, domain.NewApprovalError(domain.ApprovalErrorCodeDecisionReplayed)
	}
	next, ok := nextState(record.request.State, actionConsume)
	if !ok {
		request := record.request
		service.mu.Unlock()
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	record.request.State = next
	record.request.StateReason = domain.ApprovalReasonConsumed
	record.request.StateChangedAt = now
	request := record.request
	intent := record.request.Intent
	service.mu.Unlock()

	if contextApprovalError(ctx) != nil {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	if err := service.executor.ExecuteApprovedRestart(ctx, intent); err != nil {
		if ctx.Err() != nil {
			return request, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
		}
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeExecutorFailed)
	}
	return request, nil
}

// Snapshot returns a value copy for supervision and deterministic tests.
func (service *Service) Snapshot(requestID domain.ApprovalID) (domain.ApprovalRequest, bool) {
	if service == nil {
		return domain.ApprovalRequest{}, false
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, false
	}
	return record.request, true
}

func verifyProof(
	request domain.ApprovalRequest,
	shownDigest domain.ApprovalDigest,
	nonce domain.ApprovalNonce,
	currentScope domain.ScopeSnapshot,
) (domain.ApprovalErrorCode, domain.ApprovalStateReason) {
	expectedDigest, err := OperationDigest(request)
	if err != nil || !request.Digest.Equal(expectedDigest) || !request.Digest.Equal(shownDigest) {
		return domain.ApprovalErrorCodeDigestMismatch, domain.ApprovalReasonDigestMismatch
	}
	if !request.Nonce.Equal(nonce) {
		return domain.ApprovalErrorCodeNonceMismatch, domain.ApprovalReasonNonceMismatch
	}
	if !validCurrentScope(currentScope) || !sameScope(request.Intent.Scope, currentScope) {
		return domain.ApprovalErrorCodeStaleScope, domain.ApprovalReasonScopeChanged
	}
	return "", ""
}

func (service *Service) expireLocked(record *approvalRecord, now time.Time) domain.ApprovalRequest {
	next, ok := nextState(record.request.State, actionExpire)
	if !ok {
		return record.request
	}
	record.request.State = next
	record.request.StateReason = domain.ApprovalReasonTTLExpired
	record.request.StateChangedAt = now
	return record.request
}

func (service *Service) cancelLocked(
	record *approvalRecord,
	now time.Time,
	reason domain.ApprovalStateReason,
) domain.ApprovalRequest {
	next, ok := nextState(record.request.State, actionCancel)
	if !ok {
		return record.request
	}
	record.request.State = next
	record.request.StateReason = reason
	record.request.StateChangedAt = now
	return record.request
}

func (service *Service) invalidateLocked(
	record *approvalRecord,
	now time.Time,
	reason domain.ApprovalStateReason,
) domain.ApprovalRequest {
	next, ok := nextState(record.request.State, actionInvalidate)
	if !ok {
		return record.request
	}
	record.request.State = next
	record.request.StateReason = reason
	record.request.StateChangedAt = now
	return record.request
}

func validInvalidationReason(reason domain.ApprovalStateReason) bool {
	return reason == domain.ApprovalReasonScopeChanged ||
		reason == domain.ApprovalReasonDigestMismatch ||
		reason == domain.ApprovalReasonNonceMismatch ||
		reason == domain.ApprovalReasonDecisionReplayed
}
