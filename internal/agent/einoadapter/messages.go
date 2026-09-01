package einoadapter

import (
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const maxConversationMessages = 2 + domain.MaxAgentModelCalls + domain.MaxAgentToolCalls

func newInitialMessages(input agent.RunInput) ([]*schema.Message, error) {
	prompt, err := agent.BuildSystemPrompt(input)
	if err != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	return []*schema.Message{
		schema.SystemMessage(prompt),
		schema.UserMessage(input.Question()),
	}, nil
}

// validateConversation validates the Eino-owned ReAct conversation in place.
// It deliberately does not translate the messages into a second protocol DTO.
func (state *runState) validateConversation(messages []*schema.Message) error {
	if len(messages) == 0 || len(messages) > maxConversationMessages {
		return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
	}
	for _, message := range messages {
		if message == nil || unsupportedMessageFields(message) {
			return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
		switch message.Role {
		case schema.System, schema.User:
			if !domain.ValidModelText(message.Content, domain.MaxModelInputMessageBytes, false) ||
				message.ToolCallID != "" || message.ToolName != "" || len(message.ToolCalls) != 0 {
				return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
		case schema.Assistant:
			if message.ToolCallID != "" || message.ToolName != "" ||
				!domain.ValidModelText(message.Content, domain.MaxModelMessageBytes, true) ||
				(message.Content == "") == (len(message.ToolCalls) == 0) ||
				len(message.ToolCalls) > domain.MaxAgentToolCalls {
				return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
			seen := make(map[string]struct{}, len(message.ToolCalls))
			for index, call := range message.ToolCalls {
				if call.Extra != nil || call.Type != "" && call.Type != "function" ||
					call.Index != nil && *call.Index != index {
					return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
				}
				selection := agent.ToolSelection{
					ID:            call.ID,
					Name:          domain.ToolName(call.Function.Name),
					ArgumentsJSON: call.Function.Arguments,
				}
				if selection.Validate() != nil {
					return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
				}
				if _, duplicate := seen[selection.ID]; duplicate {
					return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
				}
				seen[selection.ID] = struct{}{}
			}
		case schema.Tool:
			name := domain.ToolName(message.ToolName)
			if !domain.ValidModelText(message.Content, domain.MaxModelInputMessageBytes, false) ||
				!domain.ValidModelToken(message.ToolCallID, domain.MaxModelToolCallIDBytes) ||
				len(message.ToolCalls) != 0 || !name.Valid() || !state.boundToolName(message.ToolCallID, name) {
				return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
			}
		default:
			return failedRuntime(domain.SafeErrorClassInvalidExternalResponse, safeInvalidModelResponse, nil)
		}
	}
	return nil
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
