package einoadapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type recordingManualCompactionSink struct {
	mu         sync.Mutex
	events     []agent.ManualCompactionEvent
	rejectKind agent.ManualCompactionEventKind
}

func (sink *recordingManualCompactionSink) AcceptManualCompaction(
	_ context.Context,
	event agent.ManualCompactionEvent,
) agent.EventSinkResult {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if event.Validate() != nil {
		return agent.EventSinkRejected
	}
	sink.events = append(sink.events, event)
	if event.Kind == sink.rejectKind {
		return agent.EventSinkRejected
	}
	return agent.EventSinkAccepted
}

func (sink *recordingManualCompactionSink) Events() []agent.ManualCompactionEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]agent.ManualCompactionEvent(nil), sink.events...)
}

func testManualCompactionInput(t testing.TB, clock *testClock, conversation agent.ConversationContext) agent.ManualCompactionInput {
	t.Helper()
	return testManualCompactionInputWithLimits(t, clock, conversation, agent.DefaultRunBudgetLimits())
}

func testManualCompactionInputWithLimits(
	t testing.TB,
	clock *testClock,
	conversation agent.ConversationContext,
	limits agent.RunBudgetLimits,
) agent.ManualCompactionInput {
	t.Helper()
	input, err := agent.NewManualCompactionInput(
		"00000000-0000-7000-8000-000000008099", testSessionID, conversation,
		domain.ClusterScope{
			Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent,
			Generation: 7, ActivatedAt: clock.Now(),
		},
		1, string(domain.ModelRoleAgent), domain.SHA256Hex(""), limits,
	)
	if err != nil {
		t.Fatalf("NewManualCompactionInput() error = %v", err)
	}
	return input
}

func TestManualCompactionUsesExistingEinoSummaryHandlerOnce(t *testing.T) {
	clock := newTestClock()
	const summaryText = "Earlier committed turns were summarized through the existing Eino handler."
	model := &recordingModel{scripts: []modelScript{scriptedChunks(&schema.Message{
		Role: schema.Assistant, Content: summaryText,
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
	})}}
	tool := new(recordingTool)
	adapter := testAdapter(t, clock, model, tool, newTestScopeGuard())
	input := testManualCompactionInput(t, clock, testConversation(t, 160))
	sink := new(recordingManualCompactionSink)

	result, err := adapter.Compact(context.Background(), input, sink)
	if err != nil || result.Noop || result.Summary == nil || result.Summary.Text != summaryText ||
		result.Summary.CoveredCount != 160-summaryRecentTailMessages {
		t.Fatalf("Compact() = %#v, %v", result, err)
	}
	requests := model.Requests()
	if len(requests) != 1 || len(tool.Calls()) != 0 {
		t.Fatalf("manual compaction model/Tool calls = %d/%d", len(requests), len(tool.Calls()))
	}
	for _, message := range requests[0].Messages {
		if strings.Contains(message.Content, manualCompactionSentinel) {
			t.Fatal("manual compaction sentinel entered the summary model request")
		}
		if len(message.ToolCalls) != 0 || message.ToolCallID != "" {
			t.Fatalf("summary request carried Tool state: %#v", message)
		}
	}
	events := sink.Events()
	if len(events) != 2 || events[0].Kind != agent.ManualCompactionStarted || events[1].Kind != agent.ManualCompactionReady ||
		events[0].Sequence != 1 || events[1].Sequence != 2 || events[1].Summary == nil ||
		events[1].Summary.CoverageDigest != result.Summary.CoverageDigest {
		t.Fatalf("manual compaction events = %#v", events)
	}
}

func TestManualCompactionNoopMakesZeroModelAndToolCalls(t *testing.T) {
	clock := newTestClock()
	model := new(recordingModel)
	tool := new(recordingTool)
	adapter := testAdapter(t, clock, model, tool, newTestScopeGuard())
	input := testManualCompactionInput(t, clock, testConversation(t, domain.SessionContextRecentTailMinimum))
	sink := new(recordingManualCompactionSink)
	result, err := adapter.Compact(context.Background(), input, sink)
	if err != nil || !result.Noop || result.Summary != nil || len(model.Requests()) != 0 || len(tool.Calls()) != 0 || len(sink.Events()) != 0 {
		t.Fatalf("no-op Compact() = %#v, %v; model=%d Tool=%d events=%d", result, err,
			len(model.Requests()), len(tool.Calls()), len(sink.Events()))
	}
}

func TestManualCompactionFailuresCommitNoSummary(t *testing.T) {
	tests := []struct {
		name       string
		script     modelScript
		rejectKind agent.ManualCompactionEventKind
		stale      bool
		wantCalls  int
	}{
		{
			name: "transport failure",
			script: func(_ context.Context, request recordedModelRequest) ([]*schema.Message, error) {
				return nil, domain.NewModelError(domain.ModelErrorCodeServiceUnavailable, domain.ModelOperationRequest, string(request.ID))
			},
			wantCalls: 1,
		},
		{
			name: "timeout",
			script: func(ctx context.Context, request recordedModelRequest) ([]*schema.Message, error) {
				return nil, domain.NewModelError(domain.ModelErrorCodeTimeout, domain.ModelOperationRequest, string(request.ID))
			},
			wantCalls: 1,
		},
		{
			name:      "oversize",
			script:    scriptedChunks(&schema.Message{Role: schema.Assistant, Content: strings.Repeat("x", domain.MaxSessionSummaryBytes+1)}),
			wantCalls: 1,
		},
		{
			name: "start barrier rejection", script: scriptedChunks(&schema.Message{Role: schema.Assistant, Content: "Not reached."}),
			rejectKind: agent.ManualCompactionStarted, wantCalls: 0,
		},
		{
			name: "ready barrier rejection", script: scriptedChunks(&schema.Message{Role: schema.Assistant, Content: "Safe bounded summary."}),
			rejectKind: agent.ManualCompactionReady, wantCalls: 1,
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			model := &recordingModel{scripts: []modelScript{current.script}}
			tool := new(recordingTool)
			adapter := testAdapter(t, clock, model, tool, newTestScopeGuard())
			sink := &recordingManualCompactionSink{rejectKind: current.rejectKind}
			result, err := adapter.Compact(context.Background(), testManualCompactionInput(t, clock, testConversation(t, 160)), sink)
			if err == nil || result.Summary != nil || result.Noop || len(model.Requests()) != current.wantCalls || len(tool.Calls()) != 0 {
				t.Fatalf("failed Compact() = %#v, %v; model=%d Tool=%d", result, err, len(model.Requests()), len(tool.Calls()))
			}
			for _, event := range sink.Events() {
				if event.Kind == agent.ManualCompactionReady && current.rejectKind != agent.ManualCompactionReady {
					t.Fatalf("failure emitted a committed summary: %#v", event)
				}
			}
		})
	}
}

func TestManualCompactionHonorsTightenedSummaryOutputBudget(t *testing.T) {
	clock := newTestClock()
	limits := agent.DefaultRunBudgetLimits()
	limits.SummaryOutputBytes = 32
	model := &recordingModel{scripts: []modelScript{scriptedChunks(&schema.Message{
		Role: schema.Assistant, Content: strings.Repeat("x", limits.SummaryOutputBytes+1),
	})}}
	tool := new(recordingTool)
	sink := new(recordingManualCompactionSink)
	input := testManualCompactionInputWithLimits(t, clock, testConversation(t, 160), limits)
	result, err := testAdapter(t, clock, model, tool, newTestScopeGuard()).Compact(context.Background(), input, sink)
	if err == nil || result.Summary != nil || result.Noop || len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
		t.Fatalf("tight summary budget result = %#v, %v; model=%d Tool=%d", result, err, len(model.Requests()), len(tool.Calls()))
	}
	for _, event := range sink.Events() {
		if event.Kind == agent.ManualCompactionReady {
			t.Fatalf("budget failure emitted a ready summary: %#v", event)
		}
	}
}

func TestManualCompactionStopsAfterCancellationOrStaleScope(t *testing.T) {
	for _, current := range []struct {
		name   string
		change func(context.CancelFunc, *testScopeGuard)
	}{
		{name: "cancel", change: func(cancel context.CancelFunc, _ *testScopeGuard) { cancel() }},
		{name: "stale scope", change: func(_ context.CancelFunc, guard *testScopeGuard) { guard.SetCurrent(false) }},
	} {
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			guard := newTestScopeGuard()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			model := &recordingModel{scripts: []modelScript{func(requestContext context.Context, _ recordedModelRequest) ([]*schema.Message, error) {
				current.change(cancel, guard)
				if errors.Is(ctx.Err(), context.Canceled) {
					return nil, ctx.Err()
				}
				return []*schema.Message{{Role: schema.Assistant, Content: "Stale summary."}}, requestContext.Err()
			}}}
			tool := new(recordingTool)
			adapter := testAdapter(t, clock, model, tool, guard)
			sink := new(recordingManualCompactionSink)
			result, err := adapter.Compact(ctx, testManualCompactionInput(t, clock, testConversation(t, 160)), sink)
			if err == nil || result.Summary != nil || len(model.Requests()) != 1 || len(tool.Calls()) != 0 {
				t.Fatalf("cancel/stale Compact() = %#v, %v; calls=%d", result, err, len(model.Requests()))
			}
			for _, event := range sink.Events() {
				if event.Kind == agent.ManualCompactionReady {
					t.Fatalf("cancel/stale operation committed summary: %#v", event)
				}
			}
		})
	}
}
