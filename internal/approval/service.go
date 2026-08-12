package approval

import (
	"context"
	"errors"
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
	Clock       Clock
	Nonces      NonceSource
	Store       PreWriteStore
	Scope       CurrentScope
	Revalidator RestartDeploymentRevalidator
	Executor    RestartDeploymentExecutor
	AuditIDs    AuditIdentifierSource
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
	store    PreWriteStore
	scope    CurrentScope
	target   RestartDeploymentRevalidator
	executor RestartDeploymentExecutor
	auditIDs AuditIdentifierSource
	records  map[domain.ApprovalID]*approvalRecord
}

type approvalRecord struct {
	request   domain.ApprovalRequest
	decision  *domain.ApprovalDecision
	executing bool
}

// NewService constructs the approval coordinator and its fixed fakeable ports.
func NewService(config ServiceConfig) (*Service, error) {
	if config.Clock == nil || config.Nonces == nil || config.Store == nil || config.Scope == nil ||
		config.Revalidator == nil || config.Executor == nil || config.AuditIDs == nil {
		return nil, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidConfiguration)
	}
	return &Service{
		clock:    config.Clock,
		nonces:   config.Nonces,
		store:    config.Store,
		scope:    config.Scope,
		target:   config.Revalidator,
		executor: config.Executor,
		auditIDs: config.AuditIDs,
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

// Consume revalidates one durably approved request, commits its consumed state
// and pre-write audit, then invokes the fixed executor at most once. Kubernetes
// I/O never occurs inside the durable transaction.
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
	if record.request.State.Terminated() || record.executing {
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
	record.executing = true
	approved := record.request
	decision := *record.decision
	service.mu.Unlock()

	storedApproved, err := NewStoredRequest(approved)
	if err != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInternal)
	}
	storedDecision, err := NewStoredDecision(decision)
	if err != nil || service.store.VerifyApproved(ctx, storedApproved, storedDecision) != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInternal)
	}
	if err := contextApprovalError(ctx); err != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonContextCancelled, domain.ApprovalErrorCodeCancelled)
	}
	if !service.scopeMatches(approved.Intent.Scope) {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonScopeChanged, domain.ApprovalErrorCodeStaleScope)
	}
	if _, err := service.executionClaim(command.RequestID); err != nil {
		return service.snapshotOrZero(command.RequestID), err
	}

	observation, targetErr := service.target.RevalidateApprovedRestart(ctx, approved.Intent)
	if targetErr != nil {
		code := domain.ApprovalErrorCodeExecutorFailed
		if ctx.Err() != nil {
			code = domain.ApprovalErrorCodeCancelled
		}
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, code)
	}
	if !service.scopeMatches(approved.Intent.Scope) {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonScopeChanged, domain.ApprovalErrorCodeStaleScope)
	}
	execution, err := NewRestartDeploymentExecution(approved.Intent, observation)
	if err != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInvalidIntent)
	}
	freshNow, timeErr := currentApprovalTime(service.clock)
	if timeErr != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInternal)
	}
	now = freshNow
	claim, claimErr := service.executionClaim(command.RequestID)
	if claimErr != nil {
		return claim, claimErr
	}
	if expiredAt(claim, now) {
		return service.expireExecution(command.RequestID, now)
	}

	consumed := approved
	consumed.State = domain.ApprovalStateConsumed
	consumed.StateReason = domain.ApprovalReasonConsumed
	consumed.StateChangedAt = now
	storedConsumed, err := NewStoredRequest(consumed)
	if err != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInternal)
	}
	auditID, err := service.auditIDs.NewAuditEventID()
	if err != nil || !auditID.Valid() {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInternal)
	}
	audit, err := writeIntentAudit(auditID, storedConsumed)
	if err != nil {
		return service.failBeforePreWrite(command.RequestID, now, domain.ApprovalReasonDigestMismatch, domain.ApprovalErrorCodeInternal)
	}
	if err := service.store.ConsumeWithAudit(ctx, storedApproved, storedDecision, storedConsumed, audit); err != nil {
		updated, approvalErr := service.failBeforePreWrite(
			command.RequestID,
			now,
			domain.ApprovalReasonDigestMismatch,
			domain.ApprovalErrorCodeInternal,
		)
		return updated, errors.Join(ErrPreWritePersistenceUnavailable, approvalErr)
	}

	service.mu.Lock()
	record, exists = service.records[command.RequestID]
	if !exists || !record.request.Digest.Equal(consumed.Digest) {
		service.mu.Unlock()
		return consumed, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	eligible := record.executing && record.request == approved
	record.request = consumed
	record.executing = false
	request := record.request
	service.mu.Unlock()
	if !eligible {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}

	freshNow, timeErr = currentApprovalTime(service.clock)
	if timeErr != nil {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeInternal)
	}
	now = freshNow
	if expiredAt(request, now) {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeExpired)
	}
	if contextApprovalError(ctx) != nil {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
	}
	if !service.scopeMatches(request.Intent.Scope) {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeStaleScope)
	}
	result, executeErr := service.executor.ExecuteApprovedRestart(ctx, execution)
	if executeErr != nil {
		if ctx.Err() != nil {
			return request, domain.NewApprovalError(domain.ApprovalErrorCodeCancelled)
		}
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeExecutorFailed)
	}
	if result.Validate() != nil || result.Scope != observation.Scope ||
		result.DeploymentName != observation.DeploymentName || result.DeploymentUID != observation.DeploymentUID ||
		result.PreviousResourceVersion != observation.ResourceVersion {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeExecutorFailed)
	}
	if !service.scopeMatches(request.Intent.Scope) {
		return request, domain.NewApprovalError(domain.ApprovalErrorCodeStaleScope)
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
	record.executing = false
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
	record.executing = false
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
	record.executing = false
	return record.request
}

func validInvalidationReason(reason domain.ApprovalStateReason) bool {
	return reason == domain.ApprovalReasonScopeChanged ||
		reason == domain.ApprovalReasonDigestMismatch ||
		reason == domain.ApprovalReasonNonceMismatch ||
		reason == domain.ApprovalReasonDecisionReplayed
}

func (service *Service) executionClaim(requestID domain.ApprovalID) (domain.ApprovalRequest, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	if record.request.State != domain.ApprovalStateApproved || !record.executing {
		return record.request, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidTransition)
	}
	return record.request, nil
}

func (service *Service) failBeforePreWrite(
	requestID domain.ApprovalID,
	now time.Time,
	reason domain.ApprovalStateReason,
	code domain.ApprovalErrorCode,
) (domain.ApprovalRequest, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	record.executing = false
	if record.request.State == domain.ApprovalStateApproved {
		if reason.ValidCancellation() {
			service.cancelLocked(record, now, reason)
		} else {
			service.invalidateLocked(record, now, reason)
		}
	}
	return record.request, domain.NewApprovalError(code)
}

func (service *Service) expireExecution(
	requestID domain.ApprovalID,
	now time.Time,
) (domain.ApprovalRequest, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}, domain.NewApprovalError(domain.ApprovalErrorCodeRequestNotFound)
	}
	record.executing = false
	if record.request.State == domain.ApprovalStateApproved {
		service.expireLocked(record, now)
	}
	return record.request, domain.NewApprovalError(domain.ApprovalErrorCodeExpired)
}

func (service *Service) snapshotOrZero(requestID domain.ApprovalID) domain.ApprovalRequest {
	service.mu.Lock()
	defer service.mu.Unlock()
	record, exists := service.records[requestID]
	if !exists {
		return domain.ApprovalRequest{}
	}
	return record.request
}

func (service *Service) scopeMatches(expected domain.ScopeSnapshot) bool {
	current, ok := service.scope.CurrentScope()
	return ok && current.Validate() == nil && current.Snapshot() == expected
}
