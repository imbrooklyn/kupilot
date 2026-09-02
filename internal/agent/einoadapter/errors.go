package einoadapter

import (
	"errors"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	// ErrInvalidConfiguration reports a missing or invalid production adapter
	// dependency without exposing framework or external data.
	ErrInvalidConfiguration = errors.New("Eino Agent adapter configuration is invalid")
)

const (
	safeInternalFailure           = "The diagnostic runtime failed safely."
	safeInvalidModelResponse      = "The model returned an invalid diagnostic response."
	safeInvalidToolResult         = "A cluster-reading tool returned data outside the safe result contract."
	safeEventRejected             = "The diagnostic event stream could not be accepted safely."
	safeScopeStale                = "The diagnostic run stopped because the Kubernetes context or namespace changed."
	safeSensitiveModelTextBlocked = "Sensitive model output was blocked before downstream use."
)

type runtimeFailure struct {
	status         domain.AgentRunStatus
	class          domain.SafeErrorClass
	safeMessage    string
	stopReason     agent.RunStopReason
	localDiagnosis bool
	cause          error
}

func (failure *runtimeFailure) Error() string {
	if failure == nil || failure.safeMessage == "" {
		return safeInternalFailure
	}
	return failure.safeMessage
}

func (failure *runtimeFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func failedRuntime(class domain.SafeErrorClass, message string, cause error) *runtimeFailure {
	return &runtimeFailure{
		status:      domain.AgentRunStatusFailed,
		class:       class,
		safeMessage: message,
		stopReason:  agent.RunStopFailed,
		cause:       cause,
	}
}

func localRuntimeStop(reason agent.RunStopReason, class domain.SafeErrorClass, message string, cause error) *runtimeFailure {
	return &runtimeFailure{
		status:         domain.AgentRunStatusCompleted,
		class:          class,
		safeMessage:    message,
		stopReason:     reason,
		localDiagnosis: true,
		cause:          cause,
	}
}

func staleRuntime(cause error) *runtimeFailure {
	return &runtimeFailure{
		status:      domain.AgentRunStatusStaleScope,
		class:       domain.SafeErrorClassStaleScope,
		safeMessage: safeScopeStale,
		stopReason:  agent.RunStopStaleScope,
		cause:       cause,
	}
}

func runtimeFailureFromBudget(err error) *runtimeFailure {
	var budgetError *agent.RunBudgetError
	if !errors.As(err, &budgetError) {
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	switch budgetError.Reason() {
	case agent.RunStopCancelled:
		return &runtimeFailure{
			status:      domain.AgentRunStatusCancelled,
			class:       domain.SafeErrorClassCancelled,
			safeMessage: budgetError.Error(),
			stopReason:  agent.RunStopCancelled,
			cause:       err,
		}
	case agent.RunStopTimedOut:
		return &runtimeFailure{
			status:      domain.AgentRunStatusTimedOut,
			class:       domain.SafeErrorClassTimeout,
			safeMessage: budgetError.Error(),
			stopReason:  agent.RunStopTimedOut,
			cause:       err,
		}
	case agent.RunStopStaleScope:
		return staleRuntime(err)
	case agent.RunStopStepLimit,
		agent.RunStopToolCallLimit,
		agent.RunStopModelCallLimit,
		agent.RunStopToolResultBytes,
		agent.RunStopRepeatedToolCall,
		agent.RunStopNoProgress,
		agent.RunStopLogCallLimit:
		return localRuntimeStop(budgetError.Reason(), budgetError.Class(), budgetError.Error(), err)
	default:
		return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
}
