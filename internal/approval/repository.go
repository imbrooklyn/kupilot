package approval

import (
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	// ErrInvalidStoredApproval reports a malformed durable-safe approval value.
	ErrInvalidStoredApproval = errors.New("stored approval data is invalid")
	// ErrStoredApprovalNotFound reports that an approval row is unavailable.
	ErrStoredApprovalNotFound = errors.New("stored approval request is unavailable")
	// ErrStoredApprovalConflict reports a stale or duplicate durable transition.
	ErrStoredApprovalConflict = errors.New("stored approval state conflicts with the requested transition")
)

// StoredRequest is the durable-safe approval request projection. It contains
// only a nonce hash; the one-time nonce never crosses the persistence port.
type StoredRequest struct {
	ID             domain.ApprovalID
	RunID          domain.AgentRunID
	SessionID      domain.SessionID
	Intent         domain.OperationIntent
	Digest         domain.ApprovalDigest
	NonceHash      domain.ApprovalNonceHash
	State          domain.ApprovalState
	StateReason    domain.ApprovalStateReason
	RequestedAt    time.Time
	ExpiresAt      time.Time
	StateChangedAt time.Time
}

// NewStoredRequest projects an in-memory request without persisting its nonce.
func NewStoredRequest(request domain.ApprovalRequest) (StoredRequest, error) {
	if request.Validate() != nil {
		return StoredRequest{}, ErrInvalidStoredApproval
	}
	stored := StoredRequest{
		ID:             request.ID,
		RunID:          request.RunID,
		SessionID:      request.SessionID,
		Intent:         request.Intent,
		Digest:         request.Digest,
		NonceHash:      request.Nonce.Hash(),
		State:          request.State,
		StateReason:    request.StateReason,
		RequestedAt:    request.RequestedAt,
		ExpiresAt:      request.ExpiresAt,
		StateChangedAt: request.StateChangedAt,
	}
	if stored.Validate() != nil {
		return StoredRequest{}, ErrInvalidStoredApproval
	}
	return stored, nil
}

// Validate checks the complete canonical request, digest, hash, and lifecycle.
func (request StoredRequest) Validate() error {
	domainRequest := request.withValidationNonce()
	if !request.NonceHash.Valid() || domainRequest.Validate() != nil {
		return ErrInvalidStoredApproval
	}
	digest, err := OperationDigest(domainRequest)
	if err != nil || !request.Digest.Equal(digest) {
		return ErrInvalidStoredApproval
	}
	return nil
}

// RequestWithoutNonce returns the safe request fields for audit projection.
// The returned zero nonce cannot be used as approval authority.
func (request StoredRequest) RequestWithoutNonce() domain.ApprovalRequest {
	return domain.ApprovalRequest{
		ID:             request.ID,
		RunID:          request.RunID,
		SessionID:      request.SessionID,
		Intent:         request.Intent,
		Digest:         request.Digest,
		State:          request.State,
		StateReason:    request.StateReason,
		RequestedAt:    request.RequestedAt,
		ExpiresAt:      request.ExpiresAt,
		StateChangedAt: request.StateChangedAt,
	}
}

// ValidateAudit checks the exact actor, outcome, subject, digest, and fixed
// scalar details for one durable approval transition.
func (request StoredRequest) ValidateAudit(event domain.AuditEvent) error {
	if request.Validate() != nil || event.Validate() != nil {
		return ErrInvalidStoredApproval
	}
	if request.State != domain.ApprovalStatePending && request.State != domain.ApprovalStateApproved &&
		request.State != domain.ApprovalStateRejected && request.State != domain.ApprovalStateExpired &&
		request.State != domain.ApprovalStateCancelled && request.State != domain.ApprovalStateInvalidated {
		return ErrInvalidStoredApproval
	}
	eventType, actor, outcome, detail := storedApprovalAuditProjection(request)
	operation := string(request.Intent.Operation)
	subject := domain.ResourceRef{
		APIVersion: domain.RestartDeploymentTargetAPIVersion,
		Kind:       domain.RestartDeploymentTargetKind,
		Namespace:  request.Intent.Scope.Namespace,
		Name:       request.Intent.DeploymentName,
		UID:        request.Intent.DeploymentUID,
	}
	if event.Type != eventType || event.Actor != actor || event.Outcome != outcome ||
		event.SessionID == nil || *event.SessionID != request.SessionID ||
		event.RunID == nil || *event.RunID != request.RunID ||
		event.Scope == nil || *event.Scope != request.Intent.Scope ||
		event.Subject == nil || *event.Subject != subject ||
		event.Details.Operation == nil || *event.Details.Operation != operation ||
		event.Details.DetailCode == nil || *event.Details.DetailCode != detail ||
		event.Details.PolicyVersion == nil || *event.Details.PolicyVersion != request.Intent.PolicyVersion ||
		event.Details.ErrorClass != nil || event.Details.ToolName != nil ||
		event.Details.Sequence != nil || event.Details.Count != nil ||
		event.CorrelationID != string(request.ID) || event.IntegrityHash != string(request.Digest) ||
		!event.OccurredAt.Equal(request.StateChangedAt) {
		return ErrInvalidStoredApproval
	}
	return nil
}

func (request StoredRequest) withValidationNonce() domain.ApprovalRequest {
	result := request.RequestWithoutNonce()
	value := make([]byte, domain.ApprovalNonceBytes)
	value[0] = 1
	result.Nonce, _ = domain.NewApprovalNonce(value)
	return result
}

// StoredDecision is the durable-safe local decision projection.
type StoredDecision struct {
	RequestID   domain.ApprovalID
	Choice      domain.ApprovalDecisionChoice
	ShownDigest domain.ApprovalDigest
	NonceHash   domain.ApprovalNonceHash
	Actor       domain.ApprovalActor
	DecidedAt   time.Time
}

// NewStoredDecision projects an in-memory decision without persisting its nonce.
func NewStoredDecision(decision domain.ApprovalDecision) (StoredDecision, error) {
	if decision.Validate() != nil {
		return StoredDecision{}, ErrInvalidStoredApproval
	}
	stored := StoredDecision{
		RequestID:   decision.RequestID,
		Choice:      decision.Choice,
		ShownDigest: decision.ShownDigest,
		NonceHash:   decision.Nonce.Hash(),
		Actor:       decision.Actor,
		DecidedAt:   decision.DecidedAt,
	}
	if stored.Validate() != nil {
		return StoredDecision{}, ErrInvalidStoredApproval
	}
	return stored, nil
}

// Validate checks the bounded decision metadata and hashed proof.
func (decision StoredDecision) Validate() error {
	if !decision.RequestID.Valid() || !decision.Choice.Valid() ||
		!decision.ShownDigest.Valid() || !decision.NonceHash.Valid() ||
		decision.Actor != domain.ApprovalActorLocalUser ||
		decision.DecidedAt.IsZero() || decision.DecidedAt.Location() != time.UTC ||
		decision.DecidedAt.UnixMilli() < 0 ||
		!decision.DecidedAt.Equal(time.UnixMilli(decision.DecidedAt.UnixMilli()).UTC()) {
		return ErrInvalidStoredApproval
	}
	return nil
}

// RecoveryTransition binds one startup-invalidated request to its audit event.
type RecoveryTransition struct {
	Before StoredRequest
	After  StoredRequest
	Audit  domain.AuditEvent
}

// Validate checks a fail-closed startup transition without reconstructing a nonce.
func (transition RecoveryTransition) Validate() error {
	if transition.Before.Validate() != nil || transition.After.Validate() != nil ||
		transition.After.ValidateAudit(transition.Audit) != nil || !sameStoredIdentity(transition.Before, transition.After) ||
		transition.After.StateChangedAt.Before(transition.Before.StateChangedAt) ||
		(transition.Before.State != domain.ApprovalStatePending && transition.Before.State != domain.ApprovalStateApproved) ||
		(transition.After.State != domain.ApprovalStateCancelled && transition.After.State != domain.ApprovalStateExpired) ||
		transition.After.State == domain.ApprovalStateCancelled && transition.After.StateReason != domain.ApprovalReasonProcessRestarted ||
		transition.After.State == domain.ApprovalStateExpired && transition.After.StateReason != domain.ApprovalReasonTTLExpired ||
		transition.Audit.Type != recoveryAuditType(transition.After.State) {
		return ErrInvalidStoredApproval
	}
	return nil
}

// RecoverAfterRestart closes pending and approved-not-executed authority.
func RecoverAfterRestart(request StoredRequest, now time.Time) (StoredRequest, error) {
	if request.Validate() != nil ||
		(request.State != domain.ApprovalStatePending && request.State != domain.ApprovalStateApproved) ||
		now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 ||
		!now.Equal(time.UnixMilli(now.UnixMilli()).UTC()) || now.Before(request.StateChangedAt) {
		return StoredRequest{}, ErrInvalidStoredApproval
	}
	result := request
	result.StateChangedAt = now
	if !now.Before(request.ExpiresAt) {
		result.State = domain.ApprovalStateExpired
		result.StateReason = domain.ApprovalReasonTTLExpired
	} else {
		result.State = domain.ApprovalStateCancelled
		result.StateReason = domain.ApprovalReasonProcessRestarted
	}
	if result.Validate() != nil {
		return StoredRequest{}, ErrInvalidStoredApproval
	}
	return result, nil
}

func sameStoredIdentity(left, right StoredRequest) bool {
	return left.ID == right.ID && left.RunID == right.RunID && left.SessionID == right.SessionID &&
		left.Intent == right.Intent && left.Digest == right.Digest && left.NonceHash == right.NonceHash &&
		left.RequestedAt.Equal(right.RequestedAt) && left.ExpiresAt.Equal(right.ExpiresAt)
}

func recoveryAuditType(state domain.ApprovalState) domain.AuditEventType {
	if state == domain.ApprovalStateExpired {
		return domain.AuditEventApprovalExpired
	}
	return domain.AuditEventApprovalCancelled
}

func storedApprovalAuditProjection(request StoredRequest) (domain.AuditEventType, domain.AuditActor, domain.AuditOutcome, string) {
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
