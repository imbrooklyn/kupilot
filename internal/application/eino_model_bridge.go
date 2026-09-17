package application

import (
	"context"
	"sync/atomic"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// preparedModelCall contains one local reservation, not another model protocol.
type preparedModelCall struct {
	requestID   domain.ModelRequestID
	reservation agent.CallReservation
	started     atomic.Bool
}

func (state *runState) prepareModelCall(ctx context.Context, messages []*schema.Message) (*preparedModelCall, error) {
	if state == nil || state.client == nil || ctx == nil {
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
	if minimumModelPayloadBytes(safeMessages) > reservation.RequestBytes {
		return nil, failedRuntime(
			domain.SafeErrorClassBudgetExhausted,
			"The model request exceeded its fixed byte limit.",
			nil,
		)
	}
	return &preparedModelCall{requestID: requestID, reservation: reservation}, nil
}

// minimumModelPayloadBytes is a provider-independent lower bound for the
// serialized messages. It deliberately excludes JSON framing and fixed Tool
// schemas: exceeding this value already proves that the configured transport
// request ceiling cannot fit, so a claimed steer can be recovered before its
// durable commit barrier and before any model I/O.
func minimumModelPayloadBytes(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		total += len(message.Role) + len(message.Content) + len(message.ToolCallID) + len(message.ToolName)
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
		}
		if total > domain.MaxModelRequestBytes {
			return total
		}
	}
	return total
}

func (state *runState) executePreparedModelCall(
	ctx context.Context,
	model einomodel.BaseChatModel,
	messages []*schema.Message,
	prepared *preparedModelCall,
	options ...einomodel.Option,
) (*schema.Message, error) {
	if state == nil || state.client == nil || model == nil || ctx == nil || prepared == nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
	}
	safeMessages := stripRunnerMessageMetadata(messages)
	requestID, reservation := prepared.requestID, prepared.reservation
	preflight, err := state.projectModelCallPreflight(agent.ModelCallAgent, safeMessages, reservation)
	if err != nil {
		return nil, err
	}
	if err := state.publish(ctx, agent.RunEvent{
		Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID, ModelPreflight: &preflight,
	}); err != nil {
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
	prepared.started.Store(true)
	message, modelError := state.client.streamBounded(modelCtx, requestID, model, safeMessages, reservation, preview.accept, options...)
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

func (state *runState) projectModelCallPreflight(kind agent.ModelCallKind, messages []*schema.Message, reservation agent.CallReservation) (agent.ModelCallPreflight, error) {
	manifest, err := state.runInputManifest()
	if err != nil {
		return agent.ModelCallPreflight{}, err
	}
	return agent.ModelCallPreflight{
		Kind: kind, MessageCount: len(messages), MessageBytes: minimumModelPayloadBytes(messages),
		CurrentInputCount: manifest.Count, CurrentInputDigest: manifest.Digest,
		ReservedRequestBytes: reservation.RequestBytes, ReservedOutputBytes: reservation.OutputBytes,
		ReservedStreamBytes: reservation.StreamBytes,
		ReservedNanoseconds: reservation.Timeout.Nanoseconds(), ReservedCostUnits: reservation.CostUnits,
	}, nil
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
		return nil, failedAt(domain.FailureStreamUnsupported, domain.SafeErrorClassInvalidExternalResponse, nil)
	}

	switch message.ResponseMeta.FinishReason {
	case "stop":
		if len(message.ToolCalls) != 0 || !domain.ValidModelText(message.Content, domain.MaxModelMessageBytes, false) {
			return nil, failedAt(domain.FailureStreamUnsupported, domain.SafeErrorClassInvalidExternalResponse, nil)
		}
		message.Role = schema.Assistant
		message.ResponseMeta = nil
		return message, nil
	case "length":
		if len(message.ToolCalls) != 0 {
			return nil, failedAt(domain.FailureStreamUnsupported, domain.SafeErrorClassInvalidExternalResponse, nil)
		}
		return nil, localRuntimeStop(
			agent.RunStopStepLimit,
			domain.SafeErrorClassBudgetExhausted,
			"The model response reached its fixed output limit.",
			nil,
		)
	case "tool_calls":
		if len(message.ToolCalls) == 0 || len(message.ToolCalls) > domain.MaxAgentToolCalls {
			return nil, failedAt(domain.FailureStreamUnsupported, domain.SafeErrorClassInvalidExternalResponse, nil)
		}
		calls := make([]agent.ToolSelection, len(message.ToolCalls))
		for index, call := range message.ToolCalls {
			if call.Extra != nil || call.Type != "" && call.Type != "function" ||
				call.Index == nil || *call.Index != index {
				return nil, failedAt(domain.FailureStreamUnsupported, domain.SafeErrorClassInvalidExternalResponse, nil)
			}
			calls[index] = agent.ToolSelection{
				ID:            call.ID,
				Name:          domain.ToolName(call.Function.Name),
				ArgumentsJSON: call.Function.Arguments,
			}
			if calls[index].Validate() != nil {
				return nil, failedAt(domain.FailureToolSelection, domain.SafeErrorClassPolicyDenied, nil)
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
		return nil, failedAt(domain.FailureStopReason, domain.SafeErrorClassInvalidExternalResponse, nil)
	}
}

func runtimeFailureFromModel(modelError *domain.ModelError) *runtimeFailure {
	if modelError == nil || modelError.Validate() != nil {
		return failedAt(domain.FailureInternal, domain.SafeErrorClassInternal, nil)
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
		reason := domain.FailureProviderTransport
		switch modelError.Code() {
		case domain.ModelErrorCodeInvalidRequest:
			reason = domain.FailureRequestPreflight
		case domain.ModelErrorCodeRequestTooLarge, domain.ModelErrorCodeStreamLimitExceeded:
			reason = domain.FailureBudget
		case domain.ModelErrorCodeUnsupportedResponse:
			reason = domain.FailureProviderProtocol
		case domain.ModelErrorCodeRequestRejected:
			reason = domain.FailureProviderRequest
		case domain.ModelErrorCodeMalformedStream:
			reason = domain.FailureStreamMalformed
		case domain.ModelErrorCodeDuplicateFinish:
			reason = domain.FailureStreamDuplicate
		case domain.ModelErrorCodeProviderReported:
			reason = domain.FailureProviderReported
		case domain.ModelErrorCodeAfterFinish:
			reason = domain.FailureStreamAfterFinish
		case domain.ModelErrorCodeMissingFinish:
			reason = domain.FailureStreamIncomplete
		case domain.ModelErrorCodeInvalidStreamUsage:
			reason = domain.FailureStreamUsage
		case domain.ModelErrorCodeInvalidStopReason:
			reason = domain.FailureStopReason
		case domain.ModelErrorCodeRedirectDenied:
			reason = domain.FailureProviderProtocol
		case domain.ModelErrorCodeInternal:
			reason = domain.FailureInternal
		}
		failure := failedRuntime(modelError.Class(), modelError.SafeMessage(), modelError)
		failure.diagnostic = reason
		return failure
	}
}
