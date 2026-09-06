// Package einoadapter contains the sole Eino translation boundary for the
// production single-Agent runtime.
package einoadapter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	einocallbacks "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	projectconfig "github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Config contains the complete project-owned inputs for the sole Eino runtime
// boundary. It exposes no Eino or provider SDK values.
type Config struct {
	ModelConfiguration domain.ModelConfiguration
	Credential         *projectconfig.SecretValue
	Logger             *slog.Logger
	Diagnostics        DiagnosticOptions
	Tools              agent.ToolHandlers
	ScopeGuard         agent.RunScopeGuard
	Identifiers        agent.RunIdentifierSource
	Now                func() time.Time
}

type runtimeConfig struct {
	profileName string
	tools       agent.ToolHandlers
	scopeGuard  agent.RunScopeGuard
	identifiers agent.RunIdentifierSource
	now         func() time.Time
}

// Adapter runs one bounded ReAct composition at a time. All policy-bearing
// state is created per Run call and never stored in Eino framework state.
type Adapter struct {
	client      *modelClient
	profileName string
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
	if config.ScopeGuard == nil || config.Identifiers == nil || config.Now == nil ||
		config.Tools.Validate() != nil || !validRuntimeTime(config.Now()) {
		return nil, ErrInvalidConfiguration
	}
	client, modelError := newModelClient(
		config.ModelConfiguration,
		config.Credential,
		config.Logger,
		config.Diagnostics,
	)
	if modelError != nil {
		return nil, modelError
	}
	if config.ModelConfiguration.Role != domain.ModelRoleAgent {
		client.close()
		return nil, ErrInvalidConfiguration
	}
	adapter, err := newAdapter(runtimeConfig{
		profileName: config.ModelConfiguration.ProfileName,
		tools:       config.Tools,
		scopeGuard:  config.ScopeGuard,
		identifiers: config.Identifiers,
		now:         config.Now,
	}, client)
	if err != nil {
		client.close()
		return nil, err
	}
	return adapter, nil
}

func newAdapter(config runtimeConfig, client *modelClient) (*Adapter, error) {
	if config.profileName == "" {
		config.profileName = string(domain.ModelRoleAgent)
	}
	if client == nil || client.model == nil || config.scopeGuard == nil || config.identifiers == nil || config.now == nil ||
		!domain.ValidModelToken(config.profileName, 128) || config.tools.Validate() != nil || !validRuntimeTime(config.now()) {
		return nil, ErrInvalidConfiguration
	}
	return &Adapter{
		client:      client,
		profileName: config.profileName,
		tools:       config.tools,
		scopeGuard:  config.scopeGuard,
		identifiers: config.identifiers,
		now:         config.now,
	}, nil
}

// Run executes one immutable single-Agent composition and returns exactly one
// project-owned terminal outcome.
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
	registry, err := agent.NewEvidenceRegistry(input.RunID(), input.Scope(), input.PolicyGeneration())
	if err != nil {
		return internalOutcome()
	}

	runCtx, cancel := context.WithTimeout(ctx, input.BudgetLimits().RunDuration)
	defer cancel()
	runCtx = einocallbacks.InitCallbacks(runCtx, nil)
	state := &runState{
		input:             input,
		client:            adapter.client,
		tools:             adapter.tools,
		scopeGuard:        adapter.scopeGuard,
		identifiers:       adapter.identifiers,
		now:               adapter.now,
		budget:            budget,
		registry:          registry,
		publisher:         publisher,
		profileName:       adapter.profileName,
		originHash:        domain.SHA256Hex(adapter.client.configuration.Origin),
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

	initialMessages, err := newInitialMessages(input)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}

	tools, err := newEinoTools(state)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}
	summaryHandler, err := state.newSummarizationMiddleware(runCtx)
	if err != nil {
		return state.finishFailure(runCtx, err)
	}
	steeringHandler := newSteeringMiddleware(state)
	productionAgent, err := adk.NewChatModelAgent(runCtx, &adk.ChatModelAgentConfig{
		Name:        "kupilot-agent",
		Instruction: initialMessages[0].Content,
		Model:       &guardedChatModel{state: state},
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools, ExecuteSequentially: true,
		}},
		MaxIterations: input.BudgetLimits().ModelCalls,
		Handlers:      []adk.ChatModelAgentMiddleware{summaryHandler, steeringHandler},
	})
	if err != nil {
		return state.finishFailure(runCtx, normalizeFrameworkError(err))
	}
	runner := adk.NewRunner(runCtx, adk.RunnerConfig{Agent: productionAgent, EnableStreaming: true})
	iterator := runner.Run(runCtx, initialMessages[1:])
	var finalMessage *schema.Message
	for {
		event, available := iterator.Next()
		if !available {
			break
		}
		if event == nil || event.Err != nil {
			if event == nil {
				err = errors.New("Eino Runner returned an empty event")
			} else {
				err = event.Err
			}
			return state.finishFailure(runCtx, normalizeFrameworkError(err))
		}
		if event.Action != nil || event.Output == nil || event.Output.MessageOutput == nil || event.Output.CustomizedOutput != nil {
			return state.finishFailure(runCtx, normalizeFrameworkError(errors.New("Eino Runner returned an unsupported event")))
		}
		message, messageErr := event.Output.MessageOutput.GetMessage()
		if messageErr != nil {
			return state.finishFailure(runCtx, normalizeFrameworkError(messageErr))
		}
		if event.Output.MessageOutput.Role == schema.Assistant {
			// Runner assigns its private message identity after the guarded model
			// has rejected provider metadata. It is execution state, not durable
			// or project authority, so it does not cross this boundary.
			value := *message
			value.Extra = nil
			finalMessage = &value
		}
	}
	if finalMessage == nil {
		return state.finishFailure(runCtx, normalizeFrameworkError(errors.New("Eino Runner returned no final assistant message")))
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
	draft, err := diagnosisDraftForMode(finalMessage, input.Mode())
	if err != nil {
		return state.finishFailure(runCtx, err)
	}
	if diagnosisDraftContainsCredential(state.client.credential, draft) {
		return state.finishFailure(runCtx, failedRuntime(
			domain.SafeErrorClassSensitiveOutputBlocked,
			safeSensitiveModelTextBlocked,
			agent.ErrSensitiveModelTextBlocked,
		))
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
	adapter.client.close()
}

func validRuntimeTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%int(time.Millisecond) == 0
}

func invalidInputOutcome() agent.RunOutcome {
	class := domain.SafeErrorClassInvalidInput
	return agent.RunOutcome{
		Status:      domain.AgentRunStatusFailed,
		ErrorClass:  &class,
		SafeMessage: "The diagnostic request is invalid.",
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
			safeMessage: "The diagnostic run was cancelled.",
			stopReason:  agent.RunStopCancelled,
			cause:       err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &runtimeFailure{
			status:      domain.AgentRunStatusTimedOut,
			class:       domain.SafeErrorClassTimeout,
			safeMessage: "The diagnostic run reached its time limit.",
			stopReason:  agent.RunStopTimedOut,
			cause:       err,
		}
	}
	return failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
}
