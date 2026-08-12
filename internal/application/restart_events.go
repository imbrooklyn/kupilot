package application

import (
	"math"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// UIRestartExecutionState is a fixed delivery projection. PATCH acceptance and
// rollout verification are intentionally separate states.
type UIRestartExecutionState string

const (
	UIRestartPatchAccepted       UIRestartExecutionState = "patch_accepted"
	UIRestartPatchFailed         UIRestartExecutionState = "patch_failed"
	UIRestartPatchOutcomeUnknown UIRestartExecutionState = "patch_outcome_unknown"
	UIRestartNotAttempted        UIRestartExecutionState = "patch_not_attempted"
	UIRestartRolloutProgress     UIRestartExecutionState = "rollout_progress"
	UIRestartRolloutSucceeded    UIRestartExecutionState = "rollout_succeeded"
	UIRestartRolloutTimedOut     UIRestartExecutionState = "rollout_timed_out"
	UIRestartRolloutFailed       UIRestartExecutionState = "rollout_failed"
	UIRestartRolloutUnavailable  UIRestartExecutionState = "rollout_unavailable"
	UIRestartResultAuditFailed   UIRestartExecutionState = "result_audit_failed"
)

// Terminal reports whether no later execution projection is expected.
func (state UIRestartExecutionState) Terminal() bool {
	switch state {
	case UIRestartPatchFailed,
		UIRestartPatchOutcomeUnknown,
		UIRestartNotAttempted,
		UIRestartRolloutSucceeded,
		UIRestartRolloutTimedOut,
		UIRestartRolloutFailed,
		UIRestartRolloutUnavailable,
		UIRestartResultAuditFailed:
		return true
	default:
		return false
	}
}

// UIRestartExecution is one bounded request-bound execution update. It carries
// no Kubernetes condition message, resource version, patch, or vendor error.
type UIRestartExecution struct {
	RequestID          domain.ApprovalID
	RunID              domain.AgentRunID
	ScopeGeneration    int64
	Sequence           int64
	EventIndex         int64
	Digest             domain.ApprovalDigest
	State              UIRestartExecutionState
	ErrorClass         domain.SafeErrorClass
	FailureCode        RestartRolloutFailureCode
	TargetGeneration   int64
	ObservedGeneration int64
	UpdatedReplicas    int64
	AvailableReplicas  int64
	TargetReplicas     int64
}

// Validate checks identity and the mutually exclusive fixed state shapes.
func (event UIRestartExecution) Validate() error {
	if !event.RequestID.Valid() || !event.RunID.Valid() || event.ScopeGeneration < 1 ||
		event.Sequence < 1 || event.Sequence > 4096 || event.EventIndex < 1 ||
		event.EventIndex > MaxRestartRolloutObservations+2 ||
		!event.Digest.Valid() {
		return ErrInvalidUIEvent
	}
	hasCounts := event.TargetGeneration > 0 && event.ObservedGeneration >= 0 &&
		event.UpdatedReplicas >= 0 && event.UpdatedReplicas <= math.MaxInt32 &&
		event.AvailableReplicas >= 0 && event.AvailableReplicas <= math.MaxInt32 &&
		event.TargetReplicas >= 0 && event.TargetReplicas <= math.MaxInt32
	zeroCounts := event.TargetGeneration == 0 && event.ObservedGeneration == 0 &&
		event.UpdatedReplicas == 0 && event.AvailableReplicas == 0 && event.TargetReplicas == 0
	switch event.State {
	case UIRestartPatchAccepted:
		if !hasCounts || event.ObservedGeneration != 0 || event.UpdatedReplicas != 0 ||
			event.AvailableReplicas != 0 || event.ErrorClass != "" || event.FailureCode != "" {
			return ErrInvalidUIEvent
		}
	case UIRestartPatchFailed, UIRestartPatchOutcomeUnknown, UIRestartNotAttempted,
		UIRestartRolloutUnavailable, UIRestartResultAuditFailed:
		if !zeroCounts || !event.ErrorClass.Valid() || event.FailureCode != "" {
			return ErrInvalidUIEvent
		}
	case UIRestartRolloutProgress, UIRestartRolloutSucceeded:
		if !hasCounts || event.ErrorClass != "" || event.FailureCode != "" {
			return ErrInvalidUIEvent
		}
	case UIRestartRolloutTimedOut:
		if (!hasCounts && !zeroCounts) || event.ErrorClass != "" || event.FailureCode != "" {
			return ErrInvalidUIEvent
		}
	case UIRestartRolloutFailed:
		if !hasCounts || event.ErrorClass != "" || !event.FailureCode.valid() {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	return nil
}
