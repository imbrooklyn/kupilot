package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestInteractionFailurePreservesCauseWithoutRenderingIt(t *testing.T) {
	cause := errors.New("synthetic private response credential canary")
	err := responseError(domain.FailureFinalShape, cause)
	if !errors.Is(err, cause) || !errors.Is(err, ErrInvalidDiagnosticResponse) || InteractionFailureOf(err, domain.FailureInternal) != domain.FailureFinalShape || strings.Contains(err.Error(), "canary") {
		t.Fatalf("unsafe cause translation: %v", err)
	}
	if InteractionFailureOf(cause, domain.FailureInternal) != domain.FailureInternal || InteractionFailureOf(nil, domain.FailureInternal) != domain.FailureInternal {
		t.Fatal("untyped cause changed diagnostic classification")
	}
}

func TestInteractionDiagnosticCannotEnterNonterminalOrSuccessfulEvents(t *testing.T) {
	event := RunEvent{RunID: testRunID, ScopeGeneration: 7, Sequence: 1, OccurredAt: time.UnixMilli(1000).UTC(), Kind: RunEventRunFailed, Failure: &RunEventFailure{Class: domain.SafeErrorClassInternal, SafeMessage: "The run failed safely."}, Diagnostic: domain.FailureInternal}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []RunEventKind{RunEventRunStarted, RunEventRunCompleted} {
		candidate := event
		candidate.Kind = kind
		candidate.Failure = nil
		if !errors.Is(candidate.Validate(), ErrInvalidRunEvent) {
			t.Fatal("failure metadata entered a nonfailure event")
		}
	}
	event.Diagnostic = "model-selected-retry"
	if !errors.Is(event.Validate(), ErrInvalidRunEvent) {
		t.Fatal("unknown diagnostic entered event stream")
	}
	if EventSinkResult("model-selected-acceptance").valid() {
		t.Fatal("unknown sink result accepted")
	}
}
