package openaicompat

import (
	"context"
	"encoding/json"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

type requestPayload struct {
	Model           string               `json:"model"`
	Messages        []messagePayload     `json:"messages"`
	Tools           []toolPayload        `json:"tools"`
	Stream          bool                 `json:"stream"`
	StreamOptions   streamOptionsPayload `json:"stream_options"`
	Temperature     float64              `json:"temperature"`
	MaxOutputTokens int                  `json:"max_tokens"`
}

type streamOptionsPayload struct {
	IncludeUsage bool `json:"include_usage"`
}

type messagePayload struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallPayload `json:"tool_calls,omitempty"`
}

type toolCallPayload struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function toolFunctionPayload `json:"function"`
}

type toolFunctionPayload struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolPayload struct {
	Type     string              `json:"type"`
	Function toolContractPayload `json:"function"`
}

type toolContractPayload struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

func marshalWireRequest(configuration domain.ModelConfiguration, request domain.ModelRequest) ([]byte, error) {
	messages := make([]messagePayload, 0, len(request.Messages))
	for _, message := range request.Messages {
		mapped := messagePayload{
			Role:       string(message.Role),
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			mapped.ToolCalls = append(mapped.ToolCalls, toolCallPayload{
				ID:   call.ID,
				Type: "function",
				Function: toolFunctionPayload{
					Name:      string(call.Name),
					Arguments: call.ArgumentsJSON,
				},
			})
		}
		messages = append(messages, mapped)
	}

	tools := make([]toolPayload, 0, len(request.Tools))
	for _, specification := range request.Tools {
		tools = append(tools, toolPayload{
			Type: "function",
			Function: toolContractPayload{
				Name:        string(specification.Name),
				Description: specification.Description,
				Parameters:  json.RawMessage(specification.InputSchemaJSON),
				Strict:      true,
			},
		})
	}

	return json.Marshal(requestPayload{
		Model:           configuration.Model,
		Messages:        messages,
		Tools:           tools,
		Stream:          true,
		StreamOptions:   streamOptionsPayload{IncludeUsage: true},
		Temperature:     configuration.Temperature,
		MaxOutputTokens: configuration.MaxOutputTokens,
	})
}

func mapEinoMessages(messages []domain.ModelMessage) []*schema.Message {
	mapped := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		current := &schema.Message{
			Role:       mapEinoRole(message.Role),
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		for index, call := range message.ToolCalls {
			toolIndex := index
			current.ToolCalls = append(current.ToolCalls, schema.ToolCall{
				Index: &toolIndex,
				ID:    call.ID,
				Type:  "function",
				Function: schema.FunctionCall{
					Name:      string(call.Name),
					Arguments: call.ArgumentsJSON,
				},
			})
		}
		mapped = append(mapped, current)
	}
	return mapped
}

func mapEinoRole(role domain.ModelMessageRole) schema.RoleType {
	switch role {
	case domain.ModelMessageRoleSystem:
		return schema.System
	case domain.ModelMessageRoleUser:
		return schema.User
	case domain.ModelMessageRoleAssistant:
		return schema.Assistant
	case domain.ModelMessageRoleTool:
		return schema.Tool
	default:
		return schema.RoleType(role)
	}
}

func fixedRequestPayload(body []byte) einoopenai.RequestPayloadModifier {
	return func(context.Context, []*schema.Message, []byte) ([]byte, error) {
		return append([]byte(nil), body...), nil
	}
}
