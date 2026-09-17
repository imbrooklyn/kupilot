//go:build integration

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// These are independent first-call measurements, not an Agent loop or retries.
// The reduced cases are test-only ablations; production keeps its full contract.
func TestNativeOllamaSelectionProbeLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != "authorized" ||
		os.Getenv("KUPILOT_INTEGRATION_MODEL_TARGET") != "ollama" ||
		os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD") != "0" ||
		os.Getenv("KUPILOT_OLLAMA_SELECTION_PROBE") != "authorized" {
		t.Skip("Explicit authorization is required for nine fixed local first-call probes.")
	}
	configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", 3*time.Minute)
	configuration.Model = "gpt-oss:20b"
	configuration.ReasoningEffort = domain.ModelReasoningEffortOmitted
	input := selectionProbeInput(t)
	budget := newLiveBudgetTransport(domain.ModelProviderOllama, 9, 9*domain.MaxModelRequestBytes)
	// Never inherit proxy routing for this explicitly loopback-only experiment.
	budget.base.Proxy = nil
	defer budget.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 27*time.Minute)
	defer cancel()
	for round := 1; round <= 3; round++ {
		for _, scenario := range []string{"full", "single_tool", "without_final_protocol"} {
			t.Run(fmt.Sprintf("%d_%s", round, scenario), func(t *testing.T) {
				messages := selectionProbeMessages(t, input, scenario)
				transport := &selectionProbeTransport{base: budget, singleTool: scenario == "single_tool"}
				client, setupFailure := newModelClientForTest(configuration, nil, nil, transport)
				if setupFailure != nil {
					t.Fatal("probe_setup/client_rejected")
				}
				defer client.close()
				bound, err := client.withTools(fixtureToolInfos(t))
				if err != nil {
					t.Fatal("probe_setup/catalog_rejected")
				}
				before := budget.calls.Load()
				started := time.Now()
				callContext, callCancel := context.WithTimeout(ctx, 3*time.Minute)
				message, modelFailure := client.stream(callContext, "00000000-0000-7000-8000-000000009801", bound, messages, nil)
				callCancel()
				elapsed := time.Since(started)
				reason, measurement := selectionProbeOutcome(input, message, modelFailure, transport.singleTool)
				usage := "unavailable"
				if message != nil && message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
					observed := message.ResponseMeta.Usage
					usage = fmt.Sprintf("input:%d,output:%d,total:%d", observed.PromptTokens, observed.CompletionTokens, observed.TotalTokens)
				}
				result := "PASS"
				if measurement != "valid_bound_tool_selection" || reason != "" {
					result = "FAIL"
				}
				t.Logf("PROBE round=%d scenario=%s result=%s stage=%s reason=%s measurement=%s model_calls=%d Tool_calls=0 Kubernetes_calls=0 request_bytes=%d usage=%s wall_ms=%d provider_tool_parse=%t provider_unexpected_end=%t",
					round, scenario, result, reason.Stage(), reason, measurement, budget.calls.Load()-before, transport.requestBytes, usage, elapsed.Milliseconds(), transport.toolParse, transport.unexpectedEnd)
				if budget.calls.Load()-before != 1 || transport.calls != 1 {
					t.Fatal("probe_invariant/exactly_one_request_required")
				}
				if result != "PASS" {
					t.Error("probe_measurement/selection_unavailable")
				}
			})
		}
	}
	if budget.calls.Load() != 9 {
		t.Error("probe_invariant/nine_independent_requests_required")
	}
	t.Logf("PROBE_TOTAL model_calls=%d Tool_calls=0 Kubernetes_calls=0 request_bytes=%d persistence=none retries=0", budget.calls.Load(), budget.requestBytes.Load())
}

func selectionProbeInput(t *testing.T) agent.RunInput {
	t.Helper()
	clock := newTestClock()
	history := []agent.ConversationTurn{
		{MessageID: "00000000-0000-7000-8000-000000008101", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 0, Role: domain.MessageRoleUser, Content: "\u4f60\u597d"},
		{MessageID: "00000000-0000-7000-8000-000000008102", RunID: "00000000-0000-7000-8000-000000008100", RunSequence: 1, Role: domain.MessageRoleAssistant, Content: "Hello."},
	}
	for index := range history {
		history[index].ContentHash = domain.MessageContentHash(history[index].Content)
	}
	conversation, err := agent.NewConversationContext(testSessionID, history, nil)
	if err != nil {
		t.Fatal("probe_setup/history_rejected")
	}
	limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
	if err != nil {
		t.Fatal("probe_setup/budget_rejected")
	}
	limits.ModelCalls, limits.ToolCalls = 1, 1
	input, err := agent.NewRunInputWithContext(testRunID, testSessionID, testMessageID, "\u5f53\u524d\u96c6\u7fa4\u6709\u54ea\u4e9b ns", domain.ClusterScope{Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now()}, nil, limits, conversation)
	if err != nil {
		t.Fatal("probe_setup/input_rejected")
	}
	return input
}

func selectionProbeMessages(t *testing.T, input agent.RunInput, scenario string) []*schema.Message {
	t.Helper()
	messages, err := newInitialMessages(input)
	if err != nil || len(messages) != 4 {
		t.Fatal("probe_setup/message_assembly_rejected")
	}
	if scenario == "without_final_protocol" {
		prompt := messages[0].Content
		start := strings.Index(prompt, "Final response protocol:\n")
		end := strings.Index(prompt, "\n\nThe selected ResourceRef is an unverified candidate.")
		if start < 0 || end <= start {
			t.Fatal("probe_setup/protocol_section_not_unique")
		}
		messages[0] = schema.SystemMessage(prompt[:start] + prompt[end:])
	}
	return messages
}

func selectionProbeOutcome(input agent.RunInput, message *schema.Message, failure *domain.ModelError, singleTool bool) (domain.InteractionFailure, string) {
	if failure != nil {
		return runtimeDiagnostic(runtimeFailureFromModel(failure)), "model_boundary_rejected"
	}
	if message == nil || message.ResponseMeta == nil {
		return domain.FailureStreamMalformed, "model_boundary_rejected"
	}
	if len(message.ToolCalls) == 0 {
		// A first-call experiment cannot label an unvalidated final or clarification
		// as an Agent failure. It only records that this measurement selected no Tool.
		return "", "no_tool_selected"
	}
	if message.ResponseMeta.FinishReason != "tool_calls" || len(message.ToolCalls) != 1 {
		return "", "unexpected_first_call_shape"
	}
	call := message.ToolCalls[0]
	selection := agent.ToolSelection{ID: call.ID, Name: domain.ToolName(call.Function.Name), ArgumentsJSON: call.Function.Arguments}
	if selection.Validate() != nil || call.Index == nil || *call.Index != 0 || call.Extra != nil {
		return domain.FailureToolSelection, "model_boundary_rejected"
	}
	if singleTool && selection.Name != domain.ToolNameGetClusterOverview {
		return domain.FailureToolPolicy, "unoffered_tool_selected"
	}
	_, err := agent.BindToolCall(input, "00000000-0000-7000-8000-000000009802", selection)
	switch {
	case err == nil:
		return "", "valid_bound_tool_selection"
	case errors.Is(err, agent.ErrToolArgumentsRejected):
		return domain.FailureToolSelection, "model_boundary_rejected"
	case errors.Is(err, agent.ErrSensitiveModelTextBlocked):
		return domain.FailureSensitiveOutput, "model_boundary_rejected"
	case errors.Is(err, agent.ErrToolPolicyDenied):
		return domain.FailureToolPolicy, "model_boundary_rejected"
	default:
		return domain.FailureInternal, "model_boundary_rejected"
	}
}

type selectionProbeTransport struct {
	base                     http.RoundTripper
	singleTool               bool
	calls, requestBytes      int
	toolParse, unexpectedEnd bool
}

func (transport *selectionProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	if transport.calls != 1 || request.URL.String() != "http://127.0.0.1:11434/api/chat" || request.Method != http.MethodPost || request.Header.Get("Authorization") != "" {
		return nil, errors.New("probe request boundary rejected")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, domain.MaxModelRequestBytes+1))
	_ = request.Body.Close()
	defer clear(body)
	if err != nil || len(body) > domain.MaxModelRequestBytes || int64(len(body)) != request.ContentLength {
		return nil, errors.New("probe request body rejected")
	}
	var wire requestAuditWire
	if json.Unmarshal(body, &wire) != nil || wire.Model != "gpt-oss:20b" || wire.Options.Temperature == nil || float32(*wire.Options.Temperature) != float32(0.1) || wire.Options.NumPredict != 2048 || string(wire.Format) != `"json"` || len(wire.Think) != 0 || !wire.Stream || len(wire.Tools) != len(agent.ToolSpecifications()) {
		return nil, errors.New("probe request settings rejected")
	}
	selected := -1
	for index, specification := range agent.ToolSpecifications() {
		var expected requestAuditSchema
		if json.Unmarshal([]byte(specification.InputSchemaJSON), &expected) != nil || len(requestAuditSchemaDifferences(expected, wire.Tools[index].Function.Parameters, "parameters")) != 0 || wire.Tools[index].Function.Name != string(specification.Name) {
			return nil, errors.New("probe request schema rejected")
		}
		if specification.Name == domain.ToolNameGetClusterOverview {
			selected = index
		}
	}
	if transport.singleTool {
		start, end, memberErr := nativeJSONMember(body, "tools")
		var tools []json.RawMessage
		if memberErr != nil || start < 0 || selected < 0 || json.Unmarshal(body[start:end], &tools) != nil {
			return nil, errors.New("probe reduced catalog rejected")
		}
		// Only reduce the validated code-owned catalog in this test transport.
		reduced := append([]byte(nil), body[:start]...)
		reduced = append(reduced, '[')
		reduced = append(reduced, tools[selected]...)
		reduced = append(reduced, ']')
		reduced = append(reduced, body[end:]...)
		clear(body)
		body = reduced
		defer clear(reduced)
	}
	request = request.Clone(request.Context())
	request.Body = io.NopCloser(bytes.NewReader(bytes.Clone(body)))
	request.ContentLength = int64(len(body))
	request.GetBody = nil
	transport.requestBytes = len(body)
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		response.Body = &selectionProbeBody{ReadCloser: response.Body, observation: transport}
	}
	return response, err
}

// Error text is examined only in this opt-in test and never drives runtime
// policy. Only two fixed booleans escape; neither model nor provider bytes do.
type selectionProbeBody struct {
	io.ReadCloser
	observation *selectionProbeTransport
	pending     []byte
}

func (body *selectionProbeBody) Read(target []byte) (int, error) {
	n, err := body.ReadCloser.Read(target)
	for _, value := range target[:n] {
		if value == '\n' {
			body.inspect()
		} else if len(body.pending) < domain.MaxModelStreamBytes {
			body.pending = append(body.pending, value)
		}
	}
	if err == io.EOF {
		body.inspect()
	}
	return n, err
}

func (body *selectionProbeBody) inspect() {
	var record struct{ Error string }
	if json.Unmarshal(body.pending, &record) == nil && record.Error != "" {
		body.observation.toolParse = strings.HasPrefix(record.Error, "error parsing tool call:")
		body.observation.unexpectedEnd = body.observation.toolParse && strings.Contains(record.Error, "unexpected end of JSON input")
	}
	clear(body.pending)
	body.pending = body.pending[:0]
}

func (body *selectionProbeBody) Close() error {
	clear(body.pending)
	return body.ReadCloser.Close()
}

func TestNativeOllamaSelectionProbeFixture(t *testing.T) {
	input := selectionProbeInput(t)
	for _, scenario := range []string{"full", "single_tool", "without_final_protocol"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			transport := &selectionProbeTransport{singleTool: scenario == "single_tool", base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				payload, err := io.ReadAll(request.Body)
				_ = request.Body.Close()
				var wire requestAuditWire
				if err != nil || json.Unmarshal(payload, &wire) != nil || int64(len(payload)) != request.ContentLength || !requestAuditHistory(wire.Messages, []string{"\u4f60\u597d", "\u5f53\u524d\u96c6\u7fa4\u6709\u54ea\u4e9b ns"}) {
					t.Fatal("probe fixture lost request framing or retained history")
				}
				wantTools := 14
				if scenario == "single_tool" {
					wantTools = 1
				}
				if len(wire.Tools) != wantTools || (scenario == "without_final_protocol") == strings.Contains(wire.Messages[0].Content, "Final response protocol:") || !strings.Contains(wire.Messages[0].Content, "Treat the trusted runtime context as immutable authority.") {
					t.Fatal("probe fixture changed the wrong independent variable")
				}
				response := `{"model":"gpt-oss:20b","message":{"role":"assistant","tool_calls":[{"function":{"name":"get_cluster_overview","arguments":{"limit":null,"purpose":"Inspect the synthetic overview."}}}]},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":8}` + "\n"
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
			configuration.Model = "gpt-oss:20b"
			configuration.ReasoningEffort = domain.ModelReasoningEffortOmitted
			client, bound := newFixtureOllamaClientWithTransport(t, configuration, nil, transport)
			message, failure := client.stream(context.Background(), "00000000-0000-7000-8000-000000009801", bound, selectionProbeMessages(t, input, scenario), nil)
			reason, measurement := selectionProbeOutcome(input, message, failure, transport.singleTool)
			if reason != "" || measurement != "valid_bound_tool_selection" || calls != 1 || transport.calls != 1 || transport.requestBytes < 1 || transport.toolParse || transport.unexpectedEnd {
				t.Fatalf("probe fixture outcome rejected: stage=%s reason=%s measurement=%s calls=%d", reason.Stage(), reason, measurement, calls)
			}
		})
	}
	observation := new(selectionProbeTransport)
	body := &selectionProbeBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":"error parsing tool call: synthetic, err=unexpected end of JSON input"}` + "\n")), observation: observation}
	if _, err := io.Copy(io.Discard, body); err != nil {
		t.Fatal("probe fixture observation read failed")
	}
	if err := body.Close(); err != nil || !observation.toolParse || !observation.unexpectedEnd || len(body.pending) != 0 {
		t.Fatal("probe fixture lost its fixed content-free parser classification")
	}
}
