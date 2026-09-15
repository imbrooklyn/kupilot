//go:build integration

package einoadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	projectagent "github.com/imbrooklyn/kupilot/internal/agent"
	projectconfig "github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	liveModelCallCeiling        = 4
	liveModelOutputTokenCeiling = 2048
	liveModelRequestByteCeiling = 1024 * 1024
	liveModelSuiteTimeout       = 15 * time.Minute
	livePreferredCostUSDCeiling = 3.0
	liveModelAuthorizationValue = "authorized"
	liveModelTargetPreferred    = "preferred"
	liveModelTargetOllama       = "ollama"
	liveModelCredentialSource   = "file"
)

// TestModelAPIIntegrationDeterministicContract is the network-independent
// protocol authority for Eino serialization, streaming, Tool pairing, safe
// failures, and response lifecycle. Live model quality cannot weaken it.
func TestModelAPIIntegrationDeterministicContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "Eino request and text stream", run: TestModelClientUsesEinoRequestAndStreamOnceForGPT5CompatibleIdentifier},
		{name: "fragmented Tool arguments", run: TestModelClientAssemblesCompatibleToolStreams},
		{name: "multi-step Tool result and structured final response", run: TestAdapterCompletesToolEvidenceAndValidatedDiagnosis},
		{name: "optional usage and finish reason", run: TestModelClientAcceptsOptionalUsageAndTerminalEOF},
		{name: "usage ordering and duplicate rejection", run: TestCollectModelMessageRequiresOneUsageChunkAfterFinish},
		{name: "malformed and out-of-order stream rejection", run: TestCollectModelMessageDoesNotObserveStreamLevelInvalidChunk},
		{name: "explicit Tool call index and identity", run: TestCollectModelMessageRejectsMissingToolIndex},
		{name: "cancellation", run: TestModelClientCancellationStopsTheOwnedRequest},
		{name: "timeout", run: TestModelClientDeadlineCauseStopsTheOwnedRequest},
		{name: "ambiguous disconnect has no continuation or retry", run: TestProtocolContinuationUnavailableDisconnectMakesOneRequestAndNoReattach},
		{name: "cross-origin redirect has no fallback", run: TestModelClientDeniesCrossOriginRedirect},
		{name: "authentication and safe HTTP errors", run: TestModelClientMapsHTTPAndStreamFailuresSafely},
		{name: "bounded authentication body", run: TestModelClientBoundsAndDiscardsHTTPErrorBody},
		{name: "response body close", run: TestModelClientClosesResponseBodyOnEveryTerminalPath},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, current.run)
	}
}

// TestModelAPIIntegrationLive performs four bounded protocol calls against
// exactly one explicitly selected profile. It asserts capabilities rather
// than answer quality and never discovers an endpoint or falls back.
func TestModelAPIIntegrationLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue {
		t.Skip("BLOCKED model API integration: set KUPILOT_INTEGRATION_LIVE=authorized only after endpoint and cost authorization")
	}
	target := os.Getenv("KUPILOT_INTEGRATION_MODEL_TARGET")
	if target != liveModelTargetPreferred && target != liveModelTargetOllama {
		t.Skip("BLOCKED model API integration: select exactly preferred or ollama with KUPILOT_INTEGRATION_MODEL_TARGET")
	}
	maximumCost, err := strconv.ParseFloat(os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD"), 64)
	if err != nil || maximumCost < 0 || maximumCost > livePreferredCostUSDCeiling || target == liveModelTargetPreferred && maximumCost == 0 {
		t.Skip("BLOCKED model API integration: KUPILOT_INTEGRATION_MAX_COST_USD must be positive for preferred, zero or positive for Ollama, and at most 3")
	}

	configuration, credential, source := loadLiveModelProfile(t, target)
	transport := newLiveBudgetTransport(configuration.ProviderKind, liveModelCallCeiling, liveModelRequestByteCeiling)
	client, modelError := newModelClientForTest(configuration, credential, nil, transport)
	if modelError != nil {
		if credential != nil {
			credential.Destroy()
		}
		t.Fatalf("FAIL model API preflight: profile configuration was rejected safely: %v", modelError)
	}
	t.Cleanup(client.close)
	bound, bindErr := client.withTools(fixtureToolInfos(t))
	if bindErr != nil {
		t.Fatalf("FAIL model API preflight: pinned Eino Tool binding failed: %v", bindErr)
	}

	t.Logf(
		"PREFLIGHT PASS model API: target=%s profile=%s model=%s origin_hash=%s credential_source=%s calls<=%d requested_output_tokens<=%d request_bytes<=%d elapsed<=%s authorized_cost_usd<=%.2f",
		target, configuration.ProfileName, configuration.Model, domain.SHA256Hex(configuration.Origin), source,
		liveModelCallCeiling, liveModelCallCeiling*liveModelOutputTokenCeiling, liveModelRequestByteCeiling,
		liveModelSuiteTimeout, maximumCost,
	)
	if os.Getenv("KUPILOT_INTEGRATION_PREFLIGHT_ONLY") == "1" {
		t.Log("PREFLIGHT ONLY model API: live calls NOT RUN and this result is not integration PASS evidence")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveModelSuiteTimeout)
	defer cancel()
	usage := liveUsageObservation{}

	textMessage, failure := client.stream(
		ctx,
		"00000000-0000-7000-8000-000000009601",
		client.model,
		[]*schema.Message{
			schema.SystemMessage("This is a bounded protocol test. Do not call a Tool for this request."),
			schema.UserMessage("Return one short plain-text sentence confirming that streaming text is available."),
		},
		nil,
	)
	if failure != nil {
		t.Fatalf("FAIL model API transport/text stream: %v", failure)
	}
	if textMessage == nil || strings.TrimSpace(textMessage.Content) == "" || len(textMessage.ToolCalls) != 0 ||
		textMessage.ResponseMeta == nil || textMessage.ResponseMeta.FinishReason != "stop" {
		t.Fatalf("MODEL_CAPABILITY_FAIL text stream: content=%t tools=%d finish=%q",
			textMessage != nil && strings.TrimSpace(textMessage.Content) != "", toolCallCount(textMessage), finishReason(textMessage))
	}
	usage.observe(textMessage)

	toolMessages := []*schema.Message{
		schema.SystemMessage("This is a bounded protocol test. First call get_resource exactly once with the supplied synthetic arguments. After the Tool result, return only one JSON object with exactly these fields in order: answer_markdown as a non-empty string, evidence_citations as an empty array, proposed_actions as an empty array, response_schema_version as 2, outcome as answer, stop_reason as completed, limitations as an empty array, and questions as an empty array."),
		schema.UserMessage(`Call get_resource with {"detail":"summary","name":"synthetic-pod","namespace":null,"purpose":"Validate the protocol fixture.","resource_type":"pods"}. Do not answer in prose before the Tool call.`),
	}
	toolMessage, failure := client.stream(
		ctx,
		"00000000-0000-7000-8000-000000009602",
		bound,
		toolMessages,
		nil,
	)
	if failure != nil {
		t.Fatalf("FAIL model API Tool stream: %v", failure)
	}
	call, capabilityErr := validateLiveToolCall(toolMessage)
	if capabilityErr != nil {
		t.Fatalf("MODEL_CAPABILITY_FAIL Tool stream: %v", capabilityErr)
	}
	usage.observe(toolMessage)

	toolMessages = append(toolMessages, toolMessage, schema.ToolMessage(
		`{"availability":"available","kind":"Pod","name":"synthetic-pod","phase":"Running"}`,
		call.ID,
		schema.WithToolName(call.Function.Name),
	))
	finalMessage, failure := client.stream(
		ctx,
		"00000000-0000-7000-8000-000000009603",
		bound,
		toolMessages,
		nil,
	)
	if failure != nil {
		t.Fatalf("FAIL model API multi-step final stream: %v", failure)
	}
	if capabilityErr := validateLiveStructuredFinal(finalMessage); capabilityErr != nil {
		t.Fatalf("MODEL_CAPABILITY_FAIL structured final response: %v", capabilityErr)
	}
	usage.observe(finalMessage)

	historical, historicalErr := projectagent.EncodeHistoricalAssistantResponse("Earlier visible answer.")
	if historicalErr != nil {
		t.Fatalf("FAIL model API historical response preflight: %v", historicalErr)
	}
	fullPrompt, promptErr := projectagent.BuildSystemPrompt(testInput(t, newTestClock(), projectagent.DefaultRunBudgetLimits()))
	if promptErr != nil {
		t.Fatalf("FAIL model API system prompt preflight: %v", promptErr)
	}
	followupMessage, failure := client.stream(
		ctx,
		"00000000-0000-7000-8000-000000009604",
		bound,
		[]*schema.Message{
			schema.SystemMessage(fullPrompt),
			schema.UserMessage("Hello."),
			schema.AssistantMessage(historical, nil),
			schema.UserMessage("Who are you?"),
		},
		nil,
	)
	if failure != nil {
		t.Fatalf("FAIL model API later-turn structured stream: %v", failure)
	}
	if capabilityErr := validateLiveStructuredFinal(followupMessage); capabilityErr != nil {
		t.Fatalf("MODEL_CAPABILITY_FAIL later-turn structured response: %v", capabilityErr)
	}
	usage.observe(followupMessage)

	if calls := transport.calls.Load(); calls != liveModelCallCeiling {
		t.Fatalf("FAIL model API request count = %d, want %d", calls, liveModelCallCeiling)
	}
	if bytesSent := transport.requestBytes.Load(); bytesSent < 1 || bytesSent > liveModelRequestByteCeiling {
		t.Fatalf("FAIL model API request bytes = %d, ceiling %d", bytesSent, liveModelRequestByteCeiling)
	}
	usageText := "unavailable"
	if usage.responses > 0 {
		usageText = fmt.Sprintf("responses=%d input=%d output=%d total=%d", usage.responses, usage.input, usage.output, usage.total)
	}
	t.Logf(
		"PASS model API: target=%s calls=%d request_bytes=%d usage=%s observed_cost=provider-billing-unavailable authorized_cost_usd<=%.2f",
		target, transport.calls.Load(), transport.requestBytes.Load(), usageText, maximumCost,
	)
}

// TestNativeOllamaFullAgentLive exercises the complete ADK Agent composition,
// including provisional answer projection, with a credential-free native
// Ollama client. Any selected Tool is handled by a synthetic in-memory result;
// the test never contacts Kubernetes or another data source.
func TestNativeOllamaFullAgentLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue ||
		os.Getenv("KUPILOT_INTEGRATION_MODEL_TARGET") != liveModelTargetOllama {
		t.Skip("BLOCKED native Ollama full-Agent integration: explicitly select the authorized ollama target")
	}
	maximumCost, err := strconv.ParseFloat(os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD"), 64)
	if err != nil || maximumCost < 0 || maximumCost > livePreferredCostUSDCeiling {
		t.Skip("BLOCKED native Ollama full-Agent integration: provide the bounded authorized cost")
	}

	configuration, credential, _ := loadLiveModelProfile(t, liveModelTargetOllama)
	if credential != nil {
		credential.Destroy()
		t.Fatal("native Ollama unexpectedly returned a credential")
	}
	transport := newLiveBudgetTransport(domain.ModelProviderOllama, 2, liveModelRequestByteCeiling)
	client, modelError := newModelClientForTest(configuration, nil, nil, transport)
	if modelError != nil {
		t.Fatalf("FAIL native Ollama full-Agent client preflight: %v", modelError)
	}
	clock := newTestClock()
	tool := &recordingTool{execute: func(_ context.Context, call projectagent.BoundToolCall) domain.ToolResult {
		return domain.ToolResult{
			InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(),
			Scope: call.Scope().Snapshot(), ObservedAt: clock.Now(),
			Status: domain.ToolResultStatusSuccess, DataJSON: `{}`,
		}
	}}
	adapter, err := newAdapter(runtimeConfig{
		tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now,
	}, client)
	if err != nil {
		client.close()
		t.Fatalf("FAIL native Ollama full-Agent adapter preflight: %v", err)
	}
	t.Cleanup(adapter.Close)
	limits := projectagent.DefaultRunBudgetLimits()
	input, err := projectagent.NewRunInput(
		testRunID, testSessionID, testMessageID,
		"Who are you? Return a brief identity answer without inspecting cluster state.",
		domain.ClusterScope{
			Context: "test-context", Namespace: "test-namespace",
			NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: clock.Now(),
		},
		nil,
		limits,
	)
	if err != nil {
		t.Fatalf("FAIL native Ollama full-Agent input preflight: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveModelSuiteTimeout)
	defer cancel()
	outcome := adapter.Run(ctx, input, newEventRecorder())
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil ||
		strings.TrimSpace(outcome.Diagnosis.AnswerMarkdown) == "" {
		t.Fatalf("MODEL_CAPABILITY_FAIL native Ollama full-Agent outcome: status=%s diagnosis=%t class=%v",
			outcome.Status, outcome.Diagnosis != nil, outcome.ErrorClass)
	}
	if calls := transport.calls.Load(); calls < 1 || calls > 2 {
		t.Fatalf("FAIL native Ollama full-Agent calls = %d, want 1..2", calls)
	}
	t.Logf("PASS native Ollama full Agent: calls=%d tools=%d request_bytes=%d", transport.calls.Load(), len(tool.Calls()), transport.requestBytes.Load())
}

type liveUsageObservation struct {
	responses int
	input     int
	output    int
	total     int
}

func (usage *liveUsageObservation) observe(message *schema.Message) {
	if usage == nil || message == nil || message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		return
	}
	current := message.ResponseMeta.Usage
	usage.responses++
	usage.input += current.PromptTokens
	usage.output += current.CompletionTokens
	usage.total += current.TotalTokens
}

type liveBudgetTransport struct {
	base         *http.Transport
	provider     domain.ModelProviderKind
	callCeiling  int64
	byteCeiling  int64
	calls        atomic.Int64
	requestBytes atomic.Int64
}

func newLiveBudgetTransport(provider domain.ModelProviderKind, callCeiling int, byteCeiling int) *liveBudgetTransport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return &liveBudgetTransport{base: base, provider: provider, callCeiling: int64(callCeiling), byteCeiling: int64(byteCeiling)}
}

func (transport *liveBudgetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || request == nil || request.Body == nil || request.ContentLength < 1 {
		return nil, errors.New("live integration request did not have a bounded body")
	}
	if transport.calls.Add(1) > transport.callCeiling {
		return nil, errors.New("live integration request call ceiling reached")
	}
	if transport.requestBytes.Add(request.ContentLength) > transport.byteCeiling {
		return nil, errors.New("live integration request byte ceiling reached")
	}
	if transport.provider == domain.ModelProviderOpenAI && request.Header.Get("Authorization") == "" {
		return nil, errors.New("live OpenAI integration request has no transport credential")
	}
	if transport.provider == domain.ModelProviderOllama && (request.Header.Get("Authorization") != "" || request.URL.Path != "/api/chat") {
		return nil, errors.New("live native Ollama integration request violated the credential-free route")
	}
	return transport.base.RoundTrip(request)
}

func (transport *liveBudgetTransport) CloseIdleConnections() {
	if transport != nil && transport.base != nil {
		transport.base.CloseIdleConnections()
	}
}

func loadLiveModelProfile(t *testing.T, target string) (domain.ModelConfiguration, *projectconfig.SecretValue, string) {
	t.Helper()
	if target == liveModelTargetOllama {
		endpoint := os.Getenv("KUPILOT_INTEGRATION_OLLAMA_ENDPOINT")
		model := os.Getenv("KUPILOT_INTEGRATION_OLLAMA_MODEL")
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "http" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
			parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1" || model == "" {
			t.Skip("BLOCKED Ollama integration: provide an explicit loopback KUPILOT_INTEGRATION_OLLAMA_ENDPOINT and KUPILOT_INTEGRATION_OLLAMA_MODEL")
		}
		configuration := domain.ModelConfiguration{
			ProfileName: "ollama-integration", Role: domain.ModelRoleAgent,
			ProviderKind:        domain.ModelProviderOllama,
			Endpoint:            strings.TrimRight(endpoint, "/"),
			Origin:              parsed.Scheme + "://" + parsed.Host,
			Model:               model,
			ReasoningEffort:     domain.ModelReasoningEffortOmitted,
			ResponseFormat:      domain.ModelResponseFormatJSONObject,
			APIKeySource:        domain.ModelAPIKeySourceNone,
			Temperature:         0,
			MaxOutputTokens:     liveModelOutputTokenCeiling,
			RequestTimeout:      10 * time.Minute,
			StreamingRequired:   true,
			ToolCallingRequired: true,
			TransportPolicy:     domain.ModelTransportPolicyLoopbackHTTPNoRedirect,
		}
		if configuration.Validate() != nil {
			t.Skip("BLOCKED Ollama integration: the explicit profile is not an admitted native loopback configuration")
		}
		return configuration, nil, "none"
	}

	paths, err := projectconfig.SystemPaths()
	if err != nil {
		t.Skipf("BLOCKED preferred model integration: default Kupilot paths are unavailable: %v", err)
	}
	loaded, err := projectconfig.Load(context.Background(), projectconfig.LoadOptions{
		Paths: paths,
		LookupEnv: func(string) (string, bool) {
			return "", false
		},
	})
	if err != nil {
		t.Skipf("BLOCKED preferred model integration: default Kupilot configuration could not be loaded safely: %v", err)
	}
	defer loaded.Credentials.Destroy()
	profile := loaded.Models.Agent
	if profile.Endpoint == "" || profile.Model == "" || loaded.Credentials.Agent.Source != projectconfig.CredentialSourceFile || !loaded.Credentials.Agent.Value.IsSet() {
		t.Skip("BLOCKED preferred model integration: the default configuration must contain an explicit Agent endpoint, model, and file-backed opaque credential")
	}
	credential, err := loaded.Credentials.Agent.Value.Clone()
	if err != nil {
		t.Skipf("BLOCKED preferred model integration: the opaque Agent credential could not be cloned: %v", err)
	}
	timeout := time.Duration(profile.RequestTimeoutSeconds) * time.Second
	if timeout > time.Minute {
		timeout = time.Minute
	}
	configuration := domain.ModelConfiguration{
		ProfileName: profile.Name, Role: domain.ModelRoleAgent,
		ProviderKind:        domain.ModelProviderOpenAI,
		Endpoint:            profile.Endpoint,
		Origin:              profile.Origin,
		Model:               profile.Model,
		ReasoningEffort:     domain.ModelReasoningEffort(profile.ReasoningEffort),
		ResponseFormat:      domain.ModelResponseFormat(profile.ResponseFormat),
		APIKeySource:        domain.ModelAPIKeySourceRuntime,
		Temperature:         profile.Temperature,
		MaxOutputTokens:     liveModelOutputTokenCeiling,
		RequestTimeout:      timeout,
		StreamingRequired:   true,
		ToolCallingRequired: true,
		TransportPolicy:     domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
	source := liveModelCredentialSource
	if selected := os.Getenv("KUPILOT_INTEGRATION_PREFERRED_MODEL"); selected != "" {
		configuration.Model = selected
		configuration.ReasoningEffort = domain.ModelReasoningEffortOmitted
		source = "file+explicit-model"
	}
	if configuration.Validate() != nil {
		credential.Destroy()
		t.Skip("BLOCKED preferred model integration: the selected default profile is not valid under the fixed model contract")
	}
	return configuration, &credential, source
}

func validateLiveToolCall(message *schema.Message) (schema.ToolCall, error) {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "tool_calls" ||
		len(message.ToolCalls) != 1 || strings.TrimSpace(message.Content) != "" {
		return schema.ToolCall{}, fmt.Errorf("expected exactly one Tool call and finish_reason=tool_calls; tools=%d finish=%q", toolCallCount(message), finishReason(message))
	}
	call := message.ToolCalls[0]
	if call.ID == "" || call.Function.Name != string(domain.ToolNameGetResource) {
		return schema.ToolCall{}, fmt.Errorf("expected get_resource with one non-empty call ID; got name=%q", call.Function.Name)
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	var arguments struct {
		Detail       string  `json:"detail"`
		Name         string  `json:"name"`
		Namespace    *string `json:"namespace"`
		Purpose      string  `json:"purpose"`
		ResourceType string  `json:"resource_type"`
	}
	if err := decoder.Decode(&arguments); err != nil {
		return schema.ToolCall{}, fmt.Errorf("Tool arguments were not the strict JSON object: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return schema.ToolCall{}, errors.New("Tool arguments contained trailing data")
	}
	if arguments.Detail != "summary" || arguments.Name != "synthetic-pod" || arguments.Namespace != nil ||
		arguments.ResourceType != "pods" || strings.TrimSpace(arguments.Purpose) == "" {
		return schema.ToolCall{}, fmt.Errorf("Tool arguments changed the synthetic fixture: %#v", arguments)
	}
	return call, nil
}

func validateLiveStructuredFinal(message *schema.Message) error {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "stop" || len(message.ToolCalls) != 0 {
		return fmt.Errorf("expected Tool-free finish_reason=stop; tools=%d finish=%q", toolCallCount(message), finishReason(message))
	}
	draft, err := projectagent.DecodeDiagnosticResponse(message.Content)
	if err != nil {
		return fmt.Errorf("final response did not satisfy the current strict contract: %w", err)
	}
	if strings.TrimSpace(draft.AnswerMarkdown) == "" || draft.ResponseSchemaVersion != 2 ||
		len(draft.ConfirmedFacts) != 0 || len(draft.ClaimCoverage) != 0 || len(draft.RecommendedActions) != 0 ||
		len(draft.MissingInformation) != 0 || draft.Clarification != nil {
		return fmt.Errorf(
			"final response changed the content-free protocol fixture: answer=%t schema=%d facts=%d claims=%d actions=%d limitations=%d clarification=%t stop=%s",
			strings.TrimSpace(draft.AnswerMarkdown) != "", draft.ResponseSchemaVersion,
			len(draft.ConfirmedFacts), len(draft.ClaimCoverage), len(draft.RecommendedActions),
			len(draft.MissingInformation), draft.Clarification != nil, draft.SuggestedStopReason,
		)
	}
	return nil
}

func toolCallCount(message *schema.Message) int {
	if message == nil {
		return 0
	}
	return len(message.ToolCalls)
}

func finishReason(message *schema.Message) string {
	if message == nil || message.ResponseMeta == nil {
		return ""
	}
	return message.ResponseMeta.FinishReason
}

var _ http.RoundTripper = (*liveBudgetTransport)(nil)
