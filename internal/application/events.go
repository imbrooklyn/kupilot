package application

import (
	"context"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// UIScopeResult is one request-bound scope activation projection.
type UIScopeResult struct {
	RequestID          uint64
	ExpectedGeneration int64
	ScopeGeneration    int64
	Context            string
	Namespace          string
	ReadOnly           bool
	Failure            UIQueryFailureCode
}

// Validate checks success and fail-closed scope result shapes.
func (result UIScopeResult) Validate() error {
	if result.RequestID == 0 || result.ExpectedGeneration < 0 || result.ScopeGeneration < result.ExpectedGeneration {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" {
		if !result.Failure.validOperational() || result.Context != "" || result.Namespace != "" || result.ReadOnly {
			return ErrInvalidUIEvent
		}
		return nil
	}
	candidate := domain.ScopeCandidate{Context: result.Context, Namespace: result.Namespace}
	if candidate.Validate() != nil || !result.ReadOnly {
		return ErrInvalidUIEvent
	}
	return nil
}

// UIStatusResult is the current safe in-memory status projection. Historic
// scope metadata and pending resume candidates are intentionally excluded.
type UIStatusResult struct {
	Session         *UISessionState
	Context         string
	Namespace       string
	ScopeGeneration int64
	ReadOnly        bool
	RunID           domain.AgentRunID
	RunActive       bool
}

// UICommandOutcome contains only the typed result shapes used by delivery.
// Exactly which fields are populated is determined by Command.
type UICommandOutcome struct {
	Command   UICommandKind
	RequestID uint64
	Session   *UISessionState
	Resumed   *UIResumedSession
	Scope     *UIScopeResult
	Resource  *UIResourceSelectionResult
	Status    *UIStatusResult
	RunID     domain.AgentRunID
	Failure   UIQueryFailureCode
}

// Validate checks command/result correlation and exclusive payload shapes.
func (result UICommandOutcome) Validate() error {
	if result.Failure != "" && (!result.Failure.valid() ||
		result.Failure == UIQueryNotResumable && result.Command != UICommandResumeSession) {
		return ErrInvalidUIEvent
	}
	switch result.Command {
	case UICommandAcceptResume:
		if result.RequestID == 0 {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
				result.Status != nil || result.RunID != "" {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.Session == nil || !result.Session.validate() || !result.Session.Resumed ||
			result.Resumed == nil || !validUIResumedSession(*result.Resumed, result.RequestID) ||
			result.Resumed.Session.ID != result.Session.ID || result.Scope == nil || result.Scope.RequestID != result.RequestID ||
			result.Scope.Validate() != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
		if result.Resource != nil {
			if result.Resource.RequestID != result.RequestID || result.Resource.ScopeGeneration != result.Scope.ScopeGeneration ||
				result.Resource.Validate() != nil {
				return ErrInvalidUIEvent
			}
		}
	case UICommandCancelResume:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSelectContext, UICommandSelectNamespace, UICommandActivateScope:
		if result.RequestID == 0 || result.Scope == nil || result.Scope.RequestID != result.RequestID || result.Scope.Validate() != nil ||
			result.Session != nil || result.Resumed != nil || result.Resource != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSelectResource:
		if result.RequestID == 0 || result.Resource == nil || result.Resource.RequestID != result.RequestID || result.Resource.Validate() != nil ||
			result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandNewSession:
		if result.RequestID != 0 || result.Session == nil || !result.Session.validate() || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandRenameSession:
		if result.RequestID != 0 || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
			result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if result.Session != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.Session == nil || !result.Session.validate() {
			return ErrInvalidUIEvent
		}
	case UICommandShowStatus:
		if result.RequestID != 0 || result.Status == nil || !result.Status.valid() || result.Session != nil || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSubmitQuestion:
		if result.RequestID != 0 || result.Failure == "" || result.RunID != "" || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil {
			return ErrInvalidUIEvent
		}
	case UICommandCancelRun:
		if result.RequestID != 0 || !result.RunID.Valid() || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandShowPrivacy:
		if result.RequestID != 0 || result.Failure == "" || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandResumeSession:
		if result.RequestID == 0 || result.Failure == "" || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	return nil
}

func (result UIStatusResult) valid() bool {
	if result.Session != nil && !result.Session.validate() || result.ScopeGeneration < 0 ||
		(result.Context == "") != (result.Namespace == "") || result.ReadOnly != (result.Context != "") ||
		result.Context != "" && (!domain.ValidContextName(result.Context) || !domain.ValidNamespaceName(result.Namespace)) ||
		result.RunActive != result.RunID.Valid() {
		return false
	}
	return true
}

// UIResourceSelectionResult accepts or rejects one request-bound ResourceRef.
type UIResourceSelectionResult struct {
	RequestID       uint64
	ScopeGeneration int64
	Resource        *domain.ResourceRef
	Cleared         bool
	Failure         UIQueryFailureCode
}

// Validate checks scope binding and exclusive selected, cleared, or failed state.
func (result UIResourceSelectionResult) Validate() error {
	if result.RequestID == 0 || result.ScopeGeneration < 1 {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" {
		if !result.Failure.validOperational() || result.Resource != nil || result.Cleared {
			return ErrInvalidUIEvent
		}
		return nil
	}
	if result.Cleared == (result.Resource != nil) {
		return ErrInvalidUIEvent
	}
	if result.Resource != nil && result.Resource.Validate() != nil {
		return ErrInvalidUIEvent
	}
	return nil
}

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
	// UIEventPersistenceDegraded is a visible nonterminal warning. It never
	// claims that incomplete data is resumable.
	UIEventPersistenceDegraded UIEventKind = "persistence_degraded"
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
	case UIEventTextDelta, UIEventRunCompleted, UIEventRunFailed, UIEventRunCancelled, UIEventPersistenceDegraded:
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

// RunObservationKind identifies fixed text-free lifecycle metadata admitted to
// the local logging observer.
type RunObservationKind string

const (
	RunObservationStarted             RunObservationKind = "started"
	RunObservationTerminal            RunObservationKind = "terminal"
	RunObservationPersistenceDegraded RunObservationKind = "persistence_degraded"
)

// RunObservation contains no user, model, Tool, Evidence, or Diagnosis text.
type RunObservation struct {
	Kind                RunObservationKind
	RunID               domain.AgentRunID
	ScopeGeneration     int64
	Status              domain.AgentRunStatus
	PersistenceDegraded bool
}

func (observation RunObservation) valid() bool {
	if !observation.RunID.Valid() || observation.ScopeGeneration < 1 {
		return false
	}
	switch observation.Kind {
	case RunObservationStarted:
		return observation.Status == domain.AgentRunStatusRunning && !observation.PersistenceDegraded
	case RunObservationTerminal:
		return observation.Status.Terminal()
	case RunObservationPersistenceDegraded:
		return observation.Status == domain.AgentRunStatusRunning && observation.PersistenceDegraded
	default:
		return false
	}
}

const uiDeltaFlushBytes = 4 * 1024

// eventBridge is the synchronous Application-to-delivery coalescing boundary.
// It owns no goroutine or channel, never drops structural events, and assigns a
// UI-local sequence so coalesced Agent deltas cannot create ambiguous ordering.
type eventBridge struct {
	runID           domain.AgentRunID
	scopeGeneration int64
	sink            UIEventSink
	sequence        int64
	started         bool
	terminal        bool
	pendingDelta    string
	diagnosis       string
}

func newEventBridge(runID domain.AgentRunID, scopeGeneration int64, sink UIEventSink) (*eventBridge, error) {
	if !runID.Valid() || scopeGeneration < 1 || sink == nil {
		return nil, ErrInvalidUIEvent
	}
	return &eventBridge{runID: runID, scopeGeneration: scopeGeneration, sink: sink}, nil
}

func (bridge *eventBridge) accept(ctx context.Context, event agent.RunEvent) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || event.Validate() != nil ||
		event.RunID != bridge.runID || event.ScopeGeneration != bridge.scopeGeneration || bridge.terminal {
		return ErrInvalidUIEvent
	}
	if event.Kind == agent.RunEventTextDelta {
		if !bridge.started {
			return ErrInvalidUIEvent
		}
		if len(bridge.pendingDelta)+len(event.TextDelta) > MaxQuestionBytes {
			if err := bridge.flushDelta(ctx); err != nil {
				return err
			}
		}
		bridge.pendingDelta += event.TextDelta
		if len(bridge.pendingDelta) >= uiDeltaFlushBytes {
			return bridge.flushDelta(ctx)
		}
		return nil
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	switch event.Kind {
	case agent.RunEventRunStarted:
		if bridge.started {
			return ErrInvalidUIEvent
		}
		bridge.started = true
		return bridge.emit(ctx, UIEvent{Kind: UIEventRunStarted})
	case agent.RunEventModelStreamStarted, agent.RunEventEvidenceCollected:
		return nil
	case agent.RunEventDiagnosisReady:
		bridge.diagnosis = event.Diagnosis.AnswerMarkdown
		return nil
	case agent.RunEventToolCallRequested, agent.RunEventToolCallStarted,
		agent.RunEventToolCallCompleted, agent.RunEventToolCallFailed, agent.RunEventToolCallDenied:
		step, err := projectToolStep(event)
		if err != nil {
			return err
		}
		return bridge.emit(ctx, UIEvent{Kind: UIEventToolStep, ToolStep: &step})
	case agent.RunEventRunCompleted:
		if bridge.diagnosis == "" {
			return ErrInvalidUIEvent
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunCompleted, Text: bridge.diagnosis})
	case agent.RunEventRunFailed:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: event.Failure.SafeMessage})
	case agent.RunEventRunCancelled:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunCancelled, Text: "The AgentRun was cancelled."})
	case agent.RunEventRunTimedOut:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The AgentRun reached its deadline."})
	case agent.RunEventRunStaleScope:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The AgentRun stopped because its Kubernetes scope changed."})
	case agent.RunEventRunInterrupted:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The AgentRun was interrupted."})
	default:
		return ErrInvalidUIEvent
	}
}

func (bridge *eventBridge) persistenceDegraded(ctx context.Context) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || !bridge.started || bridge.terminal {
		return ErrInvalidUIEvent
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	return bridge.emit(ctx, UIEvent{
		Kind: UIEventPersistenceDegraded,
		Text: "Local persistence is degraded; this run may not be resumable.",
	})
}

func (bridge *eventBridge) forceFailed(ctx context.Context, safeMessage string) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || bridge.terminal || safeMessage == "" {
		return ErrInvalidUIEvent
	}
	if !bridge.started {
		bridge.started = true
		if err := bridge.emit(ctx, UIEvent{Kind: UIEventRunStarted}); err != nil {
			return err
		}
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: safeMessage})
}

func (bridge *eventBridge) flushDelta(ctx context.Context) error {
	if bridge.pendingDelta == "" {
		return nil
	}
	value := bridge.pendingDelta
	if err := bridge.emit(ctx, UIEvent{Kind: UIEventTextDelta, Text: value}); err != nil {
		return err
	}
	bridge.pendingDelta = ""
	return nil
}

func (bridge *eventBridge) emitTerminal(ctx context.Context, event UIEvent) error {
	if err := bridge.emit(ctx, event); err != nil {
		return err
	}
	bridge.terminal = true
	return nil
}

func (bridge *eventBridge) emit(ctx context.Context, event UIEvent) error {
	nextSequence := bridge.sequence + 1
	event.RunID = bridge.runID
	event.ScopeGeneration = bridge.scopeGeneration
	event.Sequence = nextSequence
	if event.Validate() != nil {
		return ErrInvalidUIEvent
	}
	if err := bridge.sink.PublishUIEvent(ctx, event); err != nil {
		return err
	}
	bridge.sequence = nextSequence
	return nil
}

func projectToolStep(event agent.RunEvent) (ToolStep, error) {
	invocation := event.ToolInvocation
	if invocation == nil {
		return ToolStep{}, ErrInvalidUIEvent
	}
	step := ToolStep{
		InvocationID: invocation.ID,
		Name:         invocation.Name,
		Status:       ToolStepRequested,
		Truncated:    invocation.Truncated,
	}
	if invocation.Purpose != nil {
		step.Purpose = *invocation.Purpose
	}
	if invocation.ResultSummary != nil {
		step.Summary = *invocation.ResultSummary
	} else if invocation.SafeError != nil {
		step.Summary = *invocation.SafeError
	}
	switch event.Kind {
	case agent.RunEventToolCallRequested:
		step.Status = ToolStepRequested
	case agent.RunEventToolCallStarted:
		step.Status = ToolStepRunning
	case agent.RunEventToolCallCompleted:
		step.Status = ToolStepSucceeded
		if invocation.Truncated {
			step.Status = ToolStepPartial
		}
		if step.Summary == "" {
			step.Summary = "Tool collection completed."
		}
	case agent.RunEventToolCallDenied:
		step.Status = ToolStepDenied
		if step.Summary == "" {
			step.Summary = "The Tool call was denied safely."
		}
	case agent.RunEventToolCallFailed:
		step.Status = ToolStepFailed
		if invocation.Status == domain.ToolInvocationStatusCancelled {
			step.Status = ToolStepCancelled
		}
		if step.Summary == "" {
			step.Summary = "The Tool call failed safely."
		}
	default:
		return ToolStep{}, ErrInvalidUIEvent
	}
	step.EvidenceCount = invocation.EvidenceCount
	if !step.valid() {
		return ToolStep{}, ErrInvalidUIEvent
	}
	return step, nil
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
