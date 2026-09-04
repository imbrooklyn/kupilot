package domain

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	// ApprovalExecutionTTL is retained as the approval-lifecycle spelling of
	// the ActionEnvelope lifetime.
	ApprovalExecutionTTL = ActionApprovalTTL
	// ApprovalNonceBytes is the exact entropy-bearing nonce size.
	ApprovalNonceBytes = 32
	// MaxApprovalReasonSummaryBytes bounds the canonical user-visible reason.
	MaxApprovalReasonSummaryBytes = 512

	// RestartDeploymentApprovalPolicyVersion is the operation-specific spelling
	// of the single current action policy version.
	RestartDeploymentApprovalPolicyVersion = ActionPolicyVersion
	// RestartDeploymentTargetAPIVersion is implicit and cannot be supplied by a model.
	RestartDeploymentTargetAPIVersion = "apps/v1"
	// RestartDeploymentTargetKind is implicit and cannot be supplied by a model.
	RestartDeploymentTargetKind = "Deployment"
	// RestartDeploymentRiskSummary is the fixed user-visible operation risk.
	RestartDeploymentRiskSummary = "Restarting the Deployment replaces Pods and may temporarily reduce availability."
)

var (
	// ErrInvalidApprovalRequest reports an invalid immutable request snapshot.
	ErrInvalidApprovalRequest = errors.New("ApprovalRequest data is invalid")
	// ErrInvalidApprovalDecision reports an invalid local decision record.
	ErrInvalidApprovalDecision = errors.New("ApprovalDecision data is invalid")
	// ErrInvalidApprovalNonce reports an invalid opaque one-time nonce.
	ErrInvalidApprovalNonce = errors.New("Approval nonce is invalid")
	// ErrInvalidApprovalError reports an invalid stable safe error projection.
	ErrInvalidApprovalError = errors.New("ApprovalError data is invalid")
)

// ApprovalID is an opaque application-generated UUIDv7 approval identifier.
type ApprovalID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id ApprovalID) Valid() bool {
	return validUUIDv7(string(id))
}

// ApprovalOperation and OperationIntent are compatibility spellings for the
// one generalized action catalog and intent. They do not define a second enum
// or an approval-specific payload.
type ApprovalOperation = ActionOperation
type OperationIntent = ActionIntent

const ApprovalOperationRestartDeployment = ActionOperationRestartDeployment

// ValidApprovalReasonSummary reports whether model-visible proposal text is
// safe and bounded for the local approval contract.
func ValidApprovalReasonSummary(value string) bool {
	return value != "" && len(value) <= MaxApprovalReasonSummaryBytes &&
		strings.TrimSpace(value) == value && validSafeOptionalText(value, MaxApprovalReasonSummaryBytes)
}

// ApprovalDigest is the lifecycle spelling of the canonical ActionDigest.
type ApprovalDigest = ActionDigest

// ApprovalNonceHash is the durable-safe lowercase SHA-256 hash of a nonce.
type ApprovalNonceHash string

// Valid reports whether the hash has the fixed lowercase representation.
func (hash ApprovalNonceHash) Valid() bool {
	return validSHA256Hex(string(hash))
}

// ApprovalNonce is an opaque, non-renderable one-time UI decision value.
type ApprovalNonce struct {
	value [ApprovalNonceBytes]byte
}

// NewApprovalNonce defensively copies one exact non-zero nonce value.
func NewApprovalNonce(value []byte) (ApprovalNonce, error) {
	var nonce ApprovalNonce
	if len(value) != ApprovalNonceBytes {
		return nonce, ErrInvalidApprovalNonce
	}
	copy(nonce.value[:], value)
	if !nonce.Valid() {
		return ApprovalNonce{}, ErrInvalidApprovalNonce
	}
	return nonce, nil
}

// Valid reports whether the nonce has at least one non-zero byte.
func (nonce ApprovalNonce) Valid() bool {
	combined := byte(0)
	for _, current := range nonce.value {
		combined |= current
	}
	return subtle.ConstantTimeByteEq(combined, 0) == 0
}

// Equal compares two valid nonce values in constant time.
func (nonce ApprovalNonce) Equal(other ApprovalNonce) bool {
	if !nonce.Valid() || !other.Valid() {
		return false
	}
	return subtle.ConstantTimeCompare(nonce.value[:], other.value[:]) == 1
}

// Hash returns the only nonce derivative eligible for future persistence.
func (nonce ApprovalNonce) Hash() ApprovalNonceHash {
	if !nonce.Valid() {
		return ""
	}
	digest := sha256.Sum256(nonce.value[:])
	return ApprovalNonceHash(hex.EncodeToString(digest[:]))
}

// String prevents accidental nonce disclosure through ordinary formatting.
func (ApprovalNonce) String() string {
	return "[approval nonce]"
}

// GoString prevents accidental nonce disclosure through Go-syntax formatting.
func (ApprovalNonce) GoString() string {
	return "[approval nonce]"
}

// ApprovalState is the irreversible lifecycle state of one request.
type ApprovalState string

const (
	ApprovalStatePending     ApprovalState = "pending"
	ApprovalStateApproved    ApprovalState = "approved"
	ApprovalStateRejected    ApprovalState = "rejected"
	ApprovalStateExpired     ApprovalState = "expired"
	ApprovalStateCancelled   ApprovalState = "cancelled"
	ApprovalStateInvalidated ApprovalState = "invalidated"
	ApprovalStateConsumed    ApprovalState = "consumed"
)

// Valid reports whether the state is in the fixed lifecycle.
func (state ApprovalState) Valid() bool {
	switch state {
	case ApprovalStatePending,
		ApprovalStateApproved,
		ApprovalStateRejected,
		ApprovalStateExpired,
		ApprovalStateCancelled,
		ApprovalStateInvalidated,
		ApprovalStateConsumed:
		return true
	default:
		return false
	}
}

// Terminated reports whether the request can never regain execution authority.
func (state ApprovalState) Terminated() bool {
	switch state {
	case ApprovalStateRejected,
		ApprovalStateExpired,
		ApprovalStateCancelled,
		ApprovalStateInvalidated,
		ApprovalStateConsumed:
		return true
	default:
		return false
	}
}

// ApprovalAuditEventType is a fixed approval lifecycle audit intent.
type ApprovalAuditEventType string

const (
	ApprovalAuditRequested   ApprovalAuditEventType = "approval_requested"
	ApprovalAuditApproved    ApprovalAuditEventType = "approval_approved"
	ApprovalAuditRejected    ApprovalAuditEventType = "approval_rejected"
	ApprovalAuditExpired     ApprovalAuditEventType = "approval_expired"
	ApprovalAuditCancelled   ApprovalAuditEventType = "approval_cancelled"
	ApprovalAuditInvalidated ApprovalAuditEventType = "approval_invalidated"
	ApprovalAuditConsumed    ApprovalAuditEventType = "approval_consumed"
)

// Valid reports whether the event is in the fixed approval audit catalog.
func (eventType ApprovalAuditEventType) Valid() bool {
	switch eventType {
	case ApprovalAuditRequested,
		ApprovalAuditApproved,
		ApprovalAuditRejected,
		ApprovalAuditExpired,
		ApprovalAuditCancelled,
		ApprovalAuditInvalidated,
		ApprovalAuditConsumed:
		return true
	default:
		return false
	}
}

// AuditEventType returns the only audit intent associated with the state.
func (state ApprovalState) AuditEventType() ApprovalAuditEventType {
	switch state {
	case ApprovalStatePending:
		return ApprovalAuditRequested
	case ApprovalStateApproved:
		return ApprovalAuditApproved
	case ApprovalStateRejected:
		return ApprovalAuditRejected
	case ApprovalStateExpired:
		return ApprovalAuditExpired
	case ApprovalStateCancelled:
		return ApprovalAuditCancelled
	case ApprovalStateInvalidated:
		return ApprovalAuditInvalidated
	case ApprovalStateConsumed:
		return ApprovalAuditConsumed
	default:
		return ""
	}
}

// ApprovalStateReason is a fixed, value-free transition explanation.
type ApprovalStateReason string

const (
	ApprovalReasonUserApproved        ApprovalStateReason = "user_approved"
	ApprovalReasonUserRejected        ApprovalStateReason = "user_rejected"
	ApprovalReasonPolicyApproved      ApprovalStateReason = "policy_approved"
	ApprovalReasonReviewerApproved    ApprovalStateReason = "reviewer_approved"
	ApprovalReasonReviewerRejected    ApprovalStateReason = "reviewer_rejected"
	ApprovalReasonSessionRuleApproved ApprovalStateReason = "session_rule_approved"
	ApprovalReasonTTLExpired          ApprovalStateReason = "ttl_expired"
	ApprovalReasonUserCancelled       ApprovalStateReason = "user_cancelled"
	ApprovalReasonRunCancelled        ApprovalStateReason = "run_cancelled"
	ApprovalReasonProcessRestarted    ApprovalStateReason = "process_restarted"
	ApprovalReasonContextCancelled    ApprovalStateReason = "context_cancelled"
	ApprovalReasonScopeChanged        ApprovalStateReason = "scope_changed"
	ApprovalReasonPolicyChanged       ApprovalStateReason = "policy_changed"
	ApprovalReasonTargetChanged       ApprovalStateReason = "target_changed"
	ApprovalReasonDigestMismatch      ApprovalStateReason = "digest_mismatch"
	ApprovalReasonNonceMismatch       ApprovalStateReason = "nonce_mismatch"
	ApprovalReasonDecisionReplayed    ApprovalStateReason = "decision_replayed"
	ApprovalReasonConsumed            ApprovalStateReason = "approval_consumed"
)

// ValidCancellation reports whether the reason can explicitly cancel a request.
func (reason ApprovalStateReason) ValidCancellation() bool {
	switch reason {
	case ApprovalReasonUserCancelled,
		ApprovalReasonRunCancelled,
		ApprovalReasonProcessRestarted,
		ApprovalReasonContextCancelled:
		return true
	default:
		return false
	}
}

func (reason ApprovalStateReason) validForState(state ApprovalState) bool {
	switch state {
	case ApprovalStatePending:
		return reason == ""
	case ApprovalStateApproved:
		return reason == ApprovalReasonUserApproved || reason == ApprovalReasonPolicyApproved ||
			reason == ApprovalReasonReviewerApproved || reason == ApprovalReasonSessionRuleApproved
	case ApprovalStateRejected:
		return reason == ApprovalReasonUserRejected || reason == ApprovalReasonReviewerRejected
	case ApprovalStateExpired:
		return reason == ApprovalReasonTTLExpired
	case ApprovalStateCancelled:
		return reason.ValidCancellation()
	case ApprovalStateInvalidated:
		return reason == ApprovalReasonScopeChanged ||
			reason == ApprovalReasonPolicyChanged ||
			reason == ApprovalReasonTargetChanged ||
			reason == ApprovalReasonDigestMismatch ||
			reason == ApprovalReasonNonceMismatch ||
			reason == ApprovalReasonDecisionReplayed
	case ApprovalStateConsumed:
		return reason == ApprovalReasonConsumed
	default:
		return false
	}
}

// ValidForState reports whether a durable lifecycle reason matches its state.
func (reason ApprovalStateReason) ValidForState(state ApprovalState) bool {
	return reason.validForState(state)
}

// ApprovalRequest is an immutable operation proposal plus its current state.
// The nonce is opaque and the digest excludes lifecycle state and nonce.
type ApprovalRequest struct {
	ID             ApprovalID
	RunID          AgentRunID
	SessionID      SessionID
	Intent         OperationIntent
	Digest         ApprovalDigest
	Nonce          ApprovalNonce
	State          ApprovalState
	StateReason    ApprovalStateReason
	RequestedAt    time.Time
	ExpiresAt      time.Time
	StateChangedAt time.Time
}

// Validate checks the complete bounded request shape and exact TTL.
func (request ApprovalRequest) Validate() error {
	if !request.ID.Valid() || !request.RunID.Valid() || !request.SessionID.Valid() ||
		request.Intent.Validate() != nil || !request.Digest.Valid() || !request.Nonce.Valid() ||
		!request.State.Valid() || !request.StateReason.validForState(request.State) ||
		!validPersistenceTime(request.RequestedAt) || !validPersistenceTime(request.ExpiresAt) ||
		!validPersistenceTime(request.StateChangedAt) ||
		!request.ExpiresAt.Equal(request.RequestedAt.Add(ApprovalExecutionTTL)) ||
		request.StateChangedAt.Before(request.RequestedAt) ||
		request.State == ApprovalStatePending && !request.StateChangedAt.Equal(request.RequestedAt) {
		return ErrInvalidApprovalRequest
	}
	if request.State == ApprovalStateExpired {
		if request.StateChangedAt.Before(request.ExpiresAt) {
			return ErrInvalidApprovalRequest
		}
	} else if !request.StateChangedAt.Before(request.ExpiresAt) {
		return ErrInvalidApprovalRequest
	}
	envelope := request.ActionEnvelope()
	if envelope.Validate() != nil {
		return ErrInvalidApprovalRequest
	}
	return nil
}

// ActionEnvelope returns the immutable generic authority represented by this
// lifecycle record. State and nonce are deliberately excluded.
func (request ApprovalRequest) ActionEnvelope() ActionEnvelope {
	return ActionEnvelope{
		SchemaVersion: ActionEnvelopeSchemaVersion,
		RequestID:     request.ID,
		SessionID:     request.SessionID,
		RunID:         request.RunID,
		Intent:        request.Intent,
		RequestedAt:   request.RequestedAt,
		ExpiresAt:     request.ExpiresAt,
		Digest:        request.Digest,
	}
}

// ShownDigest returns the complete digest that a decision must return.
func (request ApprovalRequest) ShownDigest() ApprovalDigest {
	return request.Digest
}

// ApprovalDecisionChoice defaults to Reject at its zero value.
type ApprovalDecisionChoice uint8

const (
	ApprovalDecisionReject ApprovalDecisionChoice = iota
	ApprovalDecisionApprove
)

// Valid reports whether the choice is one of the two explicit outcomes.
func (choice ApprovalDecisionChoice) Valid() bool {
	return choice == ApprovalDecisionReject || choice == ApprovalDecisionApprove
}

// String returns the stable persistence and audit spelling.
func (choice ApprovalDecisionChoice) String() string {
	switch choice {
	case ApprovalDecisionReject:
		return "reject"
	case ApprovalDecisionApprove:
		return "approve"
	default:
		return "invalid"
	}
}

// ApprovalActor identifies the deterministic source of one durable decision.
type ApprovalActor string

const (
	ApprovalActorLocalUser        ApprovalActor = "local_user"
	ApprovalActorPermissionPolicy ApprovalActor = "permission_policy"
	ApprovalActorReviewer         ApprovalActor = "approval_reviewer"
	ApprovalActorSessionRule      ApprovalActor = "session_rule"
)

// ApprovalDecision is the authoritative local decision record.
type ApprovalDecision struct {
	RequestID          ApprovalID
	Choice             ApprovalDecisionChoice
	ShownDigest        ApprovalDigest
	Nonce              ApprovalNonce
	Actor              ApprovalActor
	Disposition        ReviewDisposition
	RuleID             PermissionRuleID
	ReviewerProfile    string
	ReviewerOriginHash string
	RationaleSummary   string
	DecidedAt          time.Time
}

// Validate checks the fixed actor, displayed proof, choice, and UTC time.
func (decision ApprovalDecision) Validate() error {
	if !decision.RequestID.Valid() || !decision.Choice.Valid() ||
		!decision.ShownDigest.Valid() || !decision.Nonce.Valid() || !decision.Disposition.Valid() ||
		!validPersistenceTime(decision.DecidedAt) || !decision.validActorMetadata() {
		return ErrInvalidApprovalDecision
	}
	return nil
}

func (decision ApprovalDecision) validActorMetadata() bool {
	switch decision.Actor {
	case ApprovalActorLocalUser:
		return decision.Disposition == ReviewDispositionHuman && decision.RuleID == "" &&
			decision.ReviewerProfile == "" && decision.ReviewerOriginHash == "" && decision.RationaleSummary == ""
	case ApprovalActorPermissionPolicy:
		return decision.Choice == ApprovalDecisionApprove && decision.Disposition == ReviewDispositionAutomatic && decision.RuleID == "" &&
			decision.ReviewerProfile == "" && decision.ReviewerOriginHash == "" && decision.RationaleSummary == ""
	case ApprovalActorSessionRule:
		return decision.Choice == ApprovalDecisionApprove && decision.Disposition == ReviewDispositionAutomatic && decision.RuleID.Valid() &&
			decision.ReviewerProfile == "" && decision.ReviewerOriginHash == "" && decision.RationaleSummary == ""
	case ApprovalActorReviewer:
		return decision.Disposition == ReviewDispositionReviewer && decision.RuleID == "" &&
			ValidModelToken(decision.ReviewerProfile, 128) && validSHA256Hex(decision.ReviewerOriginHash) &&
			validBoundedText(decision.RationaleSummary, 1, 2048)
	default:
		return false
	}
}

// ApprovalErrorCode is a stable safe failure identifier.
type ApprovalErrorCode string

const (
	ApprovalErrorCodeInvalidConfiguration ApprovalErrorCode = "approval_invalid_configuration"
	ApprovalErrorCodeInvalidIntent        ApprovalErrorCode = "approval_invalid_intent"
	ApprovalErrorCodeInvalidRequest       ApprovalErrorCode = "approval_invalid_request"
	ApprovalErrorCodeRequestNotFound      ApprovalErrorCode = "approval_request_not_found"
	ApprovalErrorCodeRequestConflict      ApprovalErrorCode = "approval_request_conflict"
	ApprovalErrorCodeInvalidTransition    ApprovalErrorCode = "approval_invalid_transition"
	ApprovalErrorCodeDecisionReplayed     ApprovalErrorCode = "approval_decision_replayed"
	ApprovalErrorCodeDigestMismatch       ApprovalErrorCode = "approval_digest_mismatch"
	ApprovalErrorCodeNonceMismatch        ApprovalErrorCode = "approval_nonce_mismatch"
	ApprovalErrorCodeStaleScope           ApprovalErrorCode = "approval_stale_scope"
	ApprovalErrorCodeNotExpired           ApprovalErrorCode = "approval_not_expired"
	ApprovalErrorCodeExpired              ApprovalErrorCode = "approval_expired"
	ApprovalErrorCodeCancelled            ApprovalErrorCode = "approval_cancelled"
	ApprovalErrorCodeExecutorFailed       ApprovalErrorCode = "approval_executor_failed"
	ApprovalErrorCodeInternal             ApprovalErrorCode = "approval_internal"
)

// Valid reports whether the code is in the stable catalog.
func (code ApprovalErrorCode) Valid() bool {
	_, _, ok := approvalErrorDefinition(code)
	return ok
}

// ApprovalError is a bounded safe error with no raw cause or external value.
type ApprovalError struct {
	code        ApprovalErrorCode
	class       SafeErrorClass
	safeMessage string
}

// NewApprovalError constructs one fail-closed stable error projection.
func NewApprovalError(code ApprovalErrorCode) *ApprovalError {
	class, message, ok := approvalErrorDefinition(code)
	if !ok {
		code = ApprovalErrorCodeInternal
		class, message, _ = approvalErrorDefinition(code)
	}
	return &ApprovalError{code: code, class: class, safeMessage: message}
}

// Error returns only code-defined English text and the stable code.
func (approvalError *ApprovalError) Error() string {
	if approvalError == nil {
		return "The approval operation failed safely. (approval_internal)"
	}
	return approvalError.safeMessage + " (" + string(approvalError.code) + ")"
}

// Is preserves context cancellation semantics without retaining a raw cause.
func (approvalError *ApprovalError) Is(target error) bool {
	return approvalError != nil && approvalError.code == ApprovalErrorCodeCancelled && target == context.Canceled
}

// Code returns the stable approval failure identifier.
func (approvalError *ApprovalError) Code() ApprovalErrorCode {
	if approvalError == nil {
		return ApprovalErrorCodeInternal
	}
	return approvalError.code
}

// Class returns the accepted project-wide safe error class.
func (approvalError *ApprovalError) Class() SafeErrorClass {
	if approvalError == nil {
		return SafeErrorClassInternal
	}
	return approvalError.class
}

// Retryable is always false; a new proposal is required after failure.
func (*ApprovalError) Retryable() bool {
	return false
}

// SafeMessage returns bounded code-defined text without request data.
func (approvalError *ApprovalError) SafeMessage() string {
	if approvalError == nil {
		return "The approval operation failed safely."
	}
	return approvalError.safeMessage
}

// Validate checks that the exposed fields match the fixed catalog.
func (approvalError *ApprovalError) Validate() error {
	if approvalError == nil {
		return ErrInvalidApprovalError
	}
	class, message, ok := approvalErrorDefinition(approvalError.code)
	if !ok || approvalError.class != class || approvalError.safeMessage != message {
		return ErrInvalidApprovalError
	}
	return nil
}

func approvalErrorDefinition(code ApprovalErrorCode) (SafeErrorClass, string, bool) {
	switch code {
	case ApprovalErrorCodeInvalidConfiguration:
		return SafeErrorClassConfigurationInvalid, "The approval service configuration is invalid.", true
	case ApprovalErrorCodeInvalidIntent:
		return SafeErrorClassInvalidInput, "The proposed operation does not satisfy the fixed approval policy.", true
	case ApprovalErrorCodeInvalidRequest:
		return SafeErrorClassInvalidInput, "The approval request is invalid.", true
	case ApprovalErrorCodeRequestNotFound:
		return SafeErrorClassNotFound, "The approval request is unavailable.", true
	case ApprovalErrorCodeRequestConflict:
		return SafeErrorClassConflict, "The approval request identity is already in use.", true
	case ApprovalErrorCodeInvalidTransition:
		return SafeErrorClassPolicyDenied, "The approval state does not permit this transition.", true
	case ApprovalErrorCodeDecisionReplayed:
		return SafeErrorClassPolicyDenied, "The approval decision was already used.", true
	case ApprovalErrorCodeDigestMismatch:
		return SafeErrorClassPolicyDenied, "The displayed operation digest does not match the immutable request.", true
	case ApprovalErrorCodeNonceMismatch:
		return SafeErrorClassPolicyDenied, "The approval decision nonce does not match the request.", true
	case ApprovalErrorCodeStaleScope:
		return SafeErrorClassStaleScope, "The approval request no longer matches the active Kubernetes context and namespace.", true
	case ApprovalErrorCodeNotExpired:
		return SafeErrorClassPolicyDenied, "The approval request has not reached its expiry.", true
	case ApprovalErrorCodeExpired:
		return SafeErrorClassPolicyDenied, "The approval request expired and cannot execute.", true
	case ApprovalErrorCodeCancelled:
		return SafeErrorClassCancelled, "The approval request was cancelled and cannot execute.", true
	case ApprovalErrorCodeExecutorFailed:
		return SafeErrorClassInternal, "The approved operation attempt failed safely and will not be retried.", true
	case ApprovalErrorCodeInternal:
		return SafeErrorClassInternal, "The approval operation failed safely.", true
	default:
		return "", "", false
	}
}
