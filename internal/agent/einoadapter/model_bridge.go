package einoadapter

import (
	"context"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// guardedChatModel is the run-scoped policy interceptor required by ReAct. It
// delegates serialization and stream semantics to the concrete Eino model.
type guardedChatModel struct {
	state *runState
	model einomodel.ToolCallingChatModel
}

var _ einomodel.ToolCallingChatModel = (*guardedChatModel)(nil)

func (model *guardedChatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if model == nil || model.state == nil || model.state.client == nil || validateBoundToolInfos(tools) != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	bound, err := model.state.client.withTools(tools)
	if err != nil {
		return nil, err
	}
	return &guardedChatModel{state: model.state, model: bound}, nil
}

func (model *guardedChatModel) Generate(ctx context.Context, input []*schema.Message, options ...einomodel.Option) (*schema.Message, error) {
	stream, err := model.Stream(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	message, err := schema.ConcatMessageStream(stream)
	if err != nil {
		return nil, normalizeFrameworkError(err)
	}
	return message, nil
}

func (model *guardedChatModel) Stream(ctx context.Context, input []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if model == nil || model.state == nil || ctx == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	bound := model.model
	if bound == nil {
		parsed := einomodel.GetCommonOptions(&einomodel.Options{}, options...)
		if len(options) == 0 || parsed.Temperature != nil || parsed.Model != nil || parsed.TopP != nil ||
			parsed.MaxTokens != nil || len(parsed.Stop) != 0 || parsed.ToolChoice != nil ||
			len(parsed.AllowedToolNames) != 0 || len(parsed.DeferredTools) != 0 || parsed.ToolSearchTool != nil ||
			parsed.AgenticToolChoice != nil || validateBoundToolInfos(parsed.Tools) != nil {
			return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		var err error
		bound, err = model.state.client.withTools(parsed.Tools)
		if err != nil {
			return nil, err
		}
	} else if len(options) != 0 {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	message, err := model.state.callModel(ctx, bound, input)
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
	safeMessages := stripRunnerMessageMetadata(messages)
	if err := state.validateConversation(safeMessages); err != nil {
		return nil, err
	}
	if err := state.publish(ctx, agent.RunEvent{Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID}); err != nil {
		return nil, err
	}
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}

	modelCtx, cancel := context.WithTimeout(ctx, reservation.Timeout)
	preview, err := newProvisionalAnswer(modelCtx, cancel, state, state.client.credential)
	if err != nil {
		cancel()
		return nil, err
	}
	message, modelError := state.client.streamBounded(modelCtx, requestID, model, safeMessages, reservation, preview.accept)
	var finishError error
	if modelError == nil && preview.failure == nil && message != nil && message.ResponseMeta != nil &&
		message.ResponseMeta.FinishReason == "stop" {
		finishError = preview.finish()
	}
	modelContextError := modelCtx.Err()
	cancel()
	if err := state.checkScope(ctx); err != nil {
		return nil, err
	}
	if preview.failure != nil {
		return nil, preview.failure
	}
	if finishError != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, finishError)
	}
	if modelContextError != nil {
		return nil, normalizeFrameworkError(modelContextError)
	}
	if modelError != nil {
		return nil, runtimeFailureFromModel(modelError)
	}
	return state.acceptModelMessage(ctx, message)
}

func stripRunnerMessageMetadata(messages []*schema.Message) []*schema.Message {
	result := cloneEinoMessages(messages)
	for _, message := range result {
		if message != nil {
			message.Extra = nil
		}
	}
	return result
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
		if len(message.ToolCalls) != 0 || !domain.ValidModelText(message.Content, domain.MaxModelMessageBytes, false) {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		message.Role = schema.Assistant
		message.ResponseMeta = nil
		return message, nil
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
		calls := make([]agent.ToolSelection, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			if call.Extra != nil || call.Type != "" && call.Type != "function" ||
				call.Index == nil || *call.Index != index {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
			calls[index] = agent.ToolSelection{
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
		for index, call := range calls {
			position := index
			message.ToolCalls[index].Index = &position
			message.ToolCalls[index].ID = call.ID
			message.ToolCalls[index].Type = "function"
			message.ToolCalls[index].Function = schema.FunctionCall{
				Name:      string(call.Name),
				Arguments: call.ArgumentsJSON,
			}
		}
		message.Role = schema.Assistant
		message.Content = ""
		message.ResponseMeta = nil
		return message, nil
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
