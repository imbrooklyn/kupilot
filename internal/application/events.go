package application

import (
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// ErrInvalidUIEvent reports an invalid UI projection without echoing its data.
var ErrInvalidUIEvent = errors.New("UI event data is invalid")

// UIEventKind identifies one ordered Application-to-TUI run projection.
type UIEventKind string

const (
	UIEventRunStarted   UIEventKind = "run_started"
	UIEventTextDelta    UIEventKind = "text_delta"
	UIEventToolStep     UIEventKind = "tool_step"
	UIEventRunCompleted UIEventKind = "run_completed"
	UIEventRunFailed    UIEventKind = "run_failed"
	UIEventRunCancelled UIEventKind = "run_cancelled"
)

// ToolStepStatus is the delivery-safe state of one inline Tool step.
type ToolStepStatus string

const (
	ToolStepRequested ToolStepStatus = "requested"
	ToolStepRunning   ToolStepStatus = "running"
	ToolStepSucceeded ToolStepStatus = "succeeded"
	ToolStepPartial   ToolStepStatus = "partial"
	ToolStepDenied    ToolStepStatus = "denied"
	ToolStepFailed    ToolStepStatus = "failed"
	ToolStepCancelled ToolStepStatus = "cancelled"
)

// ToolStep is a bounded safe UI projection, never a raw ToolResult.
type ToolStep struct {
	InvocationID  domain.ToolInvocationID
	Name          domain.ToolName
	Purpose       string
	Status        ToolStepStatus
	Summary       string
	EvidenceCount int
	Truncated     bool
}

// UIEvent carries publisher-owned identity and exactly one projected payload.
type UIEvent struct {
	Kind            UIEventKind
	RunID           domain.AgentRunID
	ScopeGeneration int64
	Sequence        int64
	Text            string
	ToolStep        *ToolStep
}

// Terminal reports whether later events for the same run must be ignored.
func (event UIEvent) Terminal() bool {
	return event.Kind == UIEventRunCompleted || event.Kind == UIEventRunFailed || event.Kind == UIEventRunCancelled
}

// Validate checks identity, payload exclusivity, and fixed event states.
func (event UIEvent) Validate() error {
	if !event.RunID.Valid() || event.ScopeGeneration < 1 || event.Sequence < 1 || event.Sequence > 4096 {
		return ErrInvalidUIEvent
	}
	switch event.Kind {
	case UIEventRunStarted:
		if event.Text != "" || event.ToolStep != nil {
			return ErrInvalidUIEvent
		}
	case UIEventTextDelta, UIEventRunCompleted, UIEventRunFailed, UIEventRunCancelled:
		if event.Text == "" || len(event.Text) > MaxQuestionBytes || event.ToolStep != nil {
			return ErrInvalidUIEvent
		}
	case UIEventToolStep:
		if event.Text != "" || event.ToolStep == nil || !event.ToolStep.valid() {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	return nil
}

func (step ToolStep) valid() bool {
	if !step.InvocationID.Valid() || !step.Name.Valid() || len(step.Purpose) > 1024 || len(step.Summary) > 4096 ||
		step.EvidenceCount < 0 || step.EvidenceCount > 100 {
		return false
	}
	switch step.Status {
	case ToolStepRequested, ToolStepRunning, ToolStepSucceeded, ToolStepPartial,
		ToolStepDenied, ToolStepFailed, ToolStepCancelled:
		return true
	default:
		return false
	}
}
