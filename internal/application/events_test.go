package application

import (
	"context"
	"errors"
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
	bridge, err := newEventBridge(runID, 7, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
		events = append(events, event)
		return nil
	}))
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	now := time.UnixMilli(1_000).UTC()
	inputs := []agent.RunEvent{
		{RunID: runID, ScopeGeneration: 7, Sequence: 1, OccurredAt: now, Kind: agent.RunEventRunStarted},
		{RunID: runID, ScopeGeneration: 7, Sequence: 2, OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: "first "},
		{RunID: runID, ScopeGeneration: 7, Sequence: 3, OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: "second"},
		{RunID: runID, ScopeGeneration: 7, Sequence: 4, OccurredAt: now, Kind: agent.RunEventModelStreamStarted, ModelRequestID: pointer(requestID)},
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
		RunID: runID, ScopeGeneration: 7, Sequence: 5, OccurredAt: now,
		Kind:    agent.RunEventRunFailed,
		Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The AgentRun failed safely."},
	}
	if err := bridge.accept(context.Background(), failure); err != nil {
		t.Fatalf("accept(terminal) error = %v", err)
	}
	wantKinds := []UIEventKind{
		UIEventRunStarted, UIEventTextDelta, UIEventPersistenceDegraded, UIEventRunFailed,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("UI event count = %d, want %d", len(events), len(wantKinds))
	}
	for index, event := range events {
		if event.Kind != wantKinds[index] || event.Sequence != int64(index+1) || event.Validate() != nil {
			t.Fatalf("UI event[%d] = %#v", index, event)
		}
	}
	if events[1].Text != "first second" {
		t.Fatalf("coalesced delta = %q", events[1].Text)
	}
	if !events[len(events)-1].Terminal() {
		t.Fatal("last UI event is not terminal")
	}
}

func TestEventBridgeDoesNotConsumeSequenceOrDeltaOnSinkFailure(t *testing.T) {
	t.Parallel()
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000111"
	now := time.UnixMilli(2_000).UTC()
	failedDelta := false
	var accepted []UIEvent
	bridge, err := newEventBridge(runID, 9, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
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
	}); err != nil {
		t.Fatalf("accept(delta) error = %v", err)
	}
	requestID := domain.ModelRequestID("00000000-0000-7000-8000-000000000112")
	if err := bridge.accept(context.Background(), agent.RunEvent{
		RunID: runID, ScopeGeneration: 9, Sequence: 3, OccurredAt: now,
		Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID,
	}); err == nil {
		t.Fatal("structural flush succeeded despite generated sink failure")
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

func pointer[T any](value T) *T { return &value }
