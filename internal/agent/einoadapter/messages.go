package einoadapter

import (
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func einoMessages(messages []domain.ModelMessage) ([]*schema.Message, error) {
	result := make([]*schema.Message, len(messages))
	for index, message := range messages {
		if message.Validate() != nil {
			return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
		switch message.Role {
		case domain.ModelMessageRoleSystem:
			result[index] = schema.SystemMessage(message.Content)
		case domain.ModelMessageRoleUser:
			result[index] = schema.UserMessage(message.Content)
		case domain.ModelMessageRoleAssistant:
			calls := make([]schema.ToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				position := callIndex
				calls[callIndex] = schema.ToolCall{
					Index: &position,
					ID:    call.ID,
					Type:  "function",
					Function: schema.FunctionCall{
						Name:      string(call.Name),
						Arguments: call.ArgumentsJSON,
					},
				}
			}
			result[index] = schema.AssistantMessage(message.Content, calls)
		case domain.ModelMessageRoleTool:
			result[index] = schema.ToolMessage(message.Content, message.ToolCallID)
		default:
			return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, nil)
		}
	}
	return result, nil
}

func (state *runState) neutralMessages(messages []*schema.Message) ([]domain.ModelMessage, error) {
	result := make([]domain.ModelMessage, len(messages))
	for index, message := range messages {
		if message == nil || unsupportedMessageFields(message) {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		neutral := domain.ModelMessage{Content: message.Content, ToolCallID: message.ToolCallID}
		switch message.Role {
		case schema.System:
			neutral.Role = domain.ModelMessageRoleSystem
			if message.ToolName != "" || len(message.ToolCalls) != 0 {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
		case schema.User:
			neutral.Role = domain.ModelMessageRoleUser
			if message.ToolName != "" || len(message.ToolCalls) != 0 {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
		case schema.Assistant:
			neutral.Role = domain.ModelMessageRoleAssistant
			if message.ToolName != "" {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
			neutral.ToolCalls = make([]domain.ModelToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				if call.Extra != nil || call.Type != "" && call.Type != "function" ||
					call.Index != nil && *call.Index != callIndex {
					return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
				}
				neutral.ToolCalls[callIndex] = domain.ModelToolCall{
					ID:            call.ID,
					Name:          domain.ToolName(call.Function.Name),
					ArgumentsJSON: call.Function.Arguments,
				}
			}
		case schema.Tool:
			neutral.Role = domain.ModelMessageRoleTool
			if len(message.ToolCalls) != 0 || !domain.ToolName(message.ToolName).Valid() ||
				!state.boundToolName(message.ToolCallID, domain.ToolName(message.ToolName)) {
				return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
		default:
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		if neutral.Validate() != nil {
			return nil, failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		result[index] = neutral
	}
	return result, nil
}

func unsupportedMessageFields(message *schema.Message) bool {
	return len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 ||
		len(message.AssistantGenMultiContent) != 0 || message.Name != "" ||
		message.ResponseMeta != nil || message.ReasoningContent != "" || message.Extra != nil
}

func (state *runState) boundToolName(callID string, name domain.ToolName) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	execution := state.boundCalls[callID]
	return execution != nil && execution.toolName == name
}
