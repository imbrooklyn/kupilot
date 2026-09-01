package einoadapter

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestInitialMessagesMapRunInputDirectlyToEino(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())

	messages, err := newInitialMessages(input)
	if err != nil {
		t.Fatalf("newInitialMessages() error = %v", err)
	}
	if len(messages) != 2 || messages[0].Role != schema.System || messages[1].Role != schema.User {
		t.Fatalf("initial Eino messages = %#v", messages)
	}
	if messages[1].Content != input.Question() || strings.Contains(messages[0].Content, input.Question()) ||
		!strings.Contains(messages[0].Content, agent.SystemPromptVersion) {
		t.Fatalf("initial Eino message content = %#v", messages)
	}
	state := &runState{boundCalls: make(map[string]*boundExecution)}
	if err := state.validateConversation(messages); err != nil {
		t.Fatalf("validateConversation(initial) error = %v", err)
	}
}

func TestConversationValidationRejectsInvalidEinoState(t *testing.T) {
	state := &runState{boundCalls: make(map[string]*boundExecution)}
	position := 0
	validCall := schema.ToolCall{
		Index: &position,
		ID:    "call-1",
		Type:  "function",
		Function: schema.FunctionCall{
			Name:      string(domain.ToolNameGetResource),
			Arguments: `{"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`,
		},
	}
	secondPosition := 1
	duplicateCall := validCall
	duplicateCall.Index = &secondPosition
	tests := []struct {
		name     string
		messages []*schema.Message
	}{
		{name: "empty conversation"},
		{name: "oversized input", messages: []*schema.Message{schema.UserMessage(strings.Repeat("x", domain.MaxModelInputMessageBytes+1))}},
		{name: "unsafe assistant text", messages: []*schema.Message{schema.AssistantMessage("unsafe"+string(rune(0x202e))+"text", nil)}},
		{name: "duplicate Tool identity", messages: []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{validCall, duplicateCall})}},
		{name: "unbound Tool result", messages: []*schema.Message{schema.ToolMessage("{}", "call-1", schema.WithToolName(string(domain.ToolNameGetResource)))}},
		{name: "provider metadata in conversation", messages: []*schema.Message{{Role: schema.User, Content: "Inspect.", ResponseMeta: &schema.ResponseMeta{}}}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			if err := state.validateConversation(current.messages); err == nil {
				t.Fatal("validateConversation() error = nil")
			}
		})
	}

	overLimit := make([]*schema.Message, maxConversationMessages+1)
	for index := range overLimit {
		overLimit[index] = schema.UserMessage("bounded")
	}
	if err := state.validateConversation(overLimit); err == nil {
		t.Fatal("validateConversation(over limit) error = nil")
	}
}
