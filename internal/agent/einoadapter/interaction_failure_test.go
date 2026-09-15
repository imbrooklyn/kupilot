package einoadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestModelFailureProjectionRetainsExactBoundaryWithoutAnotherCall(t *testing.T) {
	cases := []struct {
		cause  error
		code   domain.ModelErrorCode
		reason domain.InteractionFailure
	}{
		{errProviderFinishDuplicate, domain.ModelErrorCodeDuplicateFinish, domain.FailureStreamDuplicate},
		{errProviderAfterFinish, domain.ModelErrorCodeAfterFinish, domain.FailureStreamAfterFinish},
		{errProviderFinishMissing, domain.ModelErrorCodeMissingFinish, domain.FailureStreamIncomplete},
		{errProviderUsage, domain.ModelErrorCodeInvalidStreamUsage, domain.FailureStreamUsage},
		{errProviderStopReason, domain.ModelErrorCodeInvalidStopReason, domain.FailureStopReason},
		{errNativeProviderReported, domain.ModelErrorCodeProviderReported, domain.FailureProviderReported},
		{errUnsupportedProviderChunk, domain.ModelErrorCodeUnsupportedResponse, domain.FailureProviderProtocol},
		{errRedirectOriginDenied, domain.ModelErrorCodeRedirectDenied, domain.FailureProviderProtocol},
		{errModelRequestLimitReached, domain.ModelErrorCodeRequestTooLarge, domain.FailureBudget},
		{errModelResponseLimitReached, domain.ModelErrorCodeStreamLimitExceeded, domain.FailureBudget},
	}
	for _, test := range cases {
		mapped := mapModelRequestError(context.Background(), test.cause, &transportRequestState{})
		projected := domain.NewModelError(mapped.code, domain.ModelOperationRequest, "synthetic-request")
		failure := runtimeFailureFromModel(projected)
		if mapped.code != test.code || runtimeDiagnostic(failure) != test.reason || failure.cause != projected {
			t.Fatalf("projection = %s/%s; want %s/%s", mapped.code, runtimeDiagnostic(failure), test.code, test.reason)
		}
	}
	for _, test := range []struct {
		code   domain.ModelErrorCode
		reason domain.InteractionFailure
	}{{domain.ModelErrorCodeInvalidRequest, domain.FailureRequestPreflight}, {domain.ModelErrorCodeInternal, domain.FailureInternal}} {
		if failure := runtimeFailureFromModel(domain.NewModelError(test.code, domain.ModelOperationRequest, "synthetic-request")); runtimeDiagnostic(failure) != test.reason {
			t.Fatalf("local model error = %v", failure)
		}
	}
	if failure := runtimeFailureFromModel(nil); runtimeDiagnostic(failure) != domain.FailureInternal {
		t.Fatalf("nil model error = %v", failure)
	}
}

func TestCollectedStreamRejectsEachFinishAndUsageViolationPrecisely(t *testing.T) {
	credential, err := config.NewSecretValue("synthetic-stream-credential")
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	finish := func() *schema.Message {
		return &schema.Message{Role: schema.Assistant, Content: "safe", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}
	}
	conflictingCall := func(id string, finished bool) *schema.Message {
		position := 0
		message := schema.AssistantMessage("", []schema.ToolCall{{Index: &position, ID: id, Type: "function", Function: schema.FunctionCall{Name: "get_resource", Arguments: `{}`}}})
		if finished {
			message.ResponseMeta = &schema.ResponseMeta{FinishReason: "tool_calls"}
		}
		return message
	}
	cases := []struct {
		name   string
		chunks []*schema.Message
		want   error
	}{
		{"missing finish", []*schema.Message{{Role: schema.Assistant, Content: "safe"}}, errProviderFinishMissing},
		{"duplicate finish", []*schema.Message{finish(), finish()}, errProviderFinishDuplicate},
		{"content after finish", []*schema.Message{finish(), &schema.Message{Role: schema.Assistant, Content: "late"}}, errProviderAfterFinish},
		{"unknown finish", []*schema.Message{{Role: schema.Assistant, Content: "safe", ResponseMeta: &schema.ResponseMeta{FinishReason: "unknown"}}}, errProviderStopReason},
		{"negative usage", []*schema.Message{{Role: schema.Assistant, Content: "safe", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{TotalTokens: -1}}}}, errProviderUsage},
		{"conflicting assembled identity", []*schema.Message{conflictingCall("call-1", false), conflictingCall("call-2", true)}, errMalformedProviderChunk},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			message, err := collectModelMessage(context.Background(), fixtureRequestID, domain.ModelProviderOpenAI, schema.StreamReaderFromArray(test.chunks), &credential, nil)
			if message != nil || !errors.Is(err, test.want) {
				t.Fatalf("stream result = %v; want %v", err, test.want)
			}
		})
	}
	validator := modelStreamValidator{provider: domain.ModelProviderOllama}
	if err := validator.normalizeNativeChunk(&schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 1, TotalTokens: 1}}}); !errors.Is(err, errProviderUsage) {
		t.Fatalf("native early usage = %v", err)
	}
}

func TestAdapterInvalidInputAndStaleEventSinkMakeZeroModelToolCalls(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "input", true: "stale sink"}[stale], func(t *testing.T) {
			clock := newTestClock()
			model := new(recordingModel)
			tool := new(recordingTool)
			adapter := testAdapter(t, clock, model, tool, newTestScopeGuard())
			input := agent.RunInput{}
			sink := newEventRecorder()
			var destination agent.EventSink = sink
			want := domain.FailureRequestPreflight
			if stale {
				input = testInput(t, clock, agent.DefaultRunBudgetLimits())
				want = domain.FailureStaleGeneration
				destination = agent.EventSinkFunc(func(ctx context.Context, event agent.RunEvent) agent.EventSinkResult {
					if event.Kind == agent.RunEventModelStreamStarted {
						return agent.EventSinkStaleRejected
					}
					return sink.Publish(ctx, event)
				})
			}
			outcome := adapter.Run(context.Background(), input, destination)
			if outcome.Diagnostic != want || len(model.Requests()) != 0 || len(tool.Calls()) != 0 {
				t.Fatalf("zero-call rejection = %#v", outcome)
			}
		})
	}
}

func TestToolBindingMalformedAndDuplicateBatchesHaveNoHandlerCalls(t *testing.T) {
	for _, scenario := range []string{"empty", "invalid", "duplicate batch", "duplicate bound", "duplicate feedback", "unbound execution"} {
		t.Run(scenario, func(t *testing.T) {
			clock := newTestClock()
			tool := new(recordingTool)
			state := &runState{input: testInput(t, clock, agent.DefaultRunBudgetLimits()), tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now, boundCalls: make(map[string]*boundExecution), toolInvocationIDs: make(map[domain.ToolInvocationID]struct{})}
			selection := resourceCall("call-1", "sample-pod")
			selections := []agent.ToolSelection{selection}
			want := domain.FailureToolSelection
			switch scenario {
			case "empty":
				selections = nil
			case "invalid":
				selections[0].Name = "not_a_tool"
			case "duplicate batch":
				selections = append(selections, selection)
				want = domain.FailureToolPairing
			case "duplicate bound":
				state.boundCalls[selection.ID] = nil
				want = domain.FailureToolPairing
			case "duplicate feedback":
				selections[0] = agent.ToolSelection{ID: selection.ID, Name: domain.ToolNameListResources, ArgumentsJSON: `{"filters":[],"format":"list","limit":20,"namespace":"test-namespace","purpose":"List Nodes.","resource_type":"nodes"}`}
				state.boundCalls[selection.ID] = nil
				want = domain.FailureToolPairing
			case "unbound execution":
				want = domain.FailureToolPairing
			}
			var err error
			if scenario == "unbound execution" {
				_, err = state.executeTool(context.Background(), selection.Name, selection.ID, selection.ArgumentsJSON)
			} else {
				err = state.bindToolCalls(context.Background(), selections)
			}
			failure := normalizeFailure(context.Background(), err)
			if err == nil || runtimeDiagnostic(failure) != want || len(tool.Calls()) != 0 {
				t.Fatalf("binding = %v, handler calls %d; want %s", err, len(tool.Calls()), want)
			}
		})
	}
}

func TestRetainedConversationFailuresNeverReachModelOrTool(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	position := 0
	call := schema.ToolCall{Index: &position, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: string(domain.ToolNameGetResource), Arguments: `{}`}}
	for _, test := range []struct {
		name    string
		message *schema.Message
	}{
		{"system tool identity", &schema.Message{Role: schema.System, Content: "Safe instructions.", ToolCallID: "call-1"}},
		{"user tool identity", &schema.Message{Role: schema.User, Content: "Safe question.", ToolName: "tool"}},
		{"assistant invalid index", schema.AssistantMessage("", []schema.ToolCall{{Index: func() *int { n := 1; return &n }(), ID: call.ID, Type: call.Type, Function: call.Function}})},
		{"assistant malformed call", schema.AssistantMessage("", []schema.ToolCall{{Index: &position, ID: "", Type: call.Type, Function: call.Function}})},
		{"unknown role", &schema.Message{Role: "unknown", Content: "Safe text."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &runState{input: input}
			if err := state.validateConversation([]*schema.Message{test.message}); runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureRetainedContext || err == nil {
				t.Fatalf("retained rejection = %v", err)
			}
		})
	}
	state := &runState{input: input}
	for _, messages := range [][]*schema.Message{nil, {schema.UserMessage("A different question.")}} {
		if err := state.validateCurrentRunInputs(messages); err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureRetainedContext {
			t.Fatalf("missing exact current input = %v", err)
		}
	}
	if err := state.validateCurrentRunInputs([]*schema.Message{schema.UserMessage(input.Question())}); err != nil {
		t.Fatalf("exact current input rejected: %v", err)
	}
	// No model, Tool, or Kubernetes dependency is provided to these validators.
}

func TestAssembledMessageAndSummaryFailuresHaveExactReasons(t *testing.T) {
	position := 0
	call := schema.ToolCall{Index: &position, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: string(domain.ToolNameGetResource), Arguments: `{}`}}
	for _, test := range []struct {
		name    string
		message *schema.Message
		reason  domain.InteractionFailure
	}{
		{"missing message", nil, domain.FailureStreamUnsupported},
		{"stop with tool", &schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}, ToolCalls: []schema.ToolCall{call}}, domain.FailureStreamUnsupported},
		{"length with tool", &schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}, ToolCalls: []schema.ToolCall{call}}, domain.FailureStreamUnsupported},
		{"empty tool finish", &schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}, domain.FailureStreamUnsupported},
		{"tool index missing", &schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}, ToolCalls: []schema.ToolCall{{ID: call.ID, Type: call.Type, Function: call.Function}}}, domain.FailureStreamUnsupported},
		{"tool ID malformed", &schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}, ToolCalls: []schema.ToolCall{{Index: &position, Type: call.Type, Function: call.Function}}}, domain.FailureToolSelection},
		{"unknown finish", &schema.Message{ResponseMeta: &schema.ResponseMeta{FinishReason: "unknown"}}, domain.FailureStopReason},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := new(runState)
			message, err := state.acceptModelMessage(context.Background(), test.message)
			if message != nil || err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != test.reason {
				t.Fatalf("assembled rejection = %v; want %s", err, test.reason)
			}
		})
	}
	if _, err := diagnosisDraft(nil); err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureFinalShape {
		t.Fatalf("missing final message = %v", err)
	}
	state := &runState{client: &modelClient{}}
	if message, err := state.acceptSummaryMessage(schema.AssistantMessage("   ", nil), domain.MaxSessionSummaryBytes); message != nil || err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureSummaryResponse {
		t.Fatalf("empty normalized summary = %v", err)
	}
	for _, test := range []struct {
		class  domain.SafeErrorClass
		reason domain.InteractionFailure
	}{{domain.SafeErrorClassInvalidInput, domain.FailureRequestPreflight}, {domain.SafeErrorClassConsentRequired, domain.FailureRequestPreflight}, {domain.SafeErrorClassAuthenticationFailed, domain.FailureProviderTransport}, {domain.SafeErrorClassRateLimited, domain.FailureProviderTransport}, {domain.SafeErrorClassUnavailable, domain.FailureProviderTransport}} {
		if got := runtimeDiagnostic(&runtimeFailure{class: test.class}); got != test.reason {
			t.Fatalf("fallback = %s; want %s", got, test.reason)
		}
	}
}

func TestBoundToolResultAndFeedbackCannotChangeIdentity(t *testing.T) {
	clock := newTestClock()
	selection := resourceCall("call-1", "sample-pod")
	state := &runState{now: clock.Now, boundCalls: map[string]*boundExecution{selection.ID: {toolName: selection.Name, modelCall: selection, policyFeedback: "Fixed policy feedback."}}}
	if value, err := state.executeTool(context.Background(), selection.Name, selection.ID, `{}`); value != "" || err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureToolPairing {
		t.Fatalf("changed feedback parameters = %v", err)
	}
	for _, status := range []domain.ToolResultStatus{"unknown", domain.ToolResultStatusSuccess} {
		invocation, kind, err := state.completedToolInvocation(&boundExecution{}, domain.ToolResult{Status: status}, 0)
		if invocation.ID != "" || kind != "" || err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureToolResult {
			t.Fatalf("invalid local completion = %v", err)
		}
	}
}

func TestToolResultCannotEnterASealedEvidenceRegistry(t *testing.T) {
	clock := newTestClock()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	registry, err := agent.NewEvidenceRegistry(input.RunID(), input.Scope(), input.PolicyGeneration())
	if err != nil {
		t.Fatal(err)
	}
	budget, err := agent.NewRunBudget(input.BudgetLimits(), clock.Now(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	recorder := newEventRecorder()
	publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, recorder)
	if err != nil {
		t.Fatal(err)
	}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"ready":true}`)
	}}
	state := &runState{input: input, registry: registry, budget: budget, publisher: publisher, tools: fixedHandlers(tool), scopeGuard: newTestScopeGuard(), identifiers: &testIdentifiers{}, now: clock.Now, boundCalls: make(map[string]*boundExecution), toolInvocationIDs: make(map[domain.ToolInvocationID]struct{})}
	if err := state.publish(context.Background(), agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
		t.Fatal(err)
	}
	selections := []agent.ToolSelection{resourceCall("call-1", "sample-pod")}
	if err := state.bindToolCalls(context.Background(), selections); err != nil {
		t.Fatal(err)
	}
	// Fault injection: a terminal seal races with a previously bound result.
	// Application never grants this ordering; registry acceptance still rejects it.
	_, err = agent.ValidateDiagnosis(agent.DiagnosisDraft{AnswerMarkdown: "A bounded explanation."}, agent.DiagnosisMetadata{ID: "00000000-0000-7000-8000-000000009001", CreatedAt: clock.Now(), PolicyGeneration: input.PolicyGeneration()}, registry)
	if err != nil {
		t.Fatal(err)
	}
	selection := selections[0]
	value, err := state.executeTool(context.Background(), selection.Name, selection.ID, selection.ArgumentsJSON)
	if value != "" || err == nil || runtimeDiagnostic(normalizeFailure(context.Background(), err)) != domain.FailureEvidenceAcceptance || len(tool.Calls()) != 1 {
		t.Fatalf("sealed registry acceptance = %v, Tool calls = %d", err, len(tool.Calls()))
	}
	for _, event := range recorder.Events() {
		if event.Evidence != nil {
			t.Fatal("sealed registry emitted Evidence")
		}
	}
}
