package einoadapter

import (
	"context"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type modelBridge struct {
	state      *runState
	toolsBound bool
}

var _ einomodel.ToolCallingChatModel = (*modelBridge)(nil)

func (bridge *modelBridge) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if bridge == nil || bridge.state == nil || validateBoundToolInfos(tools) != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	return &modelBridge{state: bridge.state, toolsBound: true}, nil
}

func (bridge *modelBridge) Generate(ctx context.Context, input []*schema.Message, options ...einomodel.Option) (*schema.Message, error) {
	stream, err := bridge.Stream(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	message, err := schema.ConcatMessageStream(stream)
	if err != nil {
		return nil, normalizeFrameworkError(err)
	}
	return message, nil
}

func (bridge *modelBridge) Stream(ctx context.Context, input []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if bridge == nil || bridge.state == nil || !bridge.toolsBound || ctx == nil || len(options) != 0 {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	message, err := bridge.state.callModel(ctx, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (state *runState) callModel(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	if err := state.beginStep(ctx); err != nil {
		return nil, err
	}
	reservation, err := state.budget.ReserveModelCall(ctx)
	if err != nil {
		return nil, runtimeFailureFromBudget(err)
	}
	requestID, err := state.nextModelRequestID()
	if err != nil {
		return nil, err
	}
	neutralMessages, err := state.neutralMessages(messages)
	if err != nil {
		return nil, err
	}
	request := domain.ModelRequest{ID: requestID, Messages: neutralMessages, Tools: agent.ToolSpecifications()}
	if request.Validate() != nil {
		return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID}); err != nil {
		return nil, err
	}
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}

	modelCtx, cancel := context.WithTimeout(ctx, reservation.Timeout)
	collector := newModelCollector(state, modelCtx, cancel)
	modelError := state.model.Stream(modelCtx, request, collector.consume)
	modelContextError := modelCtx.Err()
	cancel()
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	if collector.failure != nil {
		return nil, collector.failure
	}
	if modelContextError != nil {
		return nil, normalizeFrameworkError(modelContextError)
	}
	if modelError != nil {
		return nil, runtimeFailureFromModel(modelError)
	}
	return collector.finalMessage(ctx)
}

func runtimeFailureFromModel(modelError *domain.ModelError) *runtimeFailure {
	if modelError == nil || modelError.Validate() != nil {
		return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	switch modelError.Class() {
	case domain.SafeErrorClassCancelled:
		return &runtimeFailure{
			status:      domain.AgentRunStatusCancelled,
			class:       domain.SafeErrorClassCancelled,
			safeMessage: modelError.SafeMessage(),
			stopReason:  agent.RunStopCancelled,
			cause:       modelError,
		}
	case domain.SafeErrorClassTimeout:
		return &runtimeFailure{
			status:      domain.AgentRunStatusTimedOut,
			class:       domain.SafeErrorClassTimeout,
			safeMessage: modelError.SafeMessage(),
			stopReason:  agent.RunStopTimedOut,
			cause:       modelError,
		}
	default:
		return failedRuntime(modelError.Class(), modelError.SafeMessage(), modelError)
	}
}

type toolCallAssembly struct {
	seen      bool
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}

type modelCollector struct {
	state *runState
	ctx   context.Context
	stop  context.CancelFunc

	sequence  int
	text      strings.Builder
	tools     [domain.MaxAgentToolCalls]toolCallAssembly
	maxIndex  int
	sawTool   bool
	metadata  bool
	usage     bool
	completed *domain.ModelCompletion
	failure   error
}

func newModelCollector(state *runState, ctx context.Context, stop context.CancelFunc) *modelCollector {
	return &modelCollector{state: state, ctx: ctx, stop: stop, maxIndex: -1}
}

func (collector *modelCollector) consume(event domain.ModelStreamEvent) {
	if collector.failure != nil {
		return
	}
	if err := collector.accept(event); err != nil {
		collector.failure = err
		collector.stop()
	}
}

func (collector *modelCollector) accept(event domain.ModelStreamEvent) error {
	if event.Validate() != nil || event.Sequence != collector.sequence+1 || collector.completed != nil {
		return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	collector.sequence = event.Sequence
	switch event.Kind {
	case domain.ModelStreamEventMetadata:
		if collector.metadata {
			return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		collector.metadata = true
	case domain.ModelStreamEventUsage:
		if collector.usage {
			return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		collector.usage = true
	case domain.ModelStreamEventTextDelta:
		if collector.sawTool || collector.text.Len()+len(event.TextDelta) > domain.MaxModelMessageBytes {
			return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		collector.text.WriteString(event.TextDelta)
		if err := collector.state.publish(collector.ctx, agent.RunEvent{Kind: agent.RunEventTextDelta, TextDelta: event.TextDelta}); err != nil {
			return err
		}
	case domain.ModelStreamEventToolCallFragment:
		if collector.text.Len() != 0 {
			return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		fragment := event.ToolCallFragment
		assembly := &collector.tools[fragment.Index]
		assembly.seen = true
		assembly.id.WriteString(fragment.IDFragment)
		assembly.name.WriteString(fragment.NameFragment)
		assembly.arguments.WriteString(fragment.ArgumentsFragment)
		if assembly.id.Len() > domain.MaxModelToolCallIDBytes || assembly.name.Len() > domain.MaxModelToolNameBytes ||
			assembly.arguments.Len() > domain.MaxModelToolArgumentsBytes {
			return failedRuntime(domain.SafeErrorClassBudgetExhausted, "The model Tool call exceeded a fixed limit.", nil)
		}
		collector.sawTool = true
		if fragment.Index > collector.maxIndex {
			collector.maxIndex = fragment.Index
		}
	case domain.ModelStreamEventCompleted:
		completion := *event.Completion
		collector.completed = &completion
	default:
		return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	return nil
}

func (collector *modelCollector) finalMessage(ctx context.Context) (*schema.Message, error) {
	if collector.completed == nil || collector.sequence == 0 {
		return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	switch collector.completed.FinishReason {
	case domain.ModelFinishReasonStop:
		if collector.sawTool || collector.text.Len() == 0 {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		return schema.AssistantMessage(collector.text.String(), nil), nil
	case domain.ModelFinishReasonLength:
		if collector.sawTool {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		return nil, localRuntimeStop(
			agent.RunStopStepLimit,
			domain.SafeErrorClassBudgetExhausted,
			"The model response reached its fixed output limit.",
			nil,
		)
	case domain.ModelFinishReasonToolCalls:
		if !collector.sawTool || collector.text.Len() != 0 {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		calls := make([]domain.ModelToolCall, collector.maxIndex+1)
		for index := 0; index <= collector.maxIndex; index++ {
			assembly := &collector.tools[index]
			if !assembly.seen {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
			call := domain.ModelToolCall{
				ID:            assembly.id.String(),
				Name:          domain.ToolName(assembly.name.String()),
				ArgumentsJSON: assembly.arguments.String(),
			}
			if call.Validate() != nil {
				return nil, failedRuntime(domain.SafeErrorClassPolicyDenied, "The model requested a Tool call outside the fixed policy.", nil)
			}
			calls[index] = call
		}
		if err := collector.state.bindToolCalls(ctx, calls); err != nil {
			return nil, err
		}
		einoCalls := make([]schema.ToolCall, len(calls))
		for index, call := range calls {
			position := index
			einoCalls[index] = schema.ToolCall{
				Index: &position,
				ID:    call.ID,
				Type:  "function",
				Function: schema.FunctionCall{
					Name:      string(call.Name),
					Arguments: call.ArgumentsJSON,
				},
			}
		}
		return schema.AssistantMessage("", einoCalls), nil
	default:
		return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
}
