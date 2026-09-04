package einoadapter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestToolBudgetProcessesBatchInOrderAndStopsBeforeSecondHandler(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	limits := agent.DefaultRunBudgetLimits()
	limits.ToolCalls = 1
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(
			resourceCall("call-1", "sample-pod"),
			eventsCall("call-2", "sample-pod"),
		)...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"ready":false}`)
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, limits)
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 1 || len(tool.Calls()) != 1 || tool.Calls()[0].ModelCallID() != "call-1" {
		t.Fatalf("calls: Model = %d, Tool = %#v", len(model.Requests()), tool.Calls())
	}
	foundDenied := false
	for _, event := range recorder.Events() {
		foundDenied = foundDenied || event.Kind == agent.RunEventToolCallDenied
	}
	if !foundDenied {
		t.Fatal("Tool budget stop did not publish a denial")
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestRepeatedToolCallStopsWithoutSecondHandlerOrThirdModelCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(resourceCall("call-1", "sample-pod"))...),
		scriptedChunks(toolCallChunks(resourceCall("call-2", "sample-pod"))...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"ready":false}`)
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 2 || len(tool.Calls()) != 1 {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestNoProgressStopsBeforeThirdNeutralModelCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(resourceCall("call-1", "sample-pod"))...),
		scriptedChunks(toolCallChunks(resourceCall("call-2", "other-pod"))...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return emptyToolResult(t, call, clock.Now())
	}}
	recorder := newEventRecorder()
	limits := agent.DefaultRunBudgetLimits()
	limits.NoProgressSteps = 2
	input := testInput(t, clock, limits)
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 2 || len(tool.Calls()) != 2 {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	foundNoEvidenceGap := false
	for _, missing := range outcome.Diagnosis.MissingInformation {
		foundNoEvidenceGap = foundNoEvidenceGap || missing.Kind == domain.MissingInformationAbsent
	}
	if !foundNoEvidenceGap {
		t.Fatalf("missing information = %#v", outcome.Diagnosis.MissingInformation)
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestRepeatedCorrectableToolPolicyDenialStopsAtNoProgressBudgetWithoutHandlerCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	deniedCall := func(id string) agent.ToolSelection {
		return agent.ToolSelection{
			ID:            id,
			Name:          domain.ToolNameListResources,
			ArgumentsJSON: `{"filters":[],"format":"list","limit":20,"namespace":"test-namespace","purpose":"List Nodes.","resource_type":"nodes"}`,
		}
	}
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(deniedCall("call-denied-1"))...),
		scriptedChunks(toolCallChunks(deniedCall("call-denied-2"))...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return emptyToolResult(t, call, clock.Now())
	}}
	recorder := newEventRecorder()
	limits := agent.DefaultRunBudgetLimits()
	limits.NoProgressSteps = 2
	input := testInput(t, clock, limits)
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 2 || len(tool.Calls()) != 0 {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	for _, event := range recorder.Events() {
		if event.Kind == agent.RunEventToolCallRequested || event.Kind == agent.RunEventToolCallStarted {
			t.Fatalf("locally rejected selection became a Tool event: %#v", event)
		}
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestLocalToolPolicyFeedbackHonorsCancellationTimeoutAndStaleScope(t *testing.T) {
	tests := []struct {
		name      string
		wantClass domain.SafeErrorClass
		prepare   func(*testScopeGuard) (context.Context, context.CancelFunc)
	}{
		{
			name:      "cancelled",
			wantClass: domain.SafeErrorClassCancelled,
			prepare: func(_ *testScopeGuard) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
		},
		{
			name:      "timed out",
			wantClass: domain.SafeErrorClassTimeout,
			prepare: func(_ *testScopeGuard) (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(1, 0))
			},
		},
		{
			name:      "stale scope",
			wantClass: domain.SafeErrorClassStaleScope,
			prepare: func(guard *testScopeGuard) (context.Context, context.CancelFunc) {
				guard.SetCurrent(false)
				return context.Background(), func() {}
			},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			guard := newTestScopeGuard()
			tool := new(recordingTool)
			state := &runState{
				input:             testInput(t, clock, agent.DefaultRunBudgetLimits()),
				tools:             fixedHandlers(tool),
				scopeGuard:        guard,
				identifiers:       &testIdentifiers{},
				now:               clock.Now,
				boundCalls:        make(map[string]*boundExecution),
				toolInvocationIDs: make(map[domain.ToolInvocationID]struct{}),
			}
			selection := agent.ToolSelection{
				ID:            "call-policy-feedback",
				Name:          domain.ToolNameListResources,
				ArgumentsJSON: `{"filters":[],"format":"list","limit":20,"namespace":"test-namespace","purpose":"List Nodes.","resource_type":"nodes"}`,
			}
			if err := state.bindToolCalls(context.Background(), []agent.ToolSelection{selection}); err != nil {
				t.Fatalf("bindToolCalls() error = %v", err)
			}
			ctx, cancel := current.prepare(guard)
			defer cancel()
			_, err := state.executeTool(ctx, selection.Name, selection.ID, selection.ArgumentsJSON)
			failure := normalizeFailure(ctx, err)
			if err == nil || failure.class != current.wantClass || len(tool.Calls()) != 0 {
				t.Fatalf("feedback error/class/Tool calls = %v/%q/%d", err, failure.class, len(tool.Calls()))
			}
		})
	}
}

func TestCancellationStopsBlockedModelAndPublishesOneTerminal(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	started := make(chan struct{})
	var closeOnce sync.Once
	model := &recordingModel{scripts: []modelScript{
		func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
			closeOnce.Do(func() { close(started) })
			<-ctx.Done()
			return nil, domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationStream, string(request.ID))
		},
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return emptyToolResult(t, call, clock.Now())
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	runtimeAdapter := testAdapter(t, clock, model, tool, guard)
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan agent.RunOutcome, 1)
	go func() {
		outcomes <- runtimeAdapter.Run(ctx, input, recorder)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model call did not start")
	}
	cancel()
	var outcome agent.RunOutcome
	select {
	case outcome = <-outcomes:
	case <-time.After(time.Second):
		t.Fatal("cancelled run did not terminate")
	}

	if outcome.Status != domain.AgentRunStatusCancelled || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassCancelled {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestModelDeadlineMapsToTimedOutWithoutRetry(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{
		func(_ context.Context, request recordedModelRequest) ([]*schema.Message, error) {
			return nil, domain.NewModelError(domain.ModelErrorCodeTimeout, domain.ModelOperationStream, string(request.ID))
		},
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return emptyToolResult(t, call, clock.Now())
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusTimedOut || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassTimeout {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestExpiredModelChildDeadlineStopsBeforeModelOrToolCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	limits := agent.DefaultRunBudgetLimits()
	limits.ModelRequestTimeout = time.Nanosecond
	model := &recordingModel{scripts: []modelScript{
		func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
			<-ctx.Done()
			return nil, domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationStream, string(request.ID))
		},
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return emptyToolResult(t, call, clock.Now())
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, limits)
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusTimedOut || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassTimeout {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 0 || len(tool.Calls()) != 0 {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestToolChildDeadlineStopsBeforeAnotherModelOrToolCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	limits := agent.DefaultRunBudgetLimits()
	limits.ToolRequestTimeout = time.Nanosecond
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(
			resourceCall("call-1", "sample-pod"),
			eventsCall("call-2", "sample-pod"),
		)...),
	}}
	tool := &recordingTool{execute: func(ctx context.Context, _ agent.BoundToolCall) domain.ToolResult {
		<-ctx.Done()
		return domain.ToolResult{}
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, limits)
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusTimedOut || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassTimeout {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 1 || len(tool.Calls()) != 1 || tool.Calls()[0].ModelCallID() != "call-1" {
		t.Fatalf("calls: Model = %d, Tool = %#v", len(model.Requests()), tool.Calls())
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestMissingModelFinishStopsWithoutToolCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(&schema.Message{Role: schema.Assistant, Content: `{"confirmed_facts":[]}`}),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		return emptyToolResult(t, call, clock.Now())
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassInvalidExternalResponse {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(tool.Calls()) != 0 || len(model.Requests()) != 1 {
		t.Fatalf("calls: Tool = %d, Model = %d", len(tool.Calls()), len(model.Requests()))
	}
	assertTerminalSequence(t, recorder.Events())
}

func TestStaleScopeBeforeAndAfterExternalReturnsDiscardsLateData(t *testing.T) {
	t.Run("before model", func(t *testing.T) {
		clock := newTestClock()
		guard := newTestScopeGuard()
		guard.SetCurrent(false)
		model := &recordingModel{}
		tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
			return emptyToolResult(t, call, clock.Now())
		}}
		recorder := newEventRecorder()
		input := testInput(t, clock, agent.DefaultRunBudgetLimits())
		outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)
		if outcome.Status != domain.AgentRunStatusStaleScope || len(model.Requests()) != 0 || len(tool.Calls()) != 0 {
			t.Fatalf("outcome = %#v, calls = %d/%d", outcome, len(model.Requests()), len(tool.Calls()))
		}
		assertTerminalSequence(t, recorder.Events())
	})

	t.Run("after model", func(t *testing.T) {
		clock := newTestClock()
		guard := newTestScopeGuard()
		chunks := toolCallChunks(resourceCall("call-1", "sample-pod"))
		model := &recordingModel{scripts: []modelScript{
			func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
				result, err := scriptedChunks(chunks...)(ctx, request)
				guard.SetCurrent(false)
				return result, err
			},
		}}
		tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
			return emptyToolResult(t, call, clock.Now())
		}}
		recorder := newEventRecorder()
		input := testInput(t, clock, agent.DefaultRunBudgetLimits())
		outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)
		if outcome.Status != domain.AgentRunStatusStaleScope || len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
			t.Fatalf("outcome = %#v, calls = %d/%d", outcome, len(model.Requests()), len(tool.Calls()))
		}
		assertTerminalSequence(t, recorder.Events())
	})

	t.Run("after tool", func(t *testing.T) {
		clock := newTestClock()
		guard := newTestScopeGuard()
		model := &recordingModel{scripts: []modelScript{
			scriptedChunks(toolCallChunks(resourceCall("call-1", "sample-pod"))...),
		}}
		tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
			result := successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"ready":false}`)
			guard.SetCurrent(false)
			return result
		}}
		recorder := newEventRecorder()
		input := testInput(t, clock, agent.DefaultRunBudgetLimits())
		outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)
		if outcome.Status != domain.AgentRunStatusStaleScope || len(model.Requests()) != 1 || len(tool.Calls()) != 1 {
			t.Fatalf("outcome = %#v, calls = %d/%d", outcome, len(model.Requests()), len(tool.Calls()))
		}
		for _, event := range recorder.Events() {
			if event.Kind == agent.RunEventEvidenceCollected {
				t.Fatal("late Tool result created Evidence after scope change")
			}
		}
		assertTerminalSequence(t, recorder.Events())
	})
}

func TestInvalidToolEvidenceStopsBeforeAnotherModelCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{
		scriptedChunks(toolCallChunks(
			resourceCall("call-1", "sample-pod"),
			eventsCall("call-2", "sample-pod"),
		)...),
	}}
	tool := &recordingTool{execute: func(_ context.Context, call agent.BoundToolCall) domain.ToolResult {
		result := successfulToolResult(t, call, testEvidenceID, clock.Now(), `{"ready":false}`)
		result.Evidence[0].Scope.Generation++
		return result
	}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, tool, guard).Run(context.Background(), input, recorder)

	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassInvalidExternalResponse {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(model.Requests()) != 1 || len(tool.Calls()) != 1 || tool.Calls()[0].ModelCallID() != "call-1" {
		t.Fatalf("calls: Model = %d, Tool = %d", len(model.Requests()), len(tool.Calls()))
	}
	for _, event := range recorder.Events() {
		if event.Kind == agent.RunEventEvidenceCollected {
			t.Fatal("invalid Tool Evidence reached the event sink")
		}
	}
	assertTerminalSequence(t, recorder.Events())
}
