//go:build integration

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Meter only native metadata; no response or reasoning bytes leave memory.
type responsesDiagnosticTransport struct {
	base *liveBudgetTransport
	test *testing.T
}

func (transport responsesDiagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response == nil || response.StatusCode < 400 {
		return response, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
	_ = response.Body.Close()
	if readErr != nil || len(body) > 65536 {
		return nil, errors.New("Native diagnostic response exceeded its safe bound")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		// Fixed diagnostic categories only. The provider message stays in memory.
		for _, marker := range []string{"temperature", "reasoning", "stream", "store", "text.format", "response_format", "json_object", "tools", "parameter", "unsupported", "required"} {
			if strings.Contains(strings.ToLower(envelope.Error.Message+" "+envelope.Error.Param), marker) {
				transport.test.Logf("http_status=%d rejected_field_marker=%s", response.StatusCode, marker)
			}
		}
	}
	return response, nil
}

type meteredResponsesModel struct {
	base  einomodel.AgenticModel
	usage *conformanceUsage
}

func (model meteredResponsesModel) Generate(ctx context.Context, input []*schema.AgenticMessage, options ...einomodel.Option) (*schema.AgenticMessage, error) {
	message, err := model.base.Generate(ctx, input, options...)
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		model.usage.observe(&schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: message.ResponseMeta.TokenUsage}})
		model.usage.mu.Lock()
		model.usage.reasoningTokens += message.ResponseMeta.TokenUsage.CompletionTokensDetails.ReasoningTokens
		for _, block := range message.ContentBlocks {
			if block != nil && block.Reasoning != nil {
				model.usage.reasoningBlocks++
				if block.Reasoning.Signature != "" {
					model.usage.encryptedBlocks++
				}
			}
		}
		model.usage.mu.Unlock()
	}
	return message, err
}
func (model meteredResponsesModel) Stream(ctx context.Context, input []*schema.AgenticMessage, options ...einomodel.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	return model.base.Stream(ctx, input, options...)
}

// TestNativeResponsesAgentLive uses only the explicit local Agent profile and
// synthetic Tools. Each scenario is attempted once, with no endpoint discovery.
func TestNativeResponsesAgentLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != "authorized" || os.Getenv("KUPILOT_NATIVE_RESPONSES_LIVE") != "authorized" || os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD") != "3" {
		t.Skip("Native Responses requires explicit endpoint and estimated cost authorization.")
	}
	cfg, key, _ := loadLiveModelProfile(t, liveModelTargetPreferred)
	if key == nil || cfg.Model != "gpt-5.6-luna" {
		if key != nil {
			key.Destroy()
		}
		t.Skip("This pricing-bounded matrix requires the explicitly admitted model profile.")
	}
	defer key.Destroy()
	// This opt-in matrix explicitly selects native Responses; it is not a
	// production negotiation or a fallback after another model request.
	if os.Getenv("KUPILOT_INTEGRATION_PREFLIGHT_ONLY") == "1" {
		if cfg.APIProtocol != domain.ModelAPIProtocolResponses || cfg.StreamingRequired || cfg.Temperature != nil {
			t.Fatal("Local profile does not contain the tested native settings")
		}
		t.Log("PREFLIGHT ONLY: no model request")
		return
	}
	cfg.APIProtocol = domain.ModelAPIProtocolResponses
	cfg.StreamingRequired = false
	cfg.Temperature = nil
	cfg.MaxOutputTokens = 2048
	if cfg.ReasoningEffort == domain.ModelReasoningEffortNone {
		t.Fatal("The reasoning conformance matrix must not disable reasoning.")
	}
	transport := newLiveBudgetTransport(domain.ModelProviderOpenAI, 18, 18*liveModelRequestByteCeiling)
	defer transport.base.CloseIdleConnections()
	usage := new(conformanceUsage)
	cases := []struct {
		name, question, mode string
		history              bool
		tools                int
	}{
		{"greeting", "Hello. Give a brief greeting without a Tool call.", "", false, 0},
		{"later_turn_identity", "What identity did you state earlier? Answer briefly without a Tool call.", "", true, 0},
		{"one_tool", "Inspect synthetic-ns once using get_resource for namespaces, namespace null, detail summary. State one current_observation citing its returned Evidence ID.", "", false, 1},
		{"list_inspect_final", "List Namespaces with list_resources, then inspect synthetic-ns with get_resource using resource_type namespaces, namespace null and detail summary. After both reads answer with current observations citing returned Evidence.", "", false, 2},
		{"empty_result", "Use list_resources once for resource_type namespaces, namespace null, filters [], format list, limit 20. Then report only accepted Evidence and any source limitations.", "empty", false, 1},
		{"partial_result", "Use list_resources once for resource_type namespaces, namespace null, filters [], format list, limit 20. Then report only accepted Evidence and any source limitations.", "partial", false, 1},
		{"clarification", "Before doing any cluster read, ask which namespace I mean using one typed choice question with two short choices. Do not call a Tool.", "", false, 0},
		{"reasoning_with_tool", "Inspect synthetic-ns once using get_resource for namespaces, namespace null, detail summary. Then reason through why an Active Namespace does not imply every Pod is Running. Cite the accepted Namespace Evidence for the current observation and clearly label Pod explanations as hypothetical guidance.", "", false, 1},
	}
	for _, scenario := range cases {
		if !t.Run(scenario.name, func(t *testing.T) {
			secret, err := key.Clone()
			if err != nil {
				t.Fatal("Credential clone failed.")
			}
			scenarioConfig := cfg
			if scenario.name == "reasoning_with_tool" {
				scenarioConfig.ReasoningEffort = "medium"
			}
			client, failure := newModelClientForTest(scenarioConfig, &secret, nil, responsesDiagnosticTransport{base: transport, test: t})
			if failure != nil {
				secret.Destroy()
				t.Fatal("Native Responses local preflight failed.")
			}
			client.responsesModel = meteredResponsesModel{base: client.responsesModel, usage: usage}
			client.structuredResponses = meteredResponsesModel{base: client.structuredResponses, usage: usage}
			clock := newTestClock()
			tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
				if scenario.mode == "empty" {
					return emptyToolResult(t, call, clock.Now())
				}
				id := testEvidenceID
				if scenario.tools == 2 && call.Name() == domain.ToolNameGetResource {
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
			runtime, err := newEinoRuntime(runtimeConfig{tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now}, client)
			if err != nil {
				client.close()
				t.Fatal("Native composition failed.")
			}
			defer runtime.Close()
			limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
			if err != nil {
				t.Fatal(err)
			}
			limits.ModelCalls = 4
			limits.ToolCalls = 3
			scope := domain.ClusterScope{Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now()}
			conversation, err := agent.NewConversationContext(testSessionID, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.history {
				turns := []agent.ConversationTurn{{MessageID: "00000000-0000-7000-8000-000000008101", RunID: "00000000-0000-7000-8000-000000008100", Role: domain.MessageRoleUser, Content: "Who are you?"}, {MessageID: "00000000-0000-7000-8000-000000008102", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 1, Role: domain.MessageRoleAssistant, Content: "I am Kupilot, a local Kubernetes operations assistant."}}
				for i := range turns {
					turns[i].ContentHash = domain.MessageContentHash(turns[i].Content)
				}
				conversation, err = agent.NewConversationContext(testSessionID, turns, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			input, err := agent.NewRunInputWithContext(testRunID, testSessionID, testMessageID, scenario.question, scope, nil, limits, conversation)
			if err != nil {
				t.Fatal(err)
			}
			beforeCalls, beforeBytes := transport.calls.Load(), transport.requestBytes.Load()
			beforeInput, beforeOutput := usage.input, usage.output
			beforeReasoning, beforeBlocks, beforeEncrypted := usage.reasoningTokens, usage.reasoningBlocks, usage.encryptedBlocks
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			started := time.Now()
			events := newEventRecorder()
			outcome := runtime.Run(ctx, input, events)
			t.Logf("model=%s model_calls=%d Tool_calls=%d Kubernetes_calls=0 request_bytes=%d input_tokens=%d output_tokens=%d wall_ms=%d status=%s stage=%s reason=%s", cfg.Model, transport.calls.Load()-beforeCalls, len(tool.Calls()), transport.requestBytes.Load()-beforeBytes, usage.input-beforeInput, usage.output-beforeOutput, time.Since(started).Milliseconds(), outcome.Status, outcome.Diagnostic.Stage(), outcome.Diagnostic)
			t.Logf("reasoning_tokens=%d reasoning_blocks=%d encrypted_blocks=%d", usage.reasoningTokens-beforeReasoning, usage.reasoningBlocks-beforeBlocks, usage.encryptedBlocks-beforeEncrypted)
			if scenario.name == "reasoning_with_tool" && (usage.reasoningTokens == beforeReasoning || usage.encryptedBlocks == beforeEncrypted) {
				t.Error("Explicit reasoning did not provide measured tokens and an encrypted item.")
			}
			if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil || outcome.Validate(input) != nil || len(tool.Calls()) != scenario.tools {
				t.Fatal("Native Responses structural conformance failed; no retry will run.")
			}
			if scenario.name == "clarification" && outcome.Diagnosis.Completeness.StopReason != domain.RunTerminalNeedsUserInput {
				t.Fatal("The model did not return typed clarification.")
			}
			assertTerminalSequence(t, events.Events())
		}) {
			break
		}
	}
	// Published standard token prices are estimates for a compatible service,
	// not a claim about its actual invoice. Cached-input discounts are ignored.
	estimate := float64(usage.input)*0.20/1e6 + float64(usage.output)*1.20/1e6
	t.Logf("total_calls=%d request_bytes=%d measured_responses=%d input_tokens=%d output_tokens=%d estimated_usd=%.6f billing=unavailable", transport.calls.Load(), transport.requestBytes.Load(), usage.responses, usage.input, usage.output, estimate)
	if estimate > 3 {
		t.Fatal("Observed token usage exceeded the authorized estimate.")
	}
}
