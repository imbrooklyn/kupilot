package application

import (
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// UIActionExecution is the non-restart execution projection. It preserves
// request acceptance, ambiguous outcome, verification, and local process exit
// as distinct typed fields. SafeOutput has already passed terminal, Unicode,
// sensitive-value, line, and byte policy.
type UIActionExecution struct {
	RequestID       domain.ApprovalID
	RunID           domain.AgentRunID
	ScopeGeneration int64
	Sequence        int64
	Digest          domain.ActionDigest
	Operation       domain.ActionOperation
	Limits          domain.ActionLimits
	Remediation     *domain.RemediationResult
	LocalProcess    *domain.LocalCommandResult
	SafeOutput      string
}

func (event UIActionExecution) Validate() error {
	if !event.RequestID.Valid() || !event.RunID.Valid() || event.ScopeGeneration < 1 ||
		event.Sequence < 1 || event.Sequence > 4096 || !event.Digest.Valid() || !event.Operation.Valid() ||
		event.Limits.Validate() != nil ||
		(event.Remediation == nil) == (event.LocalProcess == nil) {
		return ErrInvalidUIEvent
	}
	if event.Remediation != nil {
		if event.Operation != event.Remediation.Operation || event.SafeOutput != "" ||
			event.Remediation.Validate(event.Limits.MaximumItems) != nil {
			return ErrInvalidUIEvent
		}
		return nil
	}
	if event.Operation != domain.ActionOperationRestrictedLocalArgv && event.Operation != domain.ActionOperationShell ||
		event.LocalProcess == nil || event.SafeOutput != event.LocalProcess.SafeOutput ||
		event.LocalProcess.Validate(event.Limits) != nil ||
		event.LocalProcess.OutputDigest != domain.LocalSafeOutputDigest(event.SafeOutput) ||
		len(event.SafeOutput) > domain.MaxLocalOutputBytes {
		return ErrInvalidUIEvent
	}
	return nil
}
