package einoadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// This full-Agent regression checks the final native wire request against the
// independent code-owned catalog. No network transport is constructed.
func TestNativeOllamaRequestFidelityAudit(t *testing.T) {
	clock := newTestClock()
	configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
	configuration.Model = "gpt-oss:20b"
	configuration.ReasoningEffort = domain.ModelReasoningEffortOmitted
	transport := &requestAuditTransport{}
	client, modelError := newModelClientForTest(configuration, nil, nil, transport)
	if modelError != nil {
		t.Fatal("request_audit/client_setup_failed")
	}
	tool := new(recordingTool)
	adapter, err := newAdapter(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
	if err != nil {
		client.close()
		t.Fatal("request_audit/agent_setup_failed")
	}
	t.Cleanup(adapter.Close)
	limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
	if err != nil {
		t.Fatal("request_audit/budget_setup_failed")
	}
	limits.ModelCalls = 1
	limits.ToolCalls = 1
	var history []agent.ConversationTurn
	questions := []string{"\u4f60\u597d", "\u5f53\u524d\u96c6\u7fa4\u6709\u54ea\u4e9b ns"}
	runIDs := []domain.AgentRunID{"00000000-0000-7000-8000-000000008100", "00000000-0000-7000-8000-000000008200"}
	messageIDs := []domain.MessageID{"00000000-0000-7000-8000-000000008101", "00000000-0000-7000-8000-000000008201"}
	for step, question := range questions {
		conversation, contextErr := agent.NewConversationContext(testSessionID, history, nil)
		if contextErr != nil {
			t.Fatal("request_audit/context_setup_failed")
		}
		input, inputErr := agent.NewRunInputWithContext(runIDs[step], testSessionID, messageIDs[step], question, domain.ClusterScope{Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now()}, nil, limits, conversation)
		if inputErr != nil {
			t.Fatal("request_audit/input_setup_failed")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		outcome := adapter.Run(ctx, input, newEventRecorder())
		cancel()
		if step == 0 {
			if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Diagnosis.AnswerMarkdown != "Hello." {
				t.Fatal("request_audit/scripted_greeting_rejected")
			}
			history = []agent.ConversationTurn{
				{MessageID: "00000000-0000-7000-8000-000000008101", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 0, Role: domain.MessageRoleUser, Content: question},
				{MessageID: "00000000-0000-7000-8000-000000008102", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 1, Role: domain.MessageRoleAssistant, Content: outcome.Diagnosis.AnswerMarkdown},
			}
			for index := range history {
				history[index].ContentHash = domain.MessageContentHash(history[index].Content)
			}
		} else if outcome.Status != domain.AgentRunStatusFailed || outcome.Diagnosis != nil || outcome.Diagnostic != domain.FailureProviderReported || outcome.SafeMessage != domain.FailureProviderReported.SafeMessage() {
			t.Fatal("request_audit/scripted_provider_failure_misclassified")
		}
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.calls != 2 || len(transport.requests) != 2 || len(tool.Calls()) != 0 {
		t.Fatal("request_audit/call_count_mismatch")
	}
	t.Logf("fixture_model_calls=2 live_model_calls=0 Tool_calls=0 Kubernetes_calls=0 persistence=none request_bytes=%d,%d", transport.requests[0].bytes, transport.requests[1].bytes)
	for index, request := range transport.requests {
		t.Run([]string{"greeting", "retained_query"}[index], func(t *testing.T) {
			auditCheck(t, "request_settings", request.Model == configuration.Model && request.Stream && request.Options.Temperature != nil && float32(*request.Options.Temperature) == float32(configuration.Temperature) && request.Options.NumPredict == 2048 && string(request.Format) == `"json"` && len(request.Think) == 0)
			auditCheck(t, "history_order_and_current_question_once", requestAuditHistory(request.Messages, questions[:index+1]))
			specifications := agent.ToolSpecifications()
			if len(request.Tools) != len(specifications) {
				t.Fatal("request_audit/tool_catalog_count_mismatch")
			}
			for toolIndex, specification := range specifications {
				t.Run(string(specification.Name), func(t *testing.T) {
					info, infoErr := toolInfo(specification)
					if infoErr != nil {
						t.Fatal("request_audit/tool_info_failed")
					}
					before, schemaErr := info.ParamsOneOf.ToJSONSchema()
					encoded, marshalErr := json.Marshal(before)
					auditCheck(t, "before_native_conversion", schemaErr == nil && marshalErr == nil && sameJSON(string(encoded), specification.InputSchemaJSON))
					wireTool := request.Tools[toolIndex]
					auditCheck(t, "name_and_description", wireTool.Type == "function" && wireTool.Function.Name == string(specification.Name) && wireTool.Function.Description == specification.Description)
					var expected requestAuditSchema
					if json.Unmarshal([]byte(specification.InputSchemaJSON), &expected) != nil {
						t.Fatal("request_audit/catalog_decode_failed")
					}
					for _, failure := range requestAuditSchemaDifferences(expected, wireTool.Function.Parameters, "parameters") {
						t.Errorf("request_serialization/tool_schema_constraint_lost field=%s", failure)
					}
				})
			}
		})
	}
}

type requestAuditMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type requestAuditSchema struct {
	Type                 json.RawMessage               `json:"type"`
	Enum                 json.RawMessage               `json:"enum"`
	Required             []string                      `json:"required"`
	Properties           map[string]requestAuditSchema `json:"properties"`
	Items                *requestAuditSchema           `json:"items"`
	AdditionalProperties json.RawMessage               `json:"additionalProperties"`
	Minimum              json.RawMessage               `json:"minimum"`
	Maximum              json.RawMessage               `json:"maximum"`
	MinLength            json.RawMessage               `json:"minLength"`
	MaxLength            json.RawMessage               `json:"maxLength"`
	MinItems             json.RawMessage               `json:"minItems"`
	MaxItems             json.RawMessage               `json:"maxItems"`
	Pattern              json.RawMessage               `json:"pattern"`
}

type requestAuditWire struct {
	Model    string                `json:"model"`
	Stream   bool                  `json:"stream"`
	Format   json.RawMessage       `json:"format"`
	Think    json.RawMessage       `json:"think"`
	Messages []requestAuditMessage `json:"messages"`
	Options  struct {
		Temperature *float64 `json:"temperature"`
		NumPredict  int      `json:"num_predict"`
	} `json:"options"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string             `json:"name"`
			Description string             `json:"description"`
			Parameters  requestAuditSchema `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	bytes int
}

type requestAuditTransport struct {
	mu       sync.Mutex
	calls    int
	requests []requestAuditWire
}

func (transport *requestAuditTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	defer func() { _ = request.Body.Close() }()
	if transport.calls > 2 || request.Context().Err() != nil || request.Method != http.MethodPost || request.URL.String() != "http://127.0.0.1:11434/api/chat" || request.Header.Get("Authorization") != "" {
		return nil, errors.New("request audit transport denied")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, domain.MaxModelRequestBytes+1))
	defer clear(body)
	var wire requestAuditWire
	if err != nil || len(body) > domain.MaxModelRequestBytes || int64(len(body)) != request.ContentLength || json.Unmarshal(body, &wire) != nil {
		return nil, errors.New("request audit payload rejected")
	}
	wire.bytes = len(body)
	transport.requests = append(transport.requests, wire)
	response := `{"error":"synthetic provider rejection"}` + "\n"
	if transport.calls == 1 {
		content, marshalErr := json.Marshal(strictTestDiagnosis(`{"answer_markdown":"Hello.","evidence_citations":[],"proposed_actions":[]}`))
		if marshalErr != nil {
			return nil, errors.New("request audit fixture rejected")
		}
		response = `{"model":"gpt-oss:20b","message":{"role":"assistant","content":` + string(content) + `},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":8}` + "\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
}

func auditCheck(t *testing.T, code string, passed bool) {
	t.Helper()
	if !passed {
		t.Errorf("request_serialization/%s result=FAIL", code)
		return
	}
	t.Logf("request_serialization/%s result=PASS", code)
}

func requestAuditHistory(messages []requestAuditMessage, questions []string) bool {
	if len(messages) != 2*len(questions) || messages[0].Role != "system" || messages[0].Content == "" {
		return false
	}
	for index, question := range questions {
		if messages[index*2+1].Role != "user" || messages[index*2+1].Content != question {
			return false
		}
	}
	if len(questions) == 1 {
		return true
	}
	history, err := agent.DecodeDiagnosticResponse(messages[2].Content)
	return messages[2].Role == "assistant" && err == nil && history.AnswerMarkdown == "Hello." && len(history.ConfirmedFacts) == 0 && len(history.RecommendedActions) == 0 && len(history.ClaimCoverage) == 0 && history.Clarification == nil
}

func requestAuditSchemaDifferences(want, got requestAuditSchema, path string) []string {
	var differences []string
	for _, field := range []struct {
		name      string
		want, got json.RawMessage
	}{
		{"type", want.Type, got.Type}, {"enum", want.Enum, got.Enum},
		{"additionalProperties", want.AdditionalProperties, got.AdditionalProperties},
		{"minimum", want.Minimum, got.Minimum}, {"maximum", want.Maximum, got.Maximum},
		{"minLength", want.MinLength, got.MinLength}, {"maxLength", want.MaxLength, got.MaxLength},
		{"minItems", want.MinItems, got.MinItems}, {"maxItems", want.MaxItems, got.MaxItems},
		{"pattern", want.Pattern, got.Pattern},
	} {
		if len(field.want) == 0 && len(field.got) == 0 {
			continue
		}
		if !sameJSON(string(field.want), string(field.got)) {
			differences = append(differences, path+"."+field.name)
		}
	}
	if !reflect.DeepEqual(want.Required, got.Required) {
		differences = append(differences, path+".required")
	}
	if (want.Items == nil) != (got.Items == nil) {
		differences = append(differences, path+".items")
	} else if want.Items != nil {
		differences = append(differences, requestAuditSchemaDifferences(*want.Items, *got.Items, path+".items")...)
	}
	if len(want.Properties) != len(got.Properties) {
		differences = append(differences, path+".properties")
	}
	for key, expected := range want.Properties {
		actual, exists := got.Properties[key]
		if !exists {
			differences = append(differences, path+".properties."+key)
			continue
		}
		differences = append(differences, requestAuditSchemaDifferences(expected, actual, path+".properties."+key)...)
	}
	sort.Strings(differences)
	return differences
}

func TestNativeOllamaRequestAuditComparator(t *testing.T) {
	for _, specification := range agent.ToolSpecifications() {
		var expected requestAuditSchema
		if json.Unmarshal([]byte(specification.InputSchemaJSON), &expected) != nil || len(requestAuditSchemaDifferences(expected, expected, "parameters")) != 0 {
			t.Fatal("equal catalog schema was rejected")
		}
	}
	for _, scenario := range []struct {
		name, wire string
		want       []string
	}{
		{"equal", `{"type":"array","items":{"type":"string"}}`, nil},
		{"items_missing", `{"type":"array"}`, []string{"parameters.items"}},
		{"nested_type_changed", `{"type":"array","items":{"type":"integer"}}`, []string{"parameters.items.type"}},
		{"extra_constraint", `{"type":"array","items":{"type":"string"},"maxItems":1}`, []string{"parameters.maxItems"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var want, got requestAuditSchema
			if json.Unmarshal([]byte(`{"type":"array","items":{"type":"string"}}`), &want) != nil || json.Unmarshal([]byte(scenario.wire), &got) != nil {
				t.Fatal("audit comparator fixture invalid")
			}
			if !reflect.DeepEqual(requestAuditSchemaDifferences(want, got, "parameters"), scenario.want) {
				t.Fatal("audit comparator decision mismatch")
			}
		})
	}
}
