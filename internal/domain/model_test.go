package domain

import (
	"strings"
	"testing"
)

func TestModelMessageValidationUsesDirectionalContentLimits(t *testing.T) {
	assistant := ModelMessage{
		Role:    ModelMessageRoleAssistant,
		Content: strings.Repeat("a", MaxModelMessageBytes),
	}
	if err := assistant.Validate(); err != nil {
		t.Fatalf("Validate(assistant at limit) error = %v", err)
	}
	assistant.Content += "a"
	if err := assistant.Validate(); err == nil {
		t.Fatal("Validate(assistant over limit) error = nil")
	}

	for _, role := range []ModelMessageRole{ModelMessageRoleSystem, ModelMessageRoleUser} {
		message := ModelMessage{Role: role, Content: strings.Repeat("a", MaxModelInputMessageBytes+1)}
		if err := message.Validate(); err == nil {
			t.Fatalf("Validate(%s over input limit) error = nil", role)
		}
	}
	tool := ModelMessage{
		Role: ModelMessageRoleTool, Content: strings.Repeat("a", MaxModelInputMessageBytes+1), ToolCallID: "call-1",
	}
	if err := tool.Validate(); err == nil {
		t.Fatal("Validate(Tool over input limit) error = nil")
	}
}

func TestModelRequestMessageCountCoversTheHardRunBudget(t *testing.T) {
	messages := make([]ModelMessage, maxModelMessages)
	for index := range messages {
		messages[index] = ModelMessage{Role: ModelMessageRoleUser, Content: "bounded"}
	}
	names := []ToolName{
		ToolNameGetResource,
		ToolNameListResources,
		ToolNameGetEvents,
		ToolNameGetPodLogs,
		ToolNameGetPreviousPodLogs,
		ToolNameGetRelatedResources,
		ToolNameGetClusterOverview,
	}
	tools := make([]ModelToolSpecification, len(names))
	for index, name := range names {
		tools[index] = ModelToolSpecification{
			Name: name, Version: "test-v1", Description: "Exercise the fixed model request boundary.",
			InputSchemaJSON: `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
		}
	}
	request := ModelRequest{
		ID: "00000000-0000-7000-8000-000000000111", Messages: messages, Tools: tools,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate(at derived message limit) error = %v", err)
	}
	request.Messages = append(request.Messages, ModelMessage{Role: ModelMessageRoleUser, Content: "one over"})
	if err := request.Validate(); err == nil {
		t.Fatal("Validate(over derived message limit) error = nil")
	}
}

func TestModelToolSpecificationRequiresStrictObjectSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		valid  bool
	}{
		{
			name:   "nullable field is required",
			schema: `{"additionalProperties":false,"properties":{"optional":{"type":["string","null"]}},"required":["optional"],"type":"object"}`,
			valid:  true,
		},
		{
			name:   "empty strict object",
			schema: `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
			valid:  true,
		},
		{
			name:   "missing root required",
			schema: `{"additionalProperties":false,"properties":{"value":{"type":"string"}},"type":"object"}`,
		},
		{
			name:   "nullable root object",
			schema: `{"additionalProperties":false,"properties":{},"required":[],"type":["object","null"]}`,
		},
		{
			name:   "incomplete root required",
			schema: `{"additionalProperties":false,"properties":{"optional":{"type":["string","null"]},"value":{"type":"string"}},"required":["value"],"type":"object"}`,
		},
		{
			name:   "missing nested required",
			schema: `{"additionalProperties":false,"properties":{"resource":{"additionalProperties":false,"properties":{"name":{"type":"string"}},"type":"object"}},"required":["resource"],"type":"object"}`,
		},
		{
			name:   "duplicate required field",
			schema: `{"additionalProperties":false,"properties":{"value":{"type":"string"}},"required":["value","value"],"type":"object"}`,
		},
		{
			name:   "unsupported unique items",
			schema: `{"additionalProperties":false,"properties":{"values":{"items":{"type":"string"},"type":"array","uniqueItems":true}},"required":["values"],"type":"object"}`,
		},
	}

	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			specification := ModelToolSpecification{
				Name:            ToolNameGetResource,
				Version:         "test-v1",
				Description:     "Exercise strict model Tool schema validation.",
				InputSchemaJSON: current.schema,
			}
			if got := specification.Validate() == nil; got != current.valid {
				t.Fatalf("ModelToolSpecification.Validate() success = %t, want %t", got, current.valid)
			}
		})
	}
}
