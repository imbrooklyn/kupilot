package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// MaxRunEvents covers the hard Tool/Evidence/model budget while retaining an
// independent finite ceiling on the internal ordered stream.
const MaxRunEvents = 32 * 1024

var (
	// ErrInvalidRunEvent reports an invalid neutral event without echoing data.
	ErrInvalidRunEvent = errors.New("RunEvent data is invalid")
	// ErrRunEventTerminal reports an event attempted after terminal publication.
	ErrRunEventTerminal = errors.New("the Agent run event stream is terminal")
)

// RunEventKind identifies one Eino-neutral ordered Agent event.
type RunEventKind string

const (
	RunEventRunStarted         RunEventKind = "run_started"
	RunEventModelStreamStarted RunEventKind = "model_stream_started"
	RunEventTextDelta          RunEventKind = "text_delta"
	RunEventToolCallRequested  RunEventKind = "tool_call_requested"
	RunEventToolCallStarted    RunEventKind = "tool_call_started"
	RunEventToolCallCompleted  RunEventKind = "tool_call_completed"
	RunEventToolCallFailed     RunEventKind = "tool_call_failed"
	RunEventToolCallDenied     RunEventKind = "tool_call_denied"
	RunEventEvidenceCollected  RunEventKind = "evidence_collected"
	RunEventDiagnosisReady     RunEventKind = "diagnosis_ready"
	RunEventRunCompleted       RunEventKind = "run_completed"
	RunEventRunFailed          RunEventKind = "run_failed"
	RunEventRunCancelled       RunEventKind = "run_cancelled"
	RunEventRunTimedOut        RunEventKind = "run_timed_out"
	RunEventRunStaleScope      RunEventKind = "run_stale_scope"
	RunEventRunInterrupted     RunEventKind = "run_interrupted"
)

// RunEventFailure is a stable safe terminal failure projection.
type RunEventFailure struct {
	Class       domain.SafeErrorClass
	SafeMessage string
}

// RunTerminationReason is a code-defined non-failure terminal reason.
type RunTerminationReason string

const (
	RunTerminationUserCancelled  RunTerminationReason = "user_cancelled"
	RunTerminationOwnerCancelled RunTerminationReason = "owner_cancelled"
	RunTerminationDeadline       RunTerminationReason = "run_deadline_exceeded"
	RunTerminationScopeChanged   RunTerminationReason = "scope_changed"
	RunTerminationInterrupted    RunTerminationReason = "process_interrupted"
)

func (failure RunEventFailure) valid() bool {
	return failure.Class.Valid() && domain.ValidModelText(failure.SafeMessage, domain.MaxModelInputMessageBytes, false)
}

// RunEvent contains one typed payload plus publisher-owned ordering metadata.
// It contains no framework callback, raw ToolResult, or vendor error.
type RunEvent struct {
	RunID             domain.AgentRunID
	ScopeGeneration   int64
	Sequence          int64
	OccurredAt        time.Time
	Kind              RunEventKind
	ModelRequestID    *domain.ModelRequestID
	TextDelta         string
	ToolInvocation    *domain.ToolInvocation
	Evidence          *domain.Evidence
	Diagnosis         *domain.Diagnosis
	Failure           *RunEventFailure
	TerminationReason RunTerminationReason
}

// Terminal reports whether this event is one of the sole terminal kinds.
func (event RunEvent) Terminal() bool {
	switch event.Kind {
	case RunEventRunCompleted, RunEventRunFailed, RunEventRunCancelled,
		RunEventRunTimedOut, RunEventRunStaleScope, RunEventRunInterrupted:
		return true
	default:
		return false
	}
}

// Validate checks payload exclusivity, provenance, bounds, and UTC metadata.
func (event RunEvent) Validate() error {
	if !event.RunID.Valid() || event.ScopeGeneration < 1 || event.Sequence < 1 || event.Sequence > MaxRunEvents ||
		event.OccurredAt.IsZero() || event.OccurredAt.Location() != time.UTC {
		return ErrInvalidRunEvent
	}
	payloads := 0
	if event.ModelRequestID != nil {
		payloads++
	}
	if event.TextDelta != "" {
		payloads++
	}
	if event.ToolInvocation != nil {
		payloads++
	}
	if event.Evidence != nil {
		payloads++
	}
	if event.Diagnosis != nil {
		payloads++
	}
	if event.Failure != nil {
		payloads++
	}
	if event.TerminationReason != "" {
		payloads++
	}
	switch event.Kind {
	case RunEventRunStarted, RunEventRunCompleted:
		if payloads != 0 {
			return ErrInvalidRunEvent
		}
	case RunEventModelStreamStarted:
		if payloads != 1 || event.ModelRequestID == nil || !event.ModelRequestID.Valid() {
			return ErrInvalidRunEvent
		}
	case RunEventTextDelta:
		if payloads != 1 || !domain.ValidModelText(event.TextDelta, domain.MaxModelMessageBytes, false) {
			return ErrInvalidRunEvent
		}
	case RunEventToolCallRequested, RunEventToolCallStarted, RunEventToolCallCompleted, RunEventToolCallFailed, RunEventToolCallDenied:
		if payloads != 1 || !event.validToolInvocation() {
			return ErrInvalidRunEvent
		}
	case RunEventEvidenceCollected:
		if payloads != 1 || event.Evidence == nil || event.Evidence.Validate() != nil ||
			event.Evidence.RunID != event.RunID || event.Evidence.Scope.Generation != event.ScopeGeneration {
			return ErrInvalidRunEvent
		}
	case RunEventDiagnosisReady:
		if payloads != 1 || event.Diagnosis == nil || event.Diagnosis.Validate() != nil ||
			event.Diagnosis.RunID != event.RunID || event.Diagnosis.Scope.Generation != event.ScopeGeneration {
			return ErrInvalidRunEvent
		}
	case RunEventRunFailed:
		if payloads != 1 || event.Failure == nil || !event.Failure.valid() {
			return ErrInvalidRunEvent
		}
	case RunEventRunCancelled, RunEventRunTimedOut, RunEventRunStaleScope, RunEventRunInterrupted:
		if payloads != 1 || !event.validTerminationReason() {
			return ErrInvalidRunEvent
		}
	default:
		return ErrInvalidRunEvent
	}
	return nil
}

func (event RunEvent) validTerminationReason() bool {
	switch event.Kind {
	case RunEventRunCancelled:
		return event.TerminationReason == RunTerminationUserCancelled || event.TerminationReason == RunTerminationOwnerCancelled
	case RunEventRunTimedOut:
		return event.TerminationReason == RunTerminationDeadline
	case RunEventRunStaleScope:
		return event.TerminationReason == RunTerminationScopeChanged
	case RunEventRunInterrupted:
		return event.TerminationReason == RunTerminationInterrupted
	default:
		return false
	}
}

func (event RunEvent) validToolInvocation() bool {
	if event.ToolInvocation == nil || event.ToolInvocation.Validate() != nil ||
		event.ToolInvocation.RunID != event.RunID || event.ToolInvocation.Scope.Generation != event.ScopeGeneration {
		return false
	}
	switch event.Kind {
	case RunEventToolCallRequested:
		return event.ToolInvocation.Status == domain.ToolInvocationStatusRequested
	case RunEventToolCallStarted:
		return event.ToolInvocation.Status == domain.ToolInvocationStatusRunning
	case RunEventToolCallCompleted:
		return event.ToolInvocation.Status == domain.ToolInvocationStatusSucceeded
	case RunEventToolCallFailed:
		return event.ToolInvocation.Status == domain.ToolInvocationStatusFailed ||
			event.ToolInvocation.Status == domain.ToolInvocationStatusCancelled
	case RunEventToolCallDenied:
		return event.ToolInvocation.Status == domain.ToolInvocationStatusDenied
	default:
		return false
	}
}

// EventSinkResult makes backpressure acceptance explicit without exposing a raw
// repository or delivery error.
type EventSinkResult string

const (
	EventSinkAccepted EventSinkResult = "accepted"
	EventSinkDegraded EventSinkResult = "degraded"
	EventSinkRejected EventSinkResult = "rejected"
)

func (result EventSinkResult) valid() bool {
	return result == EventSinkAccepted || result == EventSinkDegraded || result == EventSinkRejected
}

// EventSink synchronously accepts ordered neutral events. The caller owns the
// stream lifetime and must not drop structural or terminal events.
type EventSink interface {
	Publish(context.Context, RunEvent) EventSinkResult
}

// EventSinkFunc adapts one function to the narrow EventSink port.
type EventSinkFunc func(context.Context, RunEvent) EventSinkResult

func (function EventSinkFunc) Publish(ctx context.Context, event RunEvent) EventSinkResult {
	return function(ctx, event)
}

// EventPublisher is the sole sequence and terminal owner for one run stream.
type EventPublisher struct {
	mu sync.Mutex

	runID           domain.AgentRunID
	scopeGeneration int64
	now             func() time.Time
	sink            EventSink
	sequence        int64
	started         bool
	diagnosisReady  bool
	publishing      bool
	terminal        bool
}

// NewEventPublisher validates the immutable event identity and dependencies.
func NewEventPublisher(runID domain.AgentRunID, scopeGeneration int64, now func() time.Time, sink EventSink) (*EventPublisher, error) {
	if !runID.Valid() || scopeGeneration < 1 || now == nil || sink == nil {
		return nil, ErrInvalidRunEvent
	}
	current := now()
	if current.IsZero() || current.Location() != time.UTC {
		return nil, ErrInvalidRunEvent
	}
	return &EventPublisher{runID: runID, scopeGeneration: scopeGeneration, now: now, sink: sink}, nil
}

// Publish stamps one payload, validates it before the sink, and seals before
// delivering the first terminal event so a reentrant or late event cannot win.
func (publisher *EventPublisher) Publish(ctx context.Context, payload RunEvent) (EventSinkResult, error) {
	if publisher == nil || ctx == nil {
		return "", ErrInvalidRunEvent
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	publisher.mu.Lock()
	if publisher.terminal {
		publisher.mu.Unlock()
		return "", ErrRunEventTerminal
	}
	if publisher.publishing {
		publisher.mu.Unlock()
		return "", ErrInvalidRunEvent
	}
	if payload.RunID != "" || payload.ScopeGeneration != 0 || payload.Sequence != 0 || !payload.OccurredAt.IsZero() {
		publisher.mu.Unlock()
		return "", ErrInvalidRunEvent
	}
	event := payload
	event.RunID = publisher.runID
	event.ScopeGeneration = publisher.scopeGeneration
	event.Sequence = publisher.sequence + 1
	event.OccurredAt = publisher.now()
	if event.Validate() != nil {
		publisher.mu.Unlock()
		return "", ErrInvalidRunEvent
	}
	if !publisher.started && event.Kind != RunEventRunStarted || publisher.started && event.Kind == RunEventRunStarted ||
		event.Kind == RunEventDiagnosisReady && publisher.diagnosisReady ||
		event.Kind == RunEventRunCompleted && !publisher.diagnosisReady {
		publisher.mu.Unlock()
		return "", ErrInvalidRunEvent
	}
	publisher.sequence = event.Sequence
	if event.Kind == RunEventRunStarted {
		publisher.started = true
	}
	if event.Kind == RunEventDiagnosisReady {
		publisher.diagnosisReady = true
	}
	if event.Terminal() {
		publisher.terminal = true
	}
	publisher.publishing = true
	publisher.mu.Unlock()

	result := publisher.sink.Publish(ctx, event)
	publisher.mu.Lock()
	publisher.publishing = false
	publisher.mu.Unlock()
	if !result.valid() {
		return "", ErrInvalidRunEvent
	}
	return result, nil
}

// Terminal reports whether the first terminal event has been published.
func (publisher *EventPublisher) Terminal() bool {
	if publisher == nil {
		return true
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return publisher.terminal
}
