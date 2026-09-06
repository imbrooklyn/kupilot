package einoadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestEinoSummarizationMessageThresholdAndRecentTail(t *testing.T) {
	tests := []struct {
		name         string
		turns        int
		wantSummary  bool
		wantRequests int
	}{
		{name: "before", turns: 156, wantRequests: 1},
		{name: "at", turns: 158, wantRequests: 1},
		{name: "one over", turns: 160, wantSummary: true, wantRequests: 2},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			guard := newTestScopeGuard()
			conversation := testConversation(t, current.turns)
			input := testInputWithConversation(t, clock, conversation)
			const summaryText = "Earlier Session turns described prior questions and final answers without restoring authority."
			const finalJSON = `{"answer_markdown":"The current question was handled after bounded context selection.","evidence_citations":[],"proposed_actions":[]}`
			scripts := make([]modelScript, 0, 2)
			if current.wantSummary {
				scripts = append(scripts, scriptedChunks(&schema.Message{
					Role: schema.Assistant, Content: summaryText,
					ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
				}))
			}
			scripts = append(scripts, scriptedChunks(diagnosisChunks(finalJSON)...))
			model := &recordingModel{scripts: scripts}
			recorder := newEventRecorder()
			outcome := testAdapter(t, clock, model, new(recordingTool), guard).Run(context.Background(), input, recorder)
			if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != current.wantRequests {
				t.Fatalf("outcome/requests = %#v/%d", outcome, len(model.Requests()))
			}
			events := recorder.Events()
			summaryStarted, summaryReady := 0, 0
			for _, event := range events {
				switch event.Kind {
				case agent.RunEventSummaryStarted:
					summaryStarted++
				case agent.RunEventSummaryReady:
					summaryReady++
					if event.Summary.CoveredCount != current.turns-summaryRecentTailMessages ||
						event.Summary.Text != summaryText || event.Summary.AgentProfile != string(domain.ModelRoleAgent) {
						t.Fatalf("summary event = %#v", event.Summary)
					}
				}
			}
			if current.wantSummary != (summaryStarted == 1 && summaryReady == 1) {
				t.Fatalf("summary events started/ready = %d/%d", summaryStarted, summaryReady)
			}
			if !current.wantSummary {
				return
			}
			mainInput := model.Requests()[1].Messages
			if len(mainInput) != summaryRecentTailMessages+3 || mainInput[0].Role != schema.System ||
				mainInput[1].Role != schema.User || mainInput[1].Content != summaryContextPreamble+summaryText ||
				mainInput[len(mainInput)-1].Content != input.Question() {
				t.Fatalf("post-summary main input = %#v", mainInput)
			}
			wantTail := conversation.Turns()[current.turns-summaryRecentTailMessages:]
			for index, turn := range wantTail {
				message := mainInput[index+2]
				wantContent := turn.Content
				if turn.Role == domain.MessageRoleAssistant {
					encoded, encodeErr := agent.EncodeHistoricalAssistantResponse(turn.Content)
					if encodeErr != nil {
						t.Fatalf("EncodeHistoricalAssistantResponse() error = %v", encodeErr)
					}
					wantContent = encoded
				}
				if message.Content != wantContent || string(message.Role) != string(turn.Role) {
					t.Fatalf("recent tail[%d] = %#v, want %#v", index, message, turn)
				}
			}
		})
	}
}

func TestEinoSummarizationByteThreshold(t *testing.T) {
	tests := []struct {
		name        string
		delta       int
		wantSummary bool
	}{
		{name: "before", delta: -1},
		{name: "at", delta: 0},
		{name: "one over", delta: 1, wantSummary: true},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			conversation := testConversationAtInputBytes(t, clock, domain.MaxSessionContextBytes+current.delta)
			input := testInputWithConversation(t, clock, conversation)
			scripts := make([]modelScript, 0, 2)
			if current.wantSummary {
				scripts = append(scripts, scriptedChunks(&schema.Message{
					Role: schema.Assistant, Content: "The bounded byte trigger condensed the oldest complete turn.",
					ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
				}))
			}
			scripts = append(scripts, scriptedChunks(diagnosisChunks(
				`{"answer_markdown":"The byte threshold remained bounded.","evidence_citations":[],"proposed_actions":[]}`,
			)...))
			model := &recordingModel{scripts: scripts}
			recorder := newEventRecorder()
			outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
				context.Background(), input, recorder,
			)
			wantRequests := 1
			if current.wantSummary {
				wantRequests = 2
			}
			if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != wantRequests {
				t.Fatalf("outcome/requests = %#v/%d, want completed/%d", outcome, len(model.Requests()), wantRequests)
			}
			summaryEvents := 0
			for _, event := range recorder.Events() {
				if event.Kind == agent.RunEventSummaryStarted {
					summaryEvents++
				}
			}
			if current.wantSummary != (summaryEvents == 1) {
				t.Fatalf("summary starts = %d, wantSummary=%t", summaryEvents, current.wantSummary)
			}
		})
	}
}

func TestEinoSummaryFailurePreventsMainModelRequest(t *testing.T) {
	tests := []struct {
		name   string
		script modelScript
		class  domain.SafeErrorClass
	}{
		{
			name: "transport failure",
			script: func(_ context.Context, request recordedModelRequest) ([]*schema.Message, error) {
				return nil, domain.NewModelError(domain.ModelErrorCodeServiceUnavailable, domain.ModelOperationRequest, string(request.ID))
			},
			class: domain.SafeErrorClassUnavailable,
		},
		{
			name: "oversize",
			script: scriptedChunks(&schema.Message{
				Role: schema.Assistant, Content: strings.Repeat("x", domain.MaxSessionSummaryBytes+1),
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
			}),
			class: domain.SafeErrorClassInvalidExternalResponse,
		},
		{
			name: "sensitive",
			script: scriptedChunks(&schema.Message{
				Role: schema.Assistant, Content: "-----BEGIN PRIVATE KEY----- blocked -----END PRIVATE KEY-----",
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
			}),
			class: domain.SafeErrorClassSensitiveOutputBlocked,
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			model := &recordingModel{scripts: []modelScript{current.script}}
			input := testInputWithConversation(t, clock, testConversation(t, 160))
			outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(context.Background(), input, newEventRecorder())
			if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
				*outcome.ErrorClass != current.class || len(model.Requests()) != 1 {
				t.Fatalf("outcome/requests = %#v/%d", outcome, len(model.Requests()))
			}
		})
	}
}

func TestEinoSummaryHonorsCancellationAndReservedTimeout(t *testing.T) {
	tests := []struct {
		name  string
		want  domain.AgentRunStatus
		class domain.SafeErrorClass
	}{
		{
			name: "cancelled", want: domain.AgentRunStatusCancelled, class: domain.SafeErrorClassCancelled,
		},
		{
			name: "summary request timeout", want: domain.AgentRunStatusTimedOut, class: domain.SafeErrorClassTimeout,
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			input := testInputWithConversation(t, clock, testConversation(t, 160))
			var runContext context.Context
			var trigger func()
			if current.want == domain.AgentRunStatusCancelled {
				ctx, cancel := context.WithCancel(context.Background())
				runContext, trigger = ctx, cancel
			} else {
				ctx := newManualDeadlineContext()
				runContext, trigger = ctx, ctx.expire
			}
			model := &recordingModel{scripts: []modelScript{func(ctx context.Context, _ recordedModelRequest) ([]*schema.Message, error) {
				trigger()
				<-ctx.Done()
				return nil, ctx.Err()
			}}}
			outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(runContext, input, newEventRecorder())
			if outcome.Status != current.want || outcome.ErrorClass == nil || *outcome.ErrorClass != current.class ||
				len(model.Requests()) != 1 {
				t.Fatalf("outcome/requests = %#v/%d", outcome, len(model.Requests()))
			}
		})
	}
}

func TestEinoSummaryDiscardsOutputAfterScopeBecomesStale(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{func(context.Context, recordedModelRequest) ([]*schema.Message, error) {
		guard.SetCurrent(false)
		return []*schema.Message{{
			Role: schema.Assistant, Content: "This stale summary must be discarded.",
			ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
		}}, nil
	}}}
	recorder := newEventRecorder()
	outcome := testAdapter(t, clock, model, new(recordingTool), guard).Run(
		context.Background(), testInputWithConversation(t, clock, testConversation(t, 160)), recorder,
	)
	if outcome.Status != domain.AgentRunStatusStaleScope || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassStaleScope || len(model.Requests()) != 1 {
		t.Fatalf("outcome/requests = %#v/%d", outcome, len(model.Requests()))
	}
	for _, event := range recorder.Events() {
		if event.Kind == agent.RunEventSummaryReady || event.Kind == agent.RunEventModelStreamStarted {
			t.Fatalf("stale summary advanced model context: %#v", event)
		}
	}
}

func TestEinoSummarySinkRejectionPreventsMainModelRequest(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	model := &recordingModel{scripts: []modelScript{scriptedChunks(&schema.Message{
		Role: schema.Assistant, Content: "Earlier completed turns were summarized safely.",
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
	})}}
	recorder := newEventRecorder()
	sink := agent.EventSinkFunc(func(ctx context.Context, event agent.RunEvent) agent.EventSinkResult {
		result := recorder.Publish(ctx, event)
		if event.Kind == agent.RunEventSummaryReady {
			return agent.EventSinkRejected
		}
		return result
	})
	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), testInputWithConversation(t, clock, testConversation(t, 160)), sink,
	)
	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassPersistenceUnavailable || len(model.Requests()) != 1 {
		t.Fatalf("outcome/requests = %#v/%d", outcome, len(model.Requests()))
	}
}

func TestEinoSummarizationPreservesCurrentRunToolCallResultPair(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	guard := newTestScopeGuard()
	conversation := testConversation(t, 158)
	input := testInputWithConversation(t, clock, conversation)
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(resourceCall("call-after-history", "sample-pod"))...),
		scriptedChunks(&schema.Message{
			Role: schema.Assistant, Content: "Earlier completed Session turns were condensed safely.",
			ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
		}),
		scriptedChunks(diagnosisChunks(`{"answer_markdown":"The Tool pair remained ordered after Eino rewrote history.","evidence_citations":[],"proposed_actions":[]}`)...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"phase":"Running"}`)
	}}
	recorder := newEventRecorder()
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)
	if outcome.Status != domain.AgentRunStatusCompleted || len(model.Requests()) != 3 || len(tool.Calls()) != 1 {
		t.Fatalf("outcome/model/Tool calls = %#v/%d/%d", outcome, len(model.Requests()), len(tool.Calls()))
	}
	postSummary := model.Requests()[2].Messages
	pairIndex := -1
	for index := 0; index+1 < len(postSummary); index++ {
		if postSummary[index].Role == schema.Assistant && len(postSummary[index].ToolCalls) == 1 &&
			postSummary[index].ToolCalls[0].ID == "call-after-history" {
			pairIndex = index
			break
		}
	}
	if pairIndex < 0 || postSummary[pairIndex+1].Role != schema.Tool ||
		postSummary[pairIndex+1].ToolCallID != "call-after-history" {
		t.Fatalf("post-summary Tool pairing = %#v", postSummary)
	}
	summaryStarted, summaryReady := 0, 0
	for _, event := range recorder.Events() {
		if event.Kind == agent.RunEventSummaryStarted {
			summaryStarted++
		}
		if event.Kind == agent.RunEventSummaryReady {
			summaryReady++
		}
	}
	if summaryStarted != 1 || summaryReady != 1 {
		t.Fatalf("summary events = %d/%d, want 1/1", summaryStarted, summaryReady)
	}
}

func testConversation(t *testing.T, count int) agent.ConversationContext {
	t.Helper()
	if count%2 != 0 {
		t.Fatal("test conversation count must be even")
	}
	turns := make([]agent.ConversationTurn, count)
	for index := range turns {
		role := domain.MessageRoleUser
		content := fmt.Sprintf("Prior user question %03d", index/2)
		if index%2 == 1 {
			role = domain.MessageRoleAssistant
			content = fmt.Sprintf("Prior final assistant answer %03d", index/2)
		}
		turns[index] = agent.ConversationTurn{
			MessageID: domain.MessageID(fmt.Sprintf("00000000-0000-7000-8001-%012x", index+1)),
			Role:      role, Content: content, ContentHash: domain.MessageContentHash(content),
		}
	}
	conversation, err := agent.NewConversationContext(testSessionID, turns, nil)
	if err != nil {
		t.Fatalf("NewConversationContext() error = %v", err)
	}
	return conversation
}

func testConversationAtInputBytes(t *testing.T, clock *testClock, target int) agent.ConversationContext {
	t.Helper()
	const turnCount = summaryRecentTailMessages + 2
	conversation := testConversation(t, turnCount)
	turns := conversation.Turns()
	for index := range turns {
		turns[index].Content = "x"
		turns[index].ContentHash = domain.MessageContentHash(turns[index].Content)
	}
	conversation, err := agent.NewConversationContext(testSessionID, turns, nil)
	if err != nil {
		t.Fatalf("NewConversationContext() error = %v", err)
	}
	input := testInputWithConversation(t, clock, conversation)
	messages, err := newInitialMessages(input)
	if err != nil {
		t.Fatalf("newInitialMessages() error = %v", err)
	}
	baseBytes := 0
	for _, message := range messages {
		baseBytes += len(message.Content)
	}
	additionalBytes := target - baseBytes
	if additionalBytes < 0 || additionalBytes > turnCount*(domain.MaxModelInputMessageBytes-1) {
		t.Fatalf("invalid byte-threshold fixture target %d with %d base bytes", target, baseBytes)
	}
	for index := range turns {
		size := 1 + additionalBytes/turnCount
		if index < additionalBytes%turnCount {
			size++
		}
		turns[index].Content = strings.Repeat("x", size)
		turns[index].ContentHash = domain.MessageContentHash(turns[index].Content)
	}
	conversation, err = agent.NewConversationContext(testSessionID, turns, nil)
	if err != nil {
		t.Fatalf("NewConversationContext() error = %v", err)
	}
	input = testInputWithConversation(t, clock, conversation)
	messages, err = newInitialMessages(input)
	if err != nil {
		t.Fatalf("newInitialMessages() error = %v", err)
	}
	actual := 0
	for _, message := range messages {
		actual += len(message.Content)
	}
	if actual != target {
		t.Fatalf("byte-threshold fixture = %d, want %d", actual, target)
	}
	return conversation
}

func testInputWithConversation(t *testing.T, clock *testClock, conversation agent.ConversationContext) agent.RunInput {
	return testInputWithConversationAndLimits(t, clock, conversation, agent.DefaultRunBudgetLimits())
}

func testInputWithConversationAndLimits(
	t *testing.T,
	clock *testClock,
	conversation agent.ConversationContext,
	limits agent.RunBudgetLimits,
) agent.RunInput {
	t.Helper()
	input, err := agent.NewRunInputWithContext(
		testRunID, testSessionID, testMessageID, "What is true now?",
		domain.ClusterScope{
			Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent,
			Generation: 7, ActivatedAt: clock.Now(),
		},
		nil, limits, conversation,
	)
	if err != nil {
		t.Fatalf("NewRunInputWithContext() error = %v", err)
	}
	return input
}

type manualDeadlineContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newManualDeadlineContext() *manualDeadlineContext {
	return &manualDeadlineContext{Context: context.Background(), done: make(chan struct{})}
}

func (ctx *manualDeadlineContext) Done() <-chan struct{} { return ctx.done }

func (ctx *manualDeadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (ctx *manualDeadlineContext) expire() { ctx.once.Do(func() { close(ctx.done) }) }
