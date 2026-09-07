package domain

import (
	"errors"
	"time"
)

const (
	// MaxAgentRunDuration is the non-expandable wall-clock ceiling for one run.
	MaxAgentRunDuration = 60 * time.Minute
	// MaxAgentSteps is the non-expandable single-Agent loop ceiling.
	MaxAgentSteps = 128
	// MaxAgentToolCalls is the non-expandable Tool-call ceiling for one run.
	MaxAgentToolCalls = 256
	// MaxAgentModelCalls is the non-expandable model-call ceiling for one run.
	MaxAgentModelCalls = 64
	// MaxAgentRunToolResultBytes is the non-expandable cumulative ToolResult ceiling.
	MaxAgentRunToolResultBytes = 16 * 1024 * 1024
	// MaxAgentNoProgressSteps stops collection after this many consecutive steps
	// add no accepted Evidence.
	MaxAgentNoProgressSteps = 10

	maxPromptVersionBytes      = 128
	maxToolCatalogVersionBytes = 128
	maxTerminationReasonBytes  = 1024
	maxAgentSteps              = MaxAgentSteps
	maxToolCalls               = MaxAgentToolCalls
	maxModelRequests           = MaxAgentModelCalls

	// InterruptedByRestartReason is the stable reason used by startup recovery.
	InterruptedByRestartReason = "process_interrupted"
)

var (
	// ErrInvalidScope reports an invalid bounded historic scope snapshot.
	ErrInvalidScope = errors.New("ClusterScope snapshot data is invalid")
	// ErrInvalidAgentRun reports an AgentRun invariant failure without echoing data.
	ErrInvalidAgentRun = errors.New("AgentRun data is invalid")
	// ErrInvalidAgentRunTransition reports a forbidden or stale state transition.
	ErrInvalidAgentRunTransition = errors.New("AgentRun transition is invalid")
)

// AgentRunID is an opaque application-generated UUIDv7 run identifier.
type AgentRunID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id AgentRunID) Valid() bool {
	return validUUIDv7(string(id))
}

// ScopeSnapshot is immutable historic run scope metadata without a live client.
type ScopeSnapshot struct {
	Context    string
	Namespace  string
	Generation int64
}

// Validate checks the complete bounded scope snapshot.
func (scope ScopeSnapshot) Validate() error {
	if !validBoundedText(scope.Context, 1, maxContextBytes) ||
		!validBoundedText(scope.Namespace, 1, maxNamespaceBytes) ||
		scope.Generation < 0 {
		return ErrInvalidScope
	}
	return nil
}

// AgentRunStatus is the durable lifecycle state of one bounded Agent execution.
type AgentRunStatus string

const (
	AgentRunStatusQueued      AgentRunStatus = "queued"
	AgentRunStatusRunning     AgentRunStatus = "running"
	AgentRunStatusCompleted   AgentRunStatus = "completed"
	AgentRunStatusFailed      AgentRunStatus = "failed"
	AgentRunStatusCancelled   AgentRunStatus = "cancelled"
	AgentRunStatusTimedOut    AgentRunStatus = "timed_out"
	AgentRunStatusStaleScope  AgentRunStatus = "stale_scope"
	AgentRunStatusInterrupted AgentRunStatus = "interrupted"
)

// Terminal reports whether no further runtime transition is admitted.
func (status AgentRunStatus) Terminal() bool {
	switch status {
	case AgentRunStatusCompleted,
		AgentRunStatusFailed,
		AgentRunStatusCancelled,
		AgentRunStatusTimedOut,
		AgentRunStatusStaleScope,
		AgentRunStatusInterrupted:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports one admitted Application-owned state transition.
func (status AgentRunStatus) CanTransitionTo(next AgentRunStatus) bool {
	return status == AgentRunStatusQueued && next == AgentRunStatusRunning ||
		status == AgentRunStatusRunning && next.Terminal()
}

// AgentRun contains bounded lifecycle metadata, never executable Agent state.
type AgentRun struct {
	ID                  AgentRunID
	SessionID           SessionID
	RequestMessageID    MessageID
	Status              AgentRunStatus
	Scope               ScopeSnapshot
	Resource            *ResourceRef
	PromptVersion       string
	ToolCatalogVersion  string
	StepCount           int
	ToolCallCount       int
	ModelRequestCount   int
	InputTokens         *int64
	OutputTokens        *int64
	TerminationReason   *string
	PersistenceDegraded bool
	StartedAt           *time.Time
	FinishedAt          *time.Time
}

// Validate checks the complete AgentRun persistence contract.
func (run AgentRun) Validate() error {
	if !run.ID.Valid() || !run.SessionID.Valid() || !run.RequestMessageID.Valid() ||
		run.Scope.Validate() != nil ||
		!validBoundedText(run.PromptVersion, 1, maxPromptVersionBytes) ||
		!validBoundedText(run.ToolCatalogVersion, 1, maxToolCatalogVersionBytes) ||
		run.StepCount < 0 || run.StepCount > maxAgentSteps ||
		run.ToolCallCount < 0 || run.ToolCallCount > maxToolCalls ||
		run.ModelRequestCount < 0 || run.ModelRequestCount > maxModelRequests ||
		(run.InputTokens != nil && *run.InputTokens < 0) ||
		(run.OutputTokens != nil && *run.OutputTokens < 0) {
		return ErrInvalidAgentRun
	}
	if run.Resource != nil &&
		(!ReferenceMatchesWorkingNamespace(*run.Resource, run.Scope.Namespace) || run.Resource.Validate() != nil) {
		return ErrInvalidAgentRun
	}
	if run.TerminationReason != nil && !validBoundedText(*run.TerminationReason, 1, maxTerminationReasonBytes) {
		return ErrInvalidAgentRun
	}
	if run.StartedAt != nil && !validDurableTime(*run.StartedAt) ||
		run.FinishedAt != nil && !validDurableTime(*run.FinishedAt) ||
		run.StartedAt != nil && run.FinishedAt != nil && run.FinishedAt.Before(*run.StartedAt) {
		return ErrInvalidAgentRun
	}
	switch {
	case run.Status == AgentRunStatusQueued:
		if run.StartedAt != nil || run.FinishedAt != nil || run.TerminationReason != nil {
			return ErrInvalidAgentRun
		}
	case run.Status == AgentRunStatusRunning:
		if run.StartedAt == nil || run.FinishedAt != nil || run.TerminationReason != nil {
			return ErrInvalidAgentRun
		}
	case run.Status.Terminal():
		if run.StartedAt == nil || run.FinishedAt == nil {
			return ErrInvalidAgentRun
		}
	default:
		return ErrInvalidAgentRun
	}
	return nil
}

// ValidateAgentRunTransition checks immutable identity, monotonic counters, and state.
func ValidateAgentRunTransition(current, next AgentRun) error {
	startedAtValid := (current.Status == AgentRunStatusQueued && current.StartedAt == nil && next.StartedAt != nil) ||
		(current.Status == AgentRunStatusRunning && sameInstant(current.StartedAt, next.StartedAt))
	if current.Validate() != nil || next.Validate() != nil ||
		!current.Status.CanTransitionTo(next.Status) ||
		current.ID != next.ID || current.SessionID != next.SessionID ||
		current.RequestMessageID != next.RequestMessageID ||
		current.Scope != next.Scope ||
		!sameResourceRef(current.Resource, next.Resource) ||
		current.PromptVersion != next.PromptVersion ||
		current.ToolCatalogVersion != next.ToolCatalogVersion ||
		!startedAtValid ||
		next.StepCount < current.StepCount ||
		next.ToolCallCount < current.ToolCallCount ||
		next.ModelRequestCount < current.ModelRequestCount ||
		current.PersistenceDegraded && !next.PersistenceDegraded {
		return ErrInvalidAgentRunTransition
	}
	return nil
}
