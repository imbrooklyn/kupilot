// Package einoadapter contains the sole Eino translation boundary for the
// production single-Agent runtime.
package einoadapter

import (
	"context"
	"errors"
	"sync"
	"time"

	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Config contains only neutral dependencies and run-local policy services.
// It contains no provider client, transport, repository, or global callback.
type Config struct {
	Model       agent.Model
	Tools       agent.ToolHandlers
	ScopeGuard  agent.RunScopeGuard
	Identifiers agent.RunIdentifierSource
	Now         func() time.Time
}

// Adapter runs one bounded ReAct composition at a time. All policy-bearing
// state is created per Run call and never stored in Eino framework state.
type Adapter struct {
	model       agent.Model
	tools       agent.ToolHandlers
	scopeGuard  agent.RunScopeGuard
	identifiers agent.RunIdentifierSource
	now         func() time.Time

	lifecycleMu sync.Mutex
	activeRuns  sync.WaitGroup
	closed      bool
}

var _ agent.AgentRunner = (*Adapter)(nil)

// New constructs the production single-Agent adapter without performing model
// or Tool I/O.
func New(config Config) (*Adapter, error) {
	if config.Model == nil || config.ScopeGuard == nil || config.Identifiers == nil || config.Now == nil ||
		config.Tools.Validate() != nil || !validRuntimeTime(config.Now()) {
		return nil, ErrInvalidConfiguration
	}
	return &Adapter{
		model:       config.Model,
		tools:       config.Tools,
		scopeGuard:  config.ScopeGuard,
		identifiers: config.Identifiers,
		now:         config.Now,
	}, nil
}

// Run executes one immutable single-Agent composition and returns exactly one
// neutral terminal outcome.
func (adapter *Adapter) Run(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
	if adapter == nil || ctx == nil || sink == nil || input.Validate() != nil {
		return invalidInputOutcome()
	}
	adapter.lifecycleMu.Lock()
	if adapter.closed {
		adapter.lifecycleMu.Unlock()
		return internalOutcome()
	}
	adapter.activeRuns.Add(1)
	adapter.lifecycleMu.Unlock()
	defer adapter.activeRuns.Done()
	startedAt := adapter.now()
	if !validRuntimeTime(startedAt) {
		return internalOutcome()
	}
	publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, adapter.now, sink)
	if err != nil {
		return internalOutcome()
	}
	budget, err := agent.NewRunBudget(input.BudgetLimits(), startedAt, adapter.now)
	if err != nil {
		return internalOutcome()
	}
	registry, err := agent.NewEvidenceRegistry(input.RunID(), input.Scope())
	if err != nil {
		return internalOutcome()
	}

	runCtx, cancel := context.WithTimeout(ctx, input.BudgetLimits().RunDuration)
	defer cancel()
	runCtx = einocallbacks.InitCallbacks(runCtx, nil)
	state := &runState{
		input:             input,
		model:             adapter.model,
		tools:             adapter.tools,
		scopeGuard:        adapter.scopeGuard,
		identifiers:       adapter.identifiers,
		now:               adapter.now,
		budget:            budget,
		registry:          registry,
		publisher:         publisher,
		boundCalls:        make(map[string]*boundExecution),
		modelRequestIDs:   make(map[domain.ModelRequestID]struct{}),
		toolInvocationIDs: make(map[domain.ToolInvocationID]struct{}),
	}

	startCtx := runCtx
	if runCtx.Err() != nil {
		startCtx = context.WithoutCancel(runCtx)
	}
	if publishErr := state.publish(startCtx, agent.RunEvent{Kind: agent.RunEventRunStarted}); publishErr != nil {
		return state.finishFailure(runCtx, publishErr)
	}
	if err := state.checkScope(runCtx); err != nil {
		return state.finishFailure(runCtx, err)
	}

	firstRequestID, err := adapter.identifiers.NewModelRequestID()
	if err != nil || !firstRequestID.Valid() {
		return state.finishFailure(runCtx, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err))
	}
	initialRequest, err := agent.BuildInitialModelRequest(input, firstRequestID)
	if err != nil {
		return state.finishFailure(runCtx, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err))
	}
	state.firstModelRequestID = firstRequestID
	initialMessages, err := einoMessages(initialRequest.Messages)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}

	tools, err := newEinoTools(state)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}
	productionAgent, err := react.NewAgent(runCtx, &react.AgentConfig{
		ToolCallingModel: &modelBridge{state: state},
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			ExecuteSequentially: true,
		},
		MaxStep: input.BudgetLimits().Steps*2 + 1,
	})
	if err != nil {
		return state.finishFailure(runCtx, normalizeFrameworkError(err))
	}
	output, err := productionAgent.Stream(runCtx, initialMessages)
	if err != nil {
		return state.finishFailure(runCtx, normalizeFrameworkError(err))
	}
	finalMessage, err := schema.ConcatMessageStream(output)
	if err != nil {
		return state.finishFailure(runCtx, normalizeFrameworkError(err))
	}
	if err := state.finishStep(runCtx); err != nil {
		var failure *runtimeFailure
		if !errors.As(err, &failure) || !failure.localDiagnosis {
			return state.finishFailure(runCtx, err)
		}
	}
	if err := state.checkScope(runCtx); err != nil {
		return state.finishFailure(runCtx, err)
	}
	draft, err := diagnosisDraft(finalMessage)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}
	diagnosis, err := state.validateDiagnosis(draft, true)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}
	return state.finishDiagnosis(runCtx, diagnosis)
}

// Close prevents new runs and waits for every previously admitted Run call.
// Application must cancel and wait its sole active run before invoking Close.
func (adapter *Adapter) Close() {
	if adapter == nil {
		return
	}
	adapter.lifecycleMu.Lock()
	if adapter.closed {
		adapter.lifecycleMu.Unlock()
		return
	}
	adapter.closed = true
	adapter.lifecycleMu.Unlock()
	adapter.activeRuns.Wait()
}

func validRuntimeTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%int(time.Millisecond) == 0
}

func invalidInputOutcome() agent.RunOutcome {
	class := domain.SafeErrorClassInvalidInput
	return agent.RunOutcome{
		Status:      domain.AgentRunStatusFailed,
		ErrorClass:  &class,
		SafeMessage: "The Agent run input is invalid.",
	}
}

func internalOutcome() agent.RunOutcome {
	class := domain.SafeErrorClassInternal
	return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: safeInternalFailure}
}

func normalizeFrameworkError(err error) error {
	if err == nil {
		return nil
	}
	var failure *runtimeFailure
	if errors.As(err, &failure) {
		return failure
	}
	var budgetError *agent.RunBudgetError
	if errors.As(err, &budgetError) {
		return runtimeFailureFromBudget(budgetError)
	}
	if errors.Is(err, context.Canceled) {
		return &runtimeFailure{
			status:      domain.AgentRunStatusCancelled,
			class:       domain.SafeErrorClassCancelled,
			safeMessage: "The Agent run was cancelled.",
			stopReason:  agent.RunStopCancelled,
			cause:       err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &runtimeFailure{
			status:      domain.AgentRunStatusTimedOut,
			class:       domain.SafeErrorClassTimeout,
			safeMessage: "The Agent run reached its deadline.",
			stopReason:  agent.RunStopTimedOut,
			cause:       err,
		}
	}
	return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
}
