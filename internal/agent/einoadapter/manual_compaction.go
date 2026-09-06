package einoadapter

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const manualCompactionSentinel = "Preserve the current conversation state after this explicit compaction operation."

var _ agent.ContextCompactor = (*Adapter)(nil)

type manualCompactionRunSink struct {
	mu      sync.Mutex
	input   agent.ManualCompactionInput
	sink    agent.ManualCompactionEventSink
	started bool
	ready   bool
	summary *domain.SessionContextSummary
}

func (sink *manualCompactionRunSink) Publish(ctx context.Context, event agent.RunEvent) agent.EventSinkResult {
	if sink == nil || ctx == nil || event.Validate() != nil {
		return agent.EventSinkRejected
	}
	if event.Kind == agent.RunEventRunStarted {
		return agent.EventSinkAccepted
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var projected agent.ManualCompactionEvent
	switch event.Kind {
	case agent.RunEventSummaryStarted:
		if sink.started || sink.ready {
			return agent.EventSinkRejected
		}
		projected = agent.ManualCompactionEvent{
			ID: sink.input.ID(), SessionID: sink.input.SessionID(),
			ScopeGeneration: sink.input.Scope().Generation, PolicyGeneration: sink.input.PolicyGeneration(),
			Sequence: 1, Kind: agent.ManualCompactionStarted, ModelRequestID: event.ModelRequestID,
		}
	case agent.RunEventSummaryReady:
		if !sink.started || sink.ready {
			return agent.EventSinkRejected
		}
		projected = agent.ManualCompactionEvent{
			ID: sink.input.ID(), SessionID: sink.input.SessionID(),
			ScopeGeneration: sink.input.Scope().Generation, PolicyGeneration: sink.input.PolicyGeneration(),
			Sequence: 2, Kind: agent.ManualCompactionReady, Summary: event.Summary,
		}
	default:
		return agent.EventSinkRejected
	}
	if projected.Validate() != nil {
		return agent.EventSinkRejected
	}
	result := sink.sink.AcceptManualCompaction(ctx, projected)
	if result != agent.EventSinkAccepted {
		return result
	}
	if projected.Kind == agent.ManualCompactionStarted {
		sink.started = true
	} else {
		sink.ready = true
		value := *projected.Summary
		sink.summary = &value
	}
	return result
}

func (sink *manualCompactionRunSink) result() *domain.SessionContextSummary {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.summary == nil {
		return nil
	}
	value := *sink.summary
	return &value
}

// Compact invokes the same Eino summarization middleware's public Summarize
// method. It creates no ChatModelAgent, Runner, Tool node, or ordinary model
// request and performs at most one summary request.
func (adapter *Adapter) Compact(
	ctx context.Context,
	input agent.ManualCompactionInput,
	sink agent.ManualCompactionEventSink,
) (agent.ManualCompactionResult, error) {
	if adapter == nil || ctx == nil || sink == nil || input.Validate() != nil {
		return agent.ManualCompactionResult{}, agent.ErrInvalidManualCompaction
	}
	if summaryCutTurnCount(input.Conversation().Turns()) == 0 {
		return agent.ManualCompactionResult{Noop: true}, nil
	}
	adapter.lifecycleMu.Lock()
	if adapter.closed {
		adapter.lifecycleMu.Unlock()
		return agent.ManualCompactionResult{}, errors.New("model runtime is closed")
	}
	if input.Profile() != adapter.profileName || input.OriginHash() != domain.SHA256Hex(adapter.client.configuration.Origin) {
		adapter.lifecycleMu.Unlock()
		return agent.ManualCompactionResult{}, agent.ErrInvalidManualCompaction
	}
	adapter.activeRuns.Add(1)
	adapter.lifecycleMu.Unlock()
	defer adapter.activeRuns.Done()

	generatedID, err := adapter.identifiers.NewModelRequestID()
	if err != nil || !generatedID.Valid() {
		return agent.ManualCompactionResult{}, agent.ErrInvalidManualCompaction
	}
	runInput, err := agent.NewRunInputWithPolicyContext(
		domain.AgentRunID(input.ID()), input.SessionID(), domain.MessageID(generatedID), manualCompactionSentinel,
		input.Scope(), nil, input.BudgetLimits(), input.Conversation(), domain.DefaultResourcePolicyCatalog(),
		input.PolicyGeneration(),
	)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	startedAt := adapter.now()
	budget, err := agent.NewRunBudget(input.BudgetLimits(), startedAt, adapter.now)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	registry, err := agent.NewEvidenceRegistry(runInput.RunID(), runInput.Scope(), runInput.PolicyGeneration())
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	runSink := &manualCompactionRunSink{input: input, sink: sink}
	publisher, err := agent.NewEventPublisher(runInput.RunID(), runInput.Scope().Generation, adapter.now, runSink)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	state := &runState{
		input: runInput, client: adapter.client, tools: adapter.tools, scopeGuard: adapter.scopeGuard,
		identifiers: adapter.identifiers, now: adapter.now, budget: budget, registry: registry, publisher: publisher,
		profileName: adapter.profileName, originHash: input.OriginHash(), boundCalls: make(map[string]*boundExecution),
		modelRequestIDs: make(map[domain.ModelRequestID]struct{}), toolInvocationIDs: make(map[domain.ToolInvocationID]struct{}),
	}
	operationCtx, cancel := context.WithTimeout(ctx, input.BudgetLimits().RunDuration)
	defer cancel()
	if _, err := publisher.Publish(operationCtx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
		return agent.ManualCompactionResult{}, err
	}
	messages, err := newInitialMessages(runInput)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	handler, err := state.newSummarizationMiddleware(operationCtx)
	if err != nil {
		return agent.ManualCompactionResult{}, err
	}
	summarizer, ok := handler.(interface {
		Summarize(context.Context, *adk.ChatModelAgentState) ([]*schema.Message, error)
	})
	if !ok {
		return agent.ManualCompactionResult{}, errors.New("Eino summarization handler does not expose Summarize")
	}
	if _, err := summarizer.Summarize(operationCtx, &adk.ChatModelAgentState{Messages: messages}); err != nil {
		return agent.ManualCompactionResult{}, err
	}
	summary := runSink.result()
	result := agent.ManualCompactionResult{Summary: summary}
	if result.Validate(input) != nil {
		return agent.ManualCompactionResult{}, agent.ErrInvalidManualCompaction
	}
	return result, nil
}
