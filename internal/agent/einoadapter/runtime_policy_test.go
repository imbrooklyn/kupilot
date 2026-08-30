package einoadapter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestToolBudgetProcessesBatchInOrderAndStopsBeforeSecondHandler(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	limits := agent.DefaultRunBudgetLimits()
	limits.ToolCalls = 1
	model := &recordingModel{scripts: []modelScript{
		scriptedEvents(toolCallEvents(
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
		scriptedEvents(toolCallEvents(resourceCall("call-1", "sample-pod"))...),
		scriptedEvents(toolCallEvents(resourceCall("call-2", "sample-pod"))...),
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
		scriptedEvents(toolCallEvents(resourceCall("call-1", "sample-pod"))...),
		scriptedEvents(toolCallEvents(resourceCall("call-2", "other-pod"))...),
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

func TestCancellationStopsBlockedModelAndPublishesOneTerminal(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	started := make(chan struct{})
	var closeOnce sync.Once
	model := &recordingModel{scripts: []modelScript{
		func(ctx context.Context, request domain.ModelRequest, _ agent.ModelStreamConsumer) *domain.ModelError {
			closeOnce.Do(func() { close(started) })
			<-ctx.Done()
			return domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationStream, string(request.ID))
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
		func(_ context.Context, request domain.ModelRequest, _ agent.ModelStreamConsumer) *domain.ModelError {
			return domain.NewModelError(domain.ModelErrorCodeTimeout, domain.ModelOperationStream, string(request.ID))
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

func TestModelChildDeadlineStopsWithoutRetryOrToolCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	limits := agent.DefaultRunBudgetLimits()
	limits.ModelRequestTimeout = time.Nanosecond
	model := &recordingModel{scripts: []modelScript{
		func(ctx context.Context, request domain.ModelRequest, _ agent.ModelStreamConsumer) *domain.ModelError {
			<-ctx.Done()
			return domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationStream, string(request.ID))
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
	if len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
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
		scriptedEvents(toolCallEvents(
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

func TestInvalidDeltaSequenceStopsWithoutToolCall(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	model := &recordingModel{scripts: []modelScript{
		scriptedEvents(domain.ModelStreamEvent{
			Sequence:  2,
			Kind:      domain.ModelStreamEventTextDelta,
			TextDelta: `{"confirmed_facts":[]}`,
		}),
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
		events := toolCallEvents(resourceCall("call-1", "sample-pod"))
		model := &recordingModel{scripts: []modelScript{
			func(ctx context.Context, request domain.ModelRequest, consume agent.ModelStreamConsumer) *domain.ModelError {
				result := scriptedEvents(events...)(ctx, request, consume)
				guard.SetCurrent(false)
				return result
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
			scriptedEvents(toolCallEvents(resourceCall("call-1", "sample-pod"))...),
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
		scriptedEvents(toolCallEvents(
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
