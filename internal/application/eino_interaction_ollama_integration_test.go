//go:build integration

package application

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type conformanceUsage struct {
	mu                                                sync.Mutex
	responses, input, output                          int
	reasoningTokens, reasoningBlocks, encryptedBlocks int
}

func (usage *conformanceUsage) observe(message *schema.Message) {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil || message.ResponseMeta.Usage.TotalTokens == 0 {
		return
	}
	usage.mu.Lock()
	defer usage.mu.Unlock()
	usage.responses++
	usage.input += message.ResponseMeta.Usage.PromptTokens
	usage.output += message.ResponseMeta.Usage.CompletionTokens
}

type conformanceMeteredModel struct {
	base  einomodel.ToolCallingChatModel
	usage *conformanceUsage
}

func (model conformanceMeteredModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	bound, err := model.base.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return conformanceMeteredModel{base: bound, usage: model.usage}, nil
}

func (model conformanceMeteredModel) Generate(ctx context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.Message, error) {
	message, err := model.base.Generate(ctx, messages, options...)
	model.usage.observe(message)
	return message, err
}

func (model conformanceMeteredModel) Stream(ctx context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := model.base.Stream(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(stream, func(message *schema.Message) (*schema.Message, error) {
		model.usage.observe(message)
		return message, nil
	}), nil
}

func TestNativeOllamaInteractionConformanceSoak(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue || os.Getenv("KUPILOT_INTEGRATION_MODEL_TARGET") != liveModelTargetOllama || os.Getenv("KUPILOT_OLLAMA_CONFORMANCE_SOAK") != "three_rounds" {
		t.Skip("Local Ollama conformance requires explicit three-round authorization.")
	}
	configuration, credential, _ := loadLiveModelProfile(t, liveModelTargetOllama)
	if credential != nil {
		credential.Destroy()
		t.Fatal("Native Ollama must be credential-free.")
	}
	if configuration.Endpoint != "http://127.0.0.1:11434" || configuration.Model != "gpt-oss:20b" || os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD") != "0" {
		t.Fatal("Conformance requires the exact authorized loopback model and zero remote cost.")
	}
	versionCtx, cancelVersion := context.WithTimeout(context.Background(), 5*time.Second)
	request, err := http.NewRequestWithContext(versionCtx, http.MethodGet, configuration.Endpoint+"/api/version", nil)
	if err != nil {
		cancelVersion()
		t.Fatal("Version request construction failed.")
	}
	versionClient := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := versionClient.Do(request)
	if err != nil {
		cancelVersion()
		t.Fatal("Local Ollama version is unavailable.")
	}
	var version struct {
		Version string `json:"version"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&version)
	_ = response.Body.Close()
	cancelVersion()
	if err != nil || response.StatusCode != http.StatusOK || !domain.ValidModelToken(version.Version, 64) {
		t.Fatal("Local Ollama version was invalid.")
	}
	t.Logf("ollama_version=%s model=%s rounds=3 Kubernetes_calls=0 persistence=none", version.Version, configuration.Model)
	cases := []struct {
		name, question, mode string
		history              bool
		tools                int
	}{
		{"greeting", "Hello. Give a brief greeting without a Tool call.", "", false, 0},
		{"later_turn_identity", "What identity did you state earlier? Answer briefly without a Tool call.", "", true, 0},
		{"one_tool", "Use get_resource once for the namespaces resource type, name synthetic-ns, namespace null, detail summary. Then report one current observation using returned Evidence.", "", false, 1},
		{"list_final", "Use list_resources once for resource_type namespaces, namespace null, filters [], format list, limit 20. Then answer with current observations citing returned Evidence.", "", false, 1},
		{"list_inspect_final", "List Namespaces with list_resources, then inspect synthetic-ns with get_resource using resource_type namespaces, namespace null and detail summary. After both reads answer with current observations citing returned Evidence.", "", false, 2},
		{"empty_result", "Use list_resources once for resource_type namespaces, namespace null, filters [], format list, limit 20. Then report only accepted Evidence and any source limitations.", "empty", false, 1},
		{"partial_result", "Use list_resources once for resource_type namespaces, namespace null, filters [], format list, limit 20. Then report only accepted Evidence and any source limitations.", "partial", false, 1},
		{"verified_observation", "Inspect synthetic-ns once using get_resource for namespaces, namespace null, detail summary. State one current_observation citing its returned Evidence ID.", "", false, 1},
		{"retained_history", "Using the prior context, list Namespaces once and summarize the returned Evidence. Do not treat historic content as current Evidence.", "", true, 1},
		{"clarification", "Before doing any cluster read, ask which namespace I mean using one typed choice question with two short choices. Do not call a Tool.", "", false, 0},
	}
	// These are fixed independent measurements, not retries of failed outputs.
	for round := 1; round <= 3; round++ {
		for _, scenario := range cases {
			t.Run(scenario.name+"/round_"+string(rune('0'+round)), func(t *testing.T) {
				transport := newLiveBudgetTransport(domain.ModelProviderOllama, 4, 4*liveModelRequestByteCeiling)
				client, modelError := newModelClientForTest(configuration, nil, nil, transport)
				if modelError != nil {
					t.Fatal("Local model preflight failed.")
				}
				usage := new(conformanceUsage)
				client.model = conformanceMeteredModel{base: client.model, usage: usage}
				if client.structuredModel != nil {
					client.structuredModel = conformanceMeteredModel{base: client.structuredModel, usage: usage}
				}
				clock := newTestClock()
				tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
					if scenario.mode == "empty" {
						return emptyToolResult(t, call, clock.Now())
					}
					id := testEvidenceID
					if call.Name() == domain.ToolNameGetResource && scenario.tools == 2 {
						id = secondEvidenceID
					}
					result := successfulToolResult(t, call, id, clock.Now(), `{"items":[{"name":"synthetic-ns","phase":"Active"}]}`)
					result.Evidence[0].Resource = domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "synthetic-ns"}
					result.Evidence[0].Fact = "The synthetic Namespace is Active."
					if scenario.mode == "partial" {
						result.Status = domain.ToolResultStatusPartial
						result.Evidence[0].Partial = true
					}
					return result
				}}
				adapter, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
				if err != nil {
					client.close()
					t.Fatal("Local Agent construction failed.")
				}
				defer adapter.Close()
				limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
				if err != nil {
					t.Fatal("Budget construction failed.")
				}
				limits.ModelCalls = 4
				limits.ToolCalls = 3
				input, err := agent.NewRunInput(testRunID, testSessionID, testMessageID, scenario.question, domain.ClusterScope{Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now()}, nil, limits)
				if err != nil {
					t.Fatal("Local Agent input failed.")
				}
				if scenario.history {
					turns := []agent.ConversationTurn{
						{MessageID: "00000000-0000-7000-8000-000000008101", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 0, Role: domain.MessageRoleUser, Content: "Who are you?"},
						{MessageID: "00000000-0000-7000-8000-000000008102", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 1, Role: domain.MessageRoleAssistant, Content: "I am Kupilot, a local Kubernetes operations assistant."},
					}
					for index := range turns {
						turns[index].ContentHash = domain.MessageContentHash(turns[index].Content)
					}
					conversation, err := agent.NewConversationContext(testSessionID, turns, nil)
					if err != nil {
						t.Fatal("Retained fixture construction failed.")
					}
					input, err = agent.NewRunInputWithContext(input.RunID(), input.SessionID(), testMessageID, scenario.question, input.Scope(), nil, limits, conversation)
					if err != nil {
						t.Fatal("Retained fixture binding failed.")
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				started := time.Now()
				outcome := adapter.Run(ctx, input, newEventRecorder())
				elapsed := time.Since(started)
				status := "PASS"
				if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || len(tool.Calls()) != scenario.tools {
					status = "FAIL"
				}
				usage.mu.Lock()
				responses, in, out := usage.responses, usage.input, usage.output
				usage.mu.Unlock()
				reason := domain.RunTerminalFailed
				if outcome.Diagnosis != nil {
					reason = outcome.Diagnosis.Completeness.StopReason
				}
				t.Logf("structural=%s scenario=%s round=%d model_calls=%d Tool_calls=%d Kubernetes_calls=0 request_bytes=%d measured_responses=%d input_tokens=%d output_tokens=%d wall_ms=%d terminal=%s stage=%s reason=%s", status, scenario.name, round, transport.calls.Load(), len(tool.Calls()), transport.requestBytes.Load(), responses, in, out, elapsed.Milliseconds(), reason, outcome.Diagnostic.Stage(), outcome.Diagnostic)
				if status == "FAIL" {
					measurementReason := "runtime_rejected"
					if outcome.Status == domain.AgentRunStatusCompleted {
						measurementReason = "scenario_tool_count_mismatch"
					}
					t.Errorf("Conformance failed: measurement_reason=%s terminal_status=%s typed_reason=%s expected_Tools=%d actual_Tools=%d", measurementReason, outcome.Status, outcome.Diagnostic, scenario.tools, len(tool.Calls()))
				}
			})
		}
	}
}
