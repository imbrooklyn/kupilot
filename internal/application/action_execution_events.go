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
	Authorization   *UIToolActionAuthorization
	SafeOutput      string
}

// UIToolActionAuthorization distinguishes consumed pre-operation authority
// from an external attempt or outcome. The Tool remains responsible for the
// one bounded attempt and reports its result through the normal Tool lifecycle;
// this projection contains no log, data-source, or remote output.
type UIToolActionAuthorization struct {
	State string
}

const uiToolActionAuthorized = "authorized"

func (event UIActionExecution) Validate() error {
	if !event.RequestID.Valid() || !event.RunID.Valid() || event.ScopeGeneration < 1 ||
		event.Sequence < 1 || event.Sequence > 4096 || !event.Digest.Valid() || !event.Operation.Valid() ||
		event.Limits.Validate() != nil || actionExecutionPayloadCount(event) != 1 {
		return ErrInvalidUIEvent
	}
	if event.Remediation != nil {
		if event.Operation != event.Remediation.Operation || event.SafeOutput != "" ||
			event.Remediation.Validate(event.Limits.MaximumItems) != nil {
			return ErrInvalidUIEvent
		}
		return nil
	}
	if event.Authorization != nil {
		if !supervisedToolOperation(event.Operation) || event.Authorization.State != uiToolActionAuthorized || event.SafeOutput != "" {
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

func actionExecutionPayloadCount(event UIActionExecution) int {
	count := 0
	if event.Remediation != nil {
		count++
	}
	if event.LocalProcess != nil {
		count++
	}
	if event.Authorization != nil {
		count++
	}
	return count
}

func supervisedToolOperation(operation domain.ActionOperation) bool {
	switch operation {
	case domain.ActionOperationPodDiagnostic, domain.ActionOperationPodExec,
		domain.ActionOperationContainerFileRead, domain.ActionOperationDiagnosticPod,
		domain.ActionOperationLogsCurrent, domain.ActionOperationLogsPrevious,
		domain.ActionOperationLogsAllContainers, domain.ActionOperationLogSearch,
		domain.ActionOperationPrometheusQuery, domain.ActionOperationLokiQuery:
		return true
	default:
		return false
	}
}
