package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// These fixtures verify delivery, not a scripted model's language abilities.
func testMultilingualInput(t *testing.T, clock *testClock, count int) agent.RunInput {
	t.Helper()
	turns := testConversation(t, count).Turns()
	turns[count-2].Content = "\u8bf7\u68c0\u67e5 Pod sample-pod \u7684 Ready \u72b6\u6001\u3002"
	turns[count-1].Content = "\u73fe\u5728\u306e\u72b6\u614b\u306f\u78ba\u8a8d\u3067\u304d\u307e\u305b\u3093\u3002"
	for i := count - 2; i < count; i++ {
		turns[i].ContentHash = domain.MessageContentHash(turns[i].Content)
	}
	conversation, err := agent.NewConversationContext(testSessionID, turns, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := testInput(t, clock, agent.DefaultRunBudgetLimits())
	input, err := agent.NewRunInputWithContext(testRunID, testSessionID, testMessageID,
		"\u8bf7\u91cd\u65b0\u6838\u5bf9 Pod sample-pod \u7684\u5f53\u524d\u72b6\u6001\uff0c\u628a\u73b0\u5728\u548c\u4e4b\u524d\u5206\u5f00\u8bb2\u3002",
		base.Scope(), base.Resource(), base.BudgetLimits(), conversation)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func assertResponseLanguageInput(t *testing.T, messages []*schema.Message, input agent.RunInput) {
	t.Helper()
	prompt, err := agent.BuildSystemPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[0].Role != schema.System || messages[0].Content != prompt {
		t.Fatal("The shared response-language instruction was lost or changed")
	}
	questions := 0
	for _, message := range messages {
		if message.Role == schema.User && message.Content == input.Question() {
			questions++
		}
	}
	if questions != 1 {
		t.Fatal("The current user language cue was omitted, changed or duplicated")
	}
}

func assertResponseLanguageNativeRequest(t *testing.T, body string, input agent.RunInput) {
	t.Helper()
	var wire struct {
		Input []struct {
			Role    schema.RoleType `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatal(err)
	}
	var messages []*schema.Message
	for _, item := range wire.Input {
		if item.Role == schema.System || item.Role == schema.User {
			var content string
			if err := json.Unmarshal(item.Content, &content); err != nil {
				t.Fatal(err)
			}
			messages = append(messages, &schema.Message{Role: item.Role, Content: content})
		}
	}
	assertResponseLanguageInput(t, messages, input)
}

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
