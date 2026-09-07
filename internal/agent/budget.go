package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	maxToolRequestDuration = 180 * time.Second
	maxLogCalls            = 32
	maxMetricCalls         = 32
	maxDataSourceCalls     = 64
	maxRemoteExecCalls     = 16
	maxLocalProcessCalls   = 1
	maxSummaryCalls        = 4
	maxSummaryRequestTime  = 60 * time.Second
)

var (
	// ErrInvalidRunBudget reports invalid limits, time, or accounting input.
	ErrInvalidRunBudget = errors.New("RunBudget data is invalid")
)

// BudgetProfile identifies one code-defined latency and cost envelope. A
// profile is frozen into RunInput and cannot be changed by model output.
type BudgetProfile string

const (
	BudgetProfileCompact  BudgetProfile = "compact"
	BudgetProfileBalanced BudgetProfile = "balanced"
	BudgetProfileExtended BudgetProfile = "extended"
)

// Valid reports whether the profile is selectable by an operator.
func (profile BudgetProfile) Valid() bool {
	return profile == BudgetProfileCompact || profile == BudgetProfileBalanced || profile == BudgetProfileExtended
}

// RunBudgetLimits is frozen in RunInput. Every value may be tightened but may
// not exceed its code-defined maximum.
type RunBudgetLimits struct {
	Profile               BudgetProfile
	RunDuration           time.Duration
	Steps                 int
	ToolCalls             int
	ModelCalls            int
	ModelRequestBytes     int
	ModelStreamBytes      int
	ModelCostUnits        int
	SummaryCalls          int
	SummaryRequestBytes   int
	SummaryOutputBytes    int
	SummaryCostUnits      int
	ToolResultBytes       int
	RunToolResultBytes    int
	NoProgressSteps       int
	ModelRequestTimeout   time.Duration
	SummaryRequestTimeout time.Duration
	ToolRequestTimeout    time.Duration
	LogCalls              int
	LogContainers         int
	LogBytes              int
	EventPages            int
	EventPageItems        int
	EventPageBytes        int
	EventBytes            int
	MetricCalls           int
	MetricContainers      int
	MetricBytes           int
	DataSourceCalls       int
	DataSourcePages       int
	DataSourceSeries      int
	DataSourceSamples     int
	DataSourceLines       int
	DataSourceBytes       int
	DataSourceWindow      time.Duration
	DataSourceStep        time.Duration
	RemoteExecCalls       int
	LocalProcessCalls     int
	ResourcePages         int
	ResourcePageItems     int
	ResourcePageBytes     int
	ResourceScannedItems  int
	ResourceReturnedItems int
	ResourceBytes         int
}

// DefaultRunBudgetLimits returns the balanced operational profile.
func DefaultRunBudgetLimits() RunBudgetLimits {
	limits, _ := RunBudgetLimitsForProfile(BudgetProfileBalanced)
	return limits
}

// RunBudgetLimitsForProfile returns the exact immutable limits for one known
// profile. Unknown profiles fail before a run can perform external I/O.
func RunBudgetLimitsForProfile(profile BudgetProfile) (RunBudgetLimits, error) {
	limits := RunBudgetLimits{
		Profile: profile, ToolResultBytes: domain.MaxToolResultBytes,
		ModelRequestBytes:   domain.MaxModelRequestBytes,
		ModelStreamBytes:    domain.MaxModelStreamBytes,
		SummaryRequestBytes: domain.MaxModelRequestBytes,
		SummaryOutputBytes:  domain.MaxSessionSummaryBytes,
	}
	switch profile {
	case BudgetProfileCompact:
		limits.RunDuration = 10 * time.Minute
		limits.Steps = 12
		limits.ToolCalls = 16
		limits.ModelCalls = 6
		limits.ModelCostUnits = 6
		limits.SummaryCalls = 1
		limits.SummaryCostUnits = 1
		limits.SummaryRequestTimeout = 30 * time.Second
		limits.ModelRequestTimeout = 300 * time.Second
		limits.ToolRequestTimeout = 60 * time.Second
		limits.RunToolResultBytes = 1 * 1024 * 1024
		limits.LogCalls = 4
		limits.LogContainers = 4
		limits.LogBytes = 64 * 1024
		limits.EventPages = 2
		limits.EventPageItems = 25
		limits.EventPageBytes = 64 * 1024
		limits.EventBytes = 128 * 1024
		limits.MetricCalls = 4
		limits.MetricContainers = 20
		limits.MetricBytes = 128 * 1024
		limits.DataSourceCalls = 4
		limits.DataSourcePages = 2
		limits.DataSourceSeries = 10
		limits.DataSourceSamples = 100
		limits.DataSourceLines = 100
		limits.DataSourceBytes = 128 * 1024
		limits.DataSourceWindow = time.Hour
		limits.DataSourceStep = time.Minute
		limits.RemoteExecCalls = 2
		limits.LocalProcessCalls = 1
		limits.NoProgressSteps = 2
		limits.ResourcePages = 2
		limits.ResourcePageItems = 25
		limits.ResourcePageBytes = 128 * 1024
		limits.ResourceScannedItems = 50
		limits.ResourceReturnedItems = 25
		limits.ResourceBytes = 256 * 1024
	case BudgetProfileBalanced:
		limits.RunDuration = 30 * time.Minute
		limits.Steps = 32
		limits.ToolCalls = 48
		limits.ModelCalls = 16
		limits.ModelCostUnits = 16
		limits.SummaryCalls = 2
		limits.SummaryCostUnits = 2
		limits.SummaryRequestTimeout = 45 * time.Second
		limits.ModelRequestTimeout = 600 * time.Second
		limits.ToolRequestTimeout = 120 * time.Second
		limits.RunToolResultBytes = 4 * 1024 * 1024
		limits.LogCalls = 12
		limits.LogContainers = 8
		limits.LogBytes = 256 * 1024
		limits.EventPages = 4
		limits.EventPageItems = 50
		limits.EventPageBytes = 128 * 1024
		limits.EventBytes = 512 * 1024
		limits.MetricCalls = 12
		limits.MetricContainers = 35
		limits.MetricBytes = 256 * 1024
		limits.DataSourceCalls = 16
		limits.DataSourcePages = 4
		limits.DataSourceSeries = 25
		limits.DataSourceSamples = 400
		limits.DataSourceLines = 400
		limits.DataSourceBytes = 512 * 1024
		limits.DataSourceWindow = 6 * time.Hour
		limits.DataSourceStep = 5 * time.Minute
		limits.RemoteExecCalls = 4
		limits.LocalProcessCalls = 1
		limits.NoProgressSteps = 4
		limits.ResourcePages = 4
		limits.ResourcePageItems = 50
		limits.ResourcePageBytes = 256 * 1024
		limits.ResourceScannedItems = 200
		limits.ResourceReturnedItems = domain.MaxResourceSummaries
		limits.ResourceBytes = 1 * 1024 * 1024
	case BudgetProfileExtended:
		limits.RunDuration = 60 * time.Minute
		limits.Steps = 64
		limits.ToolCalls = 128
		limits.ModelCalls = 32
		limits.ModelCostUnits = 32
		limits.SummaryCalls = 4
		limits.SummaryCostUnits = 4
		limits.SummaryRequestTimeout = 60 * time.Second
		limits.ModelRequestTimeout = 900 * time.Second
		limits.ToolRequestTimeout = 180 * time.Second
		limits.RunToolResultBytes = 12 * 1024 * 1024
		limits.LogCalls = 32
		limits.LogContainers = 16
		limits.LogBytes = 1 * 1024 * 1024
		limits.EventPages = domain.MaxObservabilityPages
		limits.EventPageItems = 100
		limits.EventPageBytes = 256 * 1024
		limits.EventBytes = 2 * 1024 * 1024
		limits.MetricCalls = maxMetricCalls
		limits.MetricContainers = domain.MaxMetricContainers
		limits.MetricBytes = 1 * 1024 * 1024
		limits.DataSourceCalls = maxDataSourceCalls
		limits.DataSourcePages = domain.MaxObservabilityPages
		limits.DataSourceSeries = domain.MaxObservabilitySeries
		limits.DataSourceSamples = domain.MaxObservabilitySamples
		limits.DataSourceLines = domain.MaxObservabilityLines
		limits.DataSourceBytes = domain.MaxObservabilityBytes
		limits.DataSourceWindow = domain.MaxObservabilityWindow
		limits.DataSourceStep = domain.MaxObservabilityStep
		limits.RemoteExecCalls = 8
		limits.LocalProcessCalls = 1
		limits.NoProgressSteps = 6
		limits.ResourcePages = domain.MaxResourceQueryPages
		limits.ResourcePageItems = domain.MaxResourcePageItems
		limits.ResourcePageBytes = domain.MaxResourcePageBytes
		limits.ResourceScannedItems = domain.MaxResourceQueryItems
		limits.ResourceReturnedItems = domain.MaxResourceSummaries
		limits.ResourceBytes = domain.MaxResourceQueryBytes
	default:
		return RunBudgetLimits{}, ErrInvalidRunBudget
	}
	return limits, nil
}

// Validate rejects zero, negative, unknown-profile, profile-expanding, and
// hard-ceiling-expanding limits. Callers may tighten a selected profile.
func (limits RunBudgetLimits) Validate() error {
	profileLimits, err := RunBudgetLimitsForProfile(limits.Profile)
	if err != nil || limits.RunDuration <= 0 || limits.RunDuration > profileLimits.RunDuration || limits.RunDuration > domain.MaxAgentRunDuration ||
		limits.Steps <= 0 || limits.Steps > profileLimits.Steps || limits.Steps > domain.MaxAgentSteps ||
		limits.ToolCalls <= 0 || limits.ToolCalls > profileLimits.ToolCalls || limits.ToolCalls > domain.MaxAgentToolCalls ||
		limits.ModelCalls <= 0 || limits.ModelCalls > profileLimits.ModelCalls || limits.ModelCalls > domain.MaxAgentModelCalls ||
		limits.ModelRequestBytes <= 0 || limits.ModelRequestBytes > profileLimits.ModelRequestBytes || limits.ModelRequestBytes > domain.MaxModelRequestBytes ||
		limits.ModelStreamBytes <= 0 || limits.ModelStreamBytes > profileLimits.ModelStreamBytes || limits.ModelStreamBytes > domain.MaxModelStreamBytes ||
		limits.ModelCostUnits <= 0 || limits.ModelCostUnits > profileLimits.ModelCostUnits ||
		limits.SummaryCalls <= 0 || limits.SummaryCalls > profileLimits.SummaryCalls || limits.SummaryCalls > maxSummaryCalls ||
		limits.SummaryRequestBytes <= 0 || limits.SummaryRequestBytes > profileLimits.SummaryRequestBytes || limits.SummaryRequestBytes > domain.MaxModelRequestBytes ||
		limits.SummaryOutputBytes <= 0 || limits.SummaryOutputBytes > profileLimits.SummaryOutputBytes || limits.SummaryOutputBytes > domain.MaxSessionSummaryBytes ||
		limits.SummaryCostUnits <= 0 || limits.SummaryCostUnits > profileLimits.SummaryCostUnits ||
		limits.ToolResultBytes <= 0 || limits.ToolResultBytes > profileLimits.ToolResultBytes || limits.ToolResultBytes > domain.MaxToolResultBytes ||
		limits.RunToolResultBytes <= 0 || limits.RunToolResultBytes > profileLimits.RunToolResultBytes || limits.RunToolResultBytes > domain.MaxAgentRunToolResultBytes ||
		limits.NoProgressSteps <= 0 || limits.NoProgressSteps > profileLimits.NoProgressSteps || limits.NoProgressSteps > domain.MaxAgentNoProgressSteps ||
		limits.ModelRequestTimeout <= 0 || limits.ModelRequestTimeout > profileLimits.ModelRequestTimeout || limits.ModelRequestTimeout > domain.MaxModelRequestTimeout ||
		limits.SummaryRequestTimeout <= 0 || limits.SummaryRequestTimeout > profileLimits.SummaryRequestTimeout || limits.SummaryRequestTimeout > maxSummaryRequestTime ||
		limits.ToolRequestTimeout <= 0 || limits.ToolRequestTimeout > profileLimits.ToolRequestTimeout || limits.ToolRequestTimeout > maxToolRequestDuration ||
		limits.LogCalls <= 0 || limits.LogCalls > profileLimits.LogCalls || limits.LogCalls > maxLogCalls ||
		limits.LogContainers <= 0 || limits.LogContainers > profileLimits.LogContainers || limits.LogContainers > domain.MaxObservabilityLogContainers ||
		limits.LogBytes <= 0 || limits.LogBytes > profileLimits.LogBytes || limits.LogBytes > domain.MaxObservabilityBytes ||
		limits.EventPages <= 0 || limits.EventPages > profileLimits.EventPages || limits.EventPages > domain.MaxObservabilityPages ||
		limits.EventPageItems <= 0 || limits.EventPageItems > profileLimits.EventPageItems || limits.EventPageItems > 100 ||
		limits.EventPageBytes <= 0 || limits.EventPageBytes > profileLimits.EventPageBytes || limits.EventPageBytes > domain.MaxObservabilityBytes ||
		limits.EventBytes <= 0 || limits.EventBytes > profileLimits.EventBytes || limits.EventBytes > domain.MaxObservabilityBytes ||
		limits.EventPageBytes > limits.EventBytes ||
		limits.MetricCalls <= 0 || limits.MetricCalls > profileLimits.MetricCalls || limits.MetricCalls > maxMetricCalls ||
		limits.MetricContainers <= 0 || limits.MetricContainers > profileLimits.MetricContainers || limits.MetricContainers > domain.MaxMetricContainers ||
		limits.MetricBytes <= 0 || limits.MetricBytes > profileLimits.MetricBytes || limits.MetricBytes > domain.MaxObservabilityBytes ||
		limits.DataSourceCalls <= 0 || limits.DataSourceCalls > profileLimits.DataSourceCalls || limits.DataSourceCalls > maxDataSourceCalls ||
		limits.DataSourcePages <= 0 || limits.DataSourcePages > profileLimits.DataSourcePages || limits.DataSourcePages > domain.MaxObservabilityPages ||
		limits.DataSourceSeries <= 0 || limits.DataSourceSeries > profileLimits.DataSourceSeries || limits.DataSourceSeries > domain.MaxObservabilitySeries ||
		limits.DataSourceSamples <= 0 || limits.DataSourceSamples > profileLimits.DataSourceSamples || limits.DataSourceSamples > domain.MaxObservabilitySamples ||
		limits.DataSourceLines <= 0 || limits.DataSourceLines > profileLimits.DataSourceLines || limits.DataSourceLines > domain.MaxObservabilityLines ||
		limits.DataSourceBytes <= 0 || limits.DataSourceBytes > profileLimits.DataSourceBytes || limits.DataSourceBytes > domain.MaxObservabilityBytes ||
		limits.DataSourceWindow <= 0 || limits.DataSourceWindow > profileLimits.DataSourceWindow || limits.DataSourceWindow > domain.MaxObservabilityWindow ||
		limits.DataSourceStep <= 0 || limits.DataSourceStep > profileLimits.DataSourceStep || limits.DataSourceStep > domain.MaxObservabilityStep ||
		limits.RemoteExecCalls <= 0 || limits.RemoteExecCalls > profileLimits.RemoteExecCalls || limits.RemoteExecCalls > maxRemoteExecCalls ||
		limits.LocalProcessCalls <= 0 || limits.LocalProcessCalls > profileLimits.LocalProcessCalls || limits.LocalProcessCalls > maxLocalProcessCalls ||
		limits.ResourcePages <= 0 || limits.ResourcePages > profileLimits.ResourcePages || limits.ResourcePages > domain.MaxResourceQueryPages ||
		limits.ResourcePageItems <= 0 || limits.ResourcePageItems > profileLimits.ResourcePageItems || limits.ResourcePageItems > domain.MaxResourcePageItems ||
		limits.ResourcePageBytes <= 0 || limits.ResourcePageBytes > profileLimits.ResourcePageBytes || limits.ResourcePageBytes > domain.MaxResourcePageBytes ||
		limits.ResourceScannedItems <= 0 || limits.ResourceScannedItems > profileLimits.ResourceScannedItems || limits.ResourceScannedItems > domain.MaxResourceQueryItems ||
		limits.ResourceReturnedItems <= 0 || limits.ResourceReturnedItems > profileLimits.ResourceReturnedItems || limits.ResourceReturnedItems > domain.MaxResourceSummaries ||
		limits.ResourceBytes <= 0 || limits.ResourceBytes > profileLimits.ResourceBytes || limits.ResourceBytes > domain.MaxResourceQueryBytes ||
		limits.ResourcePageItems > limits.ResourceScannedItems || limits.ResourcePageBytes > limits.ResourceBytes || limits.ResourceReturnedItems > limits.ResourceScannedItems {
		return ErrInvalidRunBudget
	}
	return nil
}

// RunStopReason is a code-defined terminal policy outcome.
type RunStopReason string

const (
	RunStopCompleted         RunStopReason = "completed"
	RunStopCancelled         RunStopReason = "cancelled"
	RunStopTimedOut          RunStopReason = "timed_out"
	RunStopStaleScope        RunStopReason = "stale_scope"
	RunStopFailed            RunStopReason = "failed"
	RunStopInterrupted       RunStopReason = "interrupted"
	RunStopStepLimit         RunStopReason = "step_limit"
	RunStopToolCallLimit     RunStopReason = "tool_call_limit"
	RunStopModelCallLimit    RunStopReason = "model_call_limit"
	RunStopModelCostLimit    RunStopReason = "model_cost_limit"
	RunStopSummaryCallLimit  RunStopReason = "summary_call_limit"
	RunStopSummaryCostLimit  RunStopReason = "summary_cost_limit"
	RunStopReviewerCallLimit RunStopReason = "reviewer_call_limit"
	RunStopReviewerCostLimit RunStopReason = "reviewer_cost_limit"
	RunStopToolResultBytes   RunStopReason = "tool_result_byte_limit"
	RunStopRepeatedToolCall  RunStopReason = "repeated_tool_call"
	RunStopNoProgress        RunStopReason = "no_progress"
	RunStopLogCallLimit      RunStopReason = "log_call_limit"
	RunStopMetricCallLimit   RunStopReason = "metric_call_limit"
	RunStopDataSourceLimit   RunStopReason = "data_source_call_limit"
	RunStopRemoteExecLimit   RunStopReason = "remote_exec_call_limit"
	RunStopInvalidState      RunStopReason = "invalid_runtime_state"
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
		return "The diagnostic run stopped safely."
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
	Timeout      time.Duration
	RequestBytes int
	OutputBytes  int
	StreamBytes  int
	CostUnits    int
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
	Profile               BudgetProfile
	Limits                RunBudgetLimits
	StartedAt             time.Time
	Deadline              time.Time
	CapturedAt            time.Time
	Elapsed               time.Duration
	Remaining             time.Duration
	Steps                 int
	ToolCalls             int
	ModelCalls            int
	ModelCostUnits        int
	SummaryCalls          int
	SummaryCostUnits      int
	ToolResultBytes       int
	LogCalls              int
	MetricCalls           int
	DataSourceCalls       int
	RemoteExecCalls       int
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
	modelCostUnits        int
	summaryCalls          int
	summaryCostUnits      int
	toolResultBytes       int
	logCalls              int
	metricCalls           int
	dataSourceCalls       int
	remoteExecCalls       int
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

// CompleteStep records only the number of newly accepted Evidence items. The
// profile-defined number of consecutive zero-Evidence steps seals the budget
// before another call intent.
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
	if budget.modelCostUnits >= budget.limits.ModelCostUnits {
		return CallReservation{}, budget.stopLocked(RunStopModelCostLimit)
	}
	budget.modelCalls++
	budget.modelCostUnits++
	reservation := budget.reservationLocked(current, budget.limits.ModelRequestTimeout)
	reservation.RequestBytes = budget.limits.ModelRequestBytes
	reservation.OutputBytes = domain.MaxModelMessageBytes
	reservation.StreamBytes = budget.limits.ModelStreamBytes
	reservation.CostUnits = 1
	return reservation, nil
}

// ReserveSummaryCall atomically admits one independent Agent-profile summary
// request. It never consumes the main investigation model-call allowance.
func (budget *RunBudget) ReserveSummaryCall(ctx context.Context) (CallReservation, error) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	current, err := budget.activeLocked(ctx)
	if err != nil {
		return CallReservation{}, err
	}
	if budget.summaryCalls >= budget.limits.SummaryCalls {
		return CallReservation{}, budget.stopLocked(RunStopSummaryCallLimit)
	}
	if budget.summaryCostUnits >= budget.limits.SummaryCostUnits {
		return CallReservation{}, budget.stopLocked(RunStopSummaryCostLimit)
	}
	budget.summaryCalls++
	budget.summaryCostUnits++
	reservation := budget.reservationLocked(current, budget.limits.SummaryRequestTimeout)
	reservation.RequestBytes = budget.limits.SummaryRequestBytes
	reservation.OutputBytes = budget.limits.SummaryOutputBytes
	reservation.CostUnits = 1
	return reservation, nil
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

// RecordSafeReadReuse accounts for one logical Tool result selected by the
// trusted same-run reuse policy. It reserves no external call and does not
// alter retry state; the adapter must have independently validated the cache
// identity, freshness, and accepted Evidence linkage.
func (budget *RunBudget) RecordSafeReadReuse(ctx context.Context, call BoundToolCall, resultBytes int) error {
	if call.Validate() != nil || resultBytes < 0 {
		return ErrInvalidRunBudget
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if _, err := budget.activeLocked(ctx); err != nil {
		return err
	}
	if budget.toolCalls >= budget.limits.ToolCalls {
		return budget.stopLocked(RunStopToolCallLimit)
	}
	if resultBytes > budget.limits.ToolResultBytes ||
		resultBytes > budget.limits.RunToolResultBytes-budget.toolResultBytes {
		return budget.stopLocked(RunStopToolResultBytes)
	}
	budget.toolCalls++
	budget.toolResultBytes += resultBytes
	return nil
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
		if call.ExternalCallCost() < 1 || call.ExternalCallCost() > budget.limits.LogCalls-budget.logCalls {
			return CallReservation{}, budget.stopLocked(RunStopLogCallLimit)
		}
		budget.logCalls += call.ExternalCallCost()
	}
	if call.Name() == domain.ToolNameGetPodMetrics || call.Name() == domain.ToolNameGetNodeMetrics {
		if call.ExternalCallCost() < 1 || call.ExternalCallCost() > budget.limits.MetricCalls-budget.metricCalls {
			return CallReservation{}, budget.stopLocked(RunStopMetricCallLimit)
		}
		budget.metricCalls += call.ExternalCallCost()
	}
	if call.Name() == domain.ToolNameQueryPrometheus || call.Name() == domain.ToolNameQueryLoki {
		if call.ExternalCallCost() < 1 || call.ExternalCallCost() > budget.limits.DataSourceCalls-budget.dataSourceCalls {
			return CallReservation{}, budget.stopLocked(RunStopDataSourceLimit)
		}
		budget.dataSourceCalls += call.ExternalCallCost()
	}
	if call.Name() == domain.ToolNamePodExec || call.Name() == domain.ToolNameReadContainerFile || call.Name() == domain.ToolNameRunDiagnosticPod {
		if call.ExternalCallCost() < 1 || call.ExternalCallCost() > budget.limits.RemoteExecCalls-budget.remoteExecCalls {
			return CallReservation{}, budget.stopLocked(RunStopRemoteExecLimit)
		}
		budget.remoteExecCalls += call.ExternalCallCost()
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
	capturedAt := budget.now()
	elapsed := capturedAt.Sub(budget.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	remaining := budget.deadline.Sub(capturedAt)
	if remaining < 0 {
		remaining = 0
	}
	return RunBudgetSnapshot{
		Profile:               budget.limits.Profile,
		Limits:                budget.limits,
		StartedAt:             budget.startedAt,
		Deadline:              budget.deadline,
		CapturedAt:            capturedAt,
		Elapsed:               elapsed,
		Remaining:             remaining,
		Steps:                 budget.steps,
		ToolCalls:             budget.toolCalls,
		ModelCalls:            budget.modelCalls,
		ModelCostUnits:        budget.modelCostUnits,
		SummaryCalls:          budget.summaryCalls,
		SummaryCostUnits:      budget.summaryCostUnits,
		ToolResultBytes:       budget.toolResultBytes,
		LogCalls:              budget.logCalls,
		MetricCalls:           budget.metricCalls,
		DataSourceCalls:       budget.dataSourceCalls,
		RemoteExecCalls:       budget.remoteExecCalls,
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
		budgetError.message = "The diagnostic run was cancelled before another request could start."
	case RunStopTimedOut:
		budgetError.class = domain.SafeErrorClassTimeout
		budgetError.message = "The diagnostic run reached its time limit."
	case RunStopStaleScope:
		budgetError.class = domain.SafeErrorClassStaleScope
		budgetError.message = "The diagnostic run stopped because the Kubernetes context or namespace changed."
	case RunStopStepLimit:
		budgetError.message = "The diagnostic run reached its reasoning-step limit."
	case RunStopToolCallLimit:
		budgetError.message = "The diagnostic run reached its cluster-read limit."
	case RunStopModelCallLimit:
		budgetError.message = "The diagnostic run reached its model-request limit."
	case RunStopModelCostLimit:
		budgetError.message = "The diagnostic run reached its model cost-reservation limit."
	case RunStopSummaryCallLimit:
		budgetError.message = "The diagnostic run reached its conversation-summary request limit."
	case RunStopSummaryCostLimit:
		budgetError.message = "The diagnostic run reached its conversation-summary cost-reservation limit."
	case RunStopReviewerCallLimit:
		budgetError.message = "The approval reviewer reached its request limit."
	case RunStopReviewerCostLimit:
		budgetError.message = "The approval reviewer reached its cost-reservation limit."
	case RunStopToolResultBytes:
		budgetError.message = "The diagnostic run reached its collected-data size limit."
	case RunStopRepeatedToolCall:
		budgetError.class = domain.SafeErrorClassPolicyDenied
		budgetError.message = "The diagnostic run stopped after a repeated cluster read."
	case RunStopNoProgress:
		budgetError.class = domain.SafeErrorClassPolicyDenied
		budgetError.message = "The diagnostic run stopped after repeated steps made no progress."
	case RunStopLogCallLimit:
		budgetError.message = "The diagnostic run reached its log-read limit."
	case RunStopMetricCallLimit:
		budgetError.message = "The diagnostic run reached its Kubernetes metrics-read limit."
	case RunStopDataSourceLimit:
		budgetError.message = "The diagnostic run reached its observability data-source request limit."
	case RunStopRemoteExecLimit:
		budgetError.message = "The diagnostic run reached its remote-execution request limit."
	case RunStopCompleted:
		budgetError.class = domain.SafeErrorClassInternal
		budgetError.message = "The diagnostic run is already complete."
	case RunStopFailed, RunStopInterrupted, RunStopInvalidState:
		budgetError.class = domain.SafeErrorClassInternal
		budgetError.message = "The diagnostic run stopped safely."
	default:
		budgetError.reason = RunStopInvalidState
		budgetError.class = domain.SafeErrorClassInternal
		budgetError.message = "The diagnostic run stopped safely."
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
