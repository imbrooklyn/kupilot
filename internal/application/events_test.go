package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestEventBridgeCoalescesDeltasAndPreservesStructuralOrdering(t *testing.T) {
	t.Parallel()
	const (
		runID     domain.AgentRunID     = "00000000-0000-7000-8000-000000000101"
		requestID domain.ModelRequestID = "00000000-0000-7000-8000-000000000102"
	)
	var events []UIEvent
	bridge, err := newEventBridge(runID, 7, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	configureTestModelEgress(bridge)
	now := time.UnixMilli(1_000).UTC()
	inputs := []agent.RunEvent{
		{RunID: runID, ScopeGeneration: 7, Sequence: 1, OccurredAt: now, Kind: agent.RunEventRunStarted},
		{RunID: runID, ScopeGeneration: 7, Sequence: 2, OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: "first "},
		{RunID: runID, ScopeGeneration: 7, Sequence: 3, OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: "second "},
		{RunID: runID, ScopeGeneration: 7, Sequence: 4, OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: "third"},
		{RunID: runID, ScopeGeneration: 7, Sequence: 5, OccurredAt: now, Kind: agent.RunEventModelStreamStarted, ModelRequestID: pointer(requestID), ModelPreflight: testModelCallPreflight(agent.ModelCallAgent)},
	}
	for _, event := range inputs {
		if err := bridge.accept(context.Background(), event); err != nil {
			t.Fatalf("accept(%s) error = %v", event.Kind, err)
		}
	}
	if err := bridge.persistenceDegraded(context.Background()); err != nil {
		t.Fatalf("persistenceDegraded() error = %v", err)
	}
	class := domain.SafeErrorClassInternal
	failure := agent.RunEvent{
		RunID: runID, ScopeGeneration: 7, Sequence: 6, OccurredAt: now,
		Kind:    agent.RunEventRunFailed,
		Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The AgentRun failed safely."},
	}
	if err := bridge.accept(context.Background(), failure); err != nil {
		t.Fatalf("accept(terminal) error = %v", err)
	}
	wantKinds := []UIEventKind{
		UIEventRunStarted, UIEventTextDelta, UIEventTextDelta, UIEventModelEgress, UIEventPersistenceDegraded, UIEventRunFailed,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("UI event count = %d, want %d", len(events), len(wantKinds))
	}
	for index, event := range events {
		if event.Kind != wantKinds[index] || event.Sequence != int64(index+1) || event.Validate() != nil {
			t.Fatalf("UI event[%d] = %#v", index, event)
		}
	}
	if events[1].Text != "first " || events[2].Text != "second third" {
		t.Fatalf("first/coalesced deltas = %q / %q", events[1].Text, events[2].Text)
	}
	if !events[len(events)-1].Terminal() {
		t.Fatal("last UI event is not terminal")
	}
	terminal := events[len(events)-1].TerminalOutcome
	if terminal == nil || terminalBudgetMeasure(terminal.Budget, UIBudgetModelInputBytes).Used != 128 ||
		terminalBudgetMeasure(terminal.Budget, UIBudgetModelAttempts).Used != 1 ||
		terminalBudgetMeasure(terminal.Budget, UIBudgetWallMilliseconds).Used != 0 ||
		terminalBudgetMeasure(terminal.Budget, UIBudgetQueueItems).Basis != UIBudgetUnavailable {
		t.Fatalf("terminal budget projection = %#v", terminal)
	}
}

func terminalBudgetMeasure(values []UIBudgetMeasure, category UIBudgetCategory) UIBudgetMeasure {
	for _, value := range values {
		if value.Category == category {
			return value
		}
	}
	return UIBudgetMeasure{}
}

func TestEventBridgeDoesNotConsumeSequenceOrDeltaOnSinkFailure(t *testing.T) {
	t.Parallel()
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000111"
	now := time.UnixMilli(2_000).UTC()
	failedDelta := false
	var accepted []UIEvent
	bridge, err := newEventBridge(runID, 9, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		if event.Kind == UIEventTextDelta && !failedDelta {
			failedDelta = true
			return errors.New("generated UI sink failure")
		}
		accepted = append(accepted, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 1, OccurredAt: now, Kind: agent.RunEventRunStarted,
	}); err != nil {
		t.Fatalf("accept(started) error = %v", err)
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 2, OccurredAt: now,
		Kind: agent.RunEventTextDelta, TextDelta: "retained delta",
	}); err == nil {
		t.Fatal("first delta publish succeeded despite generated sink failure")
	}
	if err := bridge.persistenceDegraded(context.Background()); err != nil {
		t.Fatalf("persistenceDegraded() retry error = %v", err)
	}
	if len(accepted) != 3 || accepted[0].Sequence != 1 || accepted[1].Sequence != 2 || accepted[2].Sequence != 3 ||
		accepted[1].Kind != UIEventTextDelta || accepted[1].Text != "retained delta" ||
		accepted[2].Kind != UIEventPersistenceDegraded {
		t.Fatalf("accepted UI events after retry = %#v", accepted)
	}
}

func TestEventBridgeFlushesByTimeAndBytesAndDropsFailedTail(t *testing.T) {
	t.Parallel()
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000113"
	start := time.UnixMilli(2_500).UTC()
	var events []UIEvent
	bridge, err := newEventBridge(runID, 9, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	largeDelta := strings.Repeat("x", uiDeltaFlushBytes)
	inputs := []agent.RunEvent{
		{RunID: runID, ScopeGeneration: 9, Sequence: 1, OccurredAt: start, Kind: agent.RunEventRunStarted},
		{RunID: runID, ScopeGeneration: 9, Sequence: 2, OccurredAt: start, Kind: agent.RunEventTextDelta, TextDelta: "a"},
		{RunID: runID, ScopeGeneration: 9, Sequence: 3, OccurredAt: start.Add(time.Millisecond), Kind: agent.RunEventTextDelta, TextDelta: "b"},
		{RunID: runID, ScopeGeneration: 9, Sequence: 4, OccurredAt: start.Add(uiDeltaFlushInterval), Kind: agent.RunEventTextDelta, TextDelta: "c"},
		{RunID: runID, ScopeGeneration: 9, Sequence: 5, OccurredAt: start.Add(uiDeltaFlushInterval + time.Millisecond), Kind: agent.RunEventTextDelta, TextDelta: largeDelta},
		{RunID: runID, ScopeGeneration: 9, Sequence: 6, OccurredAt: start.Add(uiDeltaFlushInterval + 2*time.Millisecond), Kind: agent.RunEventTextDelta, TextDelta: "discarded"},
		{
			RunID: runID, ScopeGeneration: 9, Sequence: 7, OccurredAt: start.Add(uiDeltaFlushInterval + 3*time.Millisecond),
			Kind: agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{
				Class: domain.SafeErrorClassInternal, SafeMessage: "The synthetic run failed safely.",
			},
		},
	}
	for _, event := range inputs {
		if err := bridge.accept(context.Background(), event); err != nil {
			t.Fatalf("accept(%s) error = %v", event.Kind, err)
		}
	}
	if len(events) != 5 || events[0].Kind != UIEventRunStarted || events[1].Text != "a" ||
		events[2].Text != "bc" || events[3].Text != largeDelta ||
		events[4].Kind != UIEventRunFailed || strings.Contains(fmt.Sprint(events), "discarded") {
		t.Fatalf("timed/newline UI events = %#v", events)
	}
}

func TestEventBridgeCapsProvisionalEventsWithoutLosingTerminal(t *testing.T) {
	t.Parallel()
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000114"
	now := time.UnixMilli(2_600).UTC()
	var events []UIEvent
	bridge, err := newEventBridge(runID, 9, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 1, OccurredAt: now, Kind: agent.RunEventRunStarted,
	}); err != nil {
		t.Fatalf("accept(started) error = %v", err)
	}
	for index := 0; index < maxUIDeltaEvents+1; index++ {
		if err := bridge.accept(context.Background(), agent.RunEvent{
			RunID: runID, ScopeGeneration: 9, Sequence: int64(index + 2),
			OccurredAt: now.Add(time.Duration(index) * uiDeltaFlushInterval),
			Kind:       agent.RunEventTextDelta, TextDelta: "x",
		}); err != nil {
			t.Fatalf("accept(delta %d) error = %v", index, err)
		}
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: maxUIDeltaEvents + 3, OccurredAt: now,
		Kind: agent.RunEventRunFailed,
		Failure: &agent.RunEventFailure{
			Class: domain.SafeErrorClassInternal, SafeMessage: "The synthetic run failed safely.",
		},
	}); err != nil {
		t.Fatalf("accept(terminal) error = %v", err)
	}
	if len(events) != maxUIDeltaEvents+2 || events[len(events)-1].Kind != UIEventRunFailed ||
		events[len(events)-1].Sequence != maxUIDeltaEvents+2 {
		t.Fatalf("bounded UI event shape: count=%d terminal=%#v", len(events), events[len(events)-1])
	}
	limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
	if err != nil {
		t.Fatalf("RunBudgetLimitsForProfile() error = %v", err)
	}
	worstCaseEvents := maxUIDeltaEvents + 1 + limits.ToolCalls*3 + 3
	if worstCaseEvents > maxUIEventSequence {
		t.Fatalf("provisional budget leaves insufficient UI event capacity: %d > %d", worstCaseEvents, maxUIEventSequence)
	}
}

func TestEventBridgeCapsProvisionalBytesBeforeDelivery(t *testing.T) {
	t.Parallel()
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000115"
	now := time.UnixMilli(2_700).UTC()
	var events []UIEvent
	bridge, err := newEventBridge(runID, 9, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 1, OccurredAt: now, Kind: agent.RunEventRunStarted,
	}); err != nil {
		t.Fatalf("accept(started) error = %v", err)
	}
	delta := strings.Repeat("x", MaxAnswerMarkdownBytes/2)
	for index := 0; index < 2; index++ {
		if err := bridge.accept(context.Background(), agent.RunEvent{
			RunID: runID, ScopeGeneration: 9, Sequence: int64(index + 2), OccurredAt: now,
			Kind: agent.RunEventTextDelta, TextDelta: delta,
		}); err != nil {
			t.Fatalf("accept(delta %d) error = %v", index, err)
		}
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 4, OccurredAt: now,
		Kind: agent.RunEventTextDelta, TextDelta: "overflow",
	}); err != nil {
		t.Fatalf("accept(overflow) error = %v", err)
	}
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 5, OccurredAt: now,
		Kind: agent.RunEventRunFailed,
		Failure: &agent.RunEventFailure{
			Class: domain.SafeErrorClassInternal, SafeMessage: "The synthetic run failed safely.",
		},
	}); err != nil {
		t.Fatalf("accept(terminal) error = %v", err)
	}
	if len(events) != 4 || events[1].Kind != UIEventTextDelta || events[2].Kind != UIEventTextDelta ||
		len(events[1].Text)+len(events[2].Text) != MaxAnswerMarkdownBytes ||
		strings.Contains(fmt.Sprint(events), "overflow") || events[3].Kind != UIEventRunFailed {
		t.Fatalf("bounded provisional bytes = %#v", events)
	}
}

func TestEventBridgePublishesValidationWarningBeforeFinalAnswer(t *testing.T) {
	t.Parallel()
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000131"
	now := time.UnixMilli(3_000).UTC()
	var events []UIEvent
	bridge, err := newEventBridge(runID, 7, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	diagnosis := domain.Diagnosis{
		ID: "00000000-0000-7000-8000-000000000132", RunID: runID,
		Scope:              domain.ScopeSnapshot{Context: "test-context", Namespace: "team-a", Generation: 7},
		AnswerMarkdown:     "The supported answer remains available.",
		ValidationWarnings: []string{"An invalid Evidence reference was removed."},
		CreatedAt:          now,
	}
	inputs := []agent.RunEvent{
		{RunID: runID, ScopeGeneration: 7, Sequence: 1, OccurredAt: now, Kind: agent.RunEventRunStarted},
		{RunID: runID, ScopeGeneration: 7, Sequence: 2, OccurredAt: now, Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis},
		{RunID: runID, ScopeGeneration: 7, Sequence: 3, OccurredAt: now, Kind: agent.RunEventRunCompleted},
	}
	for _, event := range inputs {
		if err := bridge.accept(context.Background(), event); err != nil {
			t.Fatalf("accept(%s) error = %v", event.Kind, err)
		}
	}
	wantKinds := []UIEventKind{UIEventRunStarted, UIEventValidationWarning, UIEventRunCompleted}
	if len(events) != len(wantKinds) {
		t.Fatalf("UI event count = %d, want %d: %#v", len(events), len(wantKinds), events)
	}
	for index, event := range events {
		if event.Kind != wantKinds[index] || event.Sequence != int64(index+1) || event.Validate() != nil {
			t.Fatalf("UI event[%d] = %#v", index, event)
		}
	}
	if !strings.Contains(events[1].Text, "unsupported final-answer metadata") || events[2].Text != diagnosis.AnswerMarkdown {
		t.Fatalf("warning/final events = %#v", events)
	}
}

func TestCompletedUIEventAcceptsAnswerAboveQuestionLimit(t *testing.T) {
	t.Parallel()
	answer := strings.Repeat("a", MaxQuestionBytes+1)
	event := UIEvent{
		Kind: UIEventRunCompleted, RunID: "00000000-0000-7000-8000-000000000141",
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: answer,
		TerminalOutcome:  testTerminalOutcome(domain.RunTerminalCompleted),
		AnswerProvenance: testAnswerProvenance(7, 1),
	}
	if len(answer) > MaxAnswerMarkdownBytes || event.Validate() != nil {
		t.Fatalf("validated final answer length = %d; event error = %v", len(answer), event.Validate())
	}
}

func TestTextDeltaUIEventAcceptsContentAboveQuestionLimit(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("a", MaxQuestionBytes+1)
	event := UIEvent{
		Kind: UIEventTextDelta, RunID: "00000000-0000-7000-8000-000000000141",
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: text,
	}
	if len(text) > MaxAnswerMarkdownBytes || event.Validate() != nil {
		t.Fatalf("validated streamed answer length = %d; event error = %v", len(text), event.Validate())
	}
}

func TestCompletedToolStepDoesNotInventAnEmptySuccessSummary(t *testing.T) {
	t.Parallel()
	invocation := &domain.ToolInvocation{
		ID:            "00000000-0000-7000-8000-000000000121",
		Name:          domain.ToolNameListResources,
		Status:        domain.ToolInvocationStatusSucceeded,
		EvidenceCount: 9,
	}
	step, err := projectToolStep(agent.RunEvent{
		Kind:           agent.RunEventToolCallCompleted,
		ToolInvocation: invocation,
	})
	if err != nil {
		t.Fatalf("projectToolStep() error = %v", err)
	}
	if step.Status != ToolStepSucceeded || step.EvidenceCount != 9 || step.Summary != "" {
		t.Fatalf("completed Tool step = %#v", step)
	}
}

func pointer[T any](value T) *T { return &value }
