package einoadapter

import (
	"context"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type modelBridge struct {
	state *runState
	model einomodel.ToolCallingChatModel
}

var _ einomodel.ToolCallingChatModel = (*modelBridge)(nil)

func (bridge *modelBridge) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if bridge == nil || bridge.state == nil || bridge.state.client == nil || validateBoundToolInfos(tools) != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	bound, err := bridge.state.client.withTools(tools)
	if err != nil {
		return nil, err
	}
	return &modelBridge{state: bridge.state, model: bound}, nil
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
	if bridge == nil || bridge.state == nil || bridge.model == nil || ctx == nil || len(options) != 0 {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	message, err := bridge.state.callModel(ctx, bridge.model, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (state *runState) callModel(
	ctx context.Context,
	model einomodel.ToolCallingChatModel,
	messages []*schema.Message,
) (*schema.Message, error) {
	if state == nil || state.client == nil || model == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
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
	message, modelError := state.client.stream(modelCtx, requestID, model, messages)
	modelContextError := modelCtx.Err()
	cancel()
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	if modelContextError != nil {
		return nil, normalizeFrameworkError(modelContextError)
	}
	if modelError != nil {
		return nil, runtimeFailureFromModel(modelError)
	}
	return state.acceptModelMessage(ctx, message)
}

func (state *runState) acceptModelMessage(ctx context.Context, message *schema.Message) (*schema.Message, error) {
	if message == nil || message.Role != "" && message.Role != schema.Assistant ||
		len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 ||
		len(message.AssistantGenMultiContent) != 0 || message.Name != "" ||
		message.ToolCallID != "" || message.ToolName != "" || message.ReasoningContent != "" ||
		message.Extra != nil || message.ResponseMeta == nil || message.ResponseMeta.LogProbs != nil {
		return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}

	switch message.ResponseMeta.FinishReason {
	case "stop":
		neutral := domain.ModelMessage{Role: domain.ModelMessageRoleAssistant, Content: message.Content}
		if len(message.ToolCalls) != 0 || neutral.Validate() != nil {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventTextDelta, TextDelta: safeModelProgress}); err != nil {
			return nil, err
		}
		return schema.AssistantMessage(message.Content, nil), nil
	case "length":
		if len(message.ToolCalls) != 0 {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		return nil, localRuntimeStop(
			agent.RunStopStepLimit,
			domain.SafeErrorClassBudgetExhausted,
			"The model response reached its fixed output limit.",
			nil,
		)
	case "tool_calls":
		if len(message.ToolCalls) == 0 || len(message.ToolCalls) > domain.MaxAgentToolCalls {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		calls := make([]domain.ModelToolCall, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			if call.Extra != nil || call.Type != "" && call.Type != "function" ||
				call.Index == nil || *call.Index != index {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
			calls[index] = domain.ModelToolCall{
				ID:            call.ID,
				Name:          domain.ToolName(call.Function.Name),
				ArgumentsJSON: call.Function.Arguments,
			}
			if calls[index].Validate() != nil {
				return nil, failedRuntime(domain.SafeErrorClassPolicyDenied, "The model requested a cluster read outside the fixed policy.", nil)
			}
		}
		if err := state.bindToolCalls(ctx, calls); err != nil {
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
