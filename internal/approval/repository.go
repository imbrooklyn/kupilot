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

// StoredActionIntent is the durable-safe authority projection. Exact typed
// parameters remain bound by their digest; raw argv, paths, and command data
// never cross the persistence port.
type StoredActionIntent struct {
	Operation              domain.ActionOperation
	OperationSchemaVersion string
	PolicyVersion          string
	PermissionProfile      domain.PermissionProfile
	Risk                   domain.RiskClass
	Effect                 domain.CapabilityEffectClass
	PolicyGeneration       domain.PolicyGeneration
	Scope                  domain.ScopeSnapshot
	NamespaceAccess        domain.NamespaceAccessPolicy
	Target                 domain.ActionTarget
	ParameterKind          domain.ActionParameterKind
	ParameterDigest        domain.ActionDigest
	Stdin                  bool
	TTY                    bool
	Shell                  bool
	DataCategories         domain.ActionDataCategories
	AllowedSinks           domain.ActionSinks
	NetworkEffects         domain.ActionNetworkEffects
	NetworkDestinationHash domain.ActionDigest
	Limits                 domain.ActionLimits
	VerificationPlanID     string
	ReasonSummary          string
	RiskSummary            string
}

func newStoredActionIntent(intent domain.ActionIntent) (StoredActionIntent, error) {
	if intent.Validate() != nil {
		return StoredActionIntent{}, ErrInvalidStoredApproval
	}
	stored := StoredActionIntent{
		Operation: intent.Operation, OperationSchemaVersion: intent.OperationSchemaVersion,
		PolicyVersion: intent.PolicyVersion, PermissionProfile: intent.PermissionProfile,
		Risk: intent.Risk, Effect: intent.Effect, PolicyGeneration: intent.PolicyGeneration,
		Scope: intent.Scope, NamespaceAccess: intent.NamespaceAccess, Target: intent.Target,
		ParameterKind: intent.Parameters.Kind, ParameterDigest: intent.Parameters.Digest(),
		Stdin: intent.Stdin, TTY: intent.TTY, Shell: intent.Shell,
		DataCategories: intent.DataCategories, AllowedSinks: intent.AllowedSinks,
		NetworkEffects: intent.NetworkEffects, NetworkDestinationHash: intent.NetworkDestinationHash,
		Limits: intent.Limits, VerificationPlanID: intent.VerificationPlanID,
		ReasonSummary: intent.ReasonSummary, RiskSummary: intent.RiskSummary,
	}
	if stored.Validate() != nil {
		return StoredActionIntent{}, ErrInvalidStoredApproval
	}
	return stored, nil
}

// Validate checks the explicit projection without reconstructing prohibited
// raw parameters. The exact in-memory request must match this value before use.
func (intent StoredActionIntent) Validate() error {
	projected := domain.ActionIntent{
		Operation: intent.Operation, OperationSchemaVersion: intent.OperationSchemaVersion,
		PolicyVersion: intent.PolicyVersion, PermissionProfile: intent.PermissionProfile,
		Risk: intent.Risk, Effect: intent.Effect, PolicyGeneration: intent.PolicyGeneration,
		Scope: intent.Scope, NamespaceAccess: intent.NamespaceAccess, Target: intent.Target,
		Parameters: domain.ActionParameters{Kind: domain.ActionParametersNone},
		Stdin:      intent.Stdin, TTY: intent.TTY, Shell: intent.Shell,
		DataCategories: intent.DataCategories, AllowedSinks: intent.AllowedSinks,
		NetworkEffects: intent.NetworkEffects, NetworkDestinationHash: intent.NetworkDestinationHash,
		Limits: intent.Limits, VerificationPlanID: intent.VerificationPlanID,
		ReasonSummary: intent.ReasonSummary, RiskSummary: intent.RiskSummary,
	}
	if projected.Validate() != nil || !intent.ParameterKind.Valid() || !intent.ParameterDigest.Valid() ||
		intent.ParameterKind == domain.ActionParametersNone &&
			intent.ParameterDigest != (domain.ActionParameters{Kind: domain.ActionParametersNone}).Digest() {
		return ErrInvalidStoredApproval
	}
	return nil
}

// StoredRequest is the durable-safe approval request projection. It contains
// only a nonce hash and parameter digest; neither one-time nor raw argv values
// cross the persistence port.
type StoredRequest struct {
	ID             domain.ApprovalID
	RunID          domain.AgentRunID
	SessionID      domain.SessionID
	Intent         StoredActionIntent
	Digest         domain.ApprovalDigest
	NonceHash      domain.ApprovalNonceHash
	State          domain.ApprovalState
	StateReason    domain.ApprovalStateReason
	RequestedAt    time.Time
	ExpiresAt      time.Time
	StateChangedAt time.Time
}

// NewStoredRequest projects an in-memory request without its nonce or raw parameters.
func NewStoredRequest(request domain.ApprovalRequest) (StoredRequest, error) {
	if request.Validate() != nil {
		return StoredRequest{}, ErrInvalidStoredApproval
	}
	intent, err := newStoredActionIntent(request.Intent)
	if err != nil {
		return StoredRequest{}, err
	}
	stored := StoredRequest{
		ID: request.ID, RunID: request.RunID, SessionID: request.SessionID,
		Intent: intent, Digest: request.Digest, NonceHash: request.Nonce.Hash(),
		State: request.State, StateReason: request.StateReason,
		RequestedAt: request.RequestedAt, ExpiresAt: request.ExpiresAt, StateChangedAt: request.StateChangedAt,
	}
	if stored.Validate() != nil {
		return StoredRequest{}, ErrInvalidStoredApproval
	}
	return stored, nil
}

// Validate checks the bounded authority projection and lifecycle. Persistence
// cannot recreate raw parameters and therefore never restores execution authority.
func (request StoredRequest) Validate() error {
	if !request.ID.Valid() || !request.RunID.Valid() || !request.SessionID.Valid() ||
		request.Intent.Validate() != nil || !request.Digest.Valid() || !request.NonceHash.Valid() ||
		!request.State.Valid() || !request.StateReason.ValidForState(request.State) ||
		!validStoredApprovalTime(request.RequestedAt) || !validStoredApprovalTime(request.ExpiresAt) ||
		!validStoredApprovalTime(request.StateChangedAt) ||
		!request.ExpiresAt.Equal(request.RequestedAt.Add(domain.ApprovalExecutionTTL)) ||
		request.StateChangedAt.Before(request.RequestedAt) ||
		request.State == domain.ApprovalStatePending && !request.StateChangedAt.Equal(request.RequestedAt) {
		return ErrInvalidStoredApproval
	}
	if request.State == domain.ApprovalStateExpired {
		if request.StateChangedAt.Before(request.ExpiresAt) {
			return ErrInvalidStoredApproval
		}
	} else if !request.StateChangedAt.Before(request.ExpiresAt) {
		return ErrInvalidStoredApproval
	}
	return nil
}

func validStoredApprovalTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 &&
		value.Equal(time.UnixMilli(value.UnixMilli()).UTC())
}

// ValidateAudit checks the exact actor, outcome, subject, digest, and fixed
// scalar details for one durable approval transition.
func (request StoredRequest) ValidateAudit(event domain.AuditEvent) error {
	if request.Validate() != nil || event.Validate() != nil {
		return ErrInvalidStoredApproval
	}
	if request.State != domain.ApprovalStatePending && request.State != domain.ApprovalStateApproved &&
		request.State != domain.ApprovalStateRejected && request.State != domain.ApprovalStateExpired &&
		request.State != domain.ApprovalStateCancelled && request.State != domain.ApprovalStateInvalidated &&
		request.State != domain.ApprovalStateConsumed {
		return ErrInvalidStoredApproval
	}
	eventType, actor, outcome, detail := storedApprovalAuditProjection(request)
	operation := string(request.Intent.Operation)
	subject := request.Intent.Target.Resource
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

// StoredDecision is the durable-safe local decision projection.
type StoredDecision struct {
	RequestID          domain.ApprovalID
	Choice             domain.ApprovalDecisionChoice
	ShownDigest        domain.ApprovalDigest
	NonceHash          domain.ApprovalNonceHash
	Actor              domain.ApprovalActor
	Disposition        domain.ReviewDisposition
	RuleID             domain.PermissionRuleID
	ReviewerProfile    string
	ReviewerOriginHash string
	RationaleSummary   string
	DecidedAt          time.Time
}

// NewStoredDecision projects an in-memory decision without persisting its nonce.
func NewStoredDecision(decision domain.ApprovalDecision) (StoredDecision, error) {
	if decision.Validate() != nil {
		return StoredDecision{}, ErrInvalidStoredApproval
	}
	stored := StoredDecision{
		RequestID:          decision.RequestID,
		Choice:             decision.Choice,
		ShownDigest:        decision.ShownDigest,
		NonceHash:          decision.Nonce.Hash(),
		Actor:              decision.Actor,
		Disposition:        decision.Disposition,
		RuleID:             decision.RuleID,
		ReviewerProfile:    decision.ReviewerProfile,
		ReviewerOriginHash: decision.ReviewerOriginHash,
		RationaleSummary:   decision.RationaleSummary,
		DecidedAt:          decision.DecidedAt,
	}
	if stored.Validate() != nil {
		return StoredDecision{}, ErrInvalidStoredApproval
	}
	return stored, nil
}

// Validate checks the bounded decision metadata and hashed proof.
func (decision StoredDecision) Validate() error {
	if !decision.RequestID.Valid() || !decision.Choice.Valid() ||
		!decision.ShownDigest.Valid() || !decision.NonceHash.Valid() || !decision.Disposition.Valid() ||
		decision.DecidedAt.IsZero() || decision.DecidedAt.Location() != time.UTC ||
		decision.DecidedAt.UnixMilli() < 0 ||
		!decision.DecidedAt.Equal(time.UnixMilli(decision.DecidedAt.UnixMilli()).UTC()) {
		return ErrInvalidStoredApproval
	}
	nonceValue := make([]byte, domain.ApprovalNonceBytes)
	nonceValue[0] = 1
	nonce, _ := domain.NewApprovalNonce(nonceValue)
	if (domain.ApprovalDecision{
		RequestID: decision.RequestID, Choice: decision.Choice, ShownDigest: decision.ShownDigest,
		Nonce: nonce, Actor: decision.Actor, Disposition: decision.Disposition, RuleID: decision.RuleID,
		ReviewerProfile: decision.ReviewerProfile, ReviewerOriginHash: decision.ReviewerOriginHash,
		RationaleSummary: decision.RationaleSummary, DecidedAt: decision.DecidedAt,
	}).Validate() != nil {
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
	case domain.ApprovalStateConsumed:
		return domain.AuditEventWriteIntent, domain.AuditActorSystem, domain.AuditOutcomeSuccess, string(request.StateReason)
	default:
		return domain.AuditEventApprovalCancelled, domain.AuditActorSystem, domain.AuditOutcomeDenied, string(request.StateReason)
	}
}
