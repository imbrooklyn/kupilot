package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	maxToolRequestDuration = 10 * time.Second
	maxLogCalls            = 2
)

var (
	// ErrInvalidRunBudget reports invalid limits, time, or accounting input.
	ErrInvalidRunBudget = errors.New("RunBudget data is invalid")
)

// RunBudgetLimits is frozen in RunInput. Every value may be tightened but may
// not exceed its code-defined maximum.
type RunBudgetLimits struct {
	RunDuration         time.Duration
	Steps               int
	ToolCalls           int
	ModelCalls          int
	ToolResultBytes     int
	RunToolResultBytes  int
	NoProgressSteps     int
	ModelRequestTimeout time.Duration
	ToolRequestTimeout  time.Duration
	LogCalls            int
}

// DefaultRunBudgetLimits returns the complete v0.1 hard-ceiling snapshot.
func DefaultRunBudgetLimits() RunBudgetLimits {
	return RunBudgetLimits{
		RunDuration:         domain.MaxAgentRunDuration,
		Steps:               domain.MaxAgentSteps,
		ToolCalls:           domain.MaxAgentToolCalls,
		ModelCalls:          domain.MaxAgentModelCalls,
		ToolResultBytes:     domain.MaxToolResultBytes,
		RunToolResultBytes:  domain.MaxAgentRunToolResultBytes,
		NoProgressSteps:     domain.MaxAgentNoProgressSteps,
		ModelRequestTimeout: domain.MaxModelRequestTimeout,
		ToolRequestTimeout:  maxToolRequestDuration,
		LogCalls:            maxLogCalls,
	}
}

// Validate rejects zero, negative, and expanding limits.
func (limits RunBudgetLimits) Validate() error {
	if limits.RunDuration <= 0 || limits.RunDuration > domain.MaxAgentRunDuration ||
		limits.Steps <= 0 || limits.Steps > domain.MaxAgentSteps ||
		limits.ToolCalls <= 0 || limits.ToolCalls > domain.MaxAgentToolCalls ||
		limits.ModelCalls <= 0 || limits.ModelCalls > domain.MaxAgentModelCalls ||
		limits.ToolResultBytes <= 0 || limits.ToolResultBytes > domain.MaxToolResultBytes ||
		limits.RunToolResultBytes <= 0 || limits.RunToolResultBytes > domain.MaxAgentRunToolResultBytes ||
		limits.NoProgressSteps <= 0 || limits.NoProgressSteps > domain.MaxAgentNoProgressSteps ||
		limits.ModelRequestTimeout <= 0 || limits.ModelRequestTimeout > domain.MaxModelRequestTimeout ||
		limits.ToolRequestTimeout <= 0 || limits.ToolRequestTimeout > maxToolRequestDuration ||
		limits.LogCalls <= 0 || limits.LogCalls > maxLogCalls {
		return ErrInvalidRunBudget
	}
	return nil
}

// RunStopReason is a code-defined terminal policy outcome.
type RunStopReason string

const (
	RunStopCompleted        RunStopReason = "completed"
	RunStopCancelled        RunStopReason = "cancelled"
	RunStopTimedOut         RunStopReason = "timed_out"
	RunStopStaleScope       RunStopReason = "stale_scope"
	RunStopFailed           RunStopReason = "failed"
	RunStopInterrupted      RunStopReason = "interrupted"
	RunStopStepLimit        RunStopReason = "step_limit"
	RunStopToolCallLimit    RunStopReason = "tool_call_limit"
	RunStopModelCallLimit   RunStopReason = "model_call_limit"
	RunStopToolResultBytes  RunStopReason = "tool_result_byte_limit"
	RunStopRepeatedToolCall RunStopReason = "repeated_tool_call"
	RunStopNoProgress       RunStopReason = "no_progress"
	RunStopLogCallLimit     RunStopReason = "log_call_limit"
	RunStopInvalidState     RunStopReason = "invalid_runtime_state"
)

// RunBudgetError exposes only a stable stop reason, class, and code-defined
// English message.
type RunBudgetError struct {
	reason  RunStopReason
	class   domain.SafeErrorClass
	message string
}

func (budgetError *RunBudgetError) Error() string {
	if budgetError == nil {
		return "The Agent run stopped safely."
	}
	return budgetError.message
}

// Is preserves Context cancellation and deadline behavior.
func (budgetError *RunBudgetError) Is(target error) bool {
	return budgetError != nil &&
		(budgetError.reason == RunStopCancelled && target == context.Canceled ||
			budgetError.reason == RunStopTimedOut && target == context.DeadlineExceeded)
}

// Reason returns the stable runtime stop reason.
func (budgetError *RunBudgetError) Reason() RunStopReason {
	if budgetError == nil {
		return RunStopInvalidState
	}
	return budgetError.reason
}

// Class returns the stable safe error class.
func (budgetError *RunBudgetError) Class() domain.SafeErrorClass {
	if budgetError == nil {
		return domain.SafeErrorClassInternal
	}
	return budgetError.class
}

// CallReservation is one atomic permission to start an external call.
type CallReservation struct {
	Timeout time.Duration
}

// ToolCallOutcome contains only runtime-observed accounting metadata. It is not
// model input and cannot request another call.
type ToolCallOutcome struct {
	ResultBytes int
	Retryable   bool
}

type repeatState struct {
	count         int
	lastRetryable bool
	pending       bool
}

// RunBudgetSnapshot is a read-only copy of current accounting state.
type RunBudgetSnapshot struct {
	Steps                 int
	ToolCalls             int
	ModelCalls            int
	ToolResultBytes       int
	LogCalls              int
	ConsecutiveNoProgress int
	Stopped               bool
	StopReason            RunStopReason
}

// RunBudget atomically owns all run-level call intentions and stop policy.
// It stores no Context and performs no I/O.
type RunBudget struct {
	mu sync.Mutex

	limits    RunBudgetLimits
	startedAt time.Time
	deadline  time.Time
	now       func() time.Time

	steps                 int
	stepPending           bool
	toolCalls             int
	modelCalls            int
	toolResultBytes       int
	logCalls              int
	consecutiveNoProgress int
	repeats               map[ToolCallIdentity]repeatState
	stopped               bool
	stopReason            RunStopReason
}

// NewRunBudget creates one concurrency-safe frozen budget with an injected UTC
// clock. The clock is consulted only while reserving or completing local state.
func NewRunBudget(limits RunBudgetLimits, startedAt time.Time, now func() time.Time) (*RunBudget, error) {
	if limits.Validate() != nil || startedAt.IsZero() || startedAt.Location() != time.UTC || now == nil {
		return nil, ErrInvalidRunBudget
	}
	current := now()
	if current.IsZero() || current.Location() != time.UTC || current.Before(startedAt) {
		return nil, ErrInvalidRunBudget
	}
	return &RunBudget{
		limits:    limits,
		startedAt: startedAt,
		deadline:  startedAt.Add(limits.RunDuration),
		now:       now,
		repeats:   make(map[ToolCallIdentity]repeatState),
	}, nil
}

// ReserveStep atomically admits one Agent loop step.
func (budget *RunBudget) ReserveStep(ctx context.Context) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if _, err := budget.activeLocked(ctx); err != nil {
		return err
	}
	if budget.stepPending {
		return budget.stopLocked(RunStopInvalidState)
	}
	if budget.steps >= budget.limits.Steps {
		return budget.stopLocked(RunStopStepLimit)
	}
	budget.steps++
	budget.stepPending = true
	return nil
}

// CompleteStep records only the number of newly accepted Evidence items. Two
// consecutive zero-Evidence steps seal the budget before another call intent.
func (budget *RunBudget) CompleteStep(newEvidence int) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if _, err := budget.activeLocked(context.Background()); err != nil {
		return err
	}
	if !budget.stepPending || newEvidence < 0 {
		return budget.stopLocked(RunStopInvalidState)
	}
	budget.stepPending = false
	if newEvidence > 0 {
		budget.consecutiveNoProgress = 0
		return nil
	}
	budget.consecutiveNoProgress++
	if budget.consecutiveNoProgress >= budget.limits.NoProgressSteps {
		return budget.stopLocked(RunStopNoProgress)
	}
	return nil
}

// ReserveModelCall atomically admits one call and returns its child timeout,
// capped by the remaining run duration.
func (budget *RunBudget) ReserveModelCall(ctx context.Context) (CallReservation, error) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	current, err := budget.activeLocked(ctx)
	if err != nil {
		return CallReservation{}, err
	}
	if budget.modelCalls >= budget.limits.ModelCalls {
		return CallReservation{}, budget.stopLocked(RunStopModelCallLimit)
	}
	budget.modelCalls++
	return budget.reservationLocked(current, budget.limits.ModelRequestTimeout), nil
}

// ReserveToolCall admits a model-selected call only when repetition state
// permits it. Model prose cannot request an explicit state revalidation.
func (budget *RunBudget) ReserveToolCall(ctx context.Context, call BoundToolCall) (CallReservation, error) {
	return budget.reserveToolCall(ctx, call, false)
}

// ReserveToolRevalidation admits the second identical call only as an explicit
// trusted runtime decision. It never accepts a model-supplied reason flag.
func (budget *RunBudget) ReserveToolRevalidation(ctx context.Context, call BoundToolCall) (CallReservation, error) {
	return budget.reserveToolCall(ctx, call, true)
}

func (budget *RunBudget) reserveToolCall(ctx context.Context, call BoundToolCall, revalidation bool) (CallReservation, error) {
	if call.Validate() != nil {
		return CallReservation{}, ErrInvalidRunBudget
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	current, err := budget.activeLocked(ctx)
	if err != nil {
		return CallReservation{}, err
	}
	identity := call.Identity()
	repeat := budget.repeats[identity]
	if repeat.pending {
		return CallReservation{}, budget.stopLocked(RunStopInvalidState)
	}
	if revalidation && repeat.count != 1 {
		return CallReservation{}, budget.stopLocked(RunStopInvalidState)
	}
	if repeat.count >= 2 || repeat.count == 1 && !repeat.lastRetryable && !revalidation {
		return CallReservation{}, budget.stopLocked(RunStopRepeatedToolCall)
	}
	if budget.toolCalls >= budget.limits.ToolCalls {
		return CallReservation{}, budget.stopLocked(RunStopToolCallLimit)
	}
	if call.Name() == domain.ToolNameGetPodLogs || call.Name() == domain.ToolNameGetPreviousPodLogs {
		if budget.logCalls >= budget.limits.LogCalls {
			return CallReservation{}, budget.stopLocked(RunStopLogCallLimit)
		}
		budget.logCalls++
	}
	budget.toolCalls++
	repeat.count++
	repeat.lastRetryable = false
	repeat.pending = true
	budget.repeats[identity] = repeat
	return budget.reservationLocked(current, budget.limits.ToolRequestTimeout), nil
}

// CompleteToolCall records one already-bounded result. Exceeding either byte
// ceiling seals the budget before the result can become model context.
func (budget *RunBudget) CompleteToolCall(call BoundToolCall, outcome ToolCallOutcome) error {
	if call.Validate() != nil {
		return ErrInvalidRunBudget
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if _, err := budget.activeLocked(context.Background()); err != nil {
		return err
	}
	identity := call.Identity()
	repeat, exists := budget.repeats[identity]
	if !exists || !repeat.pending || outcome.ResultBytes < 0 {
		return budget.stopLocked(RunStopInvalidState)
	}
	repeat.pending = false
	repeat.lastRetryable = outcome.Retryable
	budget.repeats[identity] = repeat
	if outcome.ResultBytes > budget.limits.ToolResultBytes ||
		outcome.ResultBytes > budget.limits.RunToolResultBytes-budget.toolResultBytes {
		return budget.stopLocked(RunStopToolResultBytes)
	}
	budget.toolResultBytes += outcome.ResultBytes
	return nil
}

// Terminate seals the budget exactly once for a non-budget terminal outcome.
func (budget *RunBudget) Terminate(reason RunStopReason) bool {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.stopped || !externalStopReason(reason) {
		return false
	}
	budget.stopped = true
	budget.stopReason = reason
	return true
}

// Snapshot returns a lock-protected accounting copy.
func (budget *RunBudget) Snapshot() RunBudgetSnapshot {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return RunBudgetSnapshot{
		Steps:                 budget.steps,
		ToolCalls:             budget.toolCalls,
		ModelCalls:            budget.modelCalls,
		ToolResultBytes:       budget.toolResultBytes,
		LogCalls:              budget.logCalls,
		ConsecutiveNoProgress: budget.consecutiveNoProgress,
		Stopped:               budget.stopped,
		StopReason:            budget.stopReason,
	}
}

func (budget *RunBudget) activeLocked(ctx context.Context) (time.Time, error) {
	if budget == nil || budget.now == nil {
		return time.Time{}, newRunBudgetError(RunStopInvalidState)
	}
	if budget.stopped {
		return time.Time{}, newRunBudgetError(budget.stopReason)
	}
	if ctx == nil {
		return time.Time{}, budget.stopLocked(RunStopInvalidState)
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return time.Time{}, budget.stopLocked(RunStopTimedOut)
		}
		return time.Time{}, budget.stopLocked(RunStopCancelled)
	}
	current := budget.now()
	if current.IsZero() || current.Location() != time.UTC || current.Before(budget.startedAt) {
		return time.Time{}, budget.stopLocked(RunStopInvalidState)
	}
	if !current.Before(budget.deadline) {
		return time.Time{}, budget.stopLocked(RunStopTimedOut)
	}
	return current, nil
}

func (budget *RunBudget) reservationLocked(current time.Time, maximum time.Duration) CallReservation {
	remaining := budget.deadline.Sub(current)
	if maximum < remaining {
		remaining = maximum
	}
	return CallReservation{Timeout: remaining}
}

func (budget *RunBudget) stopLocked(reason RunStopReason) error {
	if !budget.stopped {
		budget.stopped = true
		budget.stopReason = reason
	}
	return newRunBudgetError(budget.stopReason)
}

func newRunBudgetError(reason RunStopReason) *RunBudgetError {
	budgetError := &RunBudgetError{reason: reason, class: domain.SafeErrorClassBudgetExhausted}
	switch reason {
	case RunStopCancelled:
		budgetError.class = domain.SafeErrorClassCancelled
		budgetError.message = "The Agent run was cancelled before another call could start."
	case RunStopTimedOut:
		budgetError.class = domain.SafeErrorClassTimeout
		budgetError.message = "The Agent run reached its wall-clock deadline."
	case RunStopStaleScope:
		budgetError.class = domain.SafeErrorClassStaleScope
		budgetError.message = "The Agent run stopped because its ClusterScope is stale."
	case RunStopStepLimit:
		budgetError.message = "The Agent run reached its step limit."
	case RunStopToolCallLimit:
		budgetError.message = "The Agent run reached its Tool-call limit."
	case RunStopModelCallLimit:
		budgetError.message = "The Agent run reached its model-call limit."
	case RunStopToolResultBytes:
		budgetError.message = "The Agent run reached its ToolResult byte limit."
	case RunStopRepeatedToolCall:
		budgetError.class = domain.SafeErrorClassPolicyDenied
		budgetError.message = "The Agent run stopped a repeated Tool call."
	case RunStopNoProgress:
		budgetError.class = domain.SafeErrorClassPolicyDenied
		budgetError.message = "The Agent run reached its configured no-progress limit."
	case RunStopLogCallLimit:
		budgetError.message = "The Agent run reached its log-call limit."
	case RunStopCompleted:
		budgetError.class = domain.SafeErrorClassInternal
		budgetError.message = "The Agent run is already complete."
	case RunStopFailed, RunStopInterrupted, RunStopInvalidState:
		budgetError.class = domain.SafeErrorClassInternal
		budgetError.message = "The Agent run stopped safely."
	default:
		budgetError.reason = RunStopInvalidState
		budgetError.class = domain.SafeErrorClassInternal
		budgetError.message = "The Agent run stopped safely."
	}
	return budgetError
}

func externalStopReason(reason RunStopReason) bool {
	switch reason {
	case RunStopCompleted, RunStopCancelled, RunStopTimedOut, RunStopStaleScope, RunStopFailed, RunStopInterrupted:
		return true
	default:
		return false
	}
}
