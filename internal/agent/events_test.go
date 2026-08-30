package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestEventPublisherAssignsOrderAndAllowsExactlyOneTerminal(t *testing.T) {
	clock := newFakeClock()
	var received []RunEvent
	sink := EventSinkFunc(func(_ context.Context, event RunEvent) EventSinkResult {
		received = append(received, event)
		return EventSinkAccepted
	})
	publisher, err := NewEventPublisher(testRunID, 7, clock.Now, sink)
	if err != nil {
		t.Fatalf("NewEventPublisher() error = %v", err)
	}
	for _, event := range []RunEvent{
		{Kind: RunEventRunStarted},
		{Kind: RunEventTextDelta, TextDelta: "Inspecting bounded Evidence."},
		{Kind: RunEventRunCancelled, TerminationReason: "user_cancelled"},
	} {
		if result, publishErr := publisher.Publish(context.Background(), event); publishErr != nil || result != EventSinkAccepted {
			t.Fatalf("Publish(%q) = %q, %v", event.Kind, result, publishErr)
		}
	}
	if len(received) != 3 {
		t.Fatalf("event count = %d", len(received))
	}
	for index, event := range received {
		if event.RunID != testRunID || event.ScopeGeneration != 7 || event.Sequence != int64(index+1) || event.OccurredAt != clock.Now() {
			t.Fatalf("event[%d] metadata = %#v", index, event)
		}
	}
	if !received[2].Terminal() || !publisher.Terminal() {
		t.Fatal("cancel event did not seal the publisher")
	}
	_, err = publisher.Publish(context.Background(), RunEvent{Kind: RunEventRunFailed, Failure: &RunEventFailure{Class: domain.SafeErrorClassInternal, SafeMessage: "The run failed safely."}})
	if !errors.Is(err, ErrRunEventTerminal) {
		t.Fatalf("late terminal error = %v", err)
	}
	if len(received) != 3 {
		t.Fatalf("late terminal reached sink, count = %d", len(received))
	}
}

func TestRunEventUsesTheExpandedFiniteSequenceCeiling(t *testing.T) {
	clock := newFakeClock()
	event := RunEvent{
		RunID: testRunID, ScopeGeneration: 7, Sequence: MaxRunEvents,
		OccurredAt: clock.Now(), Kind: RunEventRunCancelled,
		TerminationReason: RunTerminationUserCancelled,
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate(at event ceiling) error = %v", err)
	}
	event.Sequence++
	if err := event.Validate(); err == nil {
		t.Fatal("Validate(over event ceiling) error = nil")
	}
}

func TestRunEventRejectsMismatchedEvidenceBeforeSink(t *testing.T) {
	clock := newFakeClock()
	sinkCalls := 0
	publisher, err := NewEventPublisher(testRunID, 7, clock.Now, EventSinkFunc(func(_ context.Context, _ RunEvent) EventSinkResult {
		sinkCalls++
		return EventSinkAccepted
	}))
	if err != nil {
		t.Fatalf("NewEventPublisher() error = %v", err)
	}
	evidence := domain.Evidence{
		ID:           testEvidenceID,
		RunID:        "00000000-0000-7000-8000-000000004099",
		InvocationID: testInvocationID,
		Category:     domain.EvidenceCategoryCondition,
		Scope:        domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		Resource:     domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod"},
		Fact:         "The Pod is not Ready.",
		Fingerprint:  domain.SHA256Hex("not-ready"),
		ObservedAt:   clock.Now(),
	}
	_, err = publisher.Publish(context.Background(), RunEvent{Kind: RunEventEvidenceCollected, Evidence: &evidence})
	if !errors.Is(err, ErrInvalidRunEvent) || sinkCalls != 0 {
		t.Fatalf("Publish(mismatched Evidence) error = %v, sink calls = %d", err, sinkCalls)
	}
}

func TestEventPublisherRejectsEventsBeforeRunStart(t *testing.T) {
	clock := newFakeClock()
	sinkCalls := 0
	publisher, err := NewEventPublisher(testRunID, 7, clock.Now, EventSinkFunc(func(_ context.Context, _ RunEvent) EventSinkResult {
		sinkCalls++
		return EventSinkAccepted
	}))
	if err != nil {
		t.Fatalf("NewEventPublisher() error = %v", err)
	}
	_, err = publisher.Publish(context.Background(), RunEvent{Kind: RunEventTextDelta, TextDelta: "Late text."})
	if !errors.Is(err, ErrInvalidRunEvent) || sinkCalls != 0 {
		t.Fatalf("pre-start event error = %v, sink calls = %d", err, sinkCalls)
	}
}

func TestEventPublisherRejectsReentrantEventWithoutDeadlock(t *testing.T) {
	clock := newFakeClock()
	var publisher *EventPublisher
	var reentrantErr error
	sink := EventSinkFunc(func(ctx context.Context, event RunEvent) EventSinkResult {
		if event.Terminal() {
			_, reentrantErr = publisher.Publish(ctx, RunEvent{
				Kind:    RunEventRunFailed,
				Failure: &RunEventFailure{Class: domain.SafeErrorClassInternal, SafeMessage: "The run failed safely."},
			})
		}
		return EventSinkAccepted
	})
	var err error
	publisher, err = NewEventPublisher(testRunID, 7, clock.Now, sink)
	if err != nil {
		t.Fatalf("NewEventPublisher() error = %v", err)
	}
	if _, err := publisher.Publish(context.Background(), RunEvent{Kind: RunEventRunStarted}); err != nil {
		t.Fatalf("Publish(start) error = %v", err)
	}
	if _, err := publisher.Publish(context.Background(), RunEvent{Kind: RunEventRunCancelled, TerminationReason: RunTerminationUserCancelled}); err != nil {
		t.Fatalf("Publish(cancelled) error = %v", err)
	}
	if !errors.Is(reentrantErr, ErrRunEventTerminal) {
		t.Fatalf("reentrant terminal error = %v", reentrantErr)
	}
}
